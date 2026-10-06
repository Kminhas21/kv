# 02: kv service over HTTP with versions

**What to build:** A runnable kv server. Clients can PUT, GET and DELETE string values by key and list keys under a prefix. Every write gets a version from one global, ever-increasing revision counter, returned in the `ETag` header.

**Blocked by:** 01

**Status:** resolved

- [x] Store tracks a global revision; each write's version = new revision; Get returns value + version
- [x] `PUT /keys/{key}` → 201 created / 200 updated, `ETag: <version>`
- [x] `GET /keys/{key}` → 200 + value + `ETag`, or 404
- [x] `DELETE /keys/{key}` → 204 or 404
- [x] `GET /keys?prefix=p` → JSON array `{key, value, version}` sorted by key
- [x] `GET /healthz` and `/readyz` → 200
- [x] Store-level tests for versions and prefix list; at least one `httptest` handler test

**Concepts:** `net/http` ServeMux with method+path patterns (Go 1.22+), `r.PathValue`, writing status codes and headers, `encoding/json`, `httptest.NewRecorder`, why a *global* revision (etcd `mod_revision`) beats per-key counters.

**Hints:** `http.NewServeMux`, `mux.HandleFunc("PUT /keys/{key}", ...)`, `io.ReadAll(r.Body)`, `strconv.FormatUint`, `sort.Strings` / `slices.Sort`, `strings.HasPrefix`. Keep the store free of HTTP: handlers adapt, the store decides.

**Mock vs production:** etcd also uses one cluster-wide revision, but serves it over gRPC with range queries and watches.

**Manual check:** `go run ./cmd/kv`, then `curl -i -X PUT localhost:8080/keys/a -d hello` (201 + ETag), again (200, higher ETag), `curl localhost:8080/keys?prefix=` lists it.
