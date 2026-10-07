// Package mockhv is a fake hypervisor API. It imitates the parts of vCenter
// or libvirt that make control planes hard to write: asynchronous tasks, no
// de-duplication of creates, lost responses, outages and random failures.
package mockhv

type Power string

const (
	PowerOff Power = "OFF"
	PowerOn  Power = "ON"
)

// VMStatus separates "the hypervisor knows about this VM" from "the VM is
// fully built". A VM appears in inventory as soon as creation starts, like
// a real vCenter VM, so a lookup by external ID can find it mid-creation.
type VMStatus string

const (
	VMCreating VMStatus = "creating"
	VMReady    VMStatus = "ready"
)

type VM struct {
	ID         string   `json:"id"`
	ExternalID string   `json:"external_id"`
	Name       string   `json:"name"`
	CPU        int      `json:"cpu"`
	MemoryMB   int      `json:"memory_mb"`
	HostID     string   `json:"host_id"`
	Status     VMStatus `json:"status"`
	Power      Power    `json:"power"`
}

type TaskState string

const (
	TaskRunning TaskState = "running"
	TaskSuccess TaskState = "success"
	TaskError   TaskState = "error"
)

type Task struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	VMID  string    `json:"vm_id"`
	State TaskState `json:"state"`
	Error string    `json:"error,omitempty"`
}

// Faults are switches for simulating real-world failure modes.
type Faults struct {
	// CreateDelayMS is added to the normal task duration for creates.
	CreateDelayMS int `json:"create_delay_ms"`
	// TimeoutAfterSuccessCount: the next N creates do the work, then delay
	// the HTTP response past the caller's timeout (a "lost response").
	TimeoutAfterSuccessCount int `json:"timeout_after_success_count"`
	// FailRate is the probability (0..1) that any task ends in error.
	FailRate float64 `json:"fail_rate"`
	// Outage makes every API call return 503 (except /faults and health).
	Outage bool `json:"outage"`
}

type CreateRequest struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	CPU        int    `json:"cpu"`
	MemoryMB   int    `json:"memory_mb"`
	HostID     string `json:"host_id"`
}
