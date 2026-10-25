package vm_test

import (
	"context"
	"testing"
	"time"

	"github.com/Kminhas21/kv/internal/vm"
)

// slowHV blocks the first FindByExternalID until released.
type slowHV struct {
	vm.Hypervisor
	entered chan struct{}
	release chan struct{}
}

func (h *slowHV) FindByExternalID(ctx context.Context, id string) ([]vm.ProviderVM, error) {
	select {
	case h.entered <- struct{}{}:
		<-h.release
	default:
	}
	return h.Hypervisor.FindByExternalID(ctx, id)
}

func TestReconciler_CancelDuringTickLetsTickFinish(t *testing.T) {
	s := newSystem(t)
	res := s.create("web-1")
	s.tickUntil(s.newReconciler(), "scheduled", func() bool { return s.vm(res.VMID).HostID != "" })
	ext := s.vm(res.VMID).ExternalID

	hv := &slowHV{Hypervisor: s.hvc, entered: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { vm.NewReconciler(s.repo, hv).Run(ctx, time.Hour); close(done) }()

	<-hv.entered // tick is now blocked inside the provider call
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while its tick was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(hv.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the tick finished")
	}
	if n := s.providerVMs(ext); n != 1 {
		t.Fatalf("%d provider VMs after shutdown, want 1: the in-flight tick should have completed its create", n)
	}
}
