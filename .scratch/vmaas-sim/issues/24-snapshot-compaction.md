# 24: Snapshot + WAL compaction

**What to build:** The WAL no longer grows forever. Once `SNAPSHOT_EVERY` records (default 100k) have been written since the last snapshot, kv writes a snapshot, starts a new WAL segment and deletes the old one. Startup loads the newest snapshot and replays only the WAL tail written after it, so restart time is bounded.

**Blocked by:** 22, 23 (the snapshot carries expiry times)

**Status:** resolved

- [x] Snapshot format: JSON with the revision it was taken at, plus every non-expired entry with its version and expiry time
- [x] The map is copied under the write lock and the WAL switches to a new segment in the same locked section; the file is written outside the lock
- [x] Write to a temp file, fsync it, rename it into place, fsync the directory, and only then delete WAL segments older than the snapshot
- [x] Startup loads the newest snapshot, then replays segments, skipping records at or below the snapshot revision. A data dir with no snapshot (today's layout) still opens
- [x] A crash at any step (temp file left behind, rename done but old segment not deleted) still recovers correctly
- [x] The pause the copy causes to writes is measured and noted in Comments
- [x] Tests: snapshot + tail replay matches the full history; crash-between-steps cases; revision continues correctly after reload

Notes: `Open(path, mode, snapshotEvery)`; `SNAPSHOT_EVERY` (default 100000, 0 disables) is read in cmd/kv. Layout next to `WAL_PATH`: `kv.wal` (first segment, base 0), `kv.wal.<R>` (records > R), `kv.snap`, `kv.snap.tmp`. In batch/always mode the segment switch is a marker in the group-commit queue: the flusher commits it alone, so every record queued before it is written and fsynced to the old segment first. The snapshot goroutine waits for that switch, so a snapshot never holds a write that is not yet durable. A snapshot I/O failure is logged and retried at the next threshold, and the old segments are kept. A corrupt `kv.snap` refuses to open. Only a fsynced file is ever renamed to that name, and the segments it replaced may be gone.

**Comments:** copy pause (`BenchmarkSnapshotCopy100k`, i9-14900HX): **5.4-8.2 ms per snapshot of 100k keys** (7.3 MB allocated). Manual run with `SNAPSHOT_EVERY=1000` and 5000 keys: 0.6 ms pause. Segments rotated to `kv.wal.5000` plus `kv.snap`, and after a hard kill (Stop-Process) the restart loaded all 5000 keys at revision 5000.

**Concepts:** atomic rename, fsyncing a directory, copy-on-snapshot, log segments, why the snapshot carries a revision.

**Mock vs production:** etcd snapshots on a record count too (`--snapshot-count`) and compacts MVCC history separately. Production stores snapshot without stopping writes (copy-on-write B-trees, fork() in Redis); our copy under the lock is a deliberate shortcut.

**Manual check:** set `SNAPSHOT_EVERY=1000`, write 5000 keys, watch segments rotate and the snapshot appear, restart, confirm all keys.
