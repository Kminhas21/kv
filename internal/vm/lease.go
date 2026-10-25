package vm

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/kvclient"
)

// LeaseKey is the kv key vmctl replicas compete for (ADR-0002).
const LeaseKey = "leases/vmctl"

// Lease elects one leader among vmctl replicas. It has two states:
//
//   - follower (ver == 0): every renew interval, try to create the key with
//     If-Match: 0. That only succeeds once the key is missing or expired.
//   - leader (ver > 0): every renew interval, rewrite the key with If-Match on
//     the held version, which also resets its TTL.
//
// A leader drops back to follower on a conflict (someone else holds it, or it
// expired) or when renewals keep failing past its deadline.
type Lease struct {
	kv    *kvclient.Client
	id    string
	ttl   time.Duration
	renew time.Duration
	now   func() time.Time

	mu       sync.Mutex
	ver      uint64    // version of the key we hold; 0 = not leader
	deadline time.Time // stop acting as leader at this time unless renewed
	leading  bool      // what we last logged, so each gain and loss is logged once
}

// NewLease returns a follower. renew must be shorter than ttl.
func NewLease(c *kvclient.Client, id string, ttl, renew time.Duration) *Lease {
	return &Lease{kv: c, id: id, ttl: ttl, renew: renew, now: time.Now}
}

// IsLeader reports whether this replica holds the lease and its deadline has
// not passed. The deadline check means a leader whose renewals are stuck stops
// reconciling on time even before a renewal call returns an error.
func (l *Lease) IsLeader() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ok := l.ver != 0 && l.now().Before(l.deadline)
	if !ok {
		l.note(false, "deadline passed without a successful renewal")
	}
	return ok
}

// note logs a change in leadership. l.mu must be held.
func (l *Lease) note(leading bool, reason string) {
	if leading == l.leading {
		return
	}
	l.leading = leading
	if leading {
		slog.Info("became leader", "lease", LeaseKey, "id", l.id, "ttl", l.ttl)
	} else {
		slog.Warn("lost leadership", "lease", LeaseKey, "id", l.id, "reason", reason)
	}
}

// Run acquires or renews the lease every renew interval until ctx is
// cancelled. Call Release after Run returns.
func (l *Lease) Run(ctx context.Context) {
	ticker := time.NewTicker(l.renew)
	defer ticker.Stop()
	for {
		l.try(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Lease) try(ctx context.Context) {
	l.mu.Lock()
	held := l.ver
	l.mu.Unlock()

	// Measured before the request: kv starts the TTL when it applies the write,
	// which is later, so our deadline is never later than the real expiry.
	start := l.now()
	// Not cancelled by shutdown: a renewal kv applied but we never heard about
	// would leave us with a stale version, and Release would then conflict.
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.renew)
	ver, err := l.kv.PutTTL(reqCtx, LeaseKey, l.id, &held, l.ttl)
	cancel()

	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case err == nil:
		// One renew interval of margin: we give up leadership before the key
		// can expire and be taken by someone else.
		l.ver, l.deadline = ver, start.Add(l.ttl-l.renew)
		l.note(true, "")
	case held == 0:
		if !errors.Is(err, kv.ErrConflict) && ctx.Err() == nil {
			slog.Warn("lease acquire failed", "lease", LeaseKey, "err", err)
		}
	case errors.Is(err, kv.ErrConflict):
		l.ver = 0
		l.note(false, "lease taken or expired")
	case !l.now().Before(l.deadline):
		l.ver = 0
		l.note(false, "renewals failing past deadline: "+err.Error())
	case ctx.Err() == nil:
		// Still inside the deadline: keep the version and retry next interval.
		slog.Warn("lease renew failed; will retry", "lease", LeaseKey, "err", err)
	}
}

// Release deletes the lease if this replica holds it, so a follower can take
// over on its next attempt instead of waiting out the TTL. Call it only after
// Run has returned, or a late renewal could recreate the key.
func (l *Lease) Release(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ver == 0 {
		return
	}
	held := l.ver
	l.ver, l.leading = 0, false
	// If-Match: if the key already expired and changed hands, this is a
	// harmless conflict instead of deleting the new leader's lease.
	if err := l.kv.Delete(ctx, LeaseKey, &held); err != nil {
		slog.Warn("lease release failed; it will expire on its own", "lease", LeaseKey, "err", err)
		return
	}
	slog.Info("released leadership", "lease", LeaseKey, "id", l.id)
}
