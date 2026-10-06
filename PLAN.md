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

| # | Ticket | Who | Blocked by | Time box |
|---|---|---|---|---|
| 01 | Prefactor: repo layout | agent | none | 10m |
| 02 | kv service over HTTP with versions | **you** | 01 | 35m |
| 03 | Compare-and-swap (`If-Match`, create-only) | **you** | 02 | 20m |
| 04 | Durability: WAL + replay | **you** | 03 | 30m |
| 05 | kv Go client | **you** | 03 | 20m |
| 06 | mockhv: fake hypervisor | agent | 01 | 20m |
| 07 | vmcli skeleton + kv/hv commands | agent | 05, 06 | 15m |
| 08 | Create-VM request path with idempotency | **you** | 05, 07 | 40m |
| 09 | Scheduling: hosts + first-fit | **you** | 08 | 25m |
| 10 | Provisioning: create + poll task | **you** | 06, 09 | 30m |
| 11 | Provisioning: power on + observe → Running | **you** | 10 | 25m |
| 12 | System test harness + recovery tests | agent harness, **you** tests | 11 | 25m |
| 13 | Deploy to kind | agent | 11 | 25m |
| 14 | Cluster demo script + rehearsal | agent script, **you** run | 12, 13 | 20m |

**Checkpoint at end of today:** a VM gets created, scheduled and reconciled inside Kubernetes, with no duplicate VM even after a lost response or a killed controller.

If you finish early, pull from Week 1 in order.

---

## Week 1: complete the lifecycle

| # | Ticket | Who | Blocked by |
|---|---|---|---|
| 15 | Stop / Start + 409 on concurrent op | you | 12 |
| 16 | Delete + capacity release | you | 15 |
| 17 | Migrate | you | 16 |
| 18 | Provider outage + max attempts → FAILED | you | 12 |
| 19 | Drift self-heal | you | 18 |
| 20 | Graceful shutdown | you | 11 |
| 21 | Retry backoff + optional WAL fsync | you | 04, 18 |

## Week 2: production-shaped stretch (pick by interview value)

| # | Step | Why it's worth it |
|---|---|---|
| W1 | 2 vmctl replicas + leader election via K8s Lease (client-go `leaderelection`) | Real K8s API usage. Explains why fencing matters. |
| W2 | Prometheus `/metrics` (reconcile attempts, op duration, provider errors) + Grafana in kind | "Observability" in the JD |
| W3 | Minimal Next.js page: VM list + create form + live status | JD's React/Next.js full-stack line |
| W4 | Replace polling with a watch (kv `GET /watch?prefix=` long-poll/SSE) | How real controllers avoid polling |
| W5 | Read-through: run KubeVirt on a Linux VM/WSL2 cluster, create one real VM | Seeing a real VM as a K8s object |
| W6 | Placement policy: spread vs binpack scoring, anti-affinity groups | "Resource scheduling, placement decisions" |

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
