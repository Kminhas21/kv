# PLAN — build order

Spec: `SPEC.md`. Tickets: `.scratch/vmaas-sim/issues/NN-*.md` (one per step below).

**How each step works**
1. Read the ticket: goal, the concepts it teaches, the Go APIs to reach for, and "mock vs production."
2. **ready-for-human** tickets: you write the code, then ask for a review. **ready-for-agent** tickets: Claude writes the code and walks you through it.
3. Run the ticket's automated check (`go test ...`).
4. Run the ticket's **Manual check** yourself (CLI/curl/kubectl) and confirm you see what it says.
5. Commit. Next ticket.

Rule for today: **thin and working beats deep and broken.** If a step runs over its time box, take the simplest version that passes its check and note the gap.

---

## Today (~4.5–5h): end-to-end slice

| # | Step | Who | Time box | Done when |
|---|---|---|---|---|
| 01 | Repo layout, move store into `internal/kv` | agent | 10m | `go test ./...` green |
| 02 | KV versions + CAS (`If-Match`, create-only, prefix list) | **you** | 35m | store tests for conflict/create-only/list pass |
| 03 | WAL append + replay + torn tail | **you** | 30m | reopen-from-file test passes |
| 04 | kv HTTP server (`cmd/kv`) + health | **you** | 30m | curl PUT/GET/409 work against `go run ./cmd/kv` |
| 05 | kv client package | **you** | 20m | client round-trip test passes |
| 06 | mockhv service + fault switches | agent | 20m | `curl` creates a task, it finishes after ~2s |
| 07 | vmcli (CLI for manual testing) | agent | 15m | `vmcli kv list`, `vmcli hv vms` work |
| 08 | VM domain types + repository + `POST/GET /vms` with idempotency | **you** | 40m | 202 with IDs; same key → same IDs |
| 09 | Hosts seeding + first-fit scheduler with CAS reservation | **you** | 25m | VM gets a host_id; oversized VM stays Pending |
| 10 | Reconciler: create path, lookup-before-create, timeout → UNKNOWN | **you** | 50m | `vmcli vm watch` shows Pending → Running |
| 11 | System test harness + key scenario tests | agent scaffolds harness, **you** write 2 tests | 25m | idempotency + timeout-after-success tests green |
| 12 | Dockerfile, kind config, manifests, Makefile | agent | 25m | all 3 pods Ready in `vmaas` namespace |
| 13 | Cluster demo: create, dup POST, timeout-after-success, kill vmctl, kill kv | **you** run it | 20m | you can narrate each step from logs + vmcli |

**Checkpoint at end of today:** a VM gets created, scheduled and reconciled inside Kubernetes, with no duplicate VM even after a lost response or a killed controller.

If you finish early, pull from Week 1 in order.

---

## Week 1: complete the lifecycle

| # | Step | Who |
|---|---|---|
| 14 | Stop / Start operations + 409 on concurrent op | you |
| 15 | Delete operation + capacity release | you |
| 16 | Migrate (reserve target → relocate → release source) | you |
| 17 | Provider outage handling (observed UNKNOWN, desired untouched) + max attempts → FAILED | you |
| 18 | Drift self-heal (out-of-band delete → recreate) | you |
| 19 | Graceful shutdown (`signal.NotifyContext`, `http.Server.Shutdown`, drain reconciler) | you |
| 20 | Retry backoff with jitter; WAL fsync option + measure cost | you |

## Week 2: production-shaped stretch (pick by interview value)

| # | Step | Why it's worth it |
|---|---|---|
| 21 | 2 vmctl replicas + leader election via K8s Lease (client-go `leaderelection`) | Real K8s API usage. Explains why fencing matters. |
| 22 | Prometheus `/metrics` (reconcile attempts, op duration, provider errors) + Grafana in kind | "Observability" in the JD |
| 23 | Minimal Next.js page: VM list + create form + live status | JD's React/Next.js full-stack line |
| 24 | Replace polling with a watch (kv `GET /watch?prefix=` long-poll/SSE) | How real controllers avoid polling |
| 25 | Read-through: run KubeVirt on a Linux VM/WSL2 cluster, create one real VM | Seeing a real VM as a K8s object |
| 26 | Placement policy: spread vs binpack scoring, anti-affinity groups | "Resource scheduling, placement decisions" |

---

## Roadmap: what turns this into a real product

Not to build, but to *know* and be able to discuss:

- **Real hypervisor drivers**: `govmomi` (vSphere: tasks, PropertyCollector, `instanceUuid`), `libvirt-go` (KVM domains), or KubeVirt (VMs as CRDs).
- **HA state store**: etcd or Postgres with replication; backups; migrations.
- **HA control plane**: leases + fencing tokens, sharded work queues, watches instead of polling.
- **API**: gRPC + REST gateway, pagination, filtering, API versioning, OpenAPI docs.
- **Identity**: OIDC/SSO, RBAC, per-team quotas, audit log.
- **Networking**: IPAM (IP allocation), DNS/FQDN generation, VLAN/port groups, load balancers.
- **Storage**: datastore selection, disk attach/detach, snapshots, backups.
- **VM content**: templates/images, cloud-init, guest agent, readiness/health checks, console access.
- **Scheduling**: overcommit ratios, NUMA, GPUs, affinity/anti-affinity, failure domains, maintenance mode and host evacuation (live migration).
- **Ops**: Prometheus + Grafana + alerts, OpenTelemetry tracing, SLOs, runbooks.
- **Delivery**: Helm chart / Kustomize, CI/CD, Terraform provider for the API, Ansible for host config.
- **Frontend**: Next.js dashboard for VM groups, monitoring and admin tooling.
