# 14: Cluster demo script + rehearsal

**What to build:** A step-by-step demo script (pausing between steps) that you run and narrate:
1. Create a VM → Running.
2. Duplicate POST → same IDs, 1 VM.
3. Turn on timeout-after-success, create → UNKNOWN → recovered, 1 VM.
4. Delete the vmctl pod mid-operation → the new pod resumes.
5. Delete the kv pod → data survives through the PVC.

**Blocked by:** 12, 13

**Status:** resolved (script); rehearsal is yours

- [x] Script prints what it's about to do and what to look for
- [ ] You can explain each step out loud, including the "mock vs production" line
- [ ] Notes added to the bottom of this ticket under Comments: what surprised you

**Manual check:** this whole ticket is the manual check.

## Comments

2026-10-06: `DEMO_AUTO=1 make demo` passes all 18 checks on the kind cluster (exit 0):
create→Running, idempotent retry + 422 on key reuse, lost response adopted with 1 attempt,
vmctl force-killed mid-create (new pod polled the saved task, 1 attempt, 1 VM),
kv-0 force-killed (new pod UID, same PV, WAL replayed, all records intact).
Lesson from the first run: `pipefail` + `cmd | grep -q` reports failure when cmd is
*expected* to fail; capture output first, then match.
