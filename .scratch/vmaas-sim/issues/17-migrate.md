# 17: Migrate

**What to build:** `POST /vms/{id}/migrate` with an optional target host. The reconciler reserves capacity on the target (or lets the scheduler pick a different host), submits a relocate task, then releases the source reservation and updates host_id. Phase is Migrating during the move.

**Blocked by:** 16

**Status:** ready-for-human

- [ ] Target reservation before relocate; source released only after success
- [ ] Relocate failure → target reservation released, VM stays on source
- [ ] Test: migrate moves host_id and reservations; no host is ever over capacity

**Concepts:** two-phase resource handoff, why you hold *both* reservations during the move.

**Mock vs production:** real live migration copies memory iteratively while the VM runs, does a brief stop-and-copy, needs shared or migrated storage, and updates the network (RARP/GARP). Know this for the interview.

**Manual check:** `vmcli vm migrate <id> --to host-2`, `vmcli hosts` before and after.
