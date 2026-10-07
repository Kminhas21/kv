package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
)

// Reconciler drives every VM's observed state toward its desired state, one
// step per VM per tick. All progress is persisted in kv, so a reconciler
// that restarts (or a brand new one) picks up exactly where the last left off.
type Reconciler struct {
	repo *Repo
	now  func() time.Time
}

func NewReconciler(repo *Repo) *Reconciler {
	return &Reconciler{repo: repo, now: time.Now}
}

// Run calls Tick every interval until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	slog.Info("reconciler started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		tickCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := r.Tick(tickCtx); err != nil {
			slog.Error("reconcile tick failed", "err", err)
		}
		cancel()

		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-ticker.C:
		}
	}
}

// Tick makes one pass over all VMs. Tests call it directly instead of Run,
// so they control exactly when reconciliation happens.
func (r *Reconciler) Tick(ctx context.Context) error {
	vms, err := r.repo.ListVMs(ctx)
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}
	var errs []error
	for _, v := range vms {
		// One broken VM must not stop the others from making progress.
		if err := r.reconcileVM(ctx, v); err != nil {
			errs = append(errs, fmt.Errorf("vm %s: %w", v.ID, err))
		}
	}
	return errors.Join(errs...)
}

// reconcileVM takes at most one step for v. Later tickets add steps here
// (create on the hypervisor, power on, observe, delete, migrate).
func (r *Reconciler) reconcileVM(ctx context.Context, v VM) error {
	switch {
	case v.DesiredPower == DesiredOn && v.HostID == "":
		return r.schedule(ctx, v)
	}
	return nil
}

// schedule reserves capacity on a host, then records the host on the VM.
// Both writes are compare-and-swap: on a conflict we stop and let the next
// tick re-read fresh state, never overwrite. The order (reserve, then
// assign) means a crash in between leaves a reservation keyed by this VM,
// which pickHost finds and reuses on the next tick.
func (r *Reconciler) schedule(ctx context.Context, v VM) error {
	hosts, err := r.repo.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}

	host, ok := pickHost(hosts, v.ID, v.Spec)
	if !ok {
		msg := fmt.Sprintf("unschedulable: no host has %d vCPU and %d MB free", v.CPU, v.MemoryMB)
		if v.LastError != msg {
			slog.Warn("vm unschedulable", "vm_id", v.ID, "cpu", v.CPU, "memory_mb", v.MemoryMB)
			v.LastError = msg
			v.UpdatedAt = r.now().UTC()
			return ignoreConflict(r.repo.UpdateVM(ctx, v))
		}
		return nil
	}

	if _, held := host.Reservations[v.ID]; !held {
		if host.Reservations == nil {
			host.Reservations = map[string]Reservation{}
		}
		host.Reservations[v.ID] = Reservation{CPU: v.CPU, MemoryMB: v.MemoryMB}
		if _, err := r.repo.UpdateHost(ctx, host); err != nil {
			if errors.Is(err, kv.ErrConflict) {
				slog.Info("host changed while reserving, will retry", "vm_id", v.ID, "host", host.ID)
				return nil
			}
			return fmt.Errorf("reserve on %s: %w", host.ID, err)
		}
	}

	v.HostID = host.ID
	v.LastError = ""
	v.UpdatedAt = r.now().UTC()
	if _, err := r.repo.UpdateVM(ctx, v); err != nil {
		if errors.Is(err, kv.ErrConflict) {
			// The reservation stays; next tick pickHost finds and reuses it.
			slog.Info("vm changed while assigning host, will retry", "vm_id", v.ID, "host", host.ID)
			return nil
		}
		return fmt.Errorf("assign host: %w", err)
	}
	cpuFree, memFree := host.Free()
	slog.Info("vm scheduled", "vm_id", v.ID, "host", host.ID,
		"host_cpu_free", cpuFree, "host_memory_mb_free", memFree)
	return nil
}

// ignoreConflict turns a CAS conflict into "nothing to do this tick": the
// next tick re-reads the record and decides again from fresh state.
func ignoreConflict[T any](_ T, err error) error {
	if errors.Is(err, kv.ErrConflict) {
		return nil
	}
	return err
}
