# Benchmarks

Measured numbers for the kv + VM control plane simulator, recorded by the
scripts that produce them. Each section names its environment and the command
that reproduces it. Numbers from a laptop running kind are for comparing
changes against each other, not for predicting production.

## Self-heal (kind)

Produced by `make demo`, section 6. The kind overlay sets `FAULTS=1`, which
gives kv `POST /debug/crash` (process exits 1) and `POST /debug/hang` (every
handler, `/healthz` included, blocks). The demo times the span from the
trigger until `kv-0` is `Ready` again with `restartCount` incremented, and
fails above 10s.

Probes (all three services): `periodSeconds: 2`, `failureThreshold: 2`,
`timeoutSeconds: 1`. kv also has a startupProbe, 2s x 30.

| Fault | Run 1 | Run 2 | Run 3 |
|-------|-------|-------|-------|
| crash | 4.1s  | 3.2s  | 1.9s  |
| hang  | 6.3s  | 7.8s  | 7.2s  |

Why crash is faster: the kubelet sees the container exit immediately, so there
is no detection delay; only the restart plus the first startup and readiness
probes remain. A hang needs about 2 failed liveness probes (up to ~5s) first.

Environment: Intel Core i9-14900HX, Windows 11 Home 10.0.26300, Docker Desktop
(engine 29.6.2, WSL2 kernel 6.6.87.2, 32 CPUs / 14.5 GiB visible to Docker),
kind v0.33.0, kubectl client v1.36.1, 1 control-plane + 2 workers, kv on a
1Gi PVC.
