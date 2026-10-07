package mockhv

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

var (
	ErrVMNotFound   = errors.New("vm not found")
	ErrVMBusy       = errors.New("vm is still being created")
	ErrTaskNotFound = errors.New("task not found")
)

type Config struct {
	// TaskDelay is how long every task takes to finish.
	TaskDelay time.Duration
	// HangFor is how long a timeout-after-success create withholds its
	// response. It must exceed the caller's client timeout.
	HangFor time.Duration
}

// Hypervisor holds the simulated inventory. All state is in memory: a
// restart forgets every VM, unlike a real hypervisor.
type Hypervisor struct {
	cfg Config

	mu       sync.Mutex
	vms      map[string]*VM
	tasks    map[string]*Task
	faults   Faults
	nextVM   int
	nextTask int
}

func New(cfg Config) *Hypervisor {
	return &Hypervisor{
		cfg:   cfg,
		vms:   make(map[string]*VM),
		tasks: make(map[string]*Task),
	}
}

// startTask records a running task and finishes it after delay on a
// background goroutine. apply performs the task's effect; onFail undoes any
// partial work. Both run with h.mu held. Caller must hold h.mu.
func (h *Hypervisor) startTask(kind, vmID string, delay time.Duration, apply func() error, onFail func()) Task {
	h.nextTask++
	t := &Task{ID: fmt.Sprintf("task-%04d", h.nextTask), Kind: kind, VMID: vmID, State: TaskRunning}
	h.tasks[t.ID] = t
	slog.Info("task started", "task", t.ID, "kind", kind, "vm", vmID, "delay", delay)

	time.AfterFunc(delay, func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		var err error
		if rand.Float64() < h.faults.FailRate {
			err = errors.New("simulated hypervisor failure")
		} else {
			err = apply()
		}
		if err != nil {
			t.State, t.Error = TaskError, err.Error()
			if onFail != nil {
				onFail()
			}
			slog.Warn("task failed", "task", t.ID, "kind", kind, "vm", vmID, "err", err)
			return
		}
		t.State = TaskSuccess
		slog.Info("task succeeded", "task", t.ID, "kind", kind, "vm", vmID)
	})
	return *t
}

// lookup returns the live VM, or ErrVMNotFound. Caller must hold h.mu.
func (h *Hypervisor) lookup(id string) (*VM, error) {
	vm, ok := h.vms[id]
	if !ok {
		return nil, fmt.Errorf("%s: %w", id, ErrVMNotFound)
	}
	return vm, nil
}

// CreateVM starts building a VM. It deliberately does not check whether a
// VM with the same ExternalID already exists: real hypervisors don't either.
func (h *Hypervisor) CreateVM(req CreateRequest) Task {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextVM++
	vm := &VM{
		ID:         fmt.Sprintf("vm-%04d", h.nextVM),
		ExternalID: req.ExternalID,
		Name:       req.Name,
		CPU:        req.CPU,
		MemoryMB:   req.MemoryMB,
		HostID:     req.HostID,
		Status:     VMCreating,
		Power:      PowerOff,
	}
	h.vms[vm.ID] = vm

	delay := h.cfg.TaskDelay + time.Duration(h.faults.CreateDelayMS)*time.Millisecond
	return h.startTask("create", vm.ID, delay,
		func() error {
			v, err := h.lookup(vm.ID)
			if err != nil {
				return err
			}
			v.Status = VMReady
			return nil
		},
		func() { delete(h.vms, vm.ID) },
	)
}

// startOnReadyVM is the shared shape of every operation on an existing VM.
func (h *Hypervisor) startOnReadyVM(kind, id string, apply func(*VM)) (Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	vm, err := h.lookup(id)
	if err != nil {
		return Task{}, err
	}
	if vm.Status != VMReady {
		return Task{}, fmt.Errorf("%s: %w", id, ErrVMBusy)
	}
	return h.startTask(kind, id, h.cfg.TaskDelay, func() error {
		v, err := h.lookup(id)
		if err != nil {
			return err
		}
		apply(v)
		return nil
	}, nil), nil
}

func (h *Hypervisor) PowerOn(id string) (Task, error) {
	return h.startOnReadyVM("power-on", id, func(v *VM) { v.Power = PowerOn })
}

func (h *Hypervisor) PowerOff(id string) (Task, error) {
	return h.startOnReadyVM("power-off", id, func(v *VM) { v.Power = PowerOff })
}

func (h *Hypervisor) Relocate(id, hostID string) (Task, error) {
	return h.startOnReadyVM("relocate", id, func(v *VM) { v.HostID = hostID })
}

func (h *Hypervisor) Delete(id string) (Task, error) {
	return h.startOnReadyVM("delete", id, func(v *VM) { delete(h.vms, v.ID) })
}

// DeleteOutOfBand removes a VM instantly with no task, simulating an admin
// deleting it directly in the hypervisor behind the control plane's back.
func (h *Hypervisor) DeleteOutOfBand(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.lookup(id); err != nil {
		return err
	}
	delete(h.vms, id)
	slog.Warn("vm deleted out-of-band", "vm", id)
	return nil
}

func (h *Hypervisor) GetTask(id string) (Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("%s: %w", id, ErrTaskNotFound)
	}
	return *t, nil
}

func (h *Hypervisor) GetVM(id string) (VM, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	vm, err := h.lookup(id)
	if err != nil {
		return VM{}, err
	}
	return *vm, nil
}

// ListVMs returns all VMs, or only those with the given external ID.
func (h *Hypervisor) ListVMs(externalID string) []VM {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []VM{}
	for _, vm := range h.vms {
		if externalID == "" || vm.ExternalID == externalID {
			out = append(out, *vm)
		}
	}
	slices.SortFunc(out, func(a, b VM) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (h *Hypervisor) Faults() Faults {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.faults
}

func (h *Hypervisor) SetFaults(f Faults) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.faults = f
	slog.Warn("faults updated", "faults", f)
}

// takeLostResponse consumes one timeout-after-success charge, if any.
func (h *Hypervisor) takeLostResponse() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.faults.TimeoutAfterSuccessCount == 0 {
		return false
	}
	h.faults.TimeoutAfterSuccessCount--
	return true
}
