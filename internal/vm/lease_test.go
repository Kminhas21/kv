package vm_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kminhas21/kv/internal/vm"
)

const (
	testTTL   = 300 * time.Millisecond
	testRenew = 50 * time.Millisecond
)

// replica is one in-process vmctl: a lease loop plus a leader-only reconciler,
// sharing kv and mockhv with the others.
type replica struct {
	lease  *vm.Lease
	ticks  atomic.Int64 // hypervisor observations made by this replica's reconciler
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *system) startReplica(id string) *replica {
	r := &replica{lease: vm.NewLease(s.kvc, id, testTTL, testRenew), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	hv := &countingHV{Hypervisor: s.hvc, n: &r.ticks}
	leaseDone := make(chan struct{})
	go func() { r.lease.Run(ctx); close(leaseDone) }()
	go func() {
		vm.NewReconciler(s.repo, hv).LeaderOnly(r.lease.IsLeader).Run(ctx, 20*time.Millisecond)
		<-leaseDone
		close(r.done)
	}()
	s.t.Cleanup(r.stop)
	return r
}

// countingHV counts GetVM calls: every tick observes each Running VM once,
// so this moves if and only if this replica is reconciling.
type countingHV struct {
	vm.Hypervisor
	n *atomic.Int64
}

func (h *countingHV) GetVM(ctx context.Context, id string) (vm.ProviderVM, error) {
	h.n.Add(1)
	return h.Hypervisor.GetVM(ctx, id)
}

// stop is a crash as far as the lease goes: no Release, so the key must expire.
func (r *replica) stop() {
	r.cancel()
	<-r.done
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting %v for: %s", within, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func leaders(rs []*replica) []int {
	var out []int
	for i, r := range rs {
		if r.lease.IsLeader() {
			out = append(out, i)
		}
	}
	return out
}

func TestLease_ThreeReplicasOneReconcilerAndHandoff(t *testing.T) {
	s := newSystem(t)
	rs := []*replica{s.startReplica("vmctl-a"), s.startReplica("vmctl-b"), s.startReplica("vmctl-c")}

	// Split-brain watch: sample all replicas for the whole test; never two leaders.
	var maxLeaders atomic.Int64
	stopWatch, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			if n := int64(len(leaders(rs))); n > maxLeaders.Load() {
				maxLeaders.Store(n)
			}
			select {
			case <-stopWatch:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	defer func() {
		close(stopWatch)
		<-watchDone
		if n := maxLeaders.Load(); n > 1 {
			t.Errorf("saw %d replicas reporting leader at once", n)
		}
	}()

	waitFor(t, "a leader", 2*time.Second, func() bool { return len(leaders(rs)) == 1 })
	first := leaders(rs)[0]
	steady := s.create("steady").VMID
	waitFor(t, "steady VM Running", 5*time.Second, func() bool { return s.vm(steady).Phase == vm.PhaseRunning })

	// Exactly one reconciles: over many ticks, only the leader observes VMs.
	before := make([]int64, len(rs))
	for i, r := range rs {
		before[i] = r.ticks.Load()
	}
	waitFor(t, "leader to tick", 2*time.Second, func() bool { return rs[first].ticks.Load() >= before[first]+10 })
	for i, r := range rs {
		if i != first && r.ticks.Load() != before[i] {
			t.Fatalf("follower %d ticked %d times while %d was leader", i, r.ticks.Load()-before[i], first)
		}
	}

	ids := []string{steady}
	for i := range 4 {
		ids = append(ids, s.create(fmt.Sprintf("before-%d", i)).VMID)
	}
	// Kill the leader while those are mid-provisioning.
	waitFor(t, "provisioning to start", 2*time.Second, func() bool { return s.vm(ids[1]).HostID != "" })
	killed := time.Now()
	rs[first].stop()

	waitFor(t, "a new leader", 2*time.Second, func() bool {
		l := leaders(rs)
		return len(l) > 0 && l[0] != first
	})
	took := time.Since(killed)
	second := leaders(rs)[0]
	// Ideal worst case is TTL + one acquire interval; 2xTTL leaves slack for slow CI.
	if took > 2*testTTL {
		t.Fatalf("takeover took %v, want about one TTL (%v)", took, testTTL)
	}
	t.Logf("replica %d took over %v after leader %d stopped", second, took.Round(time.Millisecond), first)

	for i := range 4 {
		ids = append(ids, s.create(fmt.Sprintf("after-%d", i)).VMID)
	}
	for _, id := range ids {
		waitFor(t, "vm "+id+" Running", 5*time.Second, func() bool { return s.vm(id).Phase == vm.PhaseRunning })
		if n := s.providerVMs(s.vm(id).ExternalID); n != 1 {
			t.Fatalf("vm %s has %d provider VMs, want exactly 1", id, n)
		}
	}
	if n := len(leaders(rs)); n != 1 {
		t.Fatalf("%d leaders at the end, want 1", n)
	}
}

func TestLease_ReleaseHandsOverWithoutWaitingForTTL(t *testing.T) {
	s := newSystem(t)
	a := vm.NewLease(s.kvc, "a", time.Hour, time.Minute) // a TTL we could never wait out
	b := vm.NewLease(s.kvc, "b", time.Hour, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	waitFor(t, "a to lead", 2*time.Second, a.IsLeader)
	cancel()
	<-done
	a.Release(context.Background())
	if a.IsLeader() {
		t.Fatal("a still leader after Release")
	}

	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { b.Run(ctx); close(done) }() // first attempt is immediate
	defer func() { cancel(); <-done }()
	waitFor(t, "b to lead after release", time.Second, b.IsLeader)

	if v, _, err := s.kvc.Get(context.Background(), vm.LeaseKey); err != nil || v != "b" {
		t.Fatalf("lease value = %q, %v; want b", v, err)
	}
}
