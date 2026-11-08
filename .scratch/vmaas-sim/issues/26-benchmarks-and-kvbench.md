# 26: Benchmarks, kvbench, recorded numbers

**What to build:** Every performance number we would quote is reproducible from the repo and written down with its conditions. In-process `testing.B` benchmarks cover the store; `kvbench`, a stdlib-only HTTP load generator, measures throughput and latency percentiles against a running kv; a replay benchmark measures restart time for 1M records.

**Blocked by:** 22, 24

**Status:** resolved

- [x] `testing.B` benchmarks for Get and Put using `b.RunParallel`, Put in each `WAL_FSYNC` mode
- [x] Replay benchmark: Open on a WAL of 1M records with snapshots off, and snapshot + tail with them on
- [x] `kvbench` flags: `-c` concurrency (64), `-d` duration (10s), `-reads` read % (100; 0 = writes only), `-keys` (10k), `-size` value bytes (128), target address. Pre-fills keys before measuring
- [x] kvbench records every request's latency per goroutine, merges and sorts them at the end, and prints ops/s plus exact p50/p99/p999 and the error count
- [x] A `make bench` target runs the in-process benchmarks
- [x] A benchmarks doc records results from native localhost runs: CPU, OS, disk type, Go version, fsync mode, and the exact command for each number. Optional kind numbers are labelled as secondary
- [x] Numbers are whatever was measured; targets (50K reads/s, 14K writes/s, 1M replay < 3s, p99 < 1ms) are listed next to them, met or not

**Concepts:** `testing.B`, `b.RunParallel`, `b.ReportMetric`, coordinated omission (why closed-loop load generators understate tail latency), percentile math.

**Mock vs production:** real load tests use wrk2/vegeta/k6 with open-loop (fixed arrival rate) load to avoid coordinated omission; kvbench is closed-loop, and the doc says so.

**Manual check:** `go run ./cmd/kv` in one terminal, `go run ./cmd/kvbench -reads 100` and `-reads 0` in another.
