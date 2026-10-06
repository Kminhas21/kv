# 16: Delete + capacity release

**What to build:** `DELETE /vms/{id}` → 202. The reconciler powers off, deletes on the provider, releases the host reservation, and marks the VM Deleted (the record is kept). A VM that couldn't fit before now schedules.

**Blocked by:** 15

**Status:** ready-for-human

- [ ] Steps are each idempotent: provider "not found" on delete counts as success
- [ ] Reservation released via CAS; releasing twice is a no-op
- [ ] Test: fill capacity, delete one, the Pending VM schedules on the next tick

**Concepts:** idempotent teardown, "not found is success" for deletes, finalizers in K8s (the same idea).

**Manual check:** delete a VM, `vmcli hosts` shows capacity back, `vmcli hv vms` no longer lists it.
