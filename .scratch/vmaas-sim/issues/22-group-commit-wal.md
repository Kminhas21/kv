# 22: Group-commit WAL (zero-loss writes)

**What to build:** A write is acknowledged only once it is a durable write (see `CONTEXT.md`). Concurrent writers share one fsync per batch, so durability doesn't cost one fsync per request. `WAL_FSYNC` picks the mode: `off` (today's behaviour), `always` (fsync every write), `batch` (group commit, the default). Disk I/O moves out from under the store lock, so readers no longer wait on writes. Follows ADR-0001: apply to memory under the lock, acknowledge after fsync.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] `WAL_FSYNC=off|always|batch`, default `batch`, read at startup and set in the ConfigMap
- [ ] Under the lock, a writer checks If-Match, assigns the next revision, applies to memory and queues its record, preserving revision order in the log. The lock is released before any disk I/O
- [ ] One flusher goroutine blocks for the first record, takes everything else already queued without waiting, writes the batch through a buffered writer, calls `Sync()` once, then wakes every writer in the batch. No timer
- [ ] The HTTP response is sent only after that writer's batch has been fsynced (`batch`/`always` modes)
- [ ] A failed write or Sync returns an error to every writer in the batch and poisons the store: all later writes fail
- [ ] `Close()` drains the queue and fsyncs before closing the file
- [ ] Tests: concurrent writers all get durable acks; batches really group more than one record under load; an injected Sync failure poisons the store; replay after reopen sees every acknowledged write; `go test -race` clean
- [ ] `testing.B` benchmark of Put in all three modes with `b.RunParallel`; ns/op recorded in the Comments section

**Concepts:** group commit, a reply channel (`chan error`) per request, a single-writer goroutine, `bufio.Writer` + `File.Sync`, why the lock must not cover I/O, durability vs latency.

**Mock vs production:** etcd and PostgreSQL both batch fsyncs (group commit). etcd applies entries only after the Raft commit, so it has no "visible before durable" window; ours does, by design (ADR-0001). Real systems also use O_DSYNC and preallocated segment files.

**Manual check:** run kv in each mode, hammer it with concurrent PUTs, compare throughput; kill -9 the process mid-load and confirm every key that got a 2xx is present after restart.
