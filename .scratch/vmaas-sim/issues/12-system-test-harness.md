# 12: System test harness + recovery tests

**What to build:** One in-process harness that starts real kv, mockhv and vmctl as `httptest` servers, steps the reconciler manually, can "restart" vmctl against the same kv, and can flip mockhv faults. Claude builds the harness; you write the scenario tests.

**Blocked by:** 11

**Status:** ready-for-agent (harness) → ready-for-human (tests)

- [x] Harness helper with `t.Cleanup` teardown
- [x] Test (you): same idempotency key twice → same IDs, 1 provider VM
- [x] Test (you): timeout-after-success → op UNKNOWN → SUCCEEDED, 1 provider VM
- [x] Test (you): restart vmctl after task submitted, before polled → resumes, 1 provider VM
- [ ] `go test -race ./...` green (blocked: needs cgo / a C compiler on this machine)

**Concepts:** test helpers + `t.Helper()`, `t.Cleanup`, the race detector, testing through public seams only, deterministic time via `Tick()`.

**Manual check:** `go test -v -run TestSystem ./...` and read the logs of one scenario top to bottom.

## Comments

Resolved by the `newSystem` harness built during tickets 10-11 (internal/vm/provision_test.go):
real kv + mockhv + vmctl over httptest, manual Tick(), fresh reconcilers to simulate restarts,
fault switches. Covers idempotency, timeout-after-success, restart mid-create and mid-power-on.
Open item: run the race detector once a C toolchain is installed.
