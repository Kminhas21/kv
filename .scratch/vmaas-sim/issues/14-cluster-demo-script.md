# 14: Cluster demo script + rehearsal

**What to build:** A step-by-step demo script (pausing between steps) that you run and narrate:
1. Create a VM → Running.
2. Duplicate POST → same IDs, 1 VM.
3. Turn on timeout-after-success, create → UNKNOWN → recovered, 1 VM.
4. Delete the vmctl pod mid-operation → the new pod resumes.
5. Delete the kv pod → data survives through the PVC.

**Blocked by:** 12, 13

**Status:** ready-for-agent (script) → ready-for-human (rehearsal)

- [ ] Script prints what it's about to do and what to look for
- [ ] You can explain each step out loud, including the "mock vs production" line
- [ ] Notes added to the bottom of this ticket under Comments: what surprised you

**Manual check:** this whole ticket is the manual check.
