# 23: TTL on keys

**What to build:** A client can PUT a key with a TTL (`?ttl=30s`). Once its expiry time passes it is an expired key and counts as missing everywhere: Get is 404, List skips it, and compare-and-swap treats it as missing, so `If-Match: 0` can create it again. A background sweeper reclaims memory. Restarts never extend a key's life.

**Blocked by:** 04 (lands after 22 to avoid conflicting store edits)

**Status:** ready-for-agent

- [ ] `?ttl=` parsed with `time.ParseDuration`; invalid or non-positive → 400
- [ ] An absolute expiry time is stored on the entry and in the WAL record; each PUT resets it; a PUT without `ttl` makes the key permanent
- [ ] Get, List and the If-Match check treat expired keys as missing, with no wait for the sweeper
- [ ] A sweeper goroutine on a `time.Ticker` (`TTL_SWEEP_INTERVAL`, default 1s) deletes expired keys under the write lock in chunks of at most 1000, writes no WAL record and does not bump the revision. It stops when the store closes
- [ ] Replay drops records that have already expired
- [ ] vmcli can set a TTL on put
- [ ] Tests with an injectable clock: expiry on read; the sweeper removes the key; `If-Match: 0` recreates an expired key; a reopen after expiry has no key; the TTL survives a restart unchanged

**Concepts:** lazy vs active expiry, `time.Ticker` + `context` for goroutine lifecycle, injecting a clock for tests, why absolute times beat durations on disk.

**Mock vs production:** Redis uses the same lazy + sampled active expiry. etcd attaches keys to leases rather than giving each key its own TTL, and lease expiry there *is* a replicated write that bumps the revision; we chose not to log expiry.

**Manual check:** `vmcli kv put tmp x --ttl 5s`, get it, wait 6s, get it → 404.
