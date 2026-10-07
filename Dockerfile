# One image holds all four binaries; each Kubernetes workload picks which one
# to run with `command:`. One build, one image to load, versions always match.

# ---- build stage: full Go toolchain, thrown away afterwards ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, source second: Docker caches each step, so editing a
# .go file does not re-run the module download layer.
COPY go.mod ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 makes fully static binaries that need no C library at
# runtime. -trimpath and -ldflags="-s -w" drop local paths and debug symbols.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# ---- runtime stage: no shell, no package manager, just the binaries ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
# distroless "nonroot" is uid 65532. A process that is hacked as non-root
# cannot do much damage to the node.
USER 65532:65532
