# KV → VM Lifecycle Simulator: One-Day Handoff

## Goal

Extend the existing Go KV project with a small, runnable **VM lifecycle control-plane simulator**. The goal is to learn the same engineering patterns as a VM-as-a-Service platform: asynchronous creation, desired versus observed state, reconciliation, idempotency, and recovery after a provider timeout. The provider is simulated; do not claim this provisions real VMware or KVM VMs.

The implementing agent has more context about the KV repository. Inspect its architecture first, reuse its existing Go conventions and database setup where practical, and keep the current KV behavior intact. Do not rewrite the KV service merely to add this feature.

## One-day scope and priorities

### P0: Working lifecycle and recovery

1. `POST /vms` accepts a minimal spec (`name`, `cpu`, `memory_mb`) and an idempotency key. Persist a stable VM ID, desired state `RUNNING`, and an operation record **before** making an external call. Return the VM ID and operation ID promptly, without waiting for boot.
2. `GET /vms/{id}` returns the spec, desired state, observed provider state, operation status, provider ID if known, last observation time, and last error. `GET /operations/{id}` returns progress and error information.
3. A separate mock provider interface or HTTP service supports asynchronous create, task lookup, and VM lookup by the control plane's stable external ID. Simulate a delay between accepting create and reporting a VM as created/powered on.
4. A background reconciler repeatedly compares desired and observed state. It checks an existing task or provider inventory before submitting another create request. It survives restart using durable state.
5. Demonstrate **timeout after success**: the provider creates the VM, but the client times out before seeing the response. The next reconciliation finds the VM by external ID and records success. It must not create a duplicate.

### P1: Only if P0 is complete

- Run two worker instances or goroutines and use a database conditional claim/transaction so only one owns an operation at a time. A process-local `sync.Mutex` is insufficient across processes.
- Add a tiny host inventory with available CPU/memory and one atomic capacity reservation. Keep the policy simple, such as first eligible host.
- Expose one metric or log field for reconciliation attempts, provider failures, and operation duration.

## Persistence and state model

Use the repository's existing PostgreSQL integration if present; otherwise choose the simplest durable store already used by the project. **Do not build a new replicated database for this exercise.** The existing KV/WAL component may hold an event log or cached observations, but durable desired VM state and operation progress must be shared across any worker replicas.

Minimum logical fields (tables and naming may follow repo conventions):

```text
VM: id, external_id, name, cpu, memory_mb, desired_power,
    observed_power, provider_id, last_observed_at, version
Operation: id, vm_id, request_key (unique), type, status,
           provider_task_id, last_error, updated_at
```

Use distinct values for `PENDING`, `RUNNING`, `SUCCEEDED`, `FAILED`, and `UNKNOWN` where helpful. Distinguish **provider power state** from **guest/application readiness**; no real guest health check is required in P0.

## Expected behavior and invariants

- Repeating `POST /vms` with the same idempotency key returns the same VM/operation. Different keys can create different VMs.
- A client timeout means the provider outcome is **unknown**, not necessarily failed.
- Every provider VM carries or can be queried by the stable control-plane external ID.
- Before retrying a side-effecting create call, query the saved task ID or lookup by external ID.
- A provider outage leaves desired state intact and marks the observation stale/unknown; it does not imply the VM was deleted.
- Workers may restart at any step and resume from durable progress.
- State changes and retries should be visible through the GET endpoints and logs.

## Acceptance demo

Provide a simple README or script that shows:

1. Start the API, database, worker, and mock provider using the repo's usual workflow.
2. Create a VM; observe `PENDING` or `RUNNING`, then `SUCCEEDED` and provider power `ON`.
3. Repeat the POST with the same idempotency key; show the same VM ID and exactly one provider VM.
4. Enable timeout-after-success; create another VM; show initial uncertain status and eventual recovery with exactly one provider VM.
5. Restart the worker during an in-progress operation; show it resumes without duplication.

Implement only meaningful tests for the idempotent request and timeout-after-success recovery paths. Document any unimplemented production concerns, especially leases/fencing, authorization, capacity accounting, real hypervisor APIs, IP/DNS/storage integration, and guest health.

## Time budget

- 1 hour: inspect repo, add model and endpoints.
- 1 hour: mock provider with async task and stable ID lookup.
- 1.5 hours: reconciler and persistence.
- 1 hour: timeout-after-success scenario and tests.
- Remaining time: documentation, restart demo, then P1 if feasible.

Prefer a small complete P0 over a broad partial implementation. The final explanation should say precisely which components are simulated and which KV features were reused.
