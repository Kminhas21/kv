package vm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Kminhas21/kv/internal/mockhv"
	"github.com/Kminhas21/kv/internal/vm"
)

func (s *system) running(id string) bool { return s.vm(id).Phase == vm.PhaseRunning }

func (s *system) setFaults(change func(*mockhv.Faults)) {
	f := s.hv.Faults()
	change(&f)
	s.hv.SetFaults(f)
}

func TestConverge_RequestToRunningEndToEnd(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")

	s.tickUntil(s.newReconciler(), "Running", func() bool { return s.running(res.VMID) })

	v := s.vm(res.VMID)
	if v.ObservedPower != vm.ObservedOn || v.DesiredPower != vm.DesiredOn || v.CurrentOpID != "" || v.LastError != "" {
		t.Errorf("vm = observed %s desired %s op %q err %q; want ON/ON, no current op, no error",
			v.ObservedPower, v.DesiredPower, v.CurrentOpID, v.LastError)
	}
	if v.LastObservedAt.IsZero() {
		t.Error("last_observed_at not set")
	}
	if op := s.op(res.OperationID); op.Status != vm.OpSucceeded || op.Attempts != 1 {
		t.Errorf("op = %s after %d attempts, want SUCCEEDED after 1", op.Status, op.Attempts)
	}
	pvs := s.hv.ListVMs(v.ExternalID)
	if len(pvs) != 1 || pvs[0].Power != mockhv.PowerOn || pvs[0].HostID != v.HostID {
		t.Fatalf("hypervisor VMs = %+v; want exactly one, powered ON, on %s", pvs, v.HostID)
	}
}

func TestConverge_SteadyStateDoesNotRewriteTheVM(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	r := s.newReconciler()
	s.tickUntil(r, "Running", func() bool { return s.running(res.VMID) })

	before := s.vm(res.VMID).Version
	for range 5 {
		r.Tick(context.Background())
	}
	if after := s.vm(res.VMID).Version; after != before {
		t.Fatalf("vm version went %d -> %d over 5 idle ticks; unchanged observations must not be written", before, after)
	}
}

func TestConverge_OutageAfterRunningKeepsDesiredState(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	r := s.newReconciler()
	s.tickUntil(r, "Running", func() bool { return s.running(res.VMID) })
	seenAt := s.vm(res.VMID).LastObservedAt

	s.setFaults(func(f *mockhv.Faults) { f.Outage = true })
	r.Tick(context.Background())
	v := s.vm(res.VMID)
	if v.ObservedPower != vm.ObservedUnknown || v.DesiredPower != vm.DesiredOn || !strings.Contains(v.LastError, "unreachable") {
		t.Fatalf("during outage: observed %s desired %s err %q; want UNKNOWN, still ON, unreachable", v.ObservedPower, v.DesiredPower, v.LastError)
	}
	if !v.LastObservedAt.Equal(seenAt) {
		t.Error("last_observed_at moved during an outage; it must mean 'last time we actually saw it'")
	}
	versionDuringOutage := v.Version
	for range 3 {
		r.Tick(context.Background())
	}
	if s.vm(res.VMID).Version != versionDuringOutage {
		t.Error("vm rewritten on every tick of the outage; only the transition should be written")
	}

	s.setFaults(func(f *mockhv.Faults) { f.Outage = false })
	r.Tick(context.Background())
	if v := s.vm(res.VMID); v.ObservedPower != vm.ObservedOn || v.LastError != "" {
		t.Fatalf("after outage: observed %s err %q; want ON and error cleared", v.ObservedPower, v.LastError)
	}
}

func TestConverge_RestartMidPowerOnFinishes(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	s.tickUntil(s.newReconciler(), "power-on submitted", func() bool {
		return s.vm(res.VMID).ProviderVMID != "" && s.op(res.OperationID).ProviderTaskID != ""
	})

	s.tickUntil(s.newReconciler(), "Running after restart", func() bool { return s.running(res.VMID) })

	if n := len(s.hv.ListVMs(s.vm(res.VMID).ExternalID)); n != 1 {
		t.Fatalf("%d hypervisor VMs, want 1", n)
	}
}

func TestConverge_VMDeletedBehindOurBackIsAbsentNotUnknown(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	r := s.newReconciler()
	s.tickUntil(r, "Running", func() bool { return s.running(res.VMID) })

	if err := s.hv.DeleteOutOfBand(s.vm(res.VMID).ProviderVMID); err != nil {
		t.Fatal(err)
	}
	r.Tick(context.Background())

	v := s.vm(res.VMID)
	if v.ObservedPower != vm.ObservedAbsent || v.DesiredPower != vm.DesiredOn {
		t.Fatalf("after out-of-band delete: observed %s desired %s; want ABSENT (the hypervisor answered), desired still ON",
			v.ObservedPower, v.DesiredPower)
	}
}

func TestConverge_FailedPowerOnIsRetried(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	r := s.newReconciler()
	s.tickUntil(r, "provisioned", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	s.setFaults(func(f *mockhv.Faults) { f.FailRate = 1 })
	s.tickUntil(r, "power-on failure recorded", func() bool {
		return strings.Contains(s.op(res.OperationID).LastError, "power-on task failed")
	})
	s.setFaults(func(f *mockhv.Faults) { f.FailRate = 0 })
	s.tickUntil(r, "Running after retry", func() bool { return s.running(res.VMID) })
}
