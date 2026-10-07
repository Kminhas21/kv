// Package hvclient implements vm.Hypervisor over the mock hypervisor's HTTP
// API. It is the only package that knows mockhv's wire format; it translates
// mockhv's words ("success", "ready") into the control plane's own types.
package hvclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Kminhas21/kv/internal/mockhv"
	"github.com/Kminhas21/kv/internal/vm"
)

type Client struct {
	base string
	http *http.Client
}

var _ vm.Hypervisor = (*Client)(nil)

// New returns a client whose every call is bounded by timeout. The timeout
// is what turns a hung hypervisor call into an "outcome unknown" error
// instead of a reconcile loop stuck forever.
func New(base string, timeout time.Duration) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *Client) CreateVM(ctx context.Context, req vm.ProviderCreate) (string, error) {
	body := mockhv.CreateRequest{
		ExternalID: req.ExternalID, Name: req.Name, CPU: req.CPU, MemoryMB: req.MemoryMB, HostID: req.HostID,
	}
	var ref struct {
		TaskID string `json:"task_id"`
	}
	if err := c.call(ctx, http.MethodPost, "/tasks/create-vm", body, &ref); err != nil {
		return "", err
	}
	return ref.TaskID, nil
}

func (c *Client) GetTask(ctx context.Context, id string) (vm.ProviderTask, error) {
	var t mockhv.Task
	if err := c.call(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, &t); err != nil {
		return vm.ProviderTask{}, err
	}
	state := vm.TaskRunning
	switch t.State {
	case mockhv.TaskSuccess:
		state = vm.TaskSucceeded
	case mockhv.TaskError:
		state = vm.TaskFailed
	}
	return vm.ProviderTask{ID: t.ID, VMID: t.VMID, State: state, Error: t.Error}, nil
}

func (c *Client) FindByExternalID(ctx context.Context, externalID string) ([]vm.ProviderVM, error) {
	var vms []mockhv.VM
	if err := c.call(ctx, http.MethodGet, "/vms?external_id="+url.QueryEscape(externalID), nil, &vms); err != nil {
		return nil, err
	}
	out := make([]vm.ProviderVM, len(vms))
	for i, v := range vms {
		out[i] = toProviderVM(v)
	}
	return out, nil
}

func (c *Client) GetVM(ctx context.Context, id string) (vm.ProviderVM, error) {
	var v mockhv.VM
	if err := c.call(ctx, http.MethodGet, "/vms/"+url.PathEscape(id), nil, &v); err != nil {
		return vm.ProviderVM{}, err
	}
	return toProviderVM(v), nil
}

func (c *Client) PowerOn(ctx context.Context, id string) (string, error) {
	var ref struct {
		TaskID string `json:"task_id"`
	}
	if err := c.call(ctx, http.MethodPost, "/vms/"+url.PathEscape(id)+"/power-on", nil, &ref); err != nil {
		return "", err
	}
	return ref.TaskID, nil
}

func toProviderVM(v mockhv.VM) vm.ProviderVM {
	power := vm.ObservedOff
	if v.Power == mockhv.PowerOn {
		power = vm.ObservedOn
	}
	return vm.ProviderVM{
		ID: v.ID, ExternalID: v.ExternalID, HostID: v.HostID,
		Ready: v.Status == mockhv.VMReady, Power: power,
	}
}

// call sends one JSON request and classifies the outcome:
//   - 404               -> vm.ErrProviderNotFound (definite: it does not exist)
//   - 409               -> vm.ErrProviderBusy     (definite: not now, try later)
//   - other 4xx         -> vm.ErrProviderRejected (definite: nothing was done)
//   - 5xx, timeouts,
//     connection errors -> plain error (ambiguous: it may or may not have happened)
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hypervisor %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		detail := fmt.Sprintf("hypervisor %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return fmt.Errorf("%s: %w", detail, vm.ErrProviderNotFound)
		case resp.StatusCode == http.StatusConflict:
			return fmt.Errorf("%s: %w", detail, vm.ErrProviderBusy)
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			return fmt.Errorf("%s: %w", detail, vm.ErrProviderRejected)
		default:
			return fmt.Errorf("%s", detail)
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
