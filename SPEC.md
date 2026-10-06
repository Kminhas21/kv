# SPEC — KV Store + VM Control Plane Simulator

Status: ready-for-agent · Master spec · Supersedes `SPEC.v1.md` (kept for reference; its Kubernetes explanations are still useful reading) · Phase 2 checklist reference: `KV_VM_Control_Plane_Handoff.md`

---

## Problem Statement

I'm interviewing for a Software Engineering internship on a VM-as-a-Service team. The job means building the control plane that provisions, schedules and operates virtual machines on hypervisors (vSphere, KVM), written in Go and running on Kubernetes. I know the systems concepts on paper, but I've never built a control plane. I've never felt why "the client timed out" is different from "the request failed," and I've never deployed a stateful service to Kubernetes and watched it survive a pod kill. My Go is beginner-to-intermediate.

I have about two weeks, and I want a working end-to-end slice today (4–5 hours). I want to learn by building and testing things myself: running the system, poking it from a CLI, breaking it on purpose, and watching it recover. I don't want a project handed to me, and I don't want to sink time into things the job doesn't care about, like consensus algorithms, gRPC plumbing or frontends.

## Solution

A small three-service system on a local `kind` Kubernetes cluster. Each service is a toy version of a real production component:

| Service | Runs as | Plays the role of |
|---|---|---|
| **kv**: a key-value store with a write-ahead log and per-key versions (compare-and-swap) | StatefulSet with a PersistentVolumeClaim | etcd (where Kubernetes, and many control planes, keep state) |
| **vmctl**: VM REST API, scheduler, reconciler | Deployment | the VMaaS control plane (in K8s terms: apiserver + scheduler + controllers) |
| **mockhv**: a fake hypervisor with async tasks and fault injection | Deployment | vCenter/vSphere API, or libvirt on a KVM host |

The user asks vmctl for a VM. vmctl records the intent (desired state) in kv and returns an operation ID right away. A background reconciler then makes reality match the intent: it picks a host with enough capacity, asks the hypervisor to create the VM, polls the async task, powers the VM on, and keeps observing it. Every step is persisted, so killing vmctl at any moment loses nothing. The hypervisor can be told to misbehave (slow, down, or "succeed but time out"), and the reconciler must recover without ever creating a duplicate VM.

A command-line tool (`vmcli`) lets me drive and inspect everything by hand: create VMs, watch state change live, look at the hypervisor's inventory, flip fault switches, and read raw kv keys.

## User Stories

### KV store (the "etcd")
1. As a control-plane developer, I want to PUT, GET and DELETE string values by key over HTTP, so that services can share state.
2. As a control-plane developer, I want every write to return a version number, so that I can tell whether a value changed since I read it.
3. As a control-plane developer, I want a PUT with `If-Match: <version>` to fail with 409 when the key has changed, so that two writers can't silently overwrite each other (optimistic concurrency, like K8s `resourceVersion`).
4. As a control-plane developer, I want `If-Match: 0` to mean "create only if absent," so that I can do safe one-time initialization (seeding hosts, claiming idempotency keys).
5. As a control-plane developer, I want to list all keys under a prefix (e.g. `vms/`) with their values and versions, so that the reconciler can scan every VM.
6. As an operator, I want every write appended to a WAL file before it is acknowledged, so that a restart recovers all data.
7. As an operator, I want the store to replay the WAL on startup and skip a torn last line, so that a crash mid-write doesn't brick the store.
8. As an operator, I want versions to keep increasing across restarts, so that a stale client can never match a recycled version.
9. As an operator, I want the kv pod to keep its data when Kubernetes deletes and recreates it, so that I can see why StatefulSets and PVCs exist.
10. As an operator, I want `/healthz` and `/readyz` endpoints, so that Kubernetes knows when the pod is alive and ready for traffic.

### VM lifecycle API (vmctl)
11. As an engineer, I want to request a VM with name, CPU and memory and get back a VM ID and an operation ID immediately (HTTP 202), so that I'm not blocked while the VM boots.
12. As an engineer, I want to pass an `Idempotency-Key` header on create, so that retrying a request (because my network flaked) returns the same VM instead of a second one.
13. As an engineer, I want reusing an idempotency key with a different request body to be rejected (422), so that a client bug doesn't silently return the wrong VM.
14. As an engineer, I want to GET a VM and see its spec, desired power state, observed power state, phase, host, provider ID, last observation time and last error, so that I understand exactly where it is.
15. As an engineer, I want to list all VMs, so that I can see my fleet.
16. As an engineer, I want to GET an operation and see its type, status, attempts and last error, so that I can track progress of an async request.
17. As an engineer, I want to stop and start a VM, so that I can manage power state.
18. As an engineer, I want to delete a VM and have its host capacity released, so that resources are reclaimed.
19. As an engineer, I want to migrate a VM to another host (or let the scheduler choose one), so that I understand what host evacuation and rebalancing involve.
20. As an engineer, I want a second lifecycle request on a VM that already has an operation in flight to be rejected with 409, so that conflicting operations don't race.
21. As an engineer, I want invalid specs (zero CPU, a name that is empty or too long, etc.) rejected with 422, so that garbage never reaches the scheduler.
22. As an engineer, I want to list hosts with their total and reserved CPU/memory, so that I can see the scheduler's view of capacity.

### Scheduling
23. As the control plane, I want to place each new VM on the first host with enough free CPU and memory (first-fit), so that VMs land somewhere valid.
24. As the control plane, I want capacity reservation to be a compare-and-swap on the host record, keyed by VM ID, so that reserving twice for the same VM is a no-op and two schedulers can't oversubscribe a host.
25. As an engineer, I want a VM that fits on no host to stay `Pending` with an "unschedulable" error (not fail), so that it gets placed once capacity frees up, like a K8s pod stuck in Pending.

### Reconciliation (desired vs observed)
26. As the control plane, I want a background loop that compares desired state to observed state for every VM and takes one corrective step at a time, so that the system converges no matter where it was interrupted.
27. As the control plane, I want to look up the provider's inventory by the VM's stable external ID before ever submitting a create, so that a retry can never produce a duplicate VM.
28. As the control plane, I want to persist the provider task ID as soon as the provider accepts a create, so that after a restart I poll the existing task instead of creating again.
29. As the control plane, I want a timed-out provider call recorded as `UNKNOWN`, not `FAILED`, so that I go find out what actually happened instead of guessing.
30. As an engineer, I want to see the timeout-after-success case recover: the hypervisor creates the VM, the response is lost, and the next reconcile finds the VM by external ID and adopts it. Exactly one VM exists at the end.
31. As an engineer, I want a hypervisor outage to mark observations stale/unknown while keeping desired state untouched, so that "can't see it" is never treated as "it's gone."
32. As an engineer, I want an operation that keeps failing to give up after a bounded number of attempts and be marked `FAILED` with the last error, so that broken requests don't retry forever.
33. As an engineer, I want a VM deleted out-of-band on the hypervisor (while desired state is still ON) to be detected as drift and recreated, so that I see self-healing like a K8s ReplicaSet.
34. As an operator, I want to kill vmctl mid-operation and see the new pod resume from kv state without duplication, so that I trust the "stateless controller, state in the store" design.
35. As an operator, I want structured logs (`slog`) for every reconcile step, state transition, provider call and retry, so that I can debug the pipeline from logs alone.

### Mock hypervisor (mockhv)
36. As the control plane, I want create, power-on, power-off, delete and relocate to be async tasks that I poll, so that the mock behaves like vSphere's task model.
37. As the control plane, I want to look up provider VMs by external ID, so that I can de-duplicate.
38. As a tester, I want a fault switch for create delay, timeout-after-success (do the work, then answer too late), random failure rate, and full outage, so that I can reproduce real failure modes on demand.
39. As a tester, I want mockhv to deliberately **not** de-duplicate creates, so that the control plane, not the hypervisor, is responsible for preventing duplicates (like a real hypervisor that happily makes two VMs).
40. As a tester, I want to delete a provider VM directly (out-of-band), so that I can simulate drift.

### Manual testing and CLI
41. As a learner, I want a `vmcli` command to create, list, get, start, stop, delete and migrate VMs, so that I can drive the system without hand-writing curl.
42. As a learner, I want `vmcli vm watch <id>` to print each state transition as it happens, so that I can *see* reconciliation progress.
43. As a learner, I want `vmcli hv vms` and `vmcli hv faults ...` to inspect hypervisor inventory and flip fault switches, so that I can verify "exactly one VM" myself.
44. As a learner, I want `vmcli kv list <prefix>` / `kv get <key>`, so that I can see the raw records the control plane stores.
45. As a learner, I want every service runnable locally with `go run` (no Kubernetes needed), so that I can iterate fast before deploying.
46. As a learner, I want a step-by-step demo script for the kind cluster (create, duplicate POST, timeout-after-success, kill vmctl, kill kv), so that I can rehearse the story I'll tell in the interview.

### Kubernetes
47. As an operator, I want one `make` target to create the kind cluster, build images, load them and apply manifests, so that the cluster loop is quick.
48. As an operator, I want resource requests/limits, liveness/readiness probes and ConfigMap-driven settings on every pod, so that I learn the knobs the real platform uses.
49. As an operator, I want to reach vmctl from my laptop through `kubectl port-forward`, so that vmcli works against the cluster just like against local processes.

## Implementation Decisions

### Domain vocabulary (use these words consistently)
- **Desired state**: what the user asked for (`desired_power`: `ON`, `OFF` or `DELETED`; `host_id` target for migration).
- **Observed state**: what the hypervisor last reported (`observed_power`: `ON`, `OFF`, `ABSENT` or `UNKNOWN`), plus `last_observed_at`.
- **Phase**: vmctl's summary of where a VM is: `Pending` → `Provisioning` → `Running`/`Stopped`; also `Migrating`, `Deleting`, `Deleted`, `Failed`.
- **Operation**: one user-requested change (`CREATE`, `START`, `STOP`, `DELETE`, `MIGRATE`) with status `PENDING`, `RUNNING`, `SUCCEEDED`, `FAILED` or `UNKNOWN`.
- **Provider**: the hypervisor (mockhv). **Provider task**: an async job ID returned by the provider. **Provider VM ID**: the provider's own ID for the VM.
- **External ID**: a stable ID vmctl assigns before calling the provider and stamps onto the provider VM. It is the de-duplication key.
- **Host**: a simulated hypervisor host with CPU/memory capacity. **Reservation**: capacity held on a host for one VM.
- **Reconcile**: one pass that compares desired state to observed state and takes at most one step per VM.

### Module layout
- **kv store module**: in-memory map plus WAL plus a global revision counter. Interface: Get, Put (with optional expected version), Delete (with optional expected version), List(prefix). Returns typed errors for "not found" and "version conflict."
- **kv server**: an HTTP adapter over the store module, plus health endpoints.
- **kv client module**: a typed Go HTTP client for kv, used by vmctl and vmcli. Maps 404/409 back to the same typed errors.
- **vm module**: domain types (VM, Operation, Host, IdempotencyRecord), a repository over the kv client (JSON encode/decode, CAS updates), the scheduler, the reconciler, and the HTTP API.
- **hypervisor client interface** inside vmctl: CreateVM, GetTask, FindByExternalID, GetVM, PowerOn, PowerOff, DeleteVM, Relocate. Implemented by an HTTP client for mockhv. The interface is the seam where a real govmomi (vSphere) or libvirt driver would plug in.
- **mockhv**: a standalone service with its own in-memory inventory (it deliberately loses everything on restart; that's a fault to talk about, not a bug).
- **vmcli**: a stdlib `flag`-based CLI with subcommands, talking to vmctl, kv and mockhv over HTTP.

### KV semantics
- **Versions**: one global revision counter; each write gets `rev+1`, and that becomes the key's version (same idea as etcd's `mod_revision`). On replay the counter resumes at the highest revision seen.
- **WAL**: JSON lines (op, key, value, version), appended before the write is applied and acknowledged. No fsync and no compaction today (documented as a production gap). A torn or corrupt final line is skipped on replay.
- **HTTP contract**:
  - `PUT /keys/{key}`: body is the raw value. Optional `If-Match: <version>`, where `0` means create-only. Returns 201 for created, 200 for updated, 409 on version mismatch. New version in the `ETag` header.
  - `GET /keys/{key}`: 200 with value and `ETag`, or 404.
  - `DELETE /keys/{key}`: optional `If-Match`. Returns 204, 404 or 409.
  - `GET /keys?prefix=p`: a JSON array of `{key, value, version}`, sorted by key.
  - `GET /healthz`, `GET /readyz`.
- **Key namespace**: `vms/{id}`, `ops/{id}`, `hosts/{id}`, `idem/{key}`.

### vmctl API contract
- `POST /vms`: requires the `Idempotency-Key` header. Body `{name, cpu, memory_mb}`. Returns 202 `{vm_id, operation_id}`. The same key with the same body returns the same IDs. The same key with a different body returns 422. Validation failures return 422.
- `GET /vms`, `GET /vms/{id}`: the VM record (fields under "Data model").
- `POST /vms/{id}/start`, `POST /vms/{id}/stop`, `DELETE /vms/{id}`, `POST /vms/{id}/migrate` (optional body `{target_host_id}`): each returns 202 `{operation_id}`. Returns 409 if another operation is in flight, 404 if the VM is unknown.
- `GET /operations/{id}`.
- `GET /hosts`: totals, reserved amounts and the list of reservations.
- Errors are JSON `{error: "..."}`.

### Data model (stored as JSON in kv)
- **VM**: id, external_id, name, cpu, memory_mb, desired_power, phase, host_id, provider_vm_id, observed_power, last_observed_at, last_error, current_op_id, created_at, updated_at. The kv version doubles as the VM's resource version.
- **Operation**: id, vm_id, type, status, provider_task_id, attempts, last_error, target_host_id (migrate), created_at, updated_at.
- **Host**: id, cpu_total, memory_mb_total, reservations (map of vm_id → {cpu, memory_mb}). Free capacity is computed, never stored. Three hosts are seeded at vmctl startup with create-only writes, so seeding is safe on every restart.
- **IdempotencyRecord**: key, request_hash, vm_id, op_id. Claimed with a create-only write *before* the VM is written, so two concurrent identical POSTs can't both win.

### Request flow for create (the core idea)
1. The API validates input and claims `idem/{key}`. If the key already exists, it returns the stored IDs (or 422 on hash mismatch).
2. It writes the VM (`desired_power=ON`, `phase=Pending`, a new external_id) and the Operation (`PENDING`), then returns 202. **No provider call happens in the request path.**
3. The reconciler then takes one step per tick:
   1. **Schedule**: a Pending VM with no host gets a CAS reservation on the first host that fits, then its `host_id` is set.
   2. **Before create**: if no provider_vm_id and no task ID, call FindByExternalID. If the VM is found, adopt it and skip the create.
   3. **Create**: mark the op `RUNNING`, call CreateVM, persist the task ID. On timeout, mark the op `UNKNOWN` and do nothing else.
   4. **Poll**: GetTask until it succeeds or fails. On success, record provider_vm_id.
   5. **Power on** if desired is ON and observed is OFF.
   6. **Observe**: GetVM refreshes observed_power and last_observed_at. On an outage, set observed `UNKNOWN` and keep desired state.
   7. **Converged**: when desired matches observed, the op is `SUCCEEDED` and the phase is `Running`.
- **Stop/Start/Delete/Migrate** follow the same "persist intent → step → poll → observe" pattern.
  - **Delete**: power off → delete → release reservation → phase `Deleted` (the record is kept for audit).
  - **Migrate**: reserve on the target host → relocate task → release the source reservation → update host_id.
- **Retry policy**: a step that fails increments `attempts`. After 5 attempts the op is marked `FAILED`. The interval comes from config (default 2s locally). Today there's no backoff, just a fixed interval (backoff is documented as a gap).
- **Concurrency**: single vmctl replica. The reconciler processes VMs sequentially inside one goroutine. Every write is a CAS on the version it read; a 409 means "re-read next tick," never "overwrite."
- **Testability**: the reconciler exposes Tick() for a single synchronous pass. The production loop is a ticker plus `context.Context` cancellation that calls Tick.

### mockhv contract
- `POST /tasks/create-vm {external_id, name, cpu, memory_mb, host_id}` → 202 `{task_id}`. It **does not** de-duplicate.
- `GET /tasks/{id}` → `{state: queued|running|success|error, vm_id, error}`.
- `GET /vms[?external_id=]`, `GET /vms/{id}`.
- `POST /vms/{id}/power-on|power-off`, `POST /vms/{id}/relocate {host_id}`, `DELETE /vms/{id}`: each returns a task.
- `DELETE /vms/{id}?out_of_band=true`: deletes instantly with no task (for drift).
- `PUT /faults {create_delay_ms, timeout_after_success_count, fail_rate, outage}`, `GET /faults`.
- **Timeout-after-success**: the provider records the VM and task, then sleeps longer than vmctl's client timeout before responding.
- Tasks finish after a configurable delay (default 2s), so `vmcli vm watch` shows real progress.

### Config and ports
- kv `:8080`, mockhv `:8081`, vmctl `:8090`.
- Env vars: `KV_ADDR`, `HV_ADDR`, `WAL_PATH`, `RECONCILE_INTERVAL`, `HV_TIMEOUT`, `LOG_LEVEL`.

### Kubernetes
- Cluster: kind with 1 control-plane node and 2 worker nodes. One multi-stage Dockerfile builds all binaries into one image; each Deployment/StatefulSet picks its command.
- Namespace `vmaas`. A ConfigMap holds the env vars.
- kv: a StatefulSet (1 replica), a volumeClaimTemplate mounted at the WAL path, and a headless Service + ClusterIP Service.
- mockhv and vmctl: a Deployment each (1 replica) plus a ClusterIP Service. Services find each other through cluster DNS.
- All pods get requests/limits and liveness/readiness probes.
- Access from the laptop uses `kubectl port-forward`.

### Logging
`log/slog` JSON handler. Standard fields: `vm_id`, `op_id`, `step`, `attempt`, `provider_task_id`, `duration_ms`, `err`.

## Testing Decisions

- **What a good test is:** it drives the system only through public interfaces (HTTP APIs, or the kv store's exported methods) and asserts on externally visible results: response codes, returned records, the provider inventory count. Tests never reach into private structs, and they never sleep waiting for timers. Time-dependent logic is driven by calling `Tick()`.
- **Seam 1 (main): in-process system harness.** One test helper starts real kv, real mockhv and real vmctl, each as an `httptest.Server`, wired together, with the reconciler stepped manually. It also offers "restart vmctl" (build a fresh vmctl against the same kv) and access to mockhv's fault switches. Scenarios it must cover:
  - Create → ticks → Running, with exactly 1 provider VM.
  - Same idempotency key twice → same IDs, 1 provider VM. Same key with a different body → 422.
  - Timeout-after-success → op goes `UNKNOWN`, then `SUCCEEDED`, with 1 provider VM.
  - Restart vmctl between "task submitted" and "task polled" → resumes, 1 provider VM.
  - Provider outage → observed `UNKNOWN`, desired unchanged; recovers when the outage clears.
  - A VM too big for every host → stays Pending/unschedulable. A delete frees capacity and the next VM schedules.
  - Stop/start/delete/migrate happy paths, and 409 on a concurrent op.
- **KV tests** (same style, store-level): CAS conflict, create-only, prefix list, WAL replay after reopening the same file, torn-last-line recovery, versions keep increasing after replay.
- **Seam 2: kind demo script.** A scripted, human-watched run covering what only Kubernetes can show: the kv pod is deleted and its data survives through the PVC; the vmctl pod is deleted mid-operation and resumes; Service DNS wiring works. It is not automated CI.
- **Manual testing is a first-class step.** Every ticket ends with a "Manual check" section of vmcli/curl/kubectl commands to run and what to expect, done by the learner before the ticket is closed.
- **Prior art:** the existing `store_test.go` (table-free, method-level tests of the in-memory store). We extend that style and add `httptest`-based tests.
- **Not tested:** mockhv internals beyond what the harness exercises, the CLI's output formatting, Kubernetes manifests (verified by the demo script only).

## Out of Scope

- Real hypervisors (vSphere/govmomi, libvirt/KVM). They're impossible on this Windows machine, and the hypervisor client interface is where they'd go.
- KV replication or consensus (Raft). Explained conceptually; the kv is single-node.
- Multiple vmctl replicas, leader election, leases, fencing tokens (a stretch goal in PLAN.md).
- gRPC API, frontend dashboard, Prometheus metrics, TTL on keys.
- Auth/RBAC, quotas, multi-tenancy, IP allocation, DNS/FQDN generation, storage attachment, guest health checks, VM images/templates, consoles.
- Real live migration (memory pre-copy, shared storage, network cutover). Migrate only moves the host assignment through a provider task.
- WAL fsync, compaction and snapshots. Retry backoff and jitter.

## Further Notes

### Mock vs production: what to say in the interview

| Our piece | What production does instead | Why it matters |
|---|---|---|
| kv (single node, WAL, no fsync) | etcd: a Raft-replicated cluster of 3–5 members, fsync on every commit, snapshots + compaction, watch streams | Losing the control plane's store loses the fleet's desired state. Raft keeps it available through node loss. |
| CAS with `If-Match` | etcd transactions / K8s `resourceVersion` / Postgres row versions or `SELECT … FOR UPDATE` | The same optimistic-concurrency idea everywhere |
| Reconciler polling every 2s | Controllers use *watches* (an event stream) plus periodic resync, with work queues, rate-limited retries and exponential backoff | Lower latency, less load on the store |
| One vmctl replica | Several replicas with leader election (K8s Lease objects) or sharded work ownership with leases + fencing tokens | Without fencing, a paused "old leader" can still act after a new one takes over |
| mockhv | vCenter (async `Task` objects, `PropertyCollector` for change feeds), or libvirt on KVM hosts; KubeVirt runs VMs *as K8s objects* | The async task + "look up before retry" pattern is exactly how vSphere automation is written |
| External ID | vSphere `instanceUuid` or a custom attribute/tag; libvirt domain UUID | The de-duplication key must live *on the hypervisor object itself* |
| First-fit over 3 hosts | vSphere DRS, OpenStack Nova filters + weighers, K8s scheduler filter/score plugins; considers affinity, NUMA, overcommit ratios, failure domains | Placement is a policy engine, not just "does it fit" |
| Reservations in a host record | Placement services (OpenStack Placement), with allocation ratios for CPU overcommit | Capacity accounting must be atomic and auditable |
| Migrate = reassign host | Live migration: iterative memory pre-copy, dirty-page tracking, brief stop-and-copy, shared or migrated storage, network ARP update | Needed for host maintenance without VM downtime |
| `kubectl port-forward` | Ingress/Gateway with TLS, OIDC auth, RBAC | |

### How this maps to Kubernetes itself
A Kubernetes Deployment works like our VM create path. You write desired state to the apiserver (stored in etcd), the scheduler picks a node (our first-fit), and the kubelet on that node makes it real and reports status (our mockhv + observation). Controllers reconcile until desired equals observed. Building vmctl means building a tiny Kubernetes-for-VMs, which is exactly what KubeVirt and VMaaS platforms are.

### Related documents
- `PLAN.md`: the step-by-step build order, today vs later, and the "full product" roadmap.
- `.scratch/vmaas-sim/issues/`: one ticket per step.
- `SPEC.v1.md`: the earlier, deeper spec (gRPC/K8s detail).
