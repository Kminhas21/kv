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
	hv   Hypervisor
	now  func() time.Time

	isLeader func() bool // nil = always reconcile
}

func NewReconciler(repo *Repo, hv Hypervisor) *Reconciler {
	return &Reconciler{repo: repo, hv: hv, now: time.Now}
}

// LeaderOnly makes Run skip every tick for which isLeader returns false, so
// only the replica holding the lease reconciles. Tick itself is unaffected.
func (r *Reconciler) LeaderOnly(isLeader func() bool) *Reconciler {
	r.isLeader = isLeader
	return r
}

// Run calls Tick every interval until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	slog.Info("reconciler started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for ctx.Err() == nil { // never start a new tick once shutting down
		if r.isLeader == nil || r.isLeader() {
			r.runTick(ctx)
		}

		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-ticker.C:
		}
	}
}

// runTick runs one Tick. A tick that started as leader may finish after
// leadership is lost: it is bounded at 8s and every write is compare-and-swap.
func (r *Reconciler) runTick(ctx context.Context) {
	// Detached from ctx so a shutdown lets the tick finish; 8s fits serve.ShutdownTimeout.
	tickCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	if err := r.Tick(tickCtx); err != nil {
		slog.Error("reconcile tick failed", "err", err)
	}
	cancel()
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
	case v.DesiredPower == DesiredOn && v.ProviderVMID == "":
		return r.provision(ctx, v)
	case v.ProviderVMID != "":
		return r.converge(ctx, v)
	}
	return nil
}

// observeRefresh is how long an unchanged observation may go unsaved. Without
// it, every VM would be rewritten every tick just to move last_observed_at,
// which for a large fleet means constant writes for no new information.
const observeRefresh = 15 * time.Second

// converge runs every tick for a VM that exists on the hypervisor:
//
//  1. If a power task is in flight, check whether it finished.
//  2. Observe the hypervisor's view of the VM.
//  3. Act only on a difference between desired and observed state, and
//     complete the operation once they match.
//
// Whether a power-on worked is judged by observing the VM, not by trusting
// the task result: observation is the ground truth.
func (r *Reconciler) converge(ctx context.Context, v VM) error {
	orig := v
	now := r.now().UTC()
	log := slog.With("vm_id", v.ID, "provider_vm_id", v.ProviderVMID)

	var op *Operation
	if v.CurrentOpID != "" {
		o, err := r.repo.GetOp(ctx, v.CurrentOpID)
		if err != nil {
			return fmt.Errorf("load operation %s: %w", v.CurrentOpID, err)
		}
		op = &o
		log = log.With("op_id", o.ID)
	}

	// 1. A power task in flight?
	taskInFlight := false
	if op != nil && op.ProviderTaskID != "" {
		task, err := r.hv.GetTask(ctx, op.ProviderTaskID)
		switch {
		case err != nil && !errors.Is(err, ErrProviderNotFound):
			v.ObservedPower, v.LastError = ObservedUnknown, "hypervisor unreachable: "+err.Error()
			return r.saveStatus(ctx, orig, v)
		case err == nil && task.State == TaskRunning:
			taskInFlight = true
		default:
			if err == nil && task.State == TaskFailed {
				log.Warn("power-on task failed; will retry", "step", "poll", "provider_task_id", task.ID, "err", task.Error)
				op.LastError = "power-on task failed: " + task.Error
			}
			op.ProviderTaskID = ""
			op.UpdatedAt = now
			saved, err := r.repo.UpdateOp(ctx, *op)
			if err != nil {
				return ignoreConflict(saved, err)
			}
			op = &saved
		}
	}

	// 2. Observe.
	pv, err := r.hv.GetVM(ctx, v.ProviderVMID)
	switch {
	case errors.Is(err, ErrProviderNotFound):
		if orig.ObservedPower != ObservedAbsent {
			log.Warn("vm missing on hypervisor", "step", "observe")
		}
		v.ObservedPower, v.LastError = ObservedAbsent, "vm is missing on the hypervisor"
		return r.saveStatus(ctx, orig, v)
	case err != nil:
		if orig.ObservedPower != ObservedUnknown {
			log.Warn("observe failed; desired state unchanged", "step", "observe", "err", err)
		}
		v.ObservedPower, v.LastError = ObservedUnknown, "hypervisor unreachable: "+err.Error()
		return r.saveStatus(ctx, orig, v)
	}
	v.ObservedPower, v.LastError = pv.Power, ""

	// 3. Act.
	switch {
	case !pv.Ready:
		// Still being built (e.g. adopted mid-create): nothing to do yet.

	case v.DesiredPower == DesiredOn && pv.Power == ObservedOn:
		v.Phase = PhaseRunning
		if op != nil {
			if op.Status != OpSucceeded {
				op.Status, op.LastError, op.UpdatedAt = OpSucceeded, "", now
				if _, err := r.repo.UpdateOp(ctx, *op); err != nil {
					return ignoreConflict(*op, err)
				}
				log.Info("operation succeeded; vm running", "step", "converged",
					"op_type", op.Type, "attempts", op.Attempts, "took", now.Sub(op.CreatedAt).Round(time.Millisecond))
			}
			v.CurrentOpID = ""
		}

	case v.DesiredPower == DesiredOn && pv.Power == ObservedOff && op != nil && !taskInFlight:
		taskID, err := r.hv.PowerOn(ctx, pv.ID)
		switch {
		case errors.Is(err, ErrProviderBusy):
			// The hypervisor says "not yet"; try again next tick.
		case errors.Is(err, ErrProviderRejected):
			log.Error("hypervisor rejected power-on", "step", "power-on", "err", err)
			return r.failOp(ctx, v, *op, err.Error())
		case err != nil:
			log.Warn("power-on request failed; will retry", "step", "power-on", "err", err)
			v.LastError = "power-on failed: " + err.Error()
		default:
			log.Info("power-on submitted", "step", "power-on", "provider_task_id", taskID)
			op.ProviderTaskID, op.UpdatedAt = taskID, now
			if _, err := r.repo.UpdateOp(ctx, *op); err != nil {
				return ignoreConflict(*op, err)
			}
		}
	}

	return r.saveStatus(ctx, orig, v)
}

// saveStatus writes next only if something meaningful changed since orig, or
// if a successful observation has not been saved for observeRefresh.
// last_observed_at only moves on a successful observation: it answers "when
// did we last actually see this VM?".
func (r *Reconciler) saveStatus(ctx context.Context, orig, next VM) error {
	now := r.now().UTC()
	changed := next.ObservedPower != orig.ObservedPower ||
		next.Phase != orig.Phase ||
		next.LastError != orig.LastError ||
		next.CurrentOpID != orig.CurrentOpID
	seen := next.ObservedPower != ObservedUnknown
	stale := seen && now.Sub(orig.LastObservedAt) >= observeRefresh
	if !changed && !stale {
		return nil
	}
	if seen {
		next.LastObservedAt = now
	}
	next.UpdatedAt = now
	return ignoreConflict(r.repo.UpdateVM(ctx, next))
}

// provision makes the VM exist on the hypervisor, one step per tick:
//
//  1. A saved task ID means a create is in flight: poll it, never re-create.
//  2. Otherwise look up the hypervisor by external ID before creating. A hit
//     means an earlier create succeeded but we never learned of it (lost
//     response, or a crash before saving the task ID): adopt that VM.
//  3. Only after a definite "not found" do we create, saving intent (op
//     RUNNING) before the call and the task ID right after it.
//
// A create that errors without a definite rejection is marked UNKNOWN, not
// FAILED: the VM may exist. Step 2 on the next tick finds out which.
func (r *Reconciler) provision(ctx context.Context, v VM) error {
	if v.CurrentOpID == "" {
		return errors.New("vm needs provisioning but has no current operation")
	}
	op, err := r.repo.GetOp(ctx, v.CurrentOpID)
	if err != nil {
		return fmt.Errorf("load operation %s: %w", v.CurrentOpID, err)
	}
	log := slog.With("vm_id", v.ID, "op_id", op.ID, "external_id", v.ExternalID)

	if op.ProviderTaskID != "" {
		return r.pollCreate(ctx, v, op, log)
	}

	found, err := r.hv.FindByExternalID(ctx, v.ExternalID)
	if err != nil {
		log.Warn("hypervisor lookup failed; not creating until we can see", "step", "lookup", "err", err)
		return r.noteError(ctx, v, "hypervisor unreachable: "+err.Error())
	}
	if len(found) > 0 {
		if len(found) > 1 {
			log.Error("duplicate VMs on hypervisor for one external ID", "count", len(found))
		}
		log.Info("adopting VM found on hypervisor", "step", "adopt", "provider_vm_id", found[0].ID)
		return r.recordProviderVM(ctx, v, op, found[0].ID)
	}

	op.Status = OpRunning
	op.Attempts++
	op.LastError = ""
	op.UpdatedAt = r.now().UTC()
	if op, err = r.repo.UpdateOp(ctx, op); err != nil {
		return ignoreConflict(op, err)
	}
	if v.Phase != PhaseProvisioning {
		v.Phase = PhaseProvisioning
		v.UpdatedAt = op.UpdatedAt
		if v, err = r.repo.UpdateVM(ctx, v); err != nil {
			return ignoreConflict(v, err)
		}
	}

	taskID, err := r.hv.CreateVM(ctx, ProviderCreate{
		ExternalID: v.ExternalID, Name: v.Name, CPU: v.CPU, MemoryMB: v.MemoryMB, HostID: v.HostID,
	})
	switch {
	case errors.Is(err, ErrProviderRejected):
		log.Error("hypervisor rejected create", "step", "create", "err", err)
		return r.failOp(ctx, v, op, err.Error())
	case err != nil:
		log.Warn("create outcome unknown; will look up before retrying", "step", "create", "err", err)
		op.Status = OpUnknown
		op.LastError = err.Error()
		op.UpdatedAt = r.now().UTC()
		return ignoreConflict(r.repo.UpdateOp(ctx, op))
	}

	log.Info("create submitted", "step", "create", "provider_task_id", taskID, "host", v.HostID, "attempt", op.Attempts)
	op.ProviderTaskID = taskID
	op.UpdatedAt = r.now().UTC()
	return ignoreConflict(r.repo.UpdateOp(ctx, op))
}

func (r *Reconciler) pollCreate(ctx context.Context, v VM, op Operation, log *slog.Logger) error {
	log = log.With("provider_task_id", op.ProviderTaskID)
	task, err := r.hv.GetTask(ctx, op.ProviderTaskID)
	switch {
	case errors.Is(err, ErrProviderNotFound):
		// The hypervisor forgot the task (e.g. it restarted). Drop it and
		// fall back to lookup-by-external-ID on the next tick.
		log.Warn("hypervisor has no record of the task; will look up by external ID", "step", "poll")
		op.ProviderTaskID = ""
		op.UpdatedAt = r.now().UTC()
		return ignoreConflict(r.repo.UpdateOp(ctx, op))
	case err != nil:
		log.Warn("polling task failed", "step", "poll", "err", err)
		return r.noteError(ctx, v, "hypervisor unreachable: "+err.Error())
	}

	switch task.State {
	case TaskRunning:
		return nil
	case TaskSucceeded:
		log.Info("create task succeeded", "step", "poll", "provider_vm_id", task.VMID)
		return r.recordProviderVM(ctx, v, op, task.VMID)
	default:
		log.Warn("create task failed; will retry", "step", "poll", "err", task.Error, "attempt", op.Attempts)
		op.ProviderTaskID = ""
		op.LastError = "create task failed: " + task.Error
		op.UpdatedAt = r.now().UTC()
		return ignoreConflict(r.repo.UpdateOp(ctx, op))
	}
}

// recordProviderVM links the VM to the hypervisor's VM. The operation stays
// RUNNING (the VM still has to be powered on) and its task slot is cleared,
// since the create task is done and the slot is next used for power-on.
func (r *Reconciler) recordProviderVM(ctx context.Context, v VM, op Operation, providerVMID string) error {
	if op.Status != OpRunning || op.LastError != "" || op.ProviderTaskID != "" {
		op.Status = OpRunning
		op.LastError = ""
		op.ProviderTaskID = ""
		op.UpdatedAt = r.now().UTC()
		if _, err := r.repo.UpdateOp(ctx, op); err != nil {
			return ignoreConflict(op, err)
		}
	}
	v.ProviderVMID = providerVMID
	v.Phase = PhaseProvisioning
	v.LastError = ""
	v.UpdatedAt = r.now().UTC()
	return ignoreConflict(r.repo.UpdateVM(ctx, v))
}

func (r *Reconciler) failOp(ctx context.Context, v VM, op Operation, reason string) error {
	op.Status = OpFailed
	op.LastError = reason
	op.UpdatedAt = r.now().UTC()
	if _, err := r.repo.UpdateOp(ctx, op); err != nil {
		return ignoreConflict(op, err)
	}
	v.Phase = PhaseFailed
	v.LastError = reason
	v.UpdatedAt = op.UpdatedAt
	return ignoreConflict(r.repo.UpdateVM(ctx, v))
}

// noteError records a transient problem on the VM without changing its
// desired state or phase, and only writes when the message changes.
func (r *Reconciler) noteError(ctx context.Context, v VM, msg string) error {
	if v.LastError == msg {
		return nil
	}
	v.LastError = msg
	v.UpdatedAt = r.now().UTC()
	return ignoreConflict(r.repo.UpdateVM(ctx, v))
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
