# Leader election uses a lease key in kv, not the Kubernetes Lease API

vmctl replicas elect a leader by competing for `leases/vmctl` in kv. They use the key's TTL and the existing If-Match compare-and-swap. This keeps the repo free of a `client-go` dependency and RBAC setup, and it gives the TTL feature a real consumer. A stale leader is guarded only by compare-and-swap on VM records, not by fencing tokens.

## Considered Options

- **`coordination.k8s.io/Lease` via client-go leaderelection.** This is the production pattern, and the one to reach for outside this learning project. Rejected here because it adds a large dependency and RBAC, and it bypasses the store we built.
