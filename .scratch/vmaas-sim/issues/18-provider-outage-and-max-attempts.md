# 18: Provider outage + max attempts

**What to build:** During a hypervisor outage, observed power becomes UNKNOWN, desired state is untouched, and nothing gets deleted or recreated. After the outage, state recovers on its own. An operation that keeps failing gives up after 5 attempts and is marked FAILED with the last error.

**Blocked by:** 12

**Status:** ready-for-human

- [ ] Outage → observed UNKNOWN, last_error set, no destructive action
- [ ] Recovery after outage clears without user action
- [ ] attempts counter; ≥5 → op FAILED, phase Failed
- [ ] Harness tests for outage + recovery and max attempts (`fail_rate=1`)

**Concepts:** "can't see it" ≠ "it's gone", bounded retries, why distributed systems need explicit UNKNOWN states.

**Manual check:** `vmcli hv faults --outage on`, watch a VM go UNKNOWN; turn it off, watch it recover.
