# 28: Prometheus metrics + Grafana dashboard

**What to build:** kv and vmctl expose `/metrics`, and a Prometheus and a Grafana running in the kind cluster (hand-written manifests, no Helm) show a live dashboard: request rate, p99 latency, fsync p99, batch size, and which vmctl replica is leader.

**Blocked by:** 22, 25

**Status:** ready-for-agent

- [ ] `prometheus/client_golang` is added as the repo's first dependency
- [ ] kv: `http_request_duration_seconds` histogram (labels route, method, code), `wal_fsync_duration_seconds`, `wal_batch_records` histogram, `wal_replay_duration_seconds` gauge, `kv_keys` gauge
- [ ] vmctl: `reconcile_total{result}`, `reconcile_duration_seconds`, `leader` gauge (0/1)
- [ ] Route labels use the mux pattern, not the raw path, so key names can't explode the label set
- [ ] Prometheus Deployment with its scrape config in a ConfigMap, discovering pods through `prometheus.io/scrape` annotations (needs RBAC to list pods)
- [ ] Grafana Deployment with the datasource and one dashboard provisioned from ConfigMaps; `make forward` exposes it
- [ ] Demo or manual check shows the dashboard moving under kvbench load

**Concepts:** counters vs gauges vs histograms, `histogram_quantile`, label cardinality, pull-based scraping, Kubernetes service discovery.

**Mock vs production:** production uses the Prometheus Operator (ServiceMonitor CRDs) or a managed service (Google Managed Prometheus on GKE), with alerting rules and long-term storage; ours is a single unreplicated Prometheus.

**Manual check:** `make forward`, open Grafana, run kvbench, watch the panels.
