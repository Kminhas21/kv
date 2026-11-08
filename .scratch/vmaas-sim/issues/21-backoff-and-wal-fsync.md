# 21: Retry backoff

**What to build:** Failed reconcile steps for a VM wait with exponential backoff + jitter before retrying instead of hammering every tick. (WAL fsync moved to ticket 22.)

**Blocked by:** 18

**Status:** ready-for-human

- [ ] Per-operation next_attempt_at; reconciler skips ops not yet due
- [ ] Backoff: base × 2^attempts, capped, with jitter

**Concepts:** thundering herd and why jitter exists, `math/rand/v2`.

**Mock vs production:** client-go work queues use rate limiters with exponential per-item backoff.

**Manual check:** `vmcli hv faults --fail-rate 1`, watch log timestamps spread out.
