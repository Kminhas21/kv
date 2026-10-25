# 20: Graceful shutdown

**What to build:** All three services shut down cleanly on SIGTERM. They stop accepting requests, finish in-flight ones, let the reconciler finish its current tick, and the kv WAL file is closed. Matters because Kubernetes sends SIGTERM before killing a pod.

**Blocked by:** 11

**Status:** resolved

- [x] `signal.NotifyContext` for SIGINT/SIGTERM
- [x] `http.Server.Shutdown` with a deadline
- [x] Reconciler exits after the current tick on context cancel
- [x] kv calls `store.Close()` after `Shutdown`, flushing and fsyncing the WAL
- [x] `terminationGracePeriodSeconds: 15` set on all three workloads
- [x] Logs show shutdown sequence

**Concepts:** context propagation, `sync.WaitGroup`, K8s `terminationGracePeriodSeconds` and preStop hooks.

**Manual check:** run vmctl, Ctrl+C mid-provisioning, read the logs; in kind, `kubectl delete pod` and read the logs.
