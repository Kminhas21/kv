package vm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kminhas21/kv/internal/kvclient"
)

const (
	vmPrefix   = "vms/"
	opPrefix   = "ops/"
	idemPrefix = "idem/"
	hostPrefix = "hosts/"
)

// Repo stores control-plane records as JSON in kv. Create is create-only and
// Update is compare-and-swap on the record's Version, so callers get
// kv.ErrConflict instead of silently overwriting someone else's change.
type Repo struct {
	kv *kvclient.Client
}

func NewRepo(c *kvclient.Client) *Repo { return &Repo{kv: c} }

// record is any stored type. The pointer-method constraint lets generic code
// set the kv version on the decoded value.
type record[T any] interface {
	*T
	setVersion(uint64)
}

func (v *VM) setVersion(n uint64)                { v.Version = n }
func (o *Operation) setVersion(n uint64)         { o.Version = n }
func (r *IdempotencyRecord) setVersion(n uint64) { r.Version = n }
func (h *Host) setVersion(n uint64)              { h.Version = n }

func get[T any, P record[T]](ctx context.Context, c *kvclient.Client, key string) (T, error) {
	var v T
	raw, ver, err := c.Get(ctx, key)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", key, err)
	}
	P(&v).setVersion(ver)
	return v, nil
}

func list[T any, P record[T]](ctx context.Context, c *kvclient.Client, prefix string) ([]T, error) {
	items, err := c.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var v T
		if err := json.Unmarshal([]byte(it.Value), &v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", it.Key, err)
		}
		P(&v).setVersion(it.Version)
		out = append(out, v)
	}
	return out, nil
}

// put writes v at key. expected is the version it must currently be at:
// 0 means "must not exist yet".
func put[T any, P record[T]](ctx context.Context, c *kvclient.Client, key string, v T, expected uint64) (T, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return v, fmt.Errorf("encode %s: %w", key, err)
	}
	ver, err := c.Put(ctx, key, string(raw), &expected)
	if err != nil {
		return v, err
	}
	P(&v).setVersion(ver)
	return v, nil
}

func (r *Repo) GetVM(ctx context.Context, id string) (VM, error) {
	return get[VM](ctx, r.kv, vmPrefix+id)
}

func (r *Repo) ListVMs(ctx context.Context) ([]VM, error) {
	return list[VM](ctx, r.kv, vmPrefix)
}

func (r *Repo) CreateVM(ctx context.Context, v VM) (VM, error) {
	return put(ctx, r.kv, vmPrefix+v.ID, v, 0)
}

// UpdateVM saves v if nobody changed it since it was read (v.Version).
func (r *Repo) UpdateVM(ctx context.Context, v VM) (VM, error) {
	return put(ctx, r.kv, vmPrefix+v.ID, v, v.Version)
}

func (r *Repo) GetOp(ctx context.Context, id string) (Operation, error) {
	return get[Operation](ctx, r.kv, opPrefix+id)
}

func (r *Repo) CreateOp(ctx context.Context, op Operation) (Operation, error) {
	return put(ctx, r.kv, opPrefix+op.ID, op, 0)
}

func (r *Repo) UpdateOp(ctx context.Context, op Operation) (Operation, error) {
	return put(ctx, r.kv, opPrefix+op.ID, op, op.Version)
}

func (r *Repo) GetIdempotency(ctx context.Context, key string) (IdempotencyRecord, error) {
	return get[IdempotencyRecord](ctx, r.kv, idemPrefix+key)
}

func (r *Repo) ListHosts(ctx context.Context) ([]Host, error) {
	return list[Host](ctx, r.kv, hostPrefix)
}

func (r *Repo) CreateHost(ctx context.Context, h Host) (Host, error) {
	return put(ctx, r.kv, hostPrefix+h.ID, h, 0)
}

func (r *Repo) UpdateHost(ctx context.Context, h Host) (Host, error) {
	return put(ctx, r.kv, hostPrefix+h.ID, h, h.Version)
}

// ClaimIdempotency stores rec only if no record exists for its key yet.
func (r *Repo) ClaimIdempotency(ctx context.Context, rec IdempotencyRecord) (IdempotencyRecord, error) {
	return put(ctx, r.kv, idemPrefix+rec.Key, rec, 0)
}
