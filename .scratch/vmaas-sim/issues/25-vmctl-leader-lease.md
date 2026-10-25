# 25: vmctl leader election, 3 replicas

**What to build:** vmctl runs as 3 replicas behind its Service. Every replica serves the API; exactly one, the leader, runs the reconciler. Leadership is a lease key `leases/vmctl` in kv with a TTL (ADR-0002). If the leader dies, another replica takes over within about one lease duration.

**Blocked by:** 23, 20 (the leader releases its lease on shutdown)

**Status:** resolved

- [x] Acquire: PUT `If-Match: 0` with `ttl=10s`, value = this replica's identity (pod name). Renew every 3s with If-Match on the held version
- [x] Before each reconcile tick the leader confirms it still holds the lease; on a lost renewal it stops reconciling immediately
- [x] Followers keep retrying acquisition; on graceful shutdown the leader deletes its lease so handoff is immediate
- [x] Lease TTL and renew interval come from config; logs say when this replica gains or loses leadership
- [x] vmctl Deployment at 3 replicas; the Service balances API traffic across all of them
- [x] Harness test: 3 in-process vmctls, exactly one reconciles; stop the leader, another takes over; no VM is provisioned twice
- [x] Demo script: kill the leader pod mid-provisioning, show the new leader finishing the work

**Concepts:** leases vs locks, renewal deadlines, why a stale leader is the classic split-brain bug, how compare-and-swap on VM records limits the damage.

**Mock vs production:** Kubernetes controllers use `coordination.k8s.io/Lease` via client-go `leaderelection`. Production adds fencing tokens so a paused ex-leader's writes are rejected by the storage layer itself; we rely on the existing If-Match on VM records.

**Manual check:** `kubectl logs -l app=vmctl -f --prefix`, delete the leader pod, watch another replica log that it became leader.
