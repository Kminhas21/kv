# 01: Prefactor: repo layout

**What to build:** Move the existing in-memory store into its own importable `kv` package under `internal/`, so the kv server, vmctl and vmcli can all be separate binaries under `cmd/`. Behavior is unchanged and tests still pass.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] Store and its tests live in an `internal/kv` package (package name `kv`, not `main`)
- [ ] `go build ./...` and `go test ./...` succeed
- [ ] Existing tests actually run (`go test -v` lists them)

**Concepts:** Go packages vs modules, the `internal/` visibility rule, the `cmd/<binary>` convention, exported vs unexported identifiers.

**Manual check:** `go test -v ./...` shows each test name with PASS.
