# 03: Compare-and-swap

**What to build:** Writers can say "only apply this if the key is still at version N." A stale write gets a 409 instead of silently overwriting. `If-Match: 0` means "create only if absent."

**Blocked by:** 02

**Status:** resolved

- [x] Store Put/Delete accept an optional expected version; mismatch returns a typed conflict error
- [x] Expected version 0 succeeds only when the key does not exist
- [x] HTTP: `If-Match` header honored on PUT and DELETE; conflict → 409 with the current version in the body
- [x] Tests: stale write rejected, correct version accepted, create-only on existing key rejected

**Concepts:** optimistic concurrency, sentinel errors (`errors.New`, `errors.Is`), why the check and the write must happen under the same lock (check-then-act race).

**Hints:** `var ErrConflict = errors.New(...)`; consider a small options value or a pointer/`ok` pair for "expected version present." Avoid a magic `-1`.

**Mock vs production:** K8s `resourceVersion` + 409 Conflict; etcd `Txn(If(ModRevision==N)).Then(Put)`; Postgres `UPDATE ... WHERE version = N`.

**Manual check:** PUT a key, note ETag. `curl -i -X PUT -H "If-Match: <old>" ...` after another write → 409. `If-Match: 0` on an existing key → 409.
