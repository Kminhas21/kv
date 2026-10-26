# 29: Terraform GKE Autopilot + Kustomize overlays

**What to build:** `make gke-up` stands up the whole system on GKE from nothing and prints how long it took; `make gke-down` destroys it. Terraform owns infrastructure only (VPC, cluster, registry) with remote state in GCS; Kustomize owns the workloads, with the same base for kind and GKE.

**Blocked by:** 13

**Status:** resolved

- [x] Manifests split into a shared base plus a kind overlay and a GKE overlay; `make up` on kind behaves exactly as before
- [x] Terraform `network` module: a VPC and one subnet with secondary IP ranges for pods and services
- [x] Terraform `gke` module: an Autopilot cluster in `us-central1` on the `regular` release channel, using that subnet
- [x] Terraform `registry` module: an Artifact Registry Docker repo
- [x] A `dev` environment wires the modules together, takes `project_id` as a variable, and uses a `gcs` backend
- [x] Bootstrap steps documented: `gcloud` auth, enabling APIs, creating the versioned state bucket by hand
- [x] `make gke-up`: terraform apply → docker build + push to Artifact Registry → kubectl apply the GKE overlay → wait for Ready → print elapsed time. `make gke-down`: terraform destroy
- [ ] The measured `gke-up` time is recorded in the benchmarks doc
- [x] The docs warn that the cluster costs money and must be destroyed after each session

**Concepts:** Terraform modules and variables, remote state and locking, the bootstrap problem for the state bucket, VPC-native clusters and secondary ranges, Autopilot vs Standard.

**Mock vs production:** production uses separate state per environment, CI-driven `terraform plan`/`apply` with review, Workload Identity, and private clusters; kind has no cloud networking or registry at all.

**Manual check:** `make gke-up`, `kubectl get pods`, run the demo against the GKE cluster, `make gke-down`.

## Comments

- Code complete. Terraform validated offline (fmt, init -backend=false, validate in hashicorp/terraform:1.9). The kind overlay renders identically to the old `kubectl kustomize k8s`.
- **Live run pending.** Nothing was applied to GCP. The user must run `make gke-up` / `make gke-down` for real and record the measured gke-up time in the benchmarks doc (the benchmarks doc exists; its gke-up time row is pending the live run, so that checkbox stays unticked). The Manual check is also theirs.
- GKE overlay uses placeholders (`__GKE_IMAGE__`/`__GKE_TAG__`) filled by sed in `make gke-up`, which pipes `kubectl kustomize` into `kubectl apply -f -` instead of `apply -k`.
