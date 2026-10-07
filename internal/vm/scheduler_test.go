package vm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPickHost(t *testing.T) {
	small := Spec{Name: "s", CPU: 2, MemoryMB: 1024}
	host := func(id string, cpu, mem int, res map[string]Reservation) Host {
		return Host{ID: id, CPUTotal: cpu, MemoryMBTotal: mem, Reservations: res}
	}
	full := map[string]Reservation{"other": {CPU: 4, MemoryMB: 4096}}

	tests := []struct {
		name   string
		hosts  []Host
		spec   Spec
		want   string
		wantOK bool
	}{
		{"first host with room, by ID", []Host{host("b", 4, 4096, nil), host("a", 4, 4096, nil)}, small, "a", true},
		{"skips a full host", []Host{host("a", 4, 4096, full), host("b", 4, 4096, nil)}, small, "b", true},
		{"memory must fit too", []Host{host("a", 64, 512, nil), host("b", 4, 4096, nil)}, small, "b", true},
		{"exact fit is a fit", []Host{host("a", 2, 1024, nil)}, small, "a", true},
		{"nothing fits", []Host{host("a", 1, 4096, nil)}, small, "", false},
		{"existing reservation wins over first-fit", []Host{
			host("a", 8, 8192, nil),
			host("b", 8, 8192, map[string]Reservation{"vm-x": {CPU: 2, MemoryMB: 1024}}),
		}, small, "b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickHost(tt.hosts, "vm-x", tt.spec)
			if ok != tt.wantOK || got.ID != tt.want {
				t.Fatalf("pickHost = %q, %v; want %q, %v", got.ID, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// offlineHypervisor fails every call, so in these scheduling-only tests the
// provision step always stops at "could not look up, not creating".
type offlineHypervisor struct{}

var errOffline = errors.New("hypervisor offline (scheduler tests)")

func (offlineHypervisor) CreateVM(context.Context, ProviderCreate) (string, error) {
	return "", errOffline
}
func (offlineHypervisor) GetTask(context.Context, string) (ProviderTask, error) {
	return ProviderTask{}, errOffline
}
func (offlineHypervisor) FindByExternalID(context.Context, string) ([]ProviderVM, error) {
	return nil, errOffline
}
func (offlineHypervisor) GetVM(context.Context, string) (ProviderVM, error) {
	return ProviderVM{}, errOffline
}
func (offlineHypervisor) PowerOn(context.Context, string) (string, error) { return "", errOffline }

func (e *testEnv) reconciler() *Reconciler { return NewReconciler(e.repo, offlineHypervisor{}) }

// seed replaces the default hosts with small ones that are easy to fill.
func (e *testEnv) seed(hosts ...Host) {
	e.t.Helper()
	if err := SeedHosts(context.Background(), e.repo, hosts); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) tick(r *Reconciler) {
	e.t.Helper()
	if err := r.Tick(context.Background()); err != nil {
		e.t.Fatalf("tick: %v", err)
	}
}

func (e *testEnv) vm(id string) VM {
	e.t.Helper()
	v, err := e.repo.GetVM(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *testEnv) hosts() map[string]Host {
	e.t.Helper()
	hs, err := e.repo.ListHosts(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]Host{}
	for _, h := range hs {
		m[h.ID] = h
	}
	return m
}

func (e *testEnv) request(key string, cpu int) string {
	e.t.Helper()
	code, res := e.createVM(key, fmt.Sprintf(`{"name":"vm-%s","cpu":%d,"memory_mb":1024}`, key, cpu))
	if code != 202 {
		e.t.Fatalf("create %s = %d %v", key, code, res)
	}
	return res["vm_id"]
}

func TestSchedule_PlacesPendingVMAndReserves(t *testing.T) {
	e := newEnv(t)
	e.seed(Host{ID: "host-a", CPUTotal: 4, MemoryMBTotal: 8192})
	id := e.request("a", 2)

	e.tick(e.reconciler())

	v := e.vm(id)
	if v.HostID != "host-a" || v.Phase != PhasePending || v.LastError != "" {
		t.Fatalf("vm = host %q phase %s err %q; want host-a, still Pending (provisioning is ticket 10), no error",
			v.HostID, v.Phase, v.LastError)
	}
	if r, ok := e.hosts()["host-a"].Reservations[id]; !ok || r.CPU != 2 {
		t.Fatalf("host-a reservations = %v, want 2 vCPU for %s", e.hosts()["host-a"].Reservations, id)
	}
}

func TestSchedule_FillsThenSpills(t *testing.T) {
	e := newEnv(t)
	e.seed(Host{ID: "host-a", CPUTotal: 4, MemoryMBTotal: 8192}, Host{ID: "host-b", CPUTotal: 4, MemoryMBTotal: 8192})
	ids := []string{e.request("1", 2), e.request("2", 2), e.request("3", 2)}

	e.tick(e.reconciler())

	count := map[string]int{}
	for _, id := range ids {
		count[e.vm(id).HostID]++
	}
	if count["host-a"] != 2 || count["host-b"] != 1 {
		t.Fatalf("placement = %v, want 2 on host-a (full) and 1 on host-b", count)
	}
}

func TestSchedule_TooBigStaysPendingThenFitsWhenCapacityAppears(t *testing.T) {
	e := newEnv(t)
	e.seed(Host{ID: "host-a", CPUTotal: 4, MemoryMBTotal: 8192})
	id := e.request("big", 8)
	r := e.reconciler()

	e.tick(r)
	v := e.vm(id)
	if v.HostID != "" || v.Phase != PhasePending || v.LastError == "" {
		t.Fatalf("oversized vm = host %q phase %s err %q; want no host, Pending, unschedulable error", v.HostID, v.Phase, v.LastError)
	}
	versionAfterFirstTick := v.Version
	e.tick(r)
	if e.vm(id).Version != versionAfterFirstTick {
		t.Error("an unchanged unschedulable VM was rewritten on the second tick; writes should happen only on change")
	}

	e.seed(Host{ID: "host-z", CPUTotal: 16, MemoryMBTotal: 32768})
	e.tick(r)
	if v := e.vm(id); v.HostID != "host-z" || v.LastError != "" {
		t.Fatalf("after adding a big host: host %q err %q; want host-z and the error cleared", v.HostID, v.LastError)
	}
}

func TestSchedule_ReusesReservationLeftByCrash(t *testing.T) {
	e := newEnv(t)
	e.seed(Host{ID: "host-a", CPUTotal: 8, MemoryMBTotal: 8192}, Host{ID: "host-b", CPUTotal: 8, MemoryMBTotal: 8192})
	id := e.request("a", 2)

	// Simulate a crash after "reserve" but before "assign": host-b (not the
	// first-fit choice) already holds a reservation for this VM.
	hb := e.hosts()["host-b"]
	hb.Reservations[id] = Reservation{CPU: 2, MemoryMB: 1024}
	if _, err := e.repo.UpdateHost(context.Background(), hb); err != nil {
		t.Fatal(err)
	}

	e.tick(e.reconciler())

	if got := e.vm(id).HostID; got != "host-b" {
		t.Fatalf("host = %q, want host-b (reuse the reservation, don't reserve twice)", got)
	}
	if _, onA := e.hosts()["host-a"].Reservations[id]; onA {
		t.Fatal("vm reserved on host-a as well: capacity leaked")
	}
}

// Many schedulers place different VMs onto the same hosts at the same
// moment: the read-modify-write on a host record races. Compare-and-swap
// must keep every host within capacity however the writes interleave.
func TestSchedule_ConcurrentSchedulersNeverOverbook(t *testing.T) {
	e := newEnv(t)
	e.seed(Host{ID: "host-a", CPUTotal: 4, MemoryMBTotal: 8192}, Host{ID: "host-b", CPUTotal: 4, MemoryMBTotal: 8192})
	var ids []string
	for i := range 10 {
		ids = append(ids, e.request(fmt.Sprint(i), 2))
	}

	r := e.reconciler()
	for range 5 {
		var wg sync.WaitGroup
		for _, id := range ids {
			v := e.vm(id)
			if v.HostID != "" {
				continue
			}
			wg.Go(func() { r.schedule(context.Background(), v) })
		}
		wg.Wait()
	}

	for id, h := range e.hosts() {
		if cpu, mem := h.Free(); cpu < 0 || mem < 0 {
			t.Errorf("%s overbooked: free cpu %d, mem %d, reservations %v", id, cpu, mem, h.Reservations)
		}
	}
	scheduled := 0
	for _, id := range ids {
		if v := e.vm(id); v.HostID != "" {
			scheduled++
			if _, ok := e.hosts()[v.HostID].Reservations[id]; !ok {
				t.Errorf("%s assigned to %s without a reservation there", id, v.HostID)
			}
		}
	}
	if scheduled != 4 {
		t.Errorf("%d VMs scheduled, want exactly 4 (8 vCPU total / 2 each)", scheduled)
	}
}
