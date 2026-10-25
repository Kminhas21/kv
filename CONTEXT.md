# kv + VM control plane

A persistent key-value store and a VM control plane (vmctl) that keeps its state in it, driving a mock hypervisor (mockhv).

## Language

### Durability

**Acknowledged write**:
A write whose HTTP success response has been sent to the client.
_Avoid_: committed write, completed write

**Durable write**:
A write whose WAL record has been fsynced to disk, so it survives a machine crash, not just a process crash.
_Avoid_: persisted, saved

**Zero-loss**:
The guarantee that every acknowledged write is a durable write. A write visible to readers but not yet acknowledged may still be lost.
_Avoid_: no data loss (too vague)

**Snapshot**:
A full copy of the store at one revision, from which replay starts instead of the beginning of the WAL.
_Avoid_: checkpoint, dump

### Expiry

**TTL**:
The lifetime given to a key on write, fixed as an absolute expiry time.
_Avoid_: timeout

**Expired key**:
A key past its expiry time. It is treated as missing everywhere, even before it is removed from memory.
_Avoid_: stale key, dead key

### Coordination

**Lease**:
A key with a TTL that a vmctl replica holds to be the leader; it must be renewed before it expires.
_Avoid_: lock, mutex

**Leader**:
The one vmctl replica holding the lease, and the only one that runs the reconciler. Every replica serves the API.
_Avoid_: master, primary
