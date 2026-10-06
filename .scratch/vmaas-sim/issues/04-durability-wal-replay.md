# 04: Durability: WAL + replay

**What to build:** Every write is appended to a write-ahead log before it's acknowledged. On startup the server replays the log, so killing and restarting kv brings back all keys and versions. A torn final line from a crash is skipped instead of failing startup.

**Blocked by:** 03

**Status:** resolved

- [x] Store opens from a WAL path (configurable via `WAL_PATH`)
- [x] Each Put/Delete appends one JSON line (op, key, value, version) before mutating memory
- [x] Replay restores data and the revision counter (next write's version > all replayed)
- [x] Corrupt/partial last line is skipped and logged
- [x] Tests: write → close → reopen same file → data present; torn tail tolerated

**Concepts:** append-only logs, `os.OpenFile` flags, `bufio.Scanner` line reading, `json.Encoder`, `Close()` and `defer`, why "log first, then apply" matters, `t.TempDir()`.

**Hints:** `os.O_APPEND|os.O_CREATE|os.O_WRONLY`, `bufio.NewScanner`, `json.Unmarshal` error on a line = stop/skip. Hold the store lock across "append + apply" so the log order matches memory order.

**Mock vs production:** no fsync (data can be lost on a machine crash, though not a process crash), no compaction (the log grows forever). etcd fsyncs every commit and snapshots + compacts. Ticket 21 measures fsync cost.

**Manual check:** run kv with `WAL_PATH=./data.wal`, PUT keys, Ctrl+C, restart, GET → values back. Open the WAL in an editor and read it.
