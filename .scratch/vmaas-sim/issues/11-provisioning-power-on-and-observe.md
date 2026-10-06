# 11: Provisioning: power on + observe → Running

**What to build:** Once the provider VM exists, the reconciler powers it on (as a task) when desired is ON and observed is OFF, refreshes observed state and `last_observed_at` every tick, and marks the operation SUCCEEDED and the phase Running when desired matches observed. Afterwards Claude adds `vmcli vm watch <id>`, which prints each transition live.

**Blocked by:** 10

**Status:** ready-for-human

- [ ] Observe step runs for every VM with a provider VM ID, every tick
- [ ] Power-on submitted once; its task polled like create
- [ ] Converged → op SUCCEEDED, phase Running, current_op_id cleared
- [ ] Test: create → N ticks → Running with exactly 1 provider VM

**Concepts:** level-triggered vs edge-triggered control (we compare states every tick, we don't react to events), separating power state from guest readiness.

**Mock vs production:** real platforms also wait for guest readiness (VMware Tools / qemu-guest-agent heartbeat, cloud-init done, IP reported) before declaring a VM usable.

**Manual check:** `vmcli vm create ...` then `vmcli vm watch <id>`: Pending → Provisioning → Running, observed OFF → ON.
