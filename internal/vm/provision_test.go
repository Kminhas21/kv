package vm_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kminhas21/kv/internal/hvclient"
	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/kvclient"
	"github.com/Kminhas21/kv/internal/mockhv"
	"github.com/Kminhas21/kv/internal/vm"
)

// system runs real kv, real mockhv and the vmctl service in-process, all
// talking over HTTP exactly as in production.
type system struct {
	t    *testing.T
	repo *vm.Repo
	svc  *vm.Service
	hv   *mockhv.Hypervisor
	hvc  *hvclient.Client
	kvc  *kvclient.Client
}

const hvTimeout = 200 * time.Millisecond

func newSystem(t *testing.T) *system {
	t.Helper()
	kvSrv := httptest.NewServer(kv.NewHandler(kv.NewStore()))
	t.Cleanup(kvSrv.Close)
	hv := mockhv.New(mockhv.Config{TaskDelay: 30 * time.Millisecond, HangFor: time.Second})
	hvSrv := httptest.NewServer(hv.Handler())
	t.Cleanup(hvSrv.Close)

	kvc := kvclient.New(kvSrv.URL)
	repo := vm.NewRepo(kvc)
	if err := vm.SeedHosts(context.Background(), repo, vm.DefaultHosts()); err != nil {
		t.Fatal(err)
	}
	return &system{t: t, repo: repo, svc: vm.NewService(repo), hv: hv, hvc: hvclient.New(hvSrv.URL, hvTimeout), kvc: kvc}
}

// newReconciler is a fresh controller with no memory of its own, exactly
// like a vmctl process that just restarted.
func (s *system) newReconciler() *vm.Reconciler { return vm.NewReconciler(s.repo, s.hvc) }

func (s *system) create(name string) vm.CreateResult {
	s.t.Helper()
	res, err := s.svc.CreateVM(context.Background(), "key-"+name, vm.Spec{Name: name, CPU: 2, MemoryMB: 2048})
	if err != nil {
		s.t.Fatal(err)
	}
	return res
}

func (s *system) vm(id string) vm.VM {
	s.t.Helper()
	v, err := s.repo.GetVM(context.Background(), id)
	if err != nil {
		s.t.Fatal(err)
	}
	return v
}

func (s *system) op(id string) vm.Operation {
	s.t.Helper()
	op, err := s.repo.GetOp(context.Background(), id)
	if err != nil {
		s.t.Fatal(err)
	}
	return op
}

// tickUntil ticks (pausing so hypervisor tasks can finish) until cond holds.
func (s *system) tickUntil(r *vm.Reconciler, what string, cond func() bool) {
	s.t.Helper()
	for range 40 {
		r.Tick(context.Background())
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("gave up waiting for: %s", what)
}

func (s *system) providerVMs(externalID string) int { return len(s.hv.ListVMs(externalID)) }

func TestProvision_CreatesOnHypervisorAndRecordsIt(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	r := s.newReconciler()

	s.tickUntil(r, "provider VM recorded", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	v := s.vm(res.VMID)
	if v.Phase != vm.PhaseProvisioning || v.HostID == "" {
		t.Errorf("vm phase %s host %q; want Provisioning on a host", v.Phase, v.HostID)
	}
	op := s.op(res.OperationID)
	if op.Status != vm.OpRunning || op.Attempts != 1 {
		t.Errorf("op = %+v; want RUNNING (power-on still to come) after exactly 1 create attempt", op)
	}
	if n := s.providerVMs(v.ExternalID); n != 1 {
		t.Fatalf("%d VMs on the hypervisor, want 1", n)
	}
}

func TestProvision_LookupBeforeCreateAdoptsExistingVM(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	v := s.vm(res.VMID)

	// The VM already exists on the hypervisor with our external ID, as if an
	// earlier vmctl created it and then died before saving anything.
	s.hv.CreateVM(mockhv.CreateRequest{ExternalID: v.ExternalID, Name: "web-1", CPU: 2, MemoryMB: 2048, HostID: "host-1"})

	s.tickUntil(s.newReconciler(), "adoption", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	if n := s.providerVMs(v.ExternalID); n != 1 {
		t.Fatalf("%d VMs on the hypervisor, want 1 (adopt, don't create)", n)
	}
	if op := s.op(res.OperationID); op.Attempts != 0 {
		t.Errorf("op attempts = %d, want 0: no create call should have been made", op.Attempts)
	}
}

func TestProvision_TimeoutAfterSuccessRecoversWithoutDuplicate(t *testing.T) {
	s := newSystem(t)
	f := s.hv.Faults()
	f.TimeoutAfterSuccessCount = 1
	s.hv.SetFaults(f)
	res := s.create("web-1")
	r := s.newReconciler()

	// Tick until the create is attempted: it times out after hvTimeout.
	s.tickUntil(r, "create attempted", func() bool { return s.op(res.OperationID).Attempts == 1 })

	op := s.op(res.OperationID)
	if op.Status != vm.OpUnknown || op.ProviderTaskID != "" {
		t.Fatalf("after the lost response op = %+v; want UNKNOWN with no task ID", op)
	}
	ext := s.vm(res.VMID).ExternalID
	if n := s.providerVMs(ext); n != 1 {
		t.Fatalf("%d VMs on the hypervisor right after the timeout, want 1 (the create did happen)", n)
	}

	s.tickUntil(r, "recovery", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	if n := s.providerVMs(ext); n != 1 {
		t.Fatalf("%d VMs on the hypervisor after recovery, want exactly 1", n)
	}
	if op := s.op(res.OperationID); op.Status != vm.OpRunning || op.Attempts != 1 {
		t.Errorf("op = %+v; want RUNNING after adoption, still 1 attempt", op)
	}
}

func TestProvision_OutageNeverCreatesBlind(t *testing.T) {
	s := newSystem(t)
	f := s.hv.Faults()
	f.Outage = true
	s.hv.SetFaults(f)
	res := s.create("web-1")
	r := s.newReconciler()

	for range 3 {
		r.Tick(context.Background())
	}
	v := s.vm(res.VMID)
	if !strings.Contains(v.LastError, "unreachable") || v.DesiredPower != vm.DesiredOn {
		t.Errorf("during outage: last error %q desired %s; want an unreachable error and desired still ON", v.LastError, v.DesiredPower)
	}
	if op := s.op(res.OperationID); op.Attempts != 0 {
		t.Fatalf("op attempts = %d during outage, want 0: never create without a successful lookup", op.Attempts)
	}

	f.Outage = false
	s.hv.SetFaults(f)
	s.tickUntil(r, "provisioned after outage", func() bool { return s.vm(res.VMID).ProviderVMID != "" })
	if v := s.vm(res.VMID); v.LastError != "" {
		t.Errorf("last error %q still set after recovery", v.LastError)
	}
}

func TestProvision_FailedTaskIsRetried(t *testing.T) {
	s := newSystem(t)
	f := s.hv.Faults()
	f.FailRate = 1
	s.hv.SetFaults(f)
	res := s.create("web-1")
	r := s.newReconciler()

	s.tickUntil(r, "a failed task observed", func() bool {
		return strings.Contains(s.op(res.OperationID).LastError, "create task failed")
	})

	f.FailRate = 0
	s.hv.SetFaults(f)
	s.tickUntil(r, "provisioned on retry", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	if op := s.op(res.OperationID); op.Attempts < 2 {
		t.Errorf("op attempts = %d, want >= 2 (first failed, a later one succeeded)", op.Attempts)
	}
	if n := s.providerVMs(s.vm(res.VMID).ExternalID); n != 1 {
		t.Fatalf("%d VMs on the hypervisor, want 1 (failed creates leave nothing behind)", n)
	}
}

func TestProvision_RestartMidCreateResumesFromSavedTask(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")

	first := s.newReconciler()
	s.tickUntil(first, "task submitted", func() bool { return s.op(res.OperationID).ProviderTaskID != "" })

	// "Kill" vmctl: throw the reconciler away and start a new one.
	second := s.newReconciler()
	s.tickUntil(second, "provisioned by the new reconciler", func() bool { return s.vm(res.VMID).ProviderVMID != "" })

	if op := s.op(res.OperationID); op.Attempts != 1 {
		t.Errorf("op attempts = %d; want 1: the new reconciler must poll the saved task, not create again", op.Attempts)
	}
	if n := s.providerVMs(s.vm(res.VMID).ExternalID); n != 1 {
		t.Fatalf("%d VMs on the hypervisor, want 1", n)
	}
}

func TestHVClient_ClassifiesRejection(t *testing.T) {
	s := newSystem(t)
	_, err := s.hvc.CreateVM(context.Background(), vm.ProviderCreate{ExternalID: "", CPU: 0})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("invalid create err = %v, want a 400", err)
	}
	if !errors.Is(err, vm.ErrProviderRejected) {
		t.Fatalf("400 from hypervisor = %v, want ErrProviderRejected", err)
	}
	if _, err := s.hvc.GetTask(context.Background(), "task-nope"); !errors.Is(err, vm.ErrProviderNotFound) {
		t.Fatalf("unknown task err = %v, want ErrProviderNotFound", err)
	}
}

func TestReconciler_RunReturnsWhenContextCancelled(t *testing.T) {
	s := newSystem(t)
	s.create("web-1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.newReconciler().Run(ctx, 10*time.Millisecond); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
