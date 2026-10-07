# 09: Scheduling: hosts + first-fit

**What to build:** vmctl seeds 3 simulated hosts on startup (safe on every restart). A reconciler with a synchronous `Tick()` places every Pending VM without a host on the first host with enough free CPU and memory, reserving capacity with a compare-and-swap keyed by VM ID. A VM that fits nowhere stays Pending with an "unschedulable" error. `GET /hosts` and `vmcli hosts` show capacity.

**Blocked by:** 08

**Status:** resolved

- [x] Host type with totals + reservations map; free capacity computed, not stored
- [x] Seeding uses create-only writes
- [x] Reservation is idempotent: reserving again for the same VM is a no-op
- [x] On CAS conflict: skip this VM this tick (re-read next tick), never overwrite
- [x] Reconciler runs `Tick` on a ticker in a goroutine, stopped via `context`
- [x] Tests: VM gets host_id after one Tick; oversized VM stays Pending; two VMs fill a host then spill to the next

**Concepts:** the reconcile loop shape, `time.NewTicker` + `select` on `ctx.Done()`, idempotent operations, why reserve-then-assign ordering is crash-safe here.

**Mock vs production:** the K8s scheduler runs filter → score plugins. vSphere DRS and OpenStack Placement handle overcommit ratios, affinity and NUMA. First-fit is the "filter" half with no scoring.

**Manual check:** create a few VMs, `vmcli hosts` shows reservations growing. Create a 999-CPU VM, `vmcli vm get` shows Pending + unschedulable.
