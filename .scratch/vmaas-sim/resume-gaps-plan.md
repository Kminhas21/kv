# Resume-gaps plan (tickets 20, 22–30)

Spec: the tickets themselves in `.scratch/vmaas-sim/issues/`, plus `CONTEXT.md` and `docs/adr/`. Branch: `resume-gaps`.

## Waves (each wave runs in parallel, in separate worktrees; merged before the next starts)

1. 22 group-commit WAL · 20 graceful shutdown · 30 CI
2. 23 TTL · 29 Terraform/GKE + Kustomize overlays
3. 24 snapshot/compaction · 25 vmctl leader lease
4. 26 benchmarks + kvbench · 27 probes + timed self-heal
5. 28 metrics + Prometheus/Grafana

Ticket 21 and VM tickets 15–19 stay with the human and are not in this plan.

## Global constraints

- Go standard library only, except `prometheus/client_golang` in ticket 28.
- Match the surrounding code: naming, comment density, error handling, package layout.
- Use the vocabulary in `CONTEXT.md`; respect ADR-0001 and ADR-0002.
- `go vet ./...` and `go test ./...` must pass. There is no cgo or gcc on this machine, so `-race` runs only in CI.
- On finishing, tick the ticket's checkboxes, set `**Status:** resolved`, and add a `## Comments` section with any measured numbers.
- One or more commits per ticket, each message ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.
