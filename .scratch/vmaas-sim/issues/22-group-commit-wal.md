# 22: Group-commit WAL (zero-loss writes)

**What to build:** A write is acknowledged only once it is a durable write (see `CONTEXT.md`). Concurrent writers share one fsync per batch, so durability doesn't cost one fsync per request. `WAL_FSYNC` picks the mode: `off` (today's behaviour), `always` (fsync every write), `batch` (group commit, the default). Disk I/O moves out from under the store lock, so readers no longer wait on writes. Follows ADR-0001: apply to memory under the lock, acknowledge after fsync.

**Blocked by:** 04

**Status:** resolved

- [x] `WAL_FSYNC=off|always|batch`, default `batch`, read at startup and set in the ConfigMap
- [x] Under the lock, a writer checks If-Match, assigns the next revision, applies to memory and queues its record, preserving revision order in the log. The lock is released before any disk I/O
- [x] One flusher goroutine blocks for the first record, takes everything else already queued without waiting, writes the batch through a buffered writer, calls `Sync()` once, then wakes every writer in the batch. No timer
- [x] The HTTP response is sent only after that writer's batch has been fsynced (`batch`/`always` modes)
- [x] A failed write or Sync returns an error to every writer in the batch and poisons the store: all later writes fail
- [x] `Close()` drains the queue and fsyncs before closing the file
- [x] Tests: concurrent writers all get durable acks; batches really group more than one record under load; an injected Sync failure poisons the store; replay after reopen sees every acknowledged write
- [ ] `go test -race` clean: pending in CI (GitHub Actions on Linux runs `go test -race ./...`); not runnable on this Windows machine without cgo
- [x] `testing.B` benchmark of Put in all three modes with `b.RunParallel`; ns/op recorded in the Comments section

**Concepts:** group commit, a reply channel (`chan error`) per request, a single-writer goroutine, `bufio.Writer` + `File.Sync`, why the lock must not cover I/O, durability vs latency.

**Mock vs production:** etcd and PostgreSQL both batch fsyncs (group commit). etcd applies entries only after the Raft commit, so it has no "visible before durable" window; ours does, by design (ADR-0001). Real systems also use O_DSYNC and preallocated segment files.

**Manual check:** run kv in each mode, hammer it with concurrent PUTs, compare throughput; kill -9 the process mid-load and confirm every key that got a 2xx is present after restart.

## Comments

Benchmark: `go test ./internal/kv -run '^$' -bench BenchmarkPut -benchtime=2s -count=3`, with `b.RunParallel` at GOMAXPROCS=32. Intel Core i9-14900HX, Windows 11 Home (amd64), Go 1.26.5, WAL in the local temp dir.

| mode | ns/op (3 runs) |
|---|---|
| off | 5720, 7865, 8317 (rerun: 3301, 6976, 7936) |
| always | 1009363, 1292599, 881015 |
| batch | 54501, 42063, 70956 |

For a baseline I ran the same benchmark against the pre-change store.go (always effectively `off`): 4681, 7510, 7737 ns/op. So `off` did not get slower. `batch` is about 20x faster than `always`. The concurrent test logged 800 writes in 50 fsyncs on this machine, about 16 per batch.

Notes:
- `off` keeps today's path: the record is encoded to the file under the lock and then applied, with no flusher and no Sync. "Lock released before disk I/O" applies to `batch` and `always`.
- `-race` was not run because this machine has no cgo. Races were checked by reasoning: `queue` and `walErr` sit behind `qmu`, and the lock order is always `s.mu` then `qmu`. In batch/always mode the encoder and buffer are used only by the flusher, and Close waits on `flushed` before it touches the file.
