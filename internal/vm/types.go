// Package vm is the VM control plane: domain types, persistence in kv,
// the create/read API and (later) the scheduler and reconciler.
package vm

import "time"

// DesiredPower is what the user asked for.
type DesiredPower string

const (
	DesiredOn      DesiredPower = "ON"
	DesiredOff     DesiredPower = "OFF"
	DesiredDeleted DesiredPower = "DELETED"
)

// ObservedPower is what the hypervisor last reported. UNKNOWN means we could
// not ask (outage, timeout); ABSENT means it answered that the VM is gone.
// The difference matters: only ABSENT may ever trigger a re-create.
type ObservedPower string

const (
	ObservedUnknown ObservedPower = "UNKNOWN"
	ObservedOn      ObservedPower = "ON"
	ObservedOff     ObservedPower = "OFF"
	ObservedAbsent  ObservedPower = "ABSENT"
)

// Phase is the control plane's one-word summary of where a VM is.
type Phase string

const (
	PhasePending      Phase = "Pending"
	PhaseProvisioning Phase = "Provisioning"
	PhaseRunning      Phase = "Running"
	PhaseStopped      Phase = "Stopped"
	PhaseMigrating    Phase = "Migrating"
	PhaseDeleting     Phase = "Deleting"
	PhaseDeleted      Phase = "Deleted"
	PhaseFailed       Phase = "Failed"
)

type OpType string

const (
	OpCreate  OpType = "CREATE"
	OpStart   OpType = "START"
	OpStop    OpType = "STOP"
	OpDelete  OpType = "DELETE"
	OpMigrate OpType = "MIGRATE"
)

type OpStatus string

const (
	OpPending   OpStatus = "PENDING"
	OpRunning   OpStatus = "RUNNING"
	OpSucceeded OpStatus = "SUCCEEDED"
	OpFailed    OpStatus = "FAILED"
	OpUnknown   OpStatus = "UNKNOWN"
)

// Spec is what a user may choose about a VM.
type Spec struct {
	Name     string `json:"name"`
	CPU      int    `json:"cpu"`
	MemoryMB int    `json:"memory_mb"`
}

// VM is stored at vms/{id}. Version is the kv version it was read at; it is
// not stored in the JSON itself, the kv store owns it (like K8s
// resourceVersion) and every update must present it.
type VM struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	Spec

	DesiredPower DesiredPower `json:"desired_power"`
	Phase        Phase        `json:"phase"`
	HostID       string       `json:"host_id,omitempty"`
	CurrentOpID  string       `json:"current_op_id,omitempty"`

	ProviderVMID   string        `json:"provider_vm_id,omitempty"`
	ObservedPower  ObservedPower `json:"observed_power"`
	LastObservedAt time.Time     `json:"last_observed_at,omitzero"`
	LastError      string        `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Version uint64 `json:"-"`
}

// Operation is stored at ops/{id}: one user-requested change and its progress.
type Operation struct {
	ID             string   `json:"id"`
	VMID           string   `json:"vm_id"`
	Type           OpType   `json:"type"`
	Status         OpStatus `json:"status"`
	ProviderTaskID string   `json:"provider_task_id,omitempty"`
	TargetHostID   string   `json:"target_host_id,omitempty"`
	Attempts       int      `json:"attempts"`
	LastError      string   `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Version uint64 `json:"-"`
}

// IdempotencyRecord is stored at idem/{key}. It is written create-only
// before anything else, so it decides which request "owns" the key.
type IdempotencyRecord struct {
	Key         string    `json:"key"`
	RequestHash string    `json:"request_hash"`
	VMID        string    `json:"vm_id"`
	OpID        string    `json:"op_id"`
	CreatedAt   time.Time `json:"created_at"`

	Version uint64 `json:"-"`
}
