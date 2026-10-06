# 05: kv Go client

**What to build:** A typed Go client other services use to talk to kv: Get, Put (with optional expected version), Delete, List(prefix). HTTP status codes map back to the same typed errors the store uses (not found, conflict), so callers write `errors.Is(err, kv.ErrConflict)` whether local or remote.

**Blocked by:** 03

**Status:** ready-for-human

- [ ] Client constructed with base URL and an `*http.Client` that has a timeout
- [ ] All methods take `context.Context` as first argument
- [ ] 404 → not-found error, 409 → conflict error, other non-2xx → error with status and body
- [ ] Round-trip test against an `httptest.Server` running the real kv handler

**Concepts:** `context.Context` for cancellation and timeouts, `http.NewRequestWithContext`, always closing `resp.Body`, error wrapping (`fmt.Errorf("...: %w", err)`), why clients need timeouts (the default client has none!).

**Mock vs production:** real clients add retries with backoff, connection pooling tuning, TLS, and auth. etcd's Go client also handles failover between members.

**Manual check:** none beyond tests. Ticket 07 gives you the CLI built on this client.
