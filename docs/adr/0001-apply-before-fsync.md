# Apply writes to memory before fsync; acknowledge only after

In `WAL_FSYNC=batch` mode a write is applied to the in-memory map under the store lock, queued for group commit, and its HTTP response waits for the batch's fsync. Readers can therefore see a write for about a millisecond before it is durable, and a machine crash in that window loses a value someone already read. No acknowledged write is lost, which is how we define zero-loss (see `CONTEXT.md`).

## Considered Options

- **Apply only after fsync.** No visibility window, but compare-and-swap checks would then have to consult a table of pending writes, or writes would serialize on the fsync again. Rejected as too complex for the gain. etcd avoids the window because Raft commits an entry before it is applied.
