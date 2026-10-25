# Benchmarks

Measured numbers for the kv + VM control plane simulator, recorded by the
scripts that produce them. Each section names its environment and the command
that reproduces it. Numbers from a laptop running kind are for comparing
changes against each other, not for predicting production.

## Store and HTTP throughput (native localhost)

### Environment

Intel Core i9-14900HX (24 cores / 32 threads), Windows 11 Home 10.0.26300,
Samsung MZVL41T0HBLB NVMe SSD (NTFS; WAL in `%TEMP%`), Go 1.26.5 windows/amd64.
kv and kvbench ran on the same machine over loopback, as separately built
binaries (`go build -o`), not `go run`. Nothing else was deliberately running,
but this is a laptop; see the variance notes. No kind cluster involved.

### Results against targets

Each HTTP number is the median of 3 runs of 10s (range in brackets); kvbench
used `-c 64 -keys 10000 -size 128`. Percentiles are the median of the three
runs' percentiles.

| Target | Config | Measured | Met? |
|--------|--------|----------|------|
| 50K reads/s | fsync=batch, `-reads 100` | 54.5K/s [52.9K-54.8K] | met (barely, see notes) |
| p99 < 1ms | same run | p50 1.01ms, p99 4.50ms, p999 7.3ms | not met |
| 14K writes/s | fsync=batch, `-reads 0` | 33.9K/s [33.8K-34.6K], p99 3.7ms | met |
| (info) | fsync=off, `-reads 0` | 51.6K/s [45.7K-59.6K], p99 3.7ms | n/a |
| (info) | fsync=always, `-reads 0` | 1.51K/s [1.50K-1.51K], p99 61ms | n/a |
| 1M replay < 3s | Open, WAL only, 1M records | 1.22s [1.21-1.23] | met |
| (info) | Open, snapshot (999K keys) + 1K-record tail | 1.05s [1.01-1.10] | met |

In-process (`testing.B`, no HTTP, 32 parallel goroutines, 5 runs each):

| Benchmark | ns/op | Implied ops/s |
|-----------|-------|---------------|
| `BenchmarkGet` | 140-157 | ~6.5M |
| `BenchmarkPut/off` | 3190-3390 | ~300K |
| `BenchmarkPut/batch` | 36.7K-42.8K | ~25K |
| `BenchmarkPut/always` | 579K-667K | ~1.6K |

Replay in records/s: ~0.82M (WAL only), ~0.95M (snapshot + tail).

### Commands

```
go build -o kv.exe ./cmd/kv && go build -o kvbench.exe ./cmd/kvbench

# server, one per fsync mode (SNAPSHOT_EVERY left at its default, 100000)
WAL_PATH=$(mktemp -d)/kv.wal WAL_FSYNC=batch ADDR=:18080 ./kv.exe

# load, 3 runs each against a fresh server
./kvbench.exe -addr http://localhost:18080 -reads 100   # fsync=batch
./kvbench.exe -addr http://localhost:18080 -reads 0     # fsync=off|always|batch

# in-process
go test -run '^$' -bench 'Get|Put' -count 5 ./internal/kv
KV_BENCH_REPLAY_N=1000000 go test -run '^$' -bench Replay1M -benchtime 1x -count 5 -timeout 30m ./internal/kv
make bench     # the same two (replay at -benchtime 3x, Get/Put at -count 3)
```

`BenchmarkReplay1M` builds its WAL(s) once per run in a temp dir, then times
only `Open`. The snapshot variant is produced by the real snapshot machinery
(`snapshotEvery = N-1000`), so it replays a snapshot plus a 1000-record tail.
The size defaults to 10K records so the CI smoke command
(`go test -run '^$' -bench . -benchtime 1x ./...`, about 8s wall) stays fast;
the 1M numbers above come from `KV_BENCH_REPLAY_N=1000000`.

### Notes and caveats

- **Closed-loop, coordinated omission.** kvbench keeps 64 requests in flight;
  each goroutine sends the next only after the previous reply. When the server
  stalls, the generator also stops sending, so the requests that would have
  queued behind the stall are never issued and never measured. Reported tail
  latencies are therefore a lower bound on what an open-loop client with a
  fixed arrival rate would see. Open-loop tools (wrk2, vegeta, k6 with
  constant-arrival-rate) exist for exactly this; kvbench does not do it.
- **Client and server share the machine.** They compete for CPU and the
  loopback stack, so reads/s and latency mix server cost with client cost.
  With 64 connections, p50 of about 1ms is what Little's law gives at ~54K/s
  (64 / 54K = 1.2ms); the generator and Windows loopback are likely a large
  part of the 4.5ms p99 and the read ceiling. I did not profile it, and I did
  not tune the server for any target.
- **Reads barely clear 50K/s** and fsync mode does not affect them, so read
  "met" as "about 50K on this machine in this setup", not as headroom.
- **fsync=off writes** vary the most (45.7K-59.6K, one run with a 116ms max).
- **Replay variance.** The 1.2s figure is from a quiet 5-run series. Earlier
  series on the same machine, while it was busier, took 3.5-4.4s and 1.4-1.6s
  for the same WAL, so "< 3s" is met in the recorded series but is not robust
  to a loaded machine. In-process Put numbers also moved by up to ~3x between
  series (Put/off 3.2us vs 12us).
- `fsync=always` is bounded by NTFS `FlushFileBuffers` latency on one NVMe
  (about 0.6ms per sync).
- Mock vs production: real load tests use separate generator machines, open
  loop, and a network between the two; here everything is one box on loopback
  with a single-node in-memory map and a local WAL, not a replicated store
  like etcd.

## Self-heal (kind)

Produced by `make demo`, section 6. The kind overlay sets `FAULTS=1`, which
gives kv `POST /debug/crash` (process exits 1) and `POST /debug/hang` (every
handler, `/healthz` included, blocks). The demo times the span from the
trigger until `kv-0` is `Ready` again with `restartCount` incremented, and
fails above 10s.

Probes (all three services): `periodSeconds: 2`, `failureThreshold: 2`,
`timeoutSeconds: 1`. kv also has a startupProbe, 2s x 30.

| Fault | Run 1 | Run 2 | Run 3 |
|-------|-------|-------|-------|
| crash | 4.1s  | 3.2s  | 1.9s  |
| hang  | 6.3s  | 7.8s  | 7.2s  |

Why crash is faster: the kubelet sees the container exit immediately, so there
is no detection delay; only the restart plus the first startup and readiness
probes remain. A hang needs about 2 failed liveness probes (up to ~5s) first.

Environment: Intel Core i9-14900HX, Windows 11 Home 10.0.26300, Docker Desktop
(engine 29.6.2, WSL2 kernel 6.6.87.2, 32 CPUs / 14.5 GiB visible to Docker),
kind v0.33.0, kubectl client v1.36.1, 1 control-plane + 2 workers, kv on a
1Gi PVC.
