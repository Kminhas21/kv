# SPEC.md — KV + VM Control Plane

> A progressive Go project: starts as an HTTP key-value store, extends into a VM lifecycle control-plane simulator with gRPC, then deploys as a stateful Kubernetes workload. Every design choice maps to real infrastructure patterns used in cloud platforms (Tesla Cloud Platform, GCP, AWS EC2).

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Phase 1 — Base KV Store](#3-phase-1--base-kv-store)
4. [Phase 2 — VM Lifecycle Control Plane](#4-phase-2--vm-lifecycle-control-plane)
5. [Phase 3 — gRPC Service Layer](#5-phase-3--grpc-service-layer)
6. [Phase 4 — Kubernetes Deployment](#6-phase-4--kubernetes-deployment)
7. [Data Models](#7-data-models)
8. [API Reference](#8-api-reference)
9. [Reconciliation Engine](#9-reconciliation-engine)
10. [Mock Provider](#10-mock-provider)
11. [Observability](#11-observability)
12. [Testing Strategy](#12-testing-strategy)
13. [Demo Scenarios](#13-demo-scenarios)
14. [Project Structure](#14-project-structure)
15. [Build & Run](#15-build--run)
16. [Learning Map](#16-learning-map)

---

## 1. Overview

### What This Project Is

A single Go binary that serves two subsystems:

1. **KV Store** — An in-memory key-value store with an HTTP API, write-ahead log (WAL) for durability, TTL-based expiration, and Prometheus metrics exposition.
2. **VM Control Plane** — A VM lifecycle simulator layered on top of the same binary. It manages virtual machine desired-state vs. observed-state reconciliation through a background reconciler, backed by a mock cloud provider that simulates async provisioning.

### Why This Architecture

Real cloud platforms (Tesla's VM-as-a-Service, GCP Compute Engine, AWS EC2) all follow this pattern:
- An API server accepts user intent (desired state)
- A reconciliation loop continuously drives observed state toward desired state
- A provider abstraction talks to the actual hypervisor/cloud API
- An operation log tracks every mutation for idempotency and auditability

This project builds that pattern from scratch in Go, using the KV store as the persistence layer for VM state.

### Technology Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.22+ |
| HTTP routing | `net/http` with Go 1.22 ServeMux method routing |
| gRPC | `google.golang.org/grpc` + `protobuf` |
| Persistence | WAL (JSON-line append log on disk), survives pod restarts via PVC |
| Metrics | Prometheus exposition format (manual, no client library) |
| Container | Docker multi-stage build (golang → alpine, target <15MB image) |
| Orchestration | Kubernetes (StatefulSet + PVC + headless Service), local via kind |
| Testing | `testing` stdlib + `net/http/httptest` |
| Build | `go build` (single binary, no CGO) |

### Non-Goals

- No real hypervisor integration (VMware/KVM) — we use a mock provider
- No real database — the WAL-backed KV store IS the database
- No frontend dashboard (metrics are Prometheus-scrapeable, viewable in Grafana if desired)
- No authentication/authorization
- No TLS termination (would be handled by a reverse proxy in production)
- No Helm charts — raw YAML manifests for maximum learning
- No multi-replica with leader election — single StatefulSet replica (understanding the primitives first)

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      Go Binary                          │
│                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │
│  │  HTTP Server  │  │ gRPC Server  │  │   Metrics    │  │
│  │  :8080        │  │  :9090       │  │   :9091      │  │
│  │              │  │              │  │  /metrics     │  │
│  │  /keys/*     │  │  KVService   │  │              │  │
│  │  /vms/*      │  │  VMService   │  │              │  │
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘  │
│         │                 │                             │
│         ▼                 ▼                             │
│  ┌─────────────────────────────┐                       │
│  │       Service Layer          │                       │
│  │  KVService  │  VMService     │                       │
│  └──────┬──────┴───────┬────────┘                       │
│         │              │                                │
│         ▼              ▼                                │
│  ┌───────────┐  ┌─────────────┐  ┌──────────────────┐  │
│  │  KV Store  │  │  Reconciler  │  │  Mock Provider   │  │
│  │  (in-mem)  │  │  (loop)      │  │  (async tasks)   │  │
│  └─────┬─────┘  └──────┬──────┘  └──────────────────┘  │
│        │               │                                │
│        ▼               │                                │
│  ┌───────────┐         │                                │
│  │    WAL     │◄────────┘                                │
│  │ (disk)     │                                          │
│  └───────────┘                                          │
└─────────────────────────────────────────────────────────┘
```

### Port Assignments

| Port | Protocol | Purpose |
|------|----------|---------|
| 8080 | HTTP | REST API for KV and VM operations |
| 9090 | gRPC | gRPC API (mirrors REST functionality) |
| 9091 | HTTP | Prometheus metrics endpoint |

All ports are configurable via environment variables: `KV_HTTP_PORT`, `KV_GRPC_PORT`, `KV_METRICS_PORT`.

---

## 3. Phase 1 — Base KV Store

### 3.1 In-Memory Store

```go
// store.go

type Entry struct {
    Value     string
    ExpiresAt time.Time // zero value = no expiration
}

type Store struct {
    mu      sync.RWMutex
    data    map[string]Entry
    wal     *WAL
    metrics *Metrics
}
```

**Design decisions:**

- **`map[string]Entry`** not `map[string]string` — the `Entry` struct carries TTL metadata alongside the value. The map key is the user's key, the value is the `Entry`.
- **Single `sync.RWMutex`** protecting the entire map — no per-key locking, no sharding. This is the right choice for a learning project: it's correct, simple, and fast enough for single-node use. The tradeoff (all writes serialize) only matters at >100K ops/sec which we won't hit.
- **Single lock boundary** — acquire lock, do map operation + WAL append, release. The WAL append happens inside the lock. This means writes block on disk I/O, but it guarantees that if the WAL has an entry, the map had it too. The alternative (WAL outside lock) creates a window where the map has data the WAL doesn't, which means a crash loses acknowledged writes.

**Methods on Store:**

```go
func NewStore(walPath string) (*Store, error)  // opens WAL, replays entries
func (s *Store) Get(key string) (string, bool) // returns value, exists; checks TTL lazily
func (s *Store) Put(key string, value string, ttl time.Duration) (created bool) // true if key was new
func (s *Store) Delete(key string) bool        // returns true if key existed
func (s *Store) Keys() []string                // returns all non-expired keys
func (s *Store) Len() int                      // count of non-expired keys
```

### 3.2 Write-Ahead Log (WAL)

The WAL provides crash recovery. Every mutation (PUT, DELETE) is appended to a file before the in-memory map is updated.

**File format:** JSON Lines (one JSON object per line, newline-delimited).

```json
{"op":"PUT","key":"server-1","value":"running","ttl_sec":300,"ts":1699000000}
{"op":"DELETE","key":"server-1","ts":1699000060}
```

**Fields:**
- `op`: `"PUT"` or `"DELETE"`
- `key`: the key string
- `value`: the value string (only for PUT)
- `ttl_sec`: TTL in seconds (only for PUT, 0 = no expiration)
- `ts`: Unix timestamp (seconds) of the operation

```go
// wal.go

type WAL struct {
    file *os.File
    enc  *json.Encoder
}

type WALEntry struct {
    Op     string `json:"op"`
    Key    string `json:"key"`
    Value  string `json:"value,omitempty"`
    TTLSec int64  `json:"ttl_sec,omitempty"`
    TS     int64  `json:"ts"`
}
```

**WAL behavior:**

1. **Open**: `os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)` — append-only.
2. **Append**: `json.Encoder.Encode(entry)` followed by `file.Sync()`. The `Sync()` call is critical — without it, the OS can buffer the write and a crash loses it. `file.Sync()` is an `fsync()` syscall that forces the write to stable storage. This is slower than plain `Write()` but guarantees durability. We accept this cost because correctness > throughput for a control plane.
3. **Replay on startup**: Read the WAL line by line. For each valid line, apply the operation to the in-memory map. Skip corrupted/incomplete tail entries (partial write from a crash) — log a warning and continue. After replay, the map matches the last consistent state.
4. **Corrupted tail handling**: A crash mid-write can leave a partial JSON line at the end of the file. Detection: `json.Decoder.Decode()` returns an error. Action: log `"WAL: skipping corrupted entry at line N"` and stop replaying. Do NOT truncate the file — a human should inspect. Do NOT treat corruption in the middle of the file as recoverable.

**WAL compaction:**

Over time the WAL grows with obsolete entries (overwritten or deleted keys). Compaction rewrites a minimal WAL.

```
Algorithm:
1. Snapshot current map (under read lock)
2. Write all current entries to a NEW temp file (wal.tmp)
3. fsync the temp file
4. os.Rename(wal.tmp, wal.dat) — atomic on POSIX
5. Swap the file handle
```

- Trigger: when WAL file exceeds 10MB (configurable via `KV_WAL_COMPACT_THRESHOLD_BYTES` env var, default `10485760`).
- The write-then-rename pattern is crash-safe: if we crash before rename, the old WAL is intact; if we crash after rename, the new WAL is complete.

### 3.3 TTL Expiration

Two mechanisms work together:

1. **Lazy expiration (on read):** When `Get()` finds a key whose `ExpiresAt` is in the past, it deletes the key from the map, appends a DELETE to the WAL, increments `kv_expired_keys_total`, and returns `("", false)` as if the key doesn't exist.

2. **Active sweeper (background goroutine):** A goroutine runs on a `time.Ticker` (every 1 second). Each tick:
   - Acquire write lock
   - Iterate `s.data`, collect keys where `ExpiresAt.Before(time.Now())` and `ExpiresAt` is not zero
   - Delete each from map, append DELETE to WAL, increment `kv_expired_keys_total`
   - Release write lock

   The sweeper ensures keys are cleaned up even if never read again. The 1-second interval is a tradeoff: more frequent = lower memory waste but higher lock contention; less frequent = stale keys linger.

**TTL storage:** The `Entry.ExpiresAt` field is a `time.Time`. When the user sends `ttl_sec: 300`, the store computes `ExpiresAt = time.Now().Add(300 * time.Second)`. Zero `time.Time` (the zero value) means no expiration. The WAL stores `ttl_sec` (the original duration), not the absolute time — on replay, `ExpiresAt` is recomputed as `ts + ttl_sec`. If `ts + ttl_sec` is already in the past at replay time, skip the entry (it's already expired).

### 3.4 HTTP API (Phase 1)

Use Go 1.22+ `http.ServeMux` method routing:

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /keys/{key}", handleGet)
mux.HandleFunc("PUT /keys/{key}", handlePut)
mux.HandleFunc("DELETE /keys/{key}", handleDelete)
mux.HandleFunc("GET /keys", handleList)
mux.HandleFunc("GET /metrics", handleMetrics)  // on metrics port
```

#### `PUT /keys/{key}`

**Request body:**
```json
{
  "value": "some-string",
  "ttl_sec": 300
}
```
`ttl_sec` is optional (0 or absent = no expiration).

**Response:**
- `201 Created` with `{"key": "...", "created": true}` — key was new
- `200 OK` with `{"key": "...", "created": false}` — key was updated (overwritten)
- `400 Bad Request` — missing `value` field, or body is not valid JSON

#### `GET /keys/{key}`

**Response:**
- `200 OK` with `{"key": "...", "value": "...", "ttl_remaining_sec": 245}` — `ttl_remaining_sec` is omitted or -1 if no TTL
- `404 Not Found` with `{"error": "key not found"}` — key doesn't exist or expired

#### `DELETE /keys/{key}`

**Response:**
- `204 No Content` — key existed and was deleted (no body)
- `404 Not Found` with `{"error": "key not found"}` — key didn't exist

#### `GET /keys`

**Response:**
- `200 OK` with `{"keys": ["key1", "key2", ...], "count": 2}` — all non-expired keys

**Error format (all endpoints):**
```json
{"error": "human-readable message"}
```

### 3.5 Graceful Shutdown

```go
// main.go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// ... start servers ...

<-ctx.Done()
// 1. Stop accepting new connections (httpServer.Shutdown with 5s timeout)
// 2. Stop the TTL sweeper (cancel its context)
// 3. Stop the reconciler (cancel its context) — Phase 2
// 4. Sync and close the WAL file
// 5. Stop the gRPC server (grpcServer.GracefulStop()) — Phase 3
```

---

## 4. Phase 2 — VM Lifecycle Control Plane

This phase adds a VM management layer that demonstrates the **desired-state / observed-state reconciliation pattern** — the core of every cloud control plane.

### 4.1 Concept: Desired vs. Observed State

The user says "I want VM `web-1` to be RUNNING." That's **desired state**. The actual VM might be STOPPED, STARTING, or UNKNOWN — that's **observed state**. The **reconciler** continuously compares the two and takes actions to converge them.

```
User → API → sets desired_power = RUNNING
                         │
                    Reconciler loop (every 5s)
                         │
              ┌──────────┴──────────┐
              │ desired ≠ observed? │
              └──────────┬──────────┘
                    yes  │
                         ▼
              ┌─────────────────────┐
              │ Call mock provider   │
              │ (start/stop VM)      │
              └──────────┬──────────┘
                         │
              Provider completes async task
                         │
              ┌─────────────────────┐
              │ Update observed_power│
              │ in KV store          │
              └─────────────────────┘
```

### 4.2 VM Data Model

VMs are stored in the KV store with structured keys and JSON values.

**Key scheme:**
```
vm:{vm_id}          → VM JSON blob
op:{op_id}          → Operation JSON blob
idx:vm:list         → JSON array of all VM IDs
idx:op:vm:{vm_id}   → JSON array of operation IDs for this VM
```

**VM struct:**

```go
// models/vm.go

type PowerState string

const (
    PowerStateRunning  PowerState = "RUNNING"
    PowerStateStopped  PowerState = "STOPPED"
    PowerStateUnknown  PowerState = "UNKNOWN"
)

type ProvisioningState string

const (
    ProvisioningCreating    ProvisioningState = "CREATING"
    ProvisioningReady       ProvisioningState = "READY"
    ProvisioningDeleting    ProvisioningState = "DELETING"
    ProvisioningDeleted     ProvisioningState = "DELETED"
    ProvisioningFailed      ProvisioningState = "FAILED"
)

type VM struct {
    ID              string            `json:"id"`               // UUID v4
    Name            string            `json:"name"`             // user-provided, unique
    CPU             int               `json:"cpu"`              // vCPU count
    MemoryMB        int               `json:"memory_mb"`        // RAM in MB
    DesiredPower    PowerState        `json:"desired_power"`    // what the user wants
    ObservedPower   PowerState        `json:"observed_power"`   // what the provider reports
    Provisioning    ProvisioningState `json:"provisioning"`     // lifecycle stage
    ProviderID      string            `json:"provider_id"`      // external ID from mock provider
    LastObservedAt  time.Time         `json:"last_observed_at"` // last provider poll
    Version         int               `json:"version"`          // optimistic concurrency
    CreatedAt       time.Time         `json:"created_at"`
    UpdatedAt       time.Time         `json:"updated_at"`
}
```

**Operation struct:**

Every mutation to a VM is tracked as an Operation. This provides:
- **Auditability**: full history of what happened
- **Idempotency**: the `RequestKey` field prevents duplicate operations
- **Debugging**: when a VM is stuck, look at its operations

```go
// models/operation.go

type OperationStatus string

const (
    OpStatusPending    OperationStatus = "PENDING"
    OpStatusRunning    OperationStatus = "RUNNING"
    OpStatusSucceeded  OperationStatus = "SUCCEEDED"
    OpStatusFailed     OperationStatus = "FAILED"
)

type OperationType string

const (
    OpTypeCreate  OperationType = "CREATE"
    OpTypeStart   OperationType = "START"
    OpTypeStop    OperationType = "STOP"
    OpTypeDelete  OperationType = "DELETE"
)

type Operation struct {
    ID              string          `json:"id"`               // UUID v4
    VMID            string          `json:"vm_id"`
    RequestKey      string          `json:"request_key"`      // idempotency key (unique)
    Type            OperationType   `json:"type"`
    Status          OperationStatus `json:"status"`
    ProviderTaskID  string          `json:"provider_task_id"` // from mock provider
    LastError       string          `json:"last_error,omitempty"`
    CreatedAt       time.Time       `json:"created_at"`
    UpdatedAt       time.Time       `json:"updated_at"`
}
```

### 4.3 VM Persistence Layer

The VM service reads and writes VM/Operation data through the KV store. This is a deliberate design choice — the KV store IS the database.

```go
// vmstore/vmstore.go

type VMStore struct {
    kv *store.Store
}

func (vs *VMStore) GetVM(id string) (*VM, error)          // GET vm:{id}, unmarshal JSON
func (vs *VMStore) PutVM(vm *VM) error                    // marshal JSON, PUT vm:{id}
func (vs *VMStore) ListVMs() ([]*VM, error)               // GET idx:vm:list, then GET each
func (vs *VMStore) DeleteVM(id string) error              // DELETE vm:{id}, update index

func (vs *VMStore) GetOperation(id string) (*Operation, error)
func (vs *VMStore) PutOperation(op *Operation) error
func (vs *VMStore) ListOperationsByVM(vmID string) ([]*Operation, error)
func (vs *VMStore) FindOperationByRequestKey(key string) (*Operation, error) // scan ops for matching request_key
```

**Optimistic concurrency:** Every `PutVM` increments `vm.Version`. The reconciler reads the VM, does work, then writes it back — if `version` in the store doesn't match what it read, the write fails and the reconciler retries next tick. Implement this by storing the version in the KV value and checking it before overwrite.

### 4.4 VM HTTP API

#### `POST /vms`

Create a new VM. The VM starts in `provisioning: CREATING`, `observed_power: UNKNOWN`.

**Request:**
```json
{
  "name": "web-1",
  "cpu": 2,
  "memory_mb": 4096,
  "request_key": "create-web-1-abc123"
}
```

**Idempotency:** If `request_key` matches an existing operation, return that operation's result instead of creating a new VM. This is critical — network retries must not create duplicate VMs.

**Response (201 Created):**
```json
{
  "vm": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "name": "web-1",
    "cpu": 2,
    "memory_mb": 4096,
    "desired_power": "RUNNING",
    "observed_power": "UNKNOWN",
    "provisioning": "CREATING",
    "provider_id": "",
    "version": 1,
    "created_at": "2024-01-15T10:00:00Z",
    "updated_at": "2024-01-15T10:00:00Z"
  },
  "operation": {
    "id": "op-uuid",
    "vm_id": "550e8400-...",
    "request_key": "create-web-1-abc123",
    "type": "CREATE",
    "status": "PENDING"
  }
}
```

**Validation:**
- `name`: required, 1-63 chars, alphanumeric + hyphens, must start with letter
- `cpu`: required, 1-96
- `memory_mb`: required, 512-393216 (384 GB), must be multiple of 512
- `request_key`: required, 1-128 chars

**Errors:**
- `400` — validation failure
- `409 Conflict` — `name` already in use by a non-deleted VM

#### `GET /vms/{id}`

**Response (200):**
```json
{
  "vm": { ... }
}
```

**Errors:**
- `404` — VM not found or deleted

#### `GET /vms`

**Response (200):**
```json
{
  "vms": [ ... ],
  "count": 3
}
```

Excludes VMs with `provisioning: DELETED`.

#### `POST /vms/{id}/start`

Set `desired_power = RUNNING`. Only valid when `provisioning` is `READY` and `desired_power` is currently `STOPPED`.

**Request:**
```json
{
  "request_key": "start-web-1-xyz789"
}
```

**Response (202 Accepted):**
```json
{
  "operation": {
    "id": "op-uuid",
    "type": "START",
    "status": "PENDING"
  }
}
```

**Errors:**
- `404` — VM not found
- `409` — VM not in a valid state for this action (e.g., already running, still creating)

#### `POST /vms/{id}/stop`

Set `desired_power = STOPPED`. Same structure as `/start`.

#### `DELETE /vms/{id}`

Set `provisioning = DELETING`. The reconciler handles actual cleanup.

**Request:**
```json
{
  "request_key": "delete-web-1-def456"
}
```

**Response (202 Accepted):**
```json
{
  "operation": {
    "id": "op-uuid",
    "type": "DELETE",
    "status": "PENDING"
  }
}
```

#### `GET /vms/{id}/operations`

List all operations for a VM, newest first.

**Response (200):**
```json
{
  "operations": [ ... ],
  "count": 5
}
```

#### `GET /operations/{id}`

Get a single operation by ID.

---

## 5. Phase 3 — gRPC Service Layer

### 5.1 Why gRPC

Tesla's Cloud Platform uses gRPC for internal service-to-service communication. Adding a gRPC layer alongside REST teaches:
- Protocol Buffers (schema-first API design)
- Code generation workflow
- Streaming (server-side streaming for watch/subscribe patterns)
- How real platforms expose the same service over two protocols

### 5.2 Proto Definitions

Create `proto/kv/v1/kv.proto`:

```protobuf
syntax = "proto3";
package kv.v1;
option go_package = "github.com/<your-username>/kv/gen/kv/v1;kvv1";

service KVService {
  rpc Get(GetRequest) returns (GetResponse);
  rpc Put(PutRequest) returns (PutResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc ListKeys(ListKeysRequest) returns (ListKeysResponse);
}

message GetRequest {
  string key = 1;
}

message GetResponse {
  string key = 1;
  string value = 2;
  int64 ttl_remaining_sec = 3;  // -1 if no TTL
}

message PutRequest {
  string key = 1;
  string value = 2;
  int64 ttl_sec = 3;  // 0 = no expiration
}

message PutResponse {
  string key = 1;
  bool created = 2;
}

message DeleteRequest {
  string key = 1;
}

message DeleteResponse {
  bool existed = 1;
}

message ListKeysRequest {}

message ListKeysResponse {
  repeated string keys = 1;
  int32 count = 2;
}
```

Create `proto/vm/v1/vm.proto`:

```protobuf
syntax = "proto3";
package vm.v1;
option go_package = "github.com/<your-username>/kv/gen/vm/v1;vmv1";

import "google/protobuf/timestamp.proto";

service VMService {
  rpc CreateVM(CreateVMRequest) returns (CreateVMResponse);
  rpc GetVM(GetVMRequest) returns (GetVMResponse);
  rpc ListVMs(ListVMsRequest) returns (ListVMsResponse);
  rpc StartVM(StartVMRequest) returns (OperationResponse);
  rpc StopVM(StopVMRequest) returns (OperationResponse);
  rpc DeleteVM(DeleteVMRequest) returns (OperationResponse);
  rpc GetOperation(GetOperationRequest) returns (OperationResponse);
  rpc ListOperations(ListOperationsRequest) returns (ListOperationsResponse);

  // Server-side streaming: watch a VM's state changes in real time
  rpc WatchVM(WatchVMRequest) returns (stream VMEvent);
}

message CreateVMRequest {
  string name = 1;
  int32 cpu = 2;
  int32 memory_mb = 3;
  string request_key = 4;
}

message CreateVMResponse {
  VMInfo vm = 1;
  OperationInfo operation = 2;
}

message GetVMRequest {
  string id = 1;
}

message GetVMResponse {
  VMInfo vm = 1;
}

message ListVMsRequest {}

message ListVMsResponse {
  repeated VMInfo vms = 1;
  int32 count = 2;
}

message StartVMRequest {
  string id = 1;
  string request_key = 2;
}

message StopVMRequest {
  string id = 1;
  string request_key = 2;
}

message DeleteVMRequest {
  string id = 1;
  string request_key = 2;
}

message GetOperationRequest {
  string id = 1;
}

message ListOperationsRequest {
  string vm_id = 1;
}

message ListOperationsResponse {
  repeated OperationInfo operations = 1;
  int32 count = 2;
}

message OperationResponse {
  OperationInfo operation = 1;
}

message WatchVMRequest {
  string id = 1;
}

message VMEvent {
  VMInfo vm = 1;
  string event_type = 2;  // "STATE_CHANGED", "OPERATION_COMPLETED"
  google.protobuf.Timestamp timestamp = 3;
}

message VMInfo {
  string id = 1;
  string name = 2;
  int32 cpu = 3;
  int32 memory_mb = 4;
  string desired_power = 5;
  string observed_power = 6;
  string provisioning = 7;
  string provider_id = 8;
  int32 version = 9;
  google.protobuf.Timestamp created_at = 10;
  google.protobuf.Timestamp updated_at = 11;
}

message OperationInfo {
  string id = 1;
  string vm_id = 2;
  string request_key = 3;
  string type = 4;
  string status = 5;
  string provider_task_id = 6;
  string last_error = 7;
  google.protobuf.Timestamp created_at = 8;
  google.protobuf.Timestamp updated_at = 9;
}
```

### 5.3 Code Generation

Add to `Makefile`:

```makefile
.PHONY: proto
proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/kv/v1/kv.proto proto/vm/v1/vm.proto
```

Install dependencies:
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

### 5.4 gRPC Server Implementation

The gRPC handlers call the SAME service layer as the HTTP handlers. No logic duplication.

```go
// grpcserver/kv.go
type kvServer struct {
    kvv1.UnimplementedKVServiceServer
    store *store.Store
}

// grpcserver/vm.go
type vmServer struct {
    vmv1.UnimplementedVMServiceServer
    vmService *service.VMService
}
```

### 5.5 WatchVM Streaming

The `WatchVM` RPC is a server-side stream. When a client calls it, the server holds the connection open and sends a `VMEvent` message every time that VM's state changes.

Implementation:
1. The `VMService` has a `*sync.Map` of watchers: `map[vmID][]chan VMEvent`.
2. When `WatchVM` is called, register a channel for that VM ID.
3. When `PutVM` updates a VM, iterate watchers for that VM and send events on their channels.
4. When the client disconnects (`stream.Context().Done()`), unregister the channel.

This demonstrates the **watch pattern** used in Kubernetes (etcd watch), GCP (long-running operations), and Tesla's state-sync system.

---

## 6. Phase 4 — Kubernetes Deployment

This is the biggest learning phase. You're taking a stateful Go service — one that writes to disk (WAL) and runs background goroutines (reconciler, sweeper) — and deploying it on Kubernetes so that it survives pod restarts, node evictions, and rolling updates without losing data.

### 6.1 Why This Matters

The Tesla JD says "Kubernetes" outright. But more importantly: every cloud platform team deploys their control-plane services ON Kubernetes. The VM-as-a-Service control plane that manages VMs is itself a pod running in a K8s cluster. Understanding how stateful services run on K8s — and the specific pitfalls — is the difference between "I've used kubectl" and "I understand how production infrastructure works."

**What you'll learn:**
- Why StatefulSets exist (and why Deployments are wrong for this)
- How PersistentVolumeClaims give your WAL file a disk that outlives the pod
- How headless Services give each pod a stable DNS name
- How liveness and readiness probes let K8s know when your service is healthy vs. starting up
- How ConfigMaps inject configuration without rebuilding the image
- How graceful shutdown (SIGTERM handling) interacts with K8s pod termination
- How to build a minimal Docker image with multi-stage builds

### 6.2 Dockerfile

Multi-stage build: compile in a full Go image, copy the binary to a minimal Alpine image. Target: sub-15MB final image.

```dockerfile
# Dockerfile

# ---- Build stage ----
FROM golang:1.22-alpine AS builder

# Install protoc and plugins for gRPC code generation
RUN apk add --no-cache protobuf protobuf-dev make git
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make proto
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /kv .

# ---- Runtime stage ----
FROM alpine:3.19

# Add ca-certificates for any future HTTPS needs, and create non-root user
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 1000 kvuser

COPY --from=builder /kv /usr/local/bin/kv

# WAL data directory — this is where the PVC mounts
RUN mkdir -p /data && chown kvuser:kvuser /data
VOLUME ["/data"]

USER kvuser

# Default WAL path points to the PVC mount
ENV KV_WAL_PATH=/data/kv.wal
ENV KV_HTTP_PORT=8080
ENV KV_GRPC_PORT=9090
ENV KV_METRICS_PORT=9091

EXPOSE 8080 9090 9091

ENTRYPOINT ["kv"]
```

**Why multi-stage:**
- Build stage: ~1.2GB (full Go toolchain + protoc)
- Runtime stage: ~12MB (Alpine + your binary)
- The `CGO_ENABLED=0` flag produces a statically-linked binary — no glibc dependency, runs on Alpine
- `-ldflags="-s -w"` strips debug symbols and DWARF info, shrinking the binary ~30%
- Non-root user (`kvuser`) — never run containers as root in production

**Build and test locally:**
```bash
docker build -t kv:latest .
docker run -p 8080:8080 -p 9090:9090 -p 9091:9091 -v kv-data:/data kv:latest
```

### 6.3 Kubernetes Manifests

All manifests live in `k8s/` directory. Raw YAML, no Helm, no Kustomize — you need to understand every field.

#### 6.3.1 Namespace

```yaml
# k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kv-system
  labels:
    app.kubernetes.io/name: kv
    app.kubernetes.io/part-of: kv-control-plane
```

Namespaces isolate workloads. `kv-system` keeps your resources separate from the default namespace.

#### 6.3.2 ConfigMap

```yaml
# k8s/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kv-config
  namespace: kv-system
  labels:
    app.kubernetes.io/name: kv
data:
  KV_HTTP_PORT: "8080"
  KV_GRPC_PORT: "9090"
  KV_METRICS_PORT: "9091"
  KV_WAL_PATH: "/data/kv.wal"
  KV_RECONCILE_INTERVAL: "5s"
  KV_WAL_COMPACT_THRESHOLD_BYTES: "10485760"
  KV_TASK_TIMEOUT: "2m"
  MOCK_CREATE_DELAY: "3s"
  MOCK_START_DELAY: "2s"
  MOCK_STOP_DELAY: "2s"
  MOCK_DELETE_DELAY: "1s"
  MOCK_FAILURE_RATE: "0.0"
```

**Why ConfigMap, not env vars in the StatefulSet:** Separation of concerns. The StatefulSet describes the workload shape (replicas, volumes, probes). The ConfigMap describes the workload configuration. Changing a config value means editing the ConfigMap and restarting the pod — you don't touch the StatefulSet spec. In production, this lets platform teams manage configs without deploying new container images.

#### 6.3.3 StatefulSet

This is the core manifest. A StatefulSet (not a Deployment) because:
1. **Stable network identity:** Each pod gets a predictable DNS name (`kv-0.kv-headless.kv-system.svc.cluster.local`). Deployments give random names.
2. **Stable storage:** Each pod gets its own PersistentVolumeClaim that follows it across restarts. If `kv-0` dies and comes back, it re-attaches the SAME disk with the SAME WAL file. Deployments share PVCs or lose them.
3. **Ordered startup/shutdown:** Pods start in order (0, 1, 2...) and stop in reverse. Critical for stateful services where startup order matters.

```yaml
# k8s/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: kv
  namespace: kv-system
  labels:
    app.kubernetes.io/name: kv
    app.kubernetes.io/component: control-plane
spec:
  serviceName: kv-headless  # must match the headless Service name
  replicas: 1               # single replica — no leader election needed
  selector:
    matchLabels:
      app.kubernetes.io/name: kv
  template:
    metadata:
      labels:
        app.kubernetes.io/name: kv
        app.kubernetes.io/component: control-plane
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9091"
        prometheus.io/path: "/metrics"
    spec:
      terminationGracePeriodSeconds: 30
      containers:
        - name: kv
          image: kv:latest
          imagePullPolicy: IfNotPresent  # for kind local images
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
            - name: grpc
              containerPort: 9090
              protocol: TCP
            - name: metrics
              containerPort: 9091
              protocol: TCP
          envFrom:
            - configMapRef:
                name: kv-config
          volumeMounts:
            - name: kv-data
              mountPath: /data
          resources:
            requests:
              cpu: 100m        # 0.1 CPU core
              memory: 128Mi    # 128MB RAM
            limits:
              cpu: 500m        # 0.5 CPU core
              memory: 256Mi    # 256MB RAM

          # --- PROBES ---

          # Liveness probe: "Is the process stuck?"
          # If this fails, K8s KILLS the pod and restarts it.
          # Points at a dedicated /healthz endpoint that returns 200
          # if the main goroutine is responsive.
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 5    # wait 5s after container starts
            periodSeconds: 10         # check every 10s
            timeoutSeconds: 3         # fail if no response in 3s
            failureThreshold: 3       # kill after 3 consecutive failures

          # Readiness probe: "Can this pod serve traffic?"
          # If this fails, K8s removes the pod from Service endpoints
          # (stops routing traffic to it) but does NOT kill it.
          # Points at /readyz which returns 200 only after WAL replay
          # is complete and the reconciler is running.
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            initialDelaySeconds: 2    # WAL replay might take a moment
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 2

          # Startup probe: "Has the container finished initializing?"
          # Until this succeeds, liveness and readiness probes are disabled.
          # This prevents K8s from killing a pod that's doing a long WAL replay.
          startupProbe:
            httpGet:
              path: /healthz
              port: http
            periodSeconds: 2
            failureThreshold: 30      # 30 * 2s = 60s max startup time

  # PersistentVolumeClaim template — K8s creates one PVC per pod
  volumeClaimTemplates:
    - metadata:
        name: kv-data
        labels:
          app.kubernetes.io/name: kv
      spec:
        accessModes: ["ReadWriteOnce"]  # only one pod reads/writes at a time
        resources:
          requests:
            storage: 1Gi  # 1GB disk for WAL data
```

**Key fields explained:**

`terminationGracePeriodSeconds: 30` — When K8s sends SIGTERM (pod deletion, rolling update), the container has 30 seconds to finish its graceful shutdown (flush WAL, drain connections) before K8s sends SIGKILL. This is why the graceful shutdown code in Phase 1 (Section 3.5) matters — without it, K8s kills the process mid-write.

`resources.requests` vs `resources.limits` — Requests are what the scheduler guarantees. Limits are the ceiling. If the container exceeds its memory limit, K8s OOM-kills it. If it exceeds CPU limit, it gets throttled. Setting both correctly prevents your pod from starving other workloads or being killed unexpectedly.

`volumeClaimTemplates` — This is the StatefulSet magic. K8s creates a PVC named `kv-data-kv-0` for pod `kv-0`. If the pod restarts, the PVC persists and re-attaches. The WAL file at `/data/kv.wal` survives pod restarts, node evictions, and even node failure (if the PV is backed by network storage). In kind, the PV is backed by a hostPath (local disk), which is fine for development.

`imagePullPolicy: IfNotPresent` — Required for kind, which loads images directly into its nodes. Without this, K8s tries to pull from a registry and fails.

#### 6.3.4 Headless Service

```yaml
# k8s/service-headless.yaml
apiVersion: v1
kind: Service
metadata:
  name: kv-headless
  namespace: kv-system
  labels:
    app.kubernetes.io/name: kv
spec:
  clusterIP: None  # THIS makes it headless
  selector:
    app.kubernetes.io/name: kv
  ports:
    - name: http
      port: 8080
      targetPort: http
    - name: grpc
      port: 9090
      targetPort: grpc
    - name: metrics
      port: 9091
      targetPort: metrics
```

**Why headless (`clusterIP: None`):** A normal Service gives you a single virtual IP that load-balances across pods. A headless Service gives each pod its OWN DNS record:
- `kv-0.kv-headless.kv-system.svc.cluster.local`

For stateful services this matters because you might need to address a specific pod (e.g., "I need the pod that has MY data"). With one replica it doesn't matter operationally, but you're learning the pattern that's used when there are many replicas.

#### 6.3.5 ClusterIP Service (for external access)

```yaml
# k8s/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: kv
  namespace: kv-system
  labels:
    app.kubernetes.io/name: kv
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: kv
  ports:
    - name: http
      port: 8080
      targetPort: http
    - name: grpc
      port: 9090
      targetPort: grpc
    - name: metrics
      port: 9091
      targetPort: metrics
```

This is the Service clients connect to. For local development, you'll use `kubectl port-forward` to reach it from your machine.

### 6.4 Health Endpoints

Add two new HTTP endpoints to the main server (port 8080):

#### `GET /healthz` (liveness)

Returns `200 OK` with `{"status": "ok"}` if the main goroutine is responsive. This should be extremely simple — just proves the HTTP server can handle a request. Don't check downstream dependencies (WAL, provider) here — that's the readiness probe's job.

```go
func handleHealthz(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

#### `GET /readyz` (readiness)

Returns `200 OK` only when the service is fully ready to serve:
1. WAL replay is complete
2. Reconciler goroutine is running
3. gRPC server is accepting connections

```go
type ReadinessChecker struct {
    walReady        atomic.Bool
    reconcilerReady atomic.Bool
    grpcReady       atomic.Bool
}

func (rc *ReadinessChecker) Handler(w http.ResponseWriter, r *http.Request) {
    checks := map[string]bool{
        "wal_replayed":      rc.walReady.Load(),
        "reconciler_running": rc.reconcilerReady.Load(),
        "grpc_listening":     rc.grpcReady.Load(),
    }

    allReady := true
    for _, ready := range checks {
        if !ready {
            allReady = false
            break
        }
    }

    w.Header().Set("Content-Type", "application/json")
    if allReady {
        w.WriteHeader(http.StatusOK)
    } else {
        w.WriteHeader(http.StatusServiceUnavailable) // 503
    }
    json.NewEncoder(w).Encode(checks)
}
```

**Why separate liveness and readiness:**
- Liveness answers: "Is the process deadlocked?" → Kill and restart if so
- Readiness answers: "Can it handle requests right now?" → Stop sending traffic but don't kill

During WAL replay (could take seconds with a large log), the pod is ALIVE but NOT READY. K8s should not kill it (liveness passes) but should not route traffic to it (readiness fails). After replay completes, readiness flips to true and K8s starts routing.

The startup probe handles the initial boot: until it succeeds, K8s doesn't run liveness/readiness at all, preventing a slow WAL replay from triggering a liveness failure and a restart loop.

### 6.5 kind Cluster Setup

kind (Kubernetes IN Docker) runs a full K8s cluster as Docker containers. Perfect for local development.

#### Install kind

```bash
# macOS
brew install kind

# Linux
go install sigs.k8s.io/kind@latest
```

#### Cluster Configuration

```yaml
# k8s/kind-config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: kv-dev
nodes:
  - role: control-plane
    # Port mappings: access services from localhost
    extraPortMappings:
      - containerPort: 30080
        hostPort: 8080
        protocol: TCP
      - containerPort: 30090
        hostPort: 9090
        protocol: TCP
      - containerPort: 30091
        hostPort: 9091
        protocol: TCP
```

#### Development Workflow

The Makefile ties everything together:

```makefile
# Makefile additions for Phase 4

# Docker
.PHONY: docker-build
docker-build:
	docker build -t kv:latest .

# kind cluster
.PHONY: kind-create
kind-create:
	kind create cluster --config k8s/kind-config.yaml
	kind load docker-image kv:latest --name kv-dev

.PHONY: kind-delete
kind-delete:
	kind delete cluster --name kv-dev

.PHONY: kind-load
kind-load: docker-build
	kind load docker-image kv:latest --name kv-dev

# K8s deploy
.PHONY: k8s-deploy
k8s-deploy:
	kubectl apply -f k8s/namespace.yaml
	kubectl apply -f k8s/configmap.yaml
	kubectl apply -f k8s/service-headless.yaml
	kubectl apply -f k8s/service.yaml
	kubectl apply -f k8s/statefulset.yaml
	kubectl -n kv-system rollout status statefulset/kv --timeout=120s

.PHONY: k8s-delete
k8s-delete:
	kubectl delete -f k8s/statefulset.yaml --ignore-not-found
	kubectl delete -f k8s/service.yaml --ignore-not-found
	kubectl delete -f k8s/service-headless.yaml --ignore-not-found
	kubectl delete -f k8s/configmap.yaml --ignore-not-found
	kubectl delete -f k8s/namespace.yaml --ignore-not-found

.PHONY: k8s-logs
k8s-logs:
	kubectl -n kv-system logs -f statefulset/kv

.PHONY: k8s-port-forward
k8s-port-forward:
	kubectl -n kv-system port-forward svc/kv 8080:8080 9090:9090 9091:9091

# Full cycle: build → load → deploy
.PHONY: k8s-up
k8s-up: kind-load k8s-deploy
	@echo "Deployed. Run 'make k8s-port-forward' to access."

# Full teardown
.PHONY: k8s-down
k8s-down: k8s-delete kind-delete
```

**Development cycle:**
```bash
# First time: create cluster + deploy
make kind-create
make k8s-up

# Access the service
make k8s-port-forward &
curl localhost:8080/keys/test

# After code changes
make k8s-up  # rebuilds image, loads into kind, redeploys

# View logs
make k8s-logs

# Teardown
make k8s-down
```

### 6.6 Stateful Behavior Across Pod Restarts

This is the core lesson. The WAL file lives on a PersistentVolume. When the pod restarts:

1. K8s sends SIGTERM → your graceful shutdown runs → WAL is fsync'd and closed
2. K8s detaches the PV from the old pod
3. K8s schedules the new pod (same name: `kv-0`)
4. K8s re-attaches the SAME PV to the new pod
5. Your Go binary starts → opens the WAL file at `/data/kv.wal` → replays entries → fully reconstructs state

**From the service's perspective, nothing changed.** The WAL replay on startup is the mechanism that makes this work. This is why we built it in Phase 1 — it wasn't just a file exercise, it was the foundation for stateful Kubernetes operation.

### 6.7 Pod Identity and DNS

With the headless Service and StatefulSet, your pod gets:
- **Pod name:** `kv-0` (stable across restarts)
- **DNS:** `kv-0.kv-headless.kv-system.svc.cluster.local`
- **PVC:** `kv-data-kv-0` (stable across restarts)

You can verify from inside the cluster:
```bash
kubectl -n kv-system exec -it kv-0 -- nslookup kv-0.kv-headless.kv-system.svc.cluster.local
```

### 6.8 Useful kubectl Commands

```bash
# See pods and their status
kubectl -n kv-system get pods -o wide

# See StatefulSet details
kubectl -n kv-system describe statefulset kv

# See PVCs (persistent disks)
kubectl -n kv-system get pvc

# See the actual PV backing the PVC
kubectl get pv

# Stream logs
kubectl -n kv-system logs kv-0 -f

# Exec into the pod
kubectl -n kv-system exec -it kv-0 -- sh

# Inspect the WAL file inside the pod
kubectl -n kv-system exec kv-0 -- ls -la /data/

# Force-delete a pod (simulate crash — K8s restarts it)
kubectl -n kv-system delete pod kv-0

# Watch pod restart
kubectl -n kv-system get pods -w

# View events (scheduling, pulling, starting)
kubectl -n kv-system get events --sort-by='.lastTimestamp'

# Check resource usage
kubectl -n kv-system top pods
```

---

## 7. Data Models

### 7.1 KV Entry (Phase 1)

| Field | Type | Description |
|-------|------|-------------|
| Value | `string` | The stored value |
| ExpiresAt | `time.Time` | When this entry expires (zero = never) |

### 7.2 VM (Phase 2)

See Section 4.2.

### 7.3 Operation (Phase 2)

See Section 4.2.

### 7.4 KV Key Namespace

| Pattern | Owner | Example |
|---------|-------|---------|
| `*` (no prefix) | User KV data | `my-key`, `config-v2` |
| `vm:{uuid}` | VM service | `vm:550e8400-e29b-41d4-a716-446655440000` |
| `op:{uuid}` | Operation service | `op:6ba7b810-9dad-11d1-80b4-00c04fd430c8` |
| `idx:vm:list` | VM index | `idx:vm:list` |
| `idx:op:vm:{uuid}` | Operation index | `idx:op:vm:550e8400-...` |

User keys cannot start with `vm:`, `op:`, or `idx:` — the PUT handler validates this and returns `400`.

---

## 8. API Reference

See Sections 3.4 (KV HTTP), 4.4 (VM HTTP), 5.2 (gRPC protos). Health endpoints for K8s probes: Section 6.4.

All HTTP responses use `Content-Type: application/json`. All request bodies are JSON. The server returns `415 Unsupported Media Type` if the request `Content-Type` is not `application/json` for endpoints that expect a body.

**Common HTTP error codes:**
| Code | Meaning |
|------|---------|
| 400 | Bad request (validation error, malformed JSON) |
| 404 | Resource not found |
| 405 | Method not allowed |
| 409 | Conflict (duplicate name, invalid state transition) |
| 415 | Unsupported media type |
| 500 | Internal server error |

---

## 9. Reconciliation Engine

The reconciler is the heart of the control plane. It runs as a background goroutine that wakes every 5 seconds (configurable via `KV_RECONCILE_INTERVAL` env var).

### 9.1 Reconciliation Loop

```
Every tick:
  1. List all VMs
  2. For each VM where desired state ≠ observed state:
     a. Check if there's already a PENDING or RUNNING operation for this VM
        - If yes, check the operation's provider task status
        - If no, create a new operation and dispatch to provider
  3. For VMs in CREATING state:
     - Poll provider for task completion
     - On success: set provisioning=READY, observed_power=RUNNING, assign provider_id
     - On failure: set provisioning=FAILED, operation status=FAILED
  4. For VMs in DELETING state:
     - Poll provider for deletion task completion
     - On success: set provisioning=DELETED
  5. For VMs where desired_power ≠ observed_power and provisioning=READY:
     - Dispatch start/stop to provider
  6. Update last_observed_at for all polled VMs
```

### 9.2 Operation State Machine

```
PENDING → RUNNING → SUCCEEDED
                  → FAILED

State transitions:
- PENDING → RUNNING: when the provider task is dispatched
- RUNNING → SUCCEEDED: when the provider task completes successfully
- RUNNING → FAILED: when the provider task fails or times out
```

### 9.3 Timeout-After-Success Recovery

This is a specific scenario to handle: the provider completes a task, but our reconciler crashes before recording the success. On next startup:

1. The operation is still in `RUNNING` status
2. The provider reports the task as completed
3. The reconciler detects this (task succeeded but operation says RUNNING)
4. It updates the operation to `SUCCEEDED` and updates the VM state

This is why we store `provider_task_id` — it lets us re-query the provider after a crash.

**Implementation:**

```go
func (r *Reconciler) reconcileOperation(vm *VM, op *Operation) error {
    if op.Status != OpStatusRunning || op.ProviderTaskID == "" {
        return nil
    }

    task, err := r.provider.GetTask(op.ProviderTaskID)
    if err != nil {
        return fmt.Errorf("polling task %s: %w", op.ProviderTaskID, err)
    }

    switch task.Status {
    case "completed":
        // Task succeeded — update operation and VM
        op.Status = OpStatusSucceeded
        r.applyTaskResult(vm, op)
    case "failed":
        op.Status = OpStatusFailed
        op.LastError = task.Error
    case "running":
        // Check for timeout (default 2 minutes)
        if time.Since(op.UpdatedAt) > r.taskTimeout {
            op.Status = OpStatusFailed
            op.LastError = "provider task timed out"
        }
    }
    return nil
}
```

### 9.4 Reconciler Idempotency

The reconciler must be safe to run multiple times without side effects:
- It checks for existing operations before creating new ones
- Provider tasks are idempotent (the mock provider uses task IDs)
- VM version checks prevent stale writes

---

## 10. Mock Provider

The mock provider simulates a cloud hypervisor (like VMware vSphere or KVM/libvirt). It doesn't manage real VMs — it simulates async task completion with configurable delays and failure rates.

### 10.1 Interface

```go
// provider/provider.go

type Provider interface {
    CreateVM(ctx context.Context, req CreateVMRequest) (taskID string, err error)
    DeleteVM(ctx context.Context, providerID string) (taskID string, err error)
    StartVM(ctx context.Context, providerID string) (taskID string, err error)
    StopVM(ctx context.Context, providerID string) (taskID string, err error)
    GetTask(taskID string) (*Task, error)
}

type CreateVMRequest struct {
    Name     string
    CPU      int
    MemoryMB int
}

type Task struct {
    ID         string
    Status     string    // "running", "completed", "failed"
    Result     string    // provider-specific result (e.g., provider VM ID)
    Error      string    // error message if failed
    CreatedAt  time.Time
    FinishedAt time.Time
}
```

### 10.2 Mock Implementation

```go
// provider/mock.go

type MockProvider struct {
    mu    sync.Mutex
    tasks map[string]*Task

    // Configurable behavior
    CreateDelay  time.Duration  // default 3s
    StartDelay   time.Duration  // default 2s
    StopDelay    time.Duration  // default 2s
    DeleteDelay  time.Duration  // default 1s
    FailureRate  float64        // 0.0-1.0, default 0.0 (no failures)
}
```

When a method like `CreateVM` is called:
1. Generate a task ID (UUID)
2. Store a `Task{Status: "running"}` in the tasks map
3. Launch a goroutine that:
   - Sleeps for `CreateDelay`
   - With probability `FailureRate`, sets status to `"failed"` with error message
   - Otherwise sets status to `"completed"` with a generated provider ID as the result
4. Return the task ID immediately (the caller polls via `GetTask`)

**Configuration via env vars:**
| Var | Default | Description |
|-----|---------|-------------|
| `MOCK_CREATE_DELAY` | `3s` | Time to simulate VM creation |
| `MOCK_START_DELAY` | `2s` | Time to simulate VM start |
| `MOCK_STOP_DELAY` | `2s` | Time to simulate VM stop |
| `MOCK_DELETE_DELAY` | `1s` | Time to simulate VM deletion |
| `MOCK_FAILURE_RATE` | `0.0` | Probability of task failure (0.0 = never, 1.0 = always) |

---

## 11. Observability

### 11.1 Prometheus Metrics

Serve metrics on the metrics port (default 9091) at `GET /metrics` in Prometheus exposition format. No client library — write the exposition format manually.

**Exposition format example:**
```
# HELP kv_requests_total Total number of KV API requests
# TYPE kv_requests_total counter
kv_requests_total{method="GET",status="200"} 1547
kv_requests_total{method="GET",status="404"} 23
kv_requests_total{method="PUT",status="201"} 892
kv_requests_total{method="PUT",status="200"} 341
kv_requests_total{method="DELETE",status="204"} 56
kv_requests_total{method="DELETE",status="404"} 12
```

**Phase 1 metrics:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kv_requests_total` | counter | `method`, `status` | Total HTTP requests by method and status code |
| `kv_request_duration_seconds` | histogram | `method` | Request latency. Buckets: 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0 |
| `kv_keys_current` | gauge | | Current number of non-expired keys |
| `kv_expired_keys_total` | counter | | Total keys removed by TTL (lazy + sweeper) |
| `kv_wal_bytes_total` | gauge | | Current WAL file size in bytes |
| `kv_wal_replay_duration_seconds` | gauge | | Time taken to replay WAL on startup |

**Phase 2 metrics:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vm_count` | gauge | `provisioning` | Current VM count by provisioning state |
| `vm_operations_total` | counter | `type`, `status` | Operations by type and final status |
| `vm_reconcile_duration_seconds` | histogram | | Time per reconciliation tick |
| `vm_reconcile_errors_total` | counter | | Reconciliation tick errors |
| `vm_provider_task_duration_seconds` | histogram | `type` | Provider task completion time |

**Phase 4 metrics (K8s-relevant):**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kv_startup_duration_seconds` | gauge | | Total time from process start to readiness (important for K8s startup probe tuning) |
| `kv_pod_restarts_total` | counter | | Number of times the process has started (increments on each WAL replay — useful for spotting restart loops) |

The Prometheus annotations on the pod (`prometheus.io/scrape: "true"`) let a Prometheus instance in the cluster auto-discover and scrape your metrics endpoint without manual configuration.

**Histogram implementation:**

```go
// metrics/histogram.go

type Histogram struct {
    mu      sync.Mutex
    buckets []float64          // upper bounds: [0.001, 0.005, 0.01, ...]
    counts  []uint64           // counts[i] = observations <= buckets[i]
    sum     float64
    count   uint64
}

func (h *Histogram) Observe(value float64) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.sum += value
    h.count++
    for i, bound := range h.buckets {
        if value <= bound {
            h.counts[i]++
        }
    }
}
```

Exposition for histograms:
```
# HELP kv_request_duration_seconds Request latency in seconds
# TYPE kv_request_duration_seconds histogram
kv_request_duration_seconds_bucket{method="GET",le="0.001"} 450
kv_request_duration_seconds_bucket{method="GET",le="0.005"} 1200
kv_request_duration_seconds_bucket{method="GET",le="0.01"} 1450
...
kv_request_duration_seconds_bucket{method="GET",le="+Inf"} 1547
kv_request_duration_seconds_sum{method="GET"} 3.842
kv_request_duration_seconds_count{method="GET"} 1547
```

### 11.2 Structured Logging

Use `log/slog` (Go 1.21+ stdlib) with JSON output:

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))
slog.SetDefault(logger)
```

Log examples:
```json
{"time":"2024-01-15T10:00:00Z","level":"INFO","msg":"server started","port":8080}
{"time":"2024-01-15T10:00:01Z","level":"INFO","msg":"WAL replayed","entries":1547,"duration_ms":23}
{"time":"2024-01-15T10:00:05Z","level":"INFO","msg":"reconcile tick","vms_checked":3,"actions_taken":1}
{"time":"2024-01-15T10:00:05Z","level":"WARN","msg":"provider task timed out","task_id":"abc","vm_id":"123"}
```

---

## 12. Testing Strategy

### 12.1 Unit Tests

Every package gets `*_test.go` files:

```
store/store_test.go          — Store CRUD, TTL expiration, concurrent access
store/wal_test.go            — WAL append, replay, corruption handling, compaction
metrics/metrics_test.go      — Counter increment, histogram buckets, exposition output
service/vm_service_test.go   — VM create/start/stop/delete, idempotency
reconciler/reconciler_test.go — Reconciliation logic with mock provider
provider/mock_test.go        — Mock provider task lifecycle
```

**Key test cases for the store:**
```go
func TestStore_PutGet(t *testing.T)           // basic round-trip
func TestStore_PutOverwrite(t *testing.T)     // overwrite returns created=false
func TestStore_GetMissing(t *testing.T)       // returns false
func TestStore_DeleteExisting(t *testing.T)   // returns true, subsequent Get returns false
func TestStore_DeleteMissing(t *testing.T)    // returns false
func TestStore_TTLExpiry(t *testing.T)        // put with TTL, sleep, get returns false
func TestStore_TTLLazyExpiry(t *testing.T)    // expired key cleaned on Get
func TestStore_ConcurrentAccess(t *testing.T) // goroutines doing Put/Get/Delete simultaneously
func TestStore_WALReplay(t *testing.T)        // close store, reopen, data persists
func TestStore_WALCorruptedTail(t *testing.T) // append garbage to WAL, reopen, valid entries survive
func TestStore_WALCompaction(t *testing.T)    // write N entries, compact, verify file is smaller
```

**Key test cases for VM service:**
```go
func TestVMService_Create(t *testing.T)                   // creates VM, returns operation
func TestVMService_CreateIdempotent(t *testing.T)         // same request_key returns same result
func TestVMService_CreateDuplicateName(t *testing.T)      // 409 conflict
func TestVMService_StartStop(t *testing.T)                // state transitions
func TestVMService_StartInvalidState(t *testing.T)        // 409 when already running
func TestVMService_DeleteCleansUp(t *testing.T)           // reconciler marks as DELETED
```

**Key test cases for reconciler:**
```go
func TestReconciler_ConvergesState(t *testing.T)          // desired=RUNNING, observed=STOPPED → starts VM
func TestReconciler_HandlesProviderFailure(t *testing.T)  // provider fails → operation marked FAILED
func TestReconciler_TimeoutAfterSuccess(t *testing.T)     // provider completed but op still RUNNING → detects and updates
func TestReconciler_Idempotent(t *testing.T)              // running reconciler twice doesn't create duplicate ops
func TestReconciler_SkipsConvergedVMs(t *testing.T)       // desired=observed → no action
```

### 12.2 Integration Tests

```
integration/api_test.go      — Full HTTP request/response cycle using httptest.Server
integration/grpc_test.go     — gRPC client against real server
integration/lifecycle_test.go — Create VM, wait for READY, stop, start, delete (end-to-end)
health/health_test.go        — Readiness transitions: not-ready → ready after WAL replay
```

**Integration test pattern:**
```go
func TestFullLifecycle(t *testing.T) {
    // 1. Start server with mock provider (0 delays for fast tests)
    // 2. POST /vms → get VM ID
    // 3. Poll GET /vms/{id} until provisioning=READY
    // 4. POST /vms/{id}/stop → wait for observed_power=STOPPED
    // 5. POST /vms/{id}/start → wait for observed_power=RUNNING
    // 6. DELETE /vms/{id} → wait for provisioning=DELETED
    // 7. GET /vms/{id} → 404
}
```

### 12.3 Running Tests

```bash
go test ./...                     # all tests
go test ./store/...               # just store package
go test -race ./...               # with race detector (MUST pass)
go test -v -run TestFullLifecycle ./integration/  # specific integration test
```

The `-race` flag enables Go's race detector, which catches concurrent access bugs at runtime. ALL tests must pass with `-race`.

---

## 13. Demo Scenarios

These are the acceptance criteria. The project is done when all of these work.

### Demo 1: KV Basics

```bash
# PUT a key
curl -X PUT localhost:8080/keys/hello -d '{"value":"world"}'
# → 201 {"key":"hello","created":true}

# GET the key
curl localhost:8080/keys/hello
# → 200 {"key":"hello","value":"world"}

# DELETE the key
curl -X DELETE localhost:8080/keys/hello
# → 204 (no body)

# GET after delete
curl localhost:8080/keys/hello
# → 404 {"error":"key not found"}
```

### Demo 2: TTL Expiration

```bash
# PUT with 5-second TTL
curl -X PUT localhost:8080/keys/temp -d '{"value":"gone-soon","ttl_sec":5}'
# → 201

# GET immediately
curl localhost:8080/keys/temp
# → 200 {"key":"temp","value":"gone-soon","ttl_remaining_sec":4}

# Wait 6 seconds...
sleep 6

# GET after expiry
curl localhost:8080/keys/temp
# → 404 {"error":"key not found"}
```

### Demo 3: WAL Crash Recovery

```bash
# PUT some keys
curl -X PUT localhost:8080/keys/persist1 -d '{"value":"survives"}'
curl -X PUT localhost:8080/keys/persist2 -d '{"value":"crash"}'

# Kill the server (simulate crash)
kill -9 $(pgrep kv)

# Restart the server
./kv

# GET keys — they survived
curl localhost:8080/keys/persist1
# → 200 {"key":"persist1","value":"survives"}
```

### Demo 4: VM Full Lifecycle

```bash
# Create a VM
curl -X POST localhost:8080/vms -d '{
  "name":"web-1","cpu":2,"memory_mb":4096,
  "request_key":"create-web1-001"
}'
# → 201 {vm: {provisioning: "CREATING", observed_power: "UNKNOWN"}}

# Wait for reconciler (poll every 2s)
watch -n2 'curl -s localhost:8080/vms/<id> | jq .vm.provisioning'
# → Eventually shows "READY"

# Stop the VM
curl -X POST localhost:8080/vms/<id>/stop -d '{"request_key":"stop-web1-001"}'
# → 202

# Watch it stop
watch -n2 'curl -s localhost:8080/vms/<id> | jq .vm.observed_power'
# → Eventually shows "STOPPED"

# Start it again
curl -X POST localhost:8080/vms/<id>/start -d '{"request_key":"start-web1-001"}'
# → 202

# Delete
curl -X DELETE localhost:8080/vms/<id> -d '{"request_key":"del-web1-001"}'
# → 202
```

### Demo 5: Idempotency Proof

```bash
# Send the same create request twice
curl -X POST localhost:8080/vms -d '{
  "name":"idem-test","cpu":1,"memory_mb":1024,
  "request_key":"create-idem-xyz"
}'
# → 201 (creates VM)

curl -X POST localhost:8080/vms -d '{
  "name":"idem-test","cpu":1,"memory_mb":1024,
  "request_key":"create-idem-xyz"
}'
# → 200 (returns SAME VM and operation, no duplicate created)

# Verify only one VM exists with this name
curl localhost:8080/vms | jq '[.vms[] | select(.name=="idem-test")] | length'
# → 1
```

### Demo 6: Timeout-After-Success Recovery

```bash
# 1. Create VM, wait for provider to complete CREATE task
# 2. Kill the server BEFORE reconciler records the success
#    (set MOCK_CREATE_DELAY=10s, kill during the 10s window)
# 3. Restart server
# 4. Reconciler detects: provider task completed, operation still RUNNING
# 5. Reconciler updates operation to SUCCEEDED, VM to READY
# 6. Verify via GET /vms/<id> → provisioning=READY
```

### Demo 7: gRPC + WatchVM

```bash
# Using grpcurl
grpcurl -plaintext -d '{"name":"grpc-vm","cpu":2,"memory_mb":2048,"request_key":"grpc-create-1"}' \
  localhost:9090 vm.v1.VMService/CreateVM

# Watch VM state changes (streams until ctrl-c)
grpcurl -plaintext -d '{"id":"<vm-id>"}' \
  localhost:9090 vm.v1.VMService/WatchVM
# → streams VMEvent messages as state changes
```

### Demo 8: Kubernetes StatefulSet Deployment

```bash
# Build and deploy to kind
make kind-create
make k8s-up

# Verify pod is running and ready
kubectl -n kv-system get pods
# → kv-0   1/1   Running   0   30s

# Verify PVC is bound
kubectl -n kv-system get pvc
# → kv-data-kv-0   Bound   pvc-xxxxx   1Gi   RWO   standard   30s

# Port-forward and interact
make k8s-port-forward &
curl -X PUT localhost:8080/keys/k8s-test -d '{"value":"from-kubernetes"}'
# → 201

curl localhost:8080/keys/k8s-test
# → 200 {"key":"k8s-test","value":"from-kubernetes"}
```

### Demo 9: Stateful Pod Restart (WAL Survives)

This is the money demo — proves state persists across pod restarts on K8s.

```bash
# Create some data
curl -X PUT localhost:8080/keys/survive1 -d '{"value":"pod-restart-proof"}'
curl -X POST localhost:8080/vms -d '{"name":"k8s-vm","cpu":2,"memory_mb":2048,"request_key":"k8s-create-1"}'

# Kill the pod (simulate crash)
kubectl -n kv-system delete pod kv-0

# Watch it come back (K8s restarts it because StatefulSet wants 1 replica)
kubectl -n kv-system get pods -w
# → kv-0   0/1   Pending     → ContainerCreating → Running → 1/1 Ready

# Verify data survived (WAL replayed from PVC)
curl localhost:8080/keys/survive1
# → 200 {"key":"survive1","value":"pod-restart-proof"}

# Verify VM state survived
curl localhost:8080/vms | jq '.vms[] | .name'
# → "k8s-vm"
```

### Demo 10: Readiness Probe During WAL Replay

```bash
# Check readiness during startup
kubectl -n kv-system delete pod kv-0
# Immediately query readiness:
kubectl -n kv-system get pods -w
# → kv-0   0/1   Running   0   1s   (not ready — WAL replaying)
# → kv-0   1/1   Running   0   3s   (ready — replay done)

# During the 0/1 window, the Service has NO endpoints:
kubectl -n kv-system get endpoints kv
# → kv   <none>   (no traffic routed during startup)
```

### Demo 11: ConfigMap Update

```bash
# Change reconciler interval from 5s to 10s
kubectl -n kv-system edit configmap kv-config
# Change KV_RECONCILE_INTERVAL from "5s" to "10s"

# Restart the pod to pick up the new config
kubectl -n kv-system rollout restart statefulset kv

# Verify via logs
kubectl -n kv-system logs kv-0 | grep "reconcile"
# → reconciler started with interval 10s
```

---

## 14. Project Structure

```
kv/
├── main.go                    # Entrypoint: wire everything, start servers
├── go.mod
├── go.sum
├── Dockerfile                 # Phase 4: Multi-stage Docker build
├── Makefile                   # proto, build, test, docker, k8s targets
├── SPEC.md                    # This file
│
├── store/                     # Phase 1: KV store
│   ├── store.go               # Store struct, Get/Put/Delete/Keys
│   ├── store_test.go
│   ├── wal.go                 # WAL append, replay, compaction
│   └── wal_test.go
│
├── metrics/                   # Prometheus metrics (manual exposition)
│   ├── metrics.go             # Counter, Gauge, Histogram types
│   ├── metrics_test.go
│   └── handler.go             # HTTP handler for /metrics
│
├── health/                    # Phase 4: Health check endpoints
│   ├── health.go              # ReadinessChecker, /healthz, /readyz
│   └── health_test.go
│
├── models/                    # Phase 2: Data models
│   ├── vm.go                  # VM, PowerState, ProvisioningState
│   └── operation.go           # Operation, OperationStatus, OperationType
│
├── vmstore/                   # Phase 2: VM persistence via KV
│   ├── vmstore.go             # VMStore: CRUD VMs/Ops via KV store
│   └── vmstore_test.go
│
├── service/                   # Phase 2: Business logic
│   ├── vm_service.go          # VMService: create/start/stop/delete
│   └── vm_service_test.go
│
├── reconciler/                # Phase 2: Background reconciliation
│   ├── reconciler.go          # Reconciler loop
│   └── reconciler_test.go
│
├── provider/                  # Phase 2: Provider abstraction
│   ├── provider.go            # Provider interface
│   ├── mock.go                # MockProvider implementation
│   └── mock_test.go
│
├── handler/                   # HTTP handlers
│   ├── kv.go                  # KV CRUD handlers
│   ├── kv_test.go
│   ├── vm.go                  # VM HTTP handlers
│   └── vm_test.go
│
├── proto/                     # Phase 3: Protobuf definitions
│   ├── kv/v1/kv.proto
│   └── vm/v1/vm.proto
│
├── gen/                       # Phase 3: Generated protobuf Go code
│   ├── kv/v1/
│   └── vm/v1/
│
├── grpcserver/                # Phase 3: gRPC server implementations
│   ├── kv.go
│   ├── vm.go
│   └── server.go              # gRPC server setup
│
├── k8s/                       # Phase 4: Kubernetes manifests
│   ├── kind-config.yaml       # kind cluster configuration
│   ├── namespace.yaml         # kv-system namespace
│   ├── configmap.yaml         # Environment configuration
│   ├── statefulset.yaml       # StatefulSet with PVC + probes
│   ├── service-headless.yaml  # Headless Service (pod DNS)
│   └── service.yaml           # ClusterIP Service (client access)
│
└── integration/               # Integration tests
    ├── api_test.go
    ├── grpc_test.go
    └── lifecycle_test.go
```

---

## 15. Build & Run

### Build

```bash
# Generate protobuf code (Phase 3)
make proto

# Build the binary
go build -o kv .

# Run tests
go test -race ./...
```

### Run

```bash
# Default settings
./kv

# Custom ports and WAL path
KV_HTTP_PORT=8080 \
KV_GRPC_PORT=9090 \
KV_METRICS_PORT=9091 \
KV_WAL_PATH=./data/kv.wal \
KV_RECONCILE_INTERVAL=5s \
KV_WAL_COMPACT_THRESHOLD_BYTES=10485760 \
MOCK_CREATE_DELAY=3s \
MOCK_FAILURE_RATE=0.0 \
./kv
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KV_HTTP_PORT` | `8080` | HTTP server port |
| `KV_GRPC_PORT` | `9090` | gRPC server port |
| `KV_METRICS_PORT` | `9091` | Prometheus metrics port |
| `KV_WAL_PATH` | `./kv.wal` | WAL file path |
| `KV_RECONCILE_INTERVAL` | `5s` | Reconciler tick interval |
| `KV_WAL_COMPACT_THRESHOLD_BYTES` | `10485760` | WAL compaction trigger (bytes) |
| `KV_TASK_TIMEOUT` | `2m` | Provider task timeout |
| `MOCK_CREATE_DELAY` | `3s` | Mock provider create delay |
| `MOCK_START_DELAY` | `2s` | Mock provider start delay |
| `MOCK_STOP_DELAY` | `2s` | Mock provider stop delay |
| `MOCK_DELETE_DELAY` | `1s` | Mock provider delete delay |
| `MOCK_FAILURE_RATE` | `0.0` | Mock provider failure probability |

---

## 16. Learning Map

Every component maps to something in the Tesla Cloud Platform JD or real infrastructure work.

| What You Build | What It Teaches | Tesla JD Alignment |
|---------------|-----------------|---------------------|
| KV Store with `sync.RWMutex` | Go concurrency primitives, shared-state protection | "Golang" — core language proficiency |
| WAL with fsync | Durability guarantees, crash recovery, append-only logs | "Background automation" — understanding data persistence |
| TTL sweeper goroutine | Background goroutines, `time.Ticker`, graceful shutdown | "Background automation for state synchronization" |
| Prometheus metrics (manual) | Observability, exposition format, histogram math | "Health monitoring", understanding what to measure |
| HTTP API with Go 1.22 routing | REST API design, status codes, content negotiation | "REST APIs for VM management" |
| VM state machine | Desired vs. observed state, state transitions | "VM as a Service control plane" |
| Reconciliation loop | Control-plane pattern, eventual consistency | "State synchronization and health monitoring" |
| Mock provider with async tasks | Provider abstraction, async polling, interface design | "Hypervisor integration (VMware/vSphere, KVM)" |
| Idempotency keys | Distributed systems correctness, retry safety | "VM management" — real provisioning requires this |
| Optimistic concurrency (version) | Conflict detection without pessimistic locking | "Resource scheduling and optimization" |
| Operations audit log | Tracking mutations, debugging production issues | Control-plane observability |
| gRPC + protobuf | Schema-first API design, code generation, binary protocol | "gRPC APIs" — explicitly listed in JD |
| WatchVM streaming | Server-side streaming, pub/sub pattern, K8s watch analog | "State synchronization" — real-time state propagation |
| Structured logging (`slog`) | Production logging practices | "Linux" — operating production Go services |
| Provider interface | Go interfaces, dependency injection, testability | "Hypervisor integration" — abstracting different providers |
| **Dockerfile (multi-stage)** | **Container image construction, layer caching, minimal images, non-root execution** | **"Kubernetes" — containers are the unit of deployment** |
| **StatefulSet + PVC** | **Stateful workloads on K8s, persistent volumes, pod identity, how WAL survives restarts** | **"Kubernetes" — explicitly listed; stateful services are the hard part** |
| **Headless Service** | **Per-pod DNS, service discovery, how K8s networking works for stateful pods** | **"Kubernetes" — networking fundamentals** |
| **Liveness / Readiness / Startup probes** | **K8s health model, why each probe exists, how K8s decides to kill vs. stop-routing** | **"Health monitoring" — K8s-native health checks** |
| **ConfigMap** | **Separating config from code, environment injection, config change workflow** | **"Kubernetes" — operational patterns** |
| **kind cluster + Makefile workflow** | **Local K8s development, image loading, kubectl fluency, debugging pods** | **"Kubernetes" — day-to-day development workflow** |
| **Graceful shutdown + SIGTERM** | **How K8s pod termination works, why terminationGracePeriodSeconds matters, drain before die** | **"Background automation" — clean shutdown is part of state synchronization** |

### Build Order

**Phase 1 (Days 1-3):** KV store → WAL → TTL → Metrics → HTTP handlers → Unit tests
**Phase 2 (Days 4-7):** VM models → VMStore → VMService → Mock provider → Reconciler → VM HTTP handlers → Integration tests
**Phase 3 (Days 8-9):** Proto definitions → Code generation → gRPC server → WatchVM streaming → gRPC tests
**Phase 4 (Days 10-13):** Dockerfile → Health endpoints (/healthz, /readyz) → kind cluster → K8s manifests (namespace → configmap → headless service → service → statefulset) → Pod restart demo → ConfigMap update demo

### Hassan Youssef Signal

Hassan Youssef (Sr. Staff Engineer, Tesla IaaS) has public Go repos showing:
- DNS-over-TLS proxy (network-level Go, goroutine management)
- `kube2consul` (Kubernetes service discovery bridge)
- Terraform and Packer LXD providers (infrastructure-as-code provider pattern)

Your mock provider pattern directly mirrors the provider plugin architecture he builds. The reconciler loop mirrors what `kube2consul` does (watch K8s state, reconcile with Consul). If you get to interview, you can talk about these patterns with specificity because you built them.
