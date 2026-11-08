# infra: GKE Autopilot for the vmaas simulator

> **WARNING: THIS COSTS REAL MONEY.** An Autopilot cluster bills per pod
> request, plus a regional cluster management fee, load balancers and disks,
> for as long as it exists. **Run `make gke-down` at the end of every
> session**, then check the GCP console that the cluster is gone. The state
> bucket is not managed by Terraform and persists after `gke-down`: cheap, but
> not free. Delete it by hand when you are finished with the project.

Terraform owns infrastructure only (VPC + subnet with pod/service ranges,
Autopilot cluster, Artifact Registry). Kustomize (`k8s/overlays/gke`) owns the
workloads.

Layout: `modules/{network,gke,registry}` are reusable; `envs/dev` wires them
together. Variables: `project_id` (required), `region` (default `us-central1`).

## One-time bootstrap (by hand)

The state bucket cannot be created by the Terraform that stores its state in
it, so create it manually.

```sh
gcloud auth login
gcloud auth application-default login      # Terraform uses these credentials
gcloud config set project YOUR_PROJECT_ID
gcloud components install gke-gcloud-auth-plugin   # kubectl auth for GKE

gcloud services enable container.googleapis.com \
  artifactregistry.googleapis.com compute.googleapis.com

# Versioned bucket: lets you recover an older state file.
gcloud storage buckets create gs://YOUR_STATE_BUCKET \
  --location=us-central1 --uniform-bucket-level-access
gcloud storage buckets update gs://YOUR_STATE_BUCKET --versioning
```

## Use

```sh
make gke-up   PROJECT_ID=YOUR_PROJECT_ID TF_STATE_BUCKET=YOUR_STATE_BUCKET
make gke-down PROJECT_ID=YOUR_PROJECT_ID TF_STATE_BUCKET=YOUR_STATE_BUCKET
```

`gke-up` runs terraform apply, builds and pushes the image, fetches
credentials, applies the overlay, waits for rollout and prints elapsed seconds.

By hand, the bucket is passed as a partial backend config and the project as a
variable:

```sh
cd infra/envs/dev
terraform init -backend-config="bucket=YOUR_STATE_BUCKET"
terraform plan -var project_id=YOUR_PROJECT_ID
```

GCS state locking is built in; no extra lock table is needed.

## Offline checks (what CI runs)

```sh
terraform fmt -check -recursive infra
cd infra/envs/dev && terraform init -backend=false && terraform validate
```

## Concepts and mock vs production

- **Autopilot vs Standard.** Autopilot: Google runs the nodes and you pay per
  pod resource request (so requests are mandatory); no node tuning or SSH.
  Standard: you pay per VM and manage node pools and upgrades. Real VM
  platforms on KVM/vSphere are closer to Standard, since you own the hosts.
- **Remote state and locking.** Here: one GCS bucket, versioned, with the
  backend's built-in lock so concurrent applies fail instead of corrupting state.
- **Separate state per environment.** Here only `dev` exists. Production gives
  each environment its own state (bucket or project) and IAM.
- **CI plan/apply.** Here you run `make gke-up` by hand. Production runs
  `terraform plan` on pull requests and `apply` from CI after review.
- **Workload Identity.** Our pods call no GCP APIs. Production maps a
  Kubernetes ServiceAccount to a Google service account for keyless access.
- **Private clusters.** Ours has a public control-plane endpoint. Production
  uses private nodes, authorized networks and Cloud NAT.
- kind has no cloud networking or registry; images are side-loaded there.
