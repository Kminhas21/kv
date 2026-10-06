# 12: System test harness + recovery tests

**What to build:** One in-process harness that starts real kv, mockhv and vmctl as `httptest` servers, steps the reconciler manually, can "restart" vmctl against the same kv, and can flip mockhv faults. Claude builds the harness; you write the scenario tests.

**Blocked by:** 11

**Status:** ready-for-agent (harness) → ready-for-human (tests)

- [ ] Harness helper with `t.Cleanup` teardown
- [ ] Test (you): same idempotency key twice → same IDs, 1 provider VM
- [ ] Test (you): timeout-after-success → op UNKNOWN → SUCCEEDED, 1 provider VM
- [ ] Test (you): restart vmctl after task submitted, before polled → resumes, 1 provider VM
- [ ] `go test -race ./...` green

**Concepts:** test helpers + `t.Helper()`, `t.Cleanup`, the race detector, testing through public seams only, deterministic time via `Tick()`.

**Manual check:** `go test -v -run TestSystem ./...` and read the logs of one scenario top to bottom.
