# 08: Create-VM request path with idempotency

**What to build:** vmctl accepts `POST /vms` with an `Idempotency-Key` and returns 202 `{vm_id, operation_id}` immediately. It stores the VM (desired ON, phase Pending, new external ID) and a PENDING operation in kv, and makes no hypervisor call. The same key returns the same IDs, and the same key with a different body returns 422. `GET /vms`, `GET /vms/{id}` and `GET /operations/{id}` read them back. Afterwards Claude adds `vmcli vm create|get|list` and `vmcli op get`.

**Blocked by:** 05, 07

**Status:** resolved

- [x] Domain types VM, Operation, IdempotencyRecord (fields per SPEC "Data model"), JSON-tagged
- [x] Repository over the kv client: create, get, list, update-with-CAS
- [x] Validation → 422 (cpu > 0, memory_mb > 0, name 1–63 chars)
- [x] Idempotency record claimed with create-only write *before* the VM is written; request hash compared on reuse
- [x] Missing `Idempotency-Key` → 400
- [x] Handler tests via `httptest` with a real kv behind it

**Concepts:** async API design (202 + operation resource), idempotency keys (Stripe-style), struct tags, ID generation (`crypto/rand` hex), hashing a request (`crypto/sha256`), keeping handlers thin over a repository.

**Mock vs production:** a real platform also authenticates the caller, scopes idempotency keys per user/tenant, expires them after ~24h, and enforces quotas here.

**Manual check:** `vmcli vm create --name web1 --cpu 2 --mem 2048 --key k1` twice → same IDs. `vmcli kv list vms/` shows the raw JSON. Same key, different cpu → 422.
