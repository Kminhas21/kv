package mockhv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type harness struct {
	t   *testing.T
	url string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	srv := httptest.NewServer(New(cfg).Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, url: srv.URL}
}

func fast() Config { return Config{TaskDelay: 20 * time.Millisecond, HangFor: 2 * time.Second} }

// call sends a JSON request and decodes a JSON response into out (if non-nil).
func (h *harness) call(method, path string, body any, out any) int {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, h.url+path, &buf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (h *harness) create(externalID string) string {
	h.t.Helper()
	var ref struct {
		TaskID string `json:"task_id"`
	}
	req := CreateRequest{ExternalID: externalID, Name: "web", CPU: 2, MemoryMB: 1024, HostID: "host-1"}
	if code := h.call("POST", "/tasks/create-vm", req, &ref); code != http.StatusAccepted {
		h.t.Fatalf("create status = %d, want 202", code)
	}
	return ref.TaskID
}

// waitTask polls until the task leaves "running", like a real control plane would.
func (h *harness) waitTask(id string) Task {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var task Task
		h.call("GET", "/tasks/"+id, nil, &task)
		if task.State != TaskRunning {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("task %s still running after 2s", id)
	return Task{}
}

func (h *harness) vmsByExternalID(externalID string) []VM {
	h.t.Helper()
	var vms []VM
	h.call("GET", "/vms?external_id="+externalID, nil, &vms)
	return vms
}

func TestCreate_TaskCompletesAndVMBecomesReady(t *testing.T) {
	h := newHarness(t, fast())

	taskID := h.create("ext-1")

	during := h.vmsByExternalID("ext-1")
	if len(during) != 1 || during[0].Status != VMCreating {
		t.Fatalf("during creation: %+v, want one VM with status creating", during)
	}

	task := h.waitTask(taskID)
	if task.State != TaskSuccess || task.VMID != during[0].ID {
		t.Fatalf("task = %+v, want success for %s", task, during[0].ID)
	}
	after := h.vmsByExternalID("ext-1")
	if len(after) != 1 || after[0].Status != VMReady || after[0].Power != PowerOff {
		t.Fatalf("after creation: %+v, want one ready VM, power OFF", after)
	}
}

func TestCreate_DoesNotDeduplicate(t *testing.T) {
	h := newHarness(t, fast())
	h.waitTask(h.create("same"))
	h.waitTask(h.create("same"))

	if vms := h.vmsByExternalID("same"); len(vms) != 2 {
		t.Fatalf("got %d VMs for one external ID, want 2: de-duplication is the control plane's job", len(vms))
	}
}

func TestPowerOnThenOff(t *testing.T) {
	h := newHarness(t, fast())
	vmID := h.waitTask(h.create("ext-1")).VMID

	var ref struct {
		TaskID string `json:"task_id"`
	}
	h.call("POST", "/vms/"+vmID+"/power-on", nil, &ref)
	h.waitTask(ref.TaskID)
	var vm VM
	h.call("GET", "/vms/"+vmID, nil, &vm)
	if vm.Power != PowerOn {
		t.Fatalf("power after power-on = %s, want ON", vm.Power)
	}

	h.call("POST", "/vms/"+vmID+"/power-off", nil, &ref)
	h.waitTask(ref.TaskID)
	h.call("GET", "/vms/"+vmID, nil, &vm)
	if vm.Power != PowerOff {
		t.Fatalf("power after power-off = %s, want OFF", vm.Power)
	}
}

func TestPowerOnWhileCreating_IsBusy(t *testing.T) {
	h := newHarness(t, Config{TaskDelay: time.Second, HangFor: time.Second})
	h.create("ext-1")
	id := h.vmsByExternalID("ext-1")[0].ID

	if code := h.call("POST", "/vms/"+id+"/power-on", nil, nil); code != http.StatusConflict {
		t.Fatalf("power-on while creating = %d, want 409", code)
	}
}

func TestTimeoutAfterSuccess_VMExistsAlthoughCallerTimedOut(t *testing.T) {
	h := newHarness(t, fast())
	h.call("PUT", "/faults", map[string]int{"timeout_after_success_count": 1}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	body, _ := json.Marshal(CreateRequest{ExternalID: "lost", Name: "web", CPU: 1, MemoryMB: 512, HostID: "host-1"})
	req, _ := http.NewRequestWithContext(ctx, "POST", h.url+"/tasks/create-vm", bytes.NewReader(body))
	_, err := http.DefaultClient.Do(req)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create err = %v, want a timeout", err)
	}
	vms := h.vmsByExternalID("lost")
	if len(vms) != 1 {
		t.Fatalf("after timed-out create: %d VMs with external ID, want 1 (the work happened)", len(vms))
	}

	h.create("next")
	if n := len(h.vmsByExternalID("next")); n != 1 {
		t.Fatalf("second create should respond normally (count was 1), got %d VMs", n)
	}
}

func TestOutage_Returns503ButFaultsStayReachable(t *testing.T) {
	h := newHarness(t, fast())
	h.call("PUT", "/faults", map[string]bool{"outage": true}, nil)

	if code := h.call("GET", "/vms", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /vms during outage = %d, want 503", code)
	}
	if code := h.call("GET", "/healthz", nil, nil); code != http.StatusOK {
		t.Fatalf("healthz during outage = %d, want 200", code)
	}

	h.call("PUT", "/faults", map[string]bool{"outage": false}, nil)
	if code := h.call("GET", "/vms", nil, nil); code != http.StatusOK {
		t.Fatalf("GET /vms after outage = %d, want 200", code)
	}
}

func TestFailRate_FailedCreateLeavesNoVM(t *testing.T) {
	h := newHarness(t, fast())
	h.call("PUT", "/faults", map[string]float64{"fail_rate": 1}, nil)

	task := h.waitTask(h.create("doomed"))
	if task.State != TaskError || task.Error == "" {
		t.Fatalf("task = %+v, want error with a message", task)
	}
	if vms := h.vmsByExternalID("doomed"); len(vms) != 0 {
		t.Fatalf("failed create left %d VMs behind, want 0", len(vms))
	}
}

func TestPartialFaultUpdateKeepsOtherSwitches(t *testing.T) {
	h := newHarness(t, fast())
	h.call("PUT", "/faults", map[string]any{"fail_rate": 0.5, "create_delay_ms": 300}, nil)
	var f Faults
	h.call("PUT", "/faults", map[string]bool{"outage": true}, &f)

	if f.FailRate != 0.5 || f.CreateDelayMS != 300 || !f.Outage {
		t.Fatalf("faults = %+v, want fail_rate 0.5, create_delay_ms 300, outage true", f)
	}
}

func TestOutOfBandDeleteDuringCreate_FailsTheTask(t *testing.T) {
	h := newHarness(t, Config{TaskDelay: 100 * time.Millisecond, HangFor: time.Second})
	taskID := h.create("ext-1")
	id := h.vmsByExternalID("ext-1")[0].ID

	if code := h.call("DELETE", "/vms/"+id+"?out_of_band=true", nil, nil); code != http.StatusNoContent {
		t.Fatalf("out-of-band delete = %d, want 204", code)
	}
	if task := h.waitTask(taskID); task.State != TaskError {
		t.Fatalf("create task = %+v, want error (its VM vanished mid-creation)", task)
	}
}
