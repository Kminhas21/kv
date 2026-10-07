package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
)

// DefaultHosts is the simulated datacenter: two big hosts and a small one.
func DefaultHosts() []Host {
	return []Host{
		{ID: "host-1", CPUTotal: 16, MemoryMBTotal: 32768},
		{ID: "host-2", CPUTotal: 16, MemoryMBTotal: 32768},
		{ID: "host-3", CPUTotal: 8, MemoryMBTotal: 16384},
	}
}

// pickHost chooses where vmID should run. If some host already holds a
// reservation for vmID (an earlier attempt reserved, then crashed before
// recording the choice), that host is returned so the work is reused.
// Otherwise it is first-fit: the first host, in ID order, with room.
//
// It is a pure function, no I/O, so placement policy can be tested in
// isolation and swapped later (e.g. spread or bin-pack scoring).
func pickHost(hosts []Host, vmID string, s Spec) (Host, bool) {
	sorted := slices.SortedFunc(slices.Values(hosts), func(a, b Host) int { return cmp.Compare(a.ID, b.ID) })

	for _, h := range sorted {
		if _, ok := h.Reservations[vmID]; ok {
			return h, true
		}
	}
	for _, h := range sorted {
		if h.Fits(s) {
			return h, true
		}
	}
	return Host{}, false
}

// SeedHosts creates any missing hosts. Create-only writes make it safe to
// run on every startup and from several replicas at once. It retries until
// kv answers, because in Kubernetes vmctl may start before kv is ready.
func SeedHosts(ctx context.Context, repo *Repo, hosts []Host) error {
	for {
		err := seedOnce(ctx, repo, hosts)
		if err == nil {
			return nil
		}
		slog.Warn("seeding hosts failed, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func seedOnce(ctx context.Context, repo *Repo, hosts []Host) error {
	for _, h := range hosts {
		h.Reservations = map[string]Reservation{}
		_, err := repo.CreateHost(ctx, h)
		switch {
		case err == nil:
			slog.Info("host seeded", "host", h.ID, "cpu", h.CPUTotal, "memory_mb", h.MemoryMBTotal)
		case errors.Is(err, kv.ErrConflict):
			// Already there from a previous run: keep its reservations.
		default:
			return fmt.Errorf("seed %s: %w", h.ID, err)
		}
	}
	return nil
}
