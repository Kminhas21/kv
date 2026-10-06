# kv — KV store + VM control plane simulator

Learning project: prep for a VM-as-a-Service internship (Go, Kubernetes, VM lifecycle, scheduling). Master spec: `SPEC.md`. Build order: `PLAN.md`. Tickets: `.scratch/vmaas-sim/issues/`.

## How to work in this repo

- The user is learning Go. Tickets marked `Status: ready-for-human` are learning checkpoints: describe the goal and the Go APIs to reach for, let the user write the code, then review it for idiom, correctness and concurrency bugs. Don't hand over full implementations of these.
- Tickets marked `Status: ready-for-agent` are plumbing (Dockerfile, k8s manifests, mockhv, CLI, scripts). Claude writes these, then explains each piece in depth.
- Every explanation should contrast our mock with production (real vSphere/KVM, etcd, multi-replica controllers, etc.).
- Commit after each completed ticket.

## Commands

- `go test ./...` runs unit tests and the in-process system harness
- `go run ./cmd/kv`, `go run ./cmd/mockhv`, `go run ./cmd/vmctl` run the services locally
- `go run ./cmd/vmcli ...` is the manual-testing CLI
- `make kind-up`, `make deploy`, `make demo` run the cluster workflow (see Makefile)

## Agent skills

### Issue tracker

Local markdown under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
