# 23: TTL on keys

**What to build:** A client can PUT a key with a TTL (`?ttl=30s`). Once its expiry time passes it is an expired key and counts as missing everywhere: Get is 404, List skips it, and compare-and-swap treats it as missing, so `If-Match: 0` can create it again. A background sweeper reclaims memory. Restarts never extend a key's life.

**Blocked by:** 04 (lands after 22 to avoid conflicting store edits)

**Status:** resolved

- [x] `?ttl=` parsed with `time.ParseDuration`; invalid or non-positive → 400
- [x] An absolute expiry time is stored on the entry and in the WAL record; each PUT resets it; a PUT without `ttl` makes the key permanent
- [x] Get, List and the If-Match check treat expired keys as missing, with no wait for the sweeper
- [x] A sweeper goroutine on a `time.Ticker` (`TTL_SWEEP_INTERVAL`, default 1s) deletes expired keys under the write lock in chunks of at most 1000, writes no WAL record and does not bump the revision. It stops when the store closes
- [x] Replay drops records that have already expired
- [x] vmcli can set a TTL on put
- [x] Tests with an injectable clock: expiry on read; the sweeper removes the key; `If-Match: 0` recreates an expired key; a reopen after expiry has no key; the TTL survives a restart unchanged

Notes: the Go API is `Store.PutTTL` / `kvclient.Client.PutTTL` (`Put` delegates with no TTL). The restart tests write through a shifted clock and replay against the real one, since `Open` reads `time.Now`. The manual check below was not run against a live cluster.

**Concepts:** lazy vs active expiry, `time.Ticker` + `context` for goroutine lifecycle, injecting a clock for tests, why absolute times beat durations on disk.

**Mock vs production:** Redis uses the same lazy + sampled active expiry. etcd attaches keys to leases rather than giving each key its own TTL, and lease expiry there *is* a replicated write that bumps the revision; we chose not to log expiry.

**Manual check:** `vmcli kv put --ttl 5s tmp x` (Go's `flag` package stops at the first positional argument, so flags come first), get it, wait 6s, get it → 404.
