# 24: Snapshot + WAL compaction

**What to build:** The WAL no longer grows forever. Once `SNAPSHOT_EVERY` records (default 100k) have been written since the last snapshot, kv writes a snapshot, starts a new WAL segment and deletes the old one. Startup loads the newest snapshot and replays only the WAL tail written after it, so restart time is bounded.

**Blocked by:** 22, 23 (the snapshot carries expiry times)

**Status:** ready-for-agent

- [ ] Snapshot format: JSON with the revision it was taken at, plus every non-expired entry with its version and expiry time
- [ ] The map is copied under the write lock and the WAL switches to a new segment in the same locked section; the file is written outside the lock
- [ ] Write to a temp file, fsync it, rename it into place, fsync the directory, and only then delete WAL segments older than the snapshot
- [ ] Startup loads the newest snapshot, then replays segments, skipping records at or below the snapshot revision. A data dir with no snapshot (today's layout) still opens
- [ ] A crash at any step (temp file left behind, rename done but old segment not deleted) still recovers correctly
- [ ] The pause the copy causes to writes is measured and noted in Comments
- [ ] Tests: snapshot + tail replay matches the full history; crash-between-steps cases; revision continues correctly after reload

**Concepts:** atomic rename, fsyncing a directory, copy-on-snapshot, log segments, why the snapshot carries a revision.

**Mock vs production:** etcd snapshots on a record count too (`--snapshot-count`) and compacts MVCC history separately. Production stores snapshot without stopping writes (copy-on-write B-trees, fork() in Redis); our copy under the lock is a deliberate shortcut.

**Manual check:** set `SNAPSHOT_EVERY=1000`, write 5000 keys, watch segments rotate and the snapshot appear, restart, confirm all keys.
