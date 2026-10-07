# 13: Deploy to kind

**What to build:** One command brings up a kind cluster (1 control-plane, 2 workers), builds one image with all binaries, loads it, and applies manifests: kv as a StatefulSet with a PVC and headless Service, mockhv and vmctl as Deployments with Services, a ConfigMap for env, probes, requests/limits. vmcli works against it through `kubectl port-forward`.

**Blocked by:** 11

**Status:** resolved

- [x] Multi-stage Dockerfile (static Go build → small runtime image, non-root)
- [x] Manifests in a k8s folder; namespace `vmaas`
- [x] Makefile targets: kind-up, image, load, deploy, forward, logs, kind-down
- [x] All pods Ready; `vmcli vm create` via port-forward reaches Running

**Concepts (walkthrough, line by line):** StatefulSet vs Deployment, volumeClaimTemplates/PVC/PV, headless Service + stable DNS, ClusterIP Services, ConfigMap → env, liveness vs readiness, requests vs limits, `imagePullPolicy` with kind-loaded images.

**Mock vs production:** real clusters pull from a registry, use Ingress + TLS, run on many nodes with a storage class backed by real disks, and the control plane would run 2+ replicas.

**Manual check:** `kubectl -n vmaas get pods,svc,pvc -o wide`, then `kubectl -n vmaas logs deploy/vmctl -f` while creating a VM.
