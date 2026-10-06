# 06: mockhv: fake hypervisor service

**What to build:** A standalone fake hypervisor. It accepts async create / power-on / power-off / relocate / delete requests as tasks that complete after a delay, keeps a VM inventory searchable by external ID, and exposes fault switches: create delay, timeout-after-success, random failure rate, full outage. It deliberately does NOT de-duplicate creates.

**Blocked by:** 01

**Status:** ready-for-agent

- [ ] Endpoints per SPEC "mockhv contract"
- [ ] Tasks move queued → running → success/error over a configurable delay
- [ ] Timeout-after-success: VM and task are recorded, then the response is delayed past the caller's timeout
- [ ] Outage: every endpoint returns 503
- [ ] Out-of-band delete removes a VM instantly
- [ ] Structured logs for every request and task transition

**Concepts (walkthrough):** goroutines + `time.AfterFunc` for async tasks, `sync.Mutex` around shared maps, why task-based APIs exist (long operations, HTTP request lifetimes).

**Mock vs production:** vCenter returns a `Task` managed object you poll or watch via PropertyCollector; libvirt calls are mostly synchronous per host, with an agent on each host. Real hypervisors persist inventory; ours forgets on restart.

**Manual check:** `go run ./cmd/mockhv`, `curl -X POST localhost:8081/tasks/create-vm -d '{"external_id":"x","name":"a","cpu":1,"memory_mb":512,"host_id":"host-1"}'`, poll the task until success, `curl localhost:8081/vms?external_id=x`.
