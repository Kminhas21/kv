# 15: Stop / Start operations

**What to build:** `POST /vms/{id}/stop` and `/start` return 202 with an operation, flip desired power, and the reconciler converges through power-off/power-on tasks. A second request while an operation is in flight returns 409. Afterwards Claude adds `vmcli vm stop|start`.

**Blocked by:** 12

**Status:** ready-for-human

- [ ] Desired power change + new op + current_op_id set in one CAS write
- [ ] 409 when current_op_id is set and that op isn't terminal
- [ ] Phase Stopped / Running after convergence
- [ ] Harness tests for stop → start and the 409

**Concepts:** generalizing the reconcile step function, avoiding duplicated code without over-abstracting.

**Manual check:** stop a running VM, `vmcli vm watch`, then `vmcli hv vms` shows power OFF.
