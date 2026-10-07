package vm

import (
	"context"
	"errors"
)

// Hypervisor is everything the control plane needs from a virtualization
// backend. It is defined here, by its consumer, and kept as small as the
// reconciler needs today. The mock hypervisor's HTTP client implements it;
// a vSphere (govmomi) or KVM (libvirt) driver would implement the same
// methods, and nothing else in this package would change.
type Hypervisor interface {
	// CreateVM starts an asynchronous create and returns the task ID. A
	// returned error does NOT mean nothing happened: unless it wraps
	// ErrProviderRejected, the VM may exist anyway.
	CreateVM(ctx context.Context, req ProviderCreate) (taskID string, err error)
	GetTask(ctx context.Context, taskID string) (ProviderTask, error)
	// FindByExternalID lists hypervisor VMs carrying our stable external ID.
	// An empty result is a definite "none"; an error is "could not ask".
	FindByExternalID(ctx context.Context, externalID string) ([]ProviderVM, error)
}

var (
	// ErrProviderNotFound: the hypervisor answered that the object does not exist.
	ErrProviderNotFound = errors.New("not found on hypervisor")
	// ErrProviderRejected: the hypervisor refused the request as invalid. It
	// definitely did nothing, and retrying the same request will not help.
	ErrProviderRejected = errors.New("rejected by hypervisor")
)

type ProviderCreate struct {
	ExternalID string
	Name       string
	CPU        int
	MemoryMB   int
	HostID     string
}

type TaskState string

const (
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
)

type ProviderTask struct {
	ID    string
	VMID  string
	State TaskState
	Error string
}

// ProviderVM is the hypervisor's view of a VM.
type ProviderVM struct {
	ID         string
	ExternalID string
	HostID     string
	Ready      bool // false while the hypervisor is still building it
	Power      ObservedPower
}
