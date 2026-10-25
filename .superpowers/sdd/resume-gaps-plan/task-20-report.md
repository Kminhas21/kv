# Task 20: Graceful shutdown (resolved)

Built: internal/serve.Run(ctx, name, addr, handler): http.Server with ListenAndServe in a goroutine, on ctx.Done calls Shutdown with a 10s deadline (fresh context), returns nil on clean drain. Each main creates signal.NotifyContext(SIGINT, SIGTERM) and calls it. terminationGracePeriodSeconds: 15 in all three manifests (kv/vmctl were 10).

Shutdown sequences
- kv: signal -> "shutdown signal received, draining" -> Shutdown -> "http server drained" -> store.Close() -> "store closed". Store package untouched; task 22's WAL_FSYNC wiring only needs to go before kv.Open.
- mockhv: signal -> drain -> exit.
- vmctl: signal cancels ctx, stopping the reconciler and HTTP server; main waits on a WaitGroup for the reconciler -> "reconciler stopped" -> "vmctl stopped". If the server fails on its own, stop() cancels the reconciler too.

Tests: internal/serve TestRun_FinishesInFlightRequestThenReturnsNil; internal/vm TestReconciler_RunReturnsWhenContextCancelled. go vet clean; go test ./... all ok.

Files: cmd/{kv,mockhv,vmctl}/main.go, internal/serve/serve{,_test}.go, internal/vm/provision_test.go, k8s/{kv,mockhv,vmctl}.yaml, ticket 20.

Concerns
- Tick ctx derives from the signal ctx, so an in-progress tick is aborted rather than finished. Safe (state persisted in kv, ops idempotent); finishing a tick (30s timeout) could blow the 10s deadline.
- gofmt -l lists most files (pre-existing, CRLF); untouched.
- Manual kind check not run.

Mock vs production
K8s sends SIGTERM and in parallel removes the pod from Service endpoints; propagation (kube-proxy, LB) lags, so requests can still arrive for seconds after SIGTERM. Production adds a preStop hook (e.g. sleep 5) so traffic drains before the listener closes. We shut down immediately; fine for one replica on kind. Grace period (15s) must exceed preStop + Shutdown deadline (10s) or SIGKILL cuts the drain.

## Fix round 1
- Reconciler.Run: each tick uses context.WithTimeout(context.WithoutCancel(ctx), 8s); loop is `for ctx.Err() == nil`, so no new tick starts after cancel and the in-flight tick finishes. Old "stops on cancel" test kept.
- New internal/vm/shutdown_test.go: cancel while a tick is blocked in FindByExternalID; Run does not return until released, then the tick's create is observable (1 provider VM) and Run returns.
- cmd/kv/main.go comment made accurate (drain timeout may leave stragglers).
- serve.Run calls signal.Reset(SIGINT, SIGTERM) when ctx fires, so a second Ctrl+C force-kills a hung drain.
- Rebased onto resume-gaps; ticket conflict resolved with the resume-gaps text, all boxes ticked, Status resolved; ci.yml untouched.
- Ran: `go test ./internal/vm/... ./internal/serve/...` -> ok both; `go vet ./...` clean; `go test ./...` all ok.
