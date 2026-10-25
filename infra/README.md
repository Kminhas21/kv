# infra: GKE Autopilot for the vmaas simulator

> **WARNING: THIS COSTS REAL MONEY.** An Autopilot cluster bills per pod
> request, plus a regional cluster management fee, load balancers and disks,
> for as long as it exists. **Run `make gke-down` at the end of every
> session**, then check the GCP console that the cluster is gone.

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

gcloud services enable container.googleapis.com \
  artifactregistry.googleapis.com compute.googleapis.com

# Versioned bucket: lets you recover an older state file.
gsutil mb -l us-central1 gs://YOUR_STATE_BUCKET
gsutil versioning set on gs://YOUR_STATE_BUCKET
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
