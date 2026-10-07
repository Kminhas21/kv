# 10: Provisioning: create + poll task

**What to build:** For a scheduled VM with no provider VM, the reconciler first looks up the hypervisor inventory by external ID and adopts the VM if it's found. Otherwise it submits a create, persists the provider task ID, and on later ticks polls the task until it succeeds, then records the provider VM ID. A timed-out create call marks the operation UNKNOWN (not FAILED), and the next tick's lookup resolves it.

**Blocked by:** 06, 09

**Status:** resolved

- [x] Hypervisor client *interface* in vmctl + HTTP implementation for mockhv (with `HV_TIMEOUT`)
- [x] Order per tick: lookup-by-external-ID → create → persist task ID → (next ticks) poll
- [x] Every state change persisted via CAS before the next step
- [x] Timeout (`context.DeadlineExceeded` / `net.Error` timeout) → op UNKNOWN, no retry create without lookup
- [x] Task error → attempts++, last_error set
- [x] slog line per step with vm_id, op_id, step, provider_task_id

**Concepts:** interfaces as seams (where govmomi/libvirt would plug in), "unknown ≠ failed," `errors.Is(err, context.DeadlineExceeded)`, step-at-a-time state machines that persist progress.

**Mock vs production:** this is exactly how vSphere automation is written: save the task MoRef, poll it, and before retrying search by `instanceUuid` or a custom attribute.

**Manual check:** `vmcli hv faults --create-delay 5s`, create a VM, watch `vmcli vm get` move from Provisioning (task ID set) to provider_vm_id set. `vmcli hv vms` shows exactly one.
