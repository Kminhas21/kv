package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/kvclient"
)

type testEnv struct {
	t    *testing.T
	url  string
	repo *Repo
}

// newEnv runs a real kv server and the vmctl API in-process.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	kvSrv := httptest.NewServer(kv.NewHandler(kv.NewStore()))
	t.Cleanup(kvSrv.Close)
	repo := NewRepo(kvclient.New(kvSrv.URL))
	api := httptest.NewServer(NewAPI(NewService(repo)))
	t.Cleanup(api.Close)
	return &testEnv{t: t, url: api.URL, repo: repo}
}

func (e *testEnv) createVM(idemKey, body string) (int, map[string]string) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.url+"/vms", strings.NewReader(body))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]string{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *testEnv) countVMs() int {
	e.t.Helper()
	vms, err := e.repo.ListVMs(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return len(vms)
}

const web1 = `{"name":"web-1","cpu":2,"memory_mb":2048}`

func TestCreate_RecordsDesiredStateAndReturns202(t *testing.T) {
	e := newEnv(t)

	code, res := e.createVM("key-1", web1)
	if code != http.StatusAccepted || res["vm_id"] == "" || res["operation_id"] == "" {
		t.Fatalf("POST /vms = %d %v, want 202 with vm_id and operation_id", code, res)
	}

	ctx := context.Background()
	vm, err := e.repo.GetVM(ctx, res["vm_id"])
	if err != nil {
		t.Fatal(err)
	}
	if vm.DesiredPower != DesiredOn || vm.Phase != PhasePending || vm.ObservedPower != ObservedUnknown {
		t.Errorf("vm state = desired %s, phase %s, observed %s; want ON, Pending, UNKNOWN",
			vm.DesiredPower, vm.Phase, vm.ObservedPower)
	}
	if vm.ExternalID == "" || vm.CurrentOpID != res["operation_id"] || vm.CPU != 2 {
		t.Errorf("vm = %+v; want external ID set, current op %s, cpu 2", vm, res["operation_id"])
	}

	op, err := e.repo.GetOp(ctx, res["operation_id"])
	if err != nil {
		t.Fatal(err)
	}
	if op.Type != OpCreate || op.Status != OpPending || op.VMID != vm.ID {
		t.Errorf("op = %+v; want CREATE PENDING for %s", op, vm.ID)
	}
}

func TestCreate_SameKeySameBodyIsIdempotent(t *testing.T) {
	e := newEnv(t)

	_, first := e.createVM("key-1", web1)
	code, second := e.createVM("key-1", `{"memory_mb":2048,"cpu":2,"name":"web-1"}`)

	if code != http.StatusAccepted || second["vm_id"] != first["vm_id"] || second["operation_id"] != first["operation_id"] {
		t.Fatalf("retry = %d %v, want 202 with the same IDs as %v", code, second, first)
	}
	if n := e.countVMs(); n != 1 {
		t.Fatalf("%d VMs after a retried create, want 1", n)
	}
}

func TestCreate_DifferentKeysCreateDifferentVMs(t *testing.T) {
	e := newEnv(t)
	_, a := e.createVM("key-a", web1)
	_, b := e.createVM("key-b", web1)
	if a["vm_id"] == b["vm_id"] || e.countVMs() != 2 {
		t.Fatalf("two keys gave %v and %v with %d VMs, want two distinct VMs", a, b, e.countVMs())
	}
}

func TestCreate_SameKeyDifferentBodyIsRejected(t *testing.T) {
	e := newEnv(t)
	e.createVM("key-1", web1)

	code, res := e.createVM("key-1", `{"name":"web-1","cpu":4,"memory_mb":2048}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(res["error"], "idempotency") {
		t.Fatalf("reused key with new body = %d %v, want 422 mentioning idempotency", code, res)
	}
	if n := e.countVMs(); n != 1 {
		t.Fatalf("%d VMs, want 1", n)
	}
}

func TestCreate_BadRequests(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name, key, body string
		wantCode        int
		wantInError     []string
	}{
		{"missing key", "", web1, 400, []string{"Idempotency-Key"}},
		{"broken JSON", "k1", `{"name":`, 400, []string{"invalid JSON"}},
		{"unknown field", "k2", `{"name":"a","cpu":1,"memory_mb":512,"host_id":"host-9"}`, 400, []string{"host_id"}},
		{"all fields invalid", "k3", `{"name":"Web_1","cpu":0,"memory_mb":5}`, 422,
			[]string{"name", "cpu", "memory_mb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, res := e.createVM(tt.key, tt.body)
			if code != tt.wantCode {
				t.Fatalf("status = %d (%v), want %d", code, res, tt.wantCode)
			}
			for _, want := range tt.wantInError {
				if !strings.Contains(res["error"], want) {
					t.Errorf("error %q should mention %q", res["error"], want)
				}
			}
		})
	}
	if n := e.countVMs(); n != 0 {
		t.Fatalf("%d VMs after only bad requests, want 0", n)
	}
}

// A previous attempt claimed the key and then crashed before writing the
// operation and VM. The retry must finish the job with the claimed IDs.
func TestCreate_RetryAfterCrashFinishesPartialWrite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.repo.ClaimIdempotency(ctx, IdempotencyRecord{
		Key: "key-1", RequestHash: requestHash(Spec{Name: "web-1", CPU: 2, MemoryMB: 2048}),
		VMID: "vm-crashed", OpID: "op-crashed",
	}); err != nil {
		t.Fatal(err)
	}

	code, res := e.createVM("key-1", web1)

	if code != http.StatusAccepted || res["vm_id"] != "vm-crashed" || res["operation_id"] != "op-crashed" {
		t.Fatalf("retry = %d %v, want 202 with the IDs claimed before the crash", code, res)
	}
	if _, err := e.repo.GetVM(ctx, "vm-crashed"); err != nil {
		t.Errorf("VM not written by the retry: %v", err)
	}
	if _, err := e.repo.GetOp(ctx, "op-crashed"); err != nil {
		t.Errorf("operation not written by the retry: %v", err)
	}
}

func TestGet_UnknownIDsAre404(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/vms/vm-nope", "/operations/op-nope"} {
		resp, err := http.Get(e.url + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestRepo_UpdateWithStaleVersionConflicts(t *testing.T) {
	e := newEnv(t)
	_, res := e.createVM("key-1", web1)
	ctx := context.Background()

	a, _ := e.repo.GetVM(ctx, res["vm_id"])
	b, _ := e.repo.GetVM(ctx, res["vm_id"])

	a.HostID = "host-1"
	if _, err := e.repo.UpdateVM(ctx, a); err != nil {
		t.Fatalf("first update: %v", err)
	}
	b.HostID = "host-2"
	if _, err := e.repo.UpdateVM(ctx, b); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("second update from a stale read err = %v, want a version conflict", err)
	}
	got, _ := e.repo.GetVM(ctx, res["vm_id"])
	if got.HostID != "host-1" {
		t.Fatalf("host = %q, want host-1 (the stale write must not win)", got.HostID)
	}
}
