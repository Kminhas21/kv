# 19: Drift self-heal

**What to build:** If a VM disappears from the hypervisor outside the control plane (out-of-band delete) while desired is ON, the reconciler detects it (observed ABSENT), logs drift, clears the provider ID, and re-provisions it through the normal create path. Exactly one VM exists afterwards.

**Blocked by:** 18

**Status:** ready-for-human

- [ ] Distinguish ABSENT (provider says 404) from UNKNOWN (provider unreachable)
- [ ] Only ABSENT triggers recreate
- [ ] Harness test: out-of-band delete → recreated, 1 VM

**Concepts:** drift detection, self-healing like a ReplicaSet, why UNKNOWN must never trigger recreate (split-brain duplicates).

**Mock vs production:** many VMaaS platforms *alert* on drift instead of auto-recreating, because a human may have deleted the VM on purpose. Be ready to discuss that policy choice.

**Manual check:** `vmcli hv kill <provider-id>`, `vmcli vm watch <id>`.
