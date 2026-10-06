# 21: Retry backoff + optional WAL fsync

**What to build:** Failed reconcile steps for a VM wait with exponential backoff + jitter before retrying instead of hammering every tick. kv gets an option to fsync each WAL write, and you measure the write-latency cost.

**Blocked by:** 04, 18

**Status:** ready-for-human

- [ ] Per-operation next_attempt_at; reconciler skips ops not yet due
- [ ] Backoff: base × 2^attempts, capped, with jitter
- [ ] `WAL_FSYNC=true` calls `File.Sync()` after each append
- [ ] Benchmark (`testing.B`) comparing writes with and without fsync; numbers noted in Comments

**Concepts:** thundering herd and why jitter exists, `math/rand/v2`, Go benchmarks, durability vs latency tradeoff.

**Mock vs production:** client-go work queues use rate limiters with exponential per-item backoff. etcd batches fsyncs (group commit) to amortize the cost.

**Manual check:** `vmcli hv faults --fail-rate 1`, watch log timestamps spread out.
