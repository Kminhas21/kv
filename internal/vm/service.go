package vm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
)

var (
	// ErrInvalid wraps every spec validation failure.
	ErrInvalid = errors.New("invalid VM spec")
	// ErrIdempotencyMismatch means a key was reused with a different request.
	ErrIdempotencyMismatch = errors.New("idempotency key already used with a different request")
)

// Service holds the control plane's request logic, independent of HTTP.
type Service struct {
	repo *Repo
	now  func() time.Time
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo, now: time.Now}
}

// CreateResult is what POST /vms returns.
type CreateResult struct {
	VMID        string `json:"vm_id"`
	OperationID string `json:"operation_id"`
}

// dnsLabel: lowercase letters, digits and '-', not starting or ending with
// '-'. VM names become hostnames, so they must be valid DNS labels.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Validate reports every problem with s at once, not just the first.
func (s Spec) Validate() error {
	var errs []error
	if len(s.Name) == 0 || len(s.Name) > 63 || !dnsLabel.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf("name %q must be 1-63 lowercase letters, digits or '-' (a DNS label)", s.Name))
	}
	if s.CPU < 1 || s.CPU > 64 {
		errs = append(errs, fmt.Errorf("cpu %d must be between 1 and 64", s.CPU))
	}
	if s.MemoryMB < 128 || s.MemoryMB > 1<<20 {
		errs = append(errs, fmt.Errorf("memory_mb %d must be between 128 and %d", s.MemoryMB, 1<<20))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
}

// CreateVM records the intent to create a VM and returns immediately. It
// never calls the hypervisor; the reconciler does that later.
//
// kv has no multi-key transactions, so the three writes are ordered to be
// safe to repeat: (1) claim the idempotency key, (2) create the operation,
// (3) create the VM. Every write is create-only, so a retry after a crash at
// any point re-runs the same steps with the same IDs and finishes the job.
func (s *Service) CreateVM(ctx context.Context, idemKey string, spec Spec) (CreateResult, error) {
	if err := spec.Validate(); err != nil {
		return CreateResult{}, err
	}
	hash := requestHash(spec)
	now := s.now().UTC()

	rec, err := s.repo.ClaimIdempotency(ctx, IdempotencyRecord{
		Key:         idemKey,
		RequestHash: hash,
		VMID:        newID("vm"),
		OpID:        newID("op"),
		CreatedAt:   now,
	})
	if errors.Is(err, kv.ErrConflict) {
		// Someone already claimed this key: this is a retry. Reuse their IDs.
		rec, err = s.repo.GetIdempotency(ctx, idemKey)
		if err != nil {
			return CreateResult{}, fmt.Errorf("read idempotency record: %w", err)
		}
		if rec.RequestHash != hash {
			return CreateResult{}, ErrIdempotencyMismatch
		}
	} else if err != nil {
		return CreateResult{}, fmt.Errorf("claim idempotency key: %w", err)
	}

	op := Operation{
		ID: rec.OpID, VMID: rec.VMID, Type: OpCreate, Status: OpPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.repo.CreateOp(ctx, op); err != nil && !errors.Is(err, kv.ErrConflict) {
		return CreateResult{}, fmt.Errorf("create operation: %w", err)
	}

	vm := VM{
		ID: rec.VMID, ExternalID: "vmaas-" + rec.VMID, Spec: spec,
		DesiredPower: DesiredOn, Phase: PhasePending, ObservedPower: ObservedUnknown,
		CurrentOpID: rec.OpID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.repo.CreateVM(ctx, vm); err != nil && !errors.Is(err, kv.ErrConflict) {
		return CreateResult{}, fmt.Errorf("create vm: %w", err)
	}

	return CreateResult{VMID: rec.VMID, OperationID: rec.OpID}, nil
}

// HostView is a host plus its computed free capacity, for the API.
type HostView struct {
	Host
	CPUFree      int `json:"cpu_free"`
	MemoryMBFree int `json:"memory_mb_free"`
}

func (s *Service) ListHosts(ctx context.Context) ([]HostView, error) {
	hosts, err := s.repo.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]HostView, len(hosts))
	for i, h := range hosts {
		cpu, mem := h.Free()
		views[i] = HostView{Host: h, CPUFree: cpu, MemoryMBFree: mem}
	}
	return views, nil
}

func (s *Service) GetVM(ctx context.Context, id string) (VM, error) { return s.repo.GetVM(ctx, id) }

func (s *Service) ListVMs(ctx context.Context) ([]VM, error) { return s.repo.ListVMs(ctx) }

func (s *Service) GetOp(ctx context.Context, id string) (Operation, error) {
	return s.repo.GetOp(ctx, id)
}

// requestHash fingerprints a request so a reused idempotency key can be
// checked against the original body.
func requestHash(spec Spec) string {
	b, _ := json.Marshal(spec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newID returns e.g. "vm-3f9a1c07b2e4": 48 random bits, plenty for a
// simulator and short enough to type.
func newID(prefix string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}
