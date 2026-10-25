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
binaries (`go build -o`), not `go run`. A kind cluster under Docker Desktop (kv,
vmctl and mockhv pods plus a demo script, owned by another task) was running
concurrently on this machine during some or all of the recorded series; which
minutes overlapped is unknown. That background load is the likely cause of the
drift described below. **Re-run on an idle machine (Docker Desktop stopped)
before quoting any number.**

### Results against targets

Every number below is the median of 3 runs (range in brackets), all taken in
one session. HTTP runs are 10s each with kvbench `-c 64 -keys 10000 -size 128`;
percentiles are the median of the three runs' percentiles. kvbench counts only
successful requests in ops/s and latency, and exits non-zero above 0.1%
errors. **Errors: 0 in all 12 HTTP runs.**

| Target | Config | Measured | Met? |
|--------|--------|----------|------|
| 50K reads/s | fsync=batch, `-reads 100` | 72.2K/s [70.1K-72.6K] | met |
| p99 < 1ms | same runs | p50 0.67ms, p99 3.83ms, p999 5.95ms | not met |
| 14K writes/s | fsync=batch, `-reads 0` | 26.3K/s [26.2K-27.6K], p99 6.4ms | met |
| (info) | fsync=off, `-reads 0` | 54.8K/s [53.3K-56.1K], p99 3.65ms | n/a |
| (info) | fsync=always, `-reads 0` | 915/s [781-934], p99 198ms | n/a |
| 1M replay < 3s | Open, WAL only, 1M records | 2.19s [2.14-2.47] | met (see variance) |
| (info) | Open, snapshot at record 999,000 (999K keys) + 1,000-record tail | 2.35s [2.03-2.40] | met (see variance) |

In-process (`testing.B`, no HTTP, 32 parallel goroutines, `-count 3`):

| Benchmark | ns/op | Implied ops/s |
|-----------|-------|---------------|
| `BenchmarkGet` | 68-90 | ~12M |
| `BenchmarkPut/off` | 7.5K-8.7K | ~130K |
| `BenchmarkPut/batch` | 72K-96K | ~12K |
| `BenchmarkPut/always` | 1.26M-1.43M | ~750 |
| `BenchmarkPutBatchWriters/x1` (32 writers) | 76K-89K | ~13K |
| `BenchmarkPutBatchWriters/x2` (64 writers) | 34K-46K | ~29K |
| `BenchmarkPutBatchWriters/x8` (256 writers) | 11K-12K | ~87K |

Replay in records/s: 0.40M-0.47M (WAL only), 0.42M-0.49M (snapshot + tail).
The snapshot variant is not faster here; the difference is inside the noise.

Raw per-run numbers and an earlier series are in the appendix at the end of
this section.

**Why HTTP batch writes can beat in-process `BenchmarkPut/batch`.** In batch
mode one fsync covers every record queued while the previous fsync ran, so
throughput scales with the number of concurrent writers. `BenchmarkPut` runs
GOMAXPROCS (32) writers; kvbench runs 64. `BenchmarkPutBatchWriters` shows the
effect directly: doubling the writers from 32 to 64 roughly halved ns/op
(76-89us to 34-46us), and 256 writers reached 11-12us. So a 64-writer HTTP
load beating a 32-writer in-process one is a batch-size effect (more records
per fsync), not evidence that HTTP is faster than a function call. The 1024-key
space and `fmt.Sprintf` in `BenchmarkPut` cost well under a microsecond next
to an fsync-bound ~80us op, so I did not isolate them. Caveat: the HTTP and
in-process series were taken minutes apart on a machine whose speed drifted (likely concurrent kind/Docker load)
(see variance), so compare the scaling within `BenchmarkPutBatchWriters`, not
the absolute numbers across tables.

### Commands

```
go build -o kv.exe ./cmd/kv && go build -o kvbench.exe ./cmd/kvbench

# server, one per fsync mode (SNAPSHOT_EVERY left at its default, 100000)
WAL_PATH=$(mktemp -d)/kv.wal WAL_FSYNC=batch ADDR=:18080 ./kv.exe

# load, 3 runs each against a fresh server
./kvbench.exe -addr http://localhost:18080 -reads 100   # fsync=batch
./kvbench.exe -addr http://localhost:18080 -reads 0     # fsync=off|always|batch

# in-process
go test -run '^$' -bench 'Get|Put' -count 3 ./internal/kv
KV_BENCH_REPLAY_N=1000000 go test -run '^$' -bench Replay -benchtime 1x -count 3 -timeout 30m ./internal/kv
make bench     # the same two (replay at -benchtime 3x)
```

`BenchmarkReplay` (N is in the sub-benchmark name) builds its WAL(s) once per run in a temp dir, then times
only `Open` (the `Close` after each iteration is outside the timed region). The snapshot variant is produced by the real snapshot machinery
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
  With 64 connections, Little's law ties p50 to throughput (64 / 72K =
  0.9ms); the generator and Windows loopback are likely a large part of the
  ~3.8ms p99 and the read ceiling. I did not profile it, and I did not tune
  the server for any target.
- **Run-to-run and session-to-session variance is large**, most likely from the concurrent kind/Docker load noted above. An earlier series
  (appendix) on the same machine gave 54.5K reads/s and 33.9K batch writes/s
  over HTTP, and 1.2s replay; the recorded series gave 72.2K, 26.3K and 2.2s.
  Replay has also taken 3.5-4.4s in a busier session. Treat every figure as
  "this order of magnitude on this laptop", and note "< 3s" replay was met in
  every recorded series but one (the busy one) and is not robust to load.
  `fsync=always` swings the most (781-934/s here, 1.5K/s earlier).
- `fsync=always` is bounded by NTFS `FlushFileBuffers` latency on one NVMe
  (roughly 0.6-1.4ms per sync across series).
- Mock vs production: real load tests use separate generator machines, open
  loop, and a network between the two; here everything is one box on loopback
  with a single-node in-memory map and a local WAL, not a replicated store
  like etcd.

<details>
<summary>Appendix: raw per-run numbers</summary>

HTTP (ops/s, p99), recorded series. Errors were 0 in every run.

| Config | Run 1 | Run 2 | Run 3 |
|--------|-------|-------|-------|
| batch, reads 100 | 72628, 3.86ms | 72249, 3.80ms | 70128, 3.83ms |
| off, writes | 53334, 3.68ms | 56082, 3.55ms | 54813, 3.65ms |
| always, writes | 781, 273ms | 915, 164ms | 934, 198ms |
| batch, writes | 27607, 5.14ms | 26200, 6.42ms | 26293, 9.46ms |

In-process ns/op: Get 67.7, 77.0, 89.8. Put/off 7515, 7835, 8664. Put/always
1261591, 1376332, 1432272. Put/batch 72385, 85810, 96193.
PutBatchWriters x1 88604, 78126, 76120; x2 34378, 33715, 46447; x8 11177,
11546, 12094.

Replay of 1M records, ns/op: WAL only 2135357000, 2187712100, 2474007400;
snapshot + tail 2399293600, 2032084300, 2349005000.

Earlier series (same machine, before the kvbench change that excludes failed
requests; zero errors were reported then too, so the counts were unaffected):
reads batch 54481/54790/52856 ops/s (p99 4.45-4.66ms); writes off
59590/51610/45694; always 1500/1508/1510; batch 34600/33814/33892.
In-process Put/off 3.2us, Put/always ~620us, Put/batch ~39us, Get 140-157ns;
replay 1.21-1.23s (WAL only), 1.01-1.10s (snapshot + tail), 5 runs. A third,
busier series gave replay 3.5-4.4s.

</details>

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

Rerun after ticket 25 (vmctl at 3 replicas): crash took 2.9s, 2.9s and 2.8s. Hang took 6.0s, but only
after two fixes; before them it took 12.2s and 17.2s:

- **Hung PUTs stalled the drain.** With lease renewals, there is now always a PUT in
  flight during a hang. Go notices a client disconnect only after the handler
  has read the request body. A hung PUT therefore never returned, and the
  drain ran out its full 10s. The hang path now discards the body first.
- **CrashLoopBackOff.** A container's second restart within 10 minutes waits 10s.
  The demo now recreates `kv-0` between the crash and the hang, so the backoff
  isn't timed as part of the hang.

Why crash is faster: the kubelet sees the container exit immediately, so there
is no detection delay; only the restart plus the first startup and readiness
probes remain. A hang needs about 2 failed liveness probes (up to ~5s) first.

Environment: Intel Core i9-14900HX, Windows 11 Home 10.0.26300, Docker Desktop
(engine 29.6.2, WSL2 kernel 6.6.87.2, 32 CPUs / 14.5 GiB visible to Docker),
kind v0.33.0, kubectl client v1.36.1, 1 control-plane + 2 workers, kv on a
1Gi PVC.
