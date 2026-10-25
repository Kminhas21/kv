# 27: Real probes + timed self-heal

**What to build:** Probes reflect each service's own health, and the demo proves how fast the cluster recovers. kv is ready only after replay and while writes are allowed. With `FAULTS=1`, debug endpoints let the demo crash or hang a pod; the demo times each recovery from fault until the pod is Ready again and fails if it takes more than 10s.

**Blocked by:** 20, 22

**Status:** resolved

- [x] kv readiness: not ready until replay has finished, and not ready once the store is poisoned
- [x] vmctl and mockhv readiness reflect only their own state, never a dependency's
- [x] Liveness and readiness: `periodSeconds: 2`, `failureThreshold: 2`, `timeoutSeconds: 1` on all three services; kv gets a startupProbe (2s × 30)
- [x] With `FAULTS=1`, kv exposes a debug crash endpoint (process exits) and a debug hang endpoint (the HTTP handlers stop responding); without it those routes don't exist
- [x] The demo enables `FAULTS=1` and times crash and hang recovery separately, printing both durations and failing above 10s
- [x] Recovery times recorded in the benchmarks doc

**Concepts:** liveness vs readiness vs startup probes, why readiness must not depend on downstream services (cascading failures), CrashLoopBackOff timing.

**Mock vs production:** production fault injection uses chaos tooling (Chaos Mesh, Litmus) rather than debug endpoints in the binary; production probes are usually less aggressive to avoid flapping under load.

**Manual check:** `make demo`, read the printed recovery times.
