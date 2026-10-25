# Local Kubernetes workflow. Needs docker, kind, kubectl (and Git Bash on Windows).
CLUSTER := vmaas
IMAGE   := vmaas-sim:dev
NS      := vmaas
# Git Bash rewrites /unix/paths in arguments; this turns that off for docker/kubectl.
export MSYS_NO_PATHCONV := 1

.PHONY: test kind-up kind-down image load deploy redeploy up status logs forward wait demo gke-check gke-up gke-down

test:
	go vet ./...
	go test ./...

kind-up:
	kind get clusters | grep -qx $(CLUSTER) || kind create cluster --config k8s/kind-config.yaml

kind-down:
	kind delete cluster --name $(CLUSTER)

image:
	docker build -t $(IMAGE) .

# kind nodes cannot see your local Docker images; this copies the image into
# every node's container runtime (no registry needed).
load: image
	kind load docker-image $(IMAGE) --name $(CLUSTER)

deploy:
	kubectl apply -k k8s/overlays/kind

# After a code change: rebuild, load, and restart the pods so they run it.
# (The tag stays "dev", so Kubernetes would not notice a new image by itself.)
redeploy: load
	kubectl -n $(NS) rollout restart statefulset/kv deployment/mockhv deployment/vmctl
	$(MAKE) wait

wait:
	kubectl -n $(NS) rollout status statefulset/kv --timeout=120s
	kubectl -n $(NS) rollout status deployment/mockhv --timeout=120s
	kubectl -n $(NS) rollout status deployment/vmctl --timeout=120s

# Everything from scratch: cluster, image, deploy, wait until Ready.
up: kind-up load deploy wait status

status:
	kubectl -n $(NS) get pods,svc,pvc -o wide

# Interactive walkthrough with self-checks; DEMO_AUTO=1 make demo runs unattended.
demo:
	bash scripts/demo.sh

logs:
	kubectl -n $(NS) logs -f deployment/vmctl

# Makes vmctl, kv and mockhv reachable on localhost for vmcli. Runs until Ctrl+C.
forward:
	kubectl -n $(NS) port-forward svc/vmctl 8090:8090 & \
	kubectl -n $(NS) port-forward svc/kv 8080:8080 & \
	kubectl -n $(NS) port-forward svc/mockhv 8081:8081 & \
	wait

# ---- GKE Autopilot (COSTS MONEY: always run gke-down when done) ----
# Usage: make gke-up PROJECT_ID=my-project TF_STATE_BUCKET=my-state-bucket
REGION          ?= us-central1
PROJECT_ID      ?=
TF_STATE_BUCKET ?=
TAG             := $(shell git rev-parse --short HEAD)
GKE_IMAGE       := $(REGION)-docker.pkg.dev/$(PROJECT_ID)/vmaas/vmaas-sim:$(TAG)
TF_DIR          := infra/envs/dev
TF_INIT         := terraform -chdir=$(TF_DIR) init -input=false -backend-config="bucket=$(TF_STATE_BUCKET)"
TF_VARS         := -var project_id=$(PROJECT_ID) -var region=$(REGION)

gke-check:
	@test -n "$(PROJECT_ID)" -a -n "$(TF_STATE_BUCKET)" || { echo "set PROJECT_ID and TF_STATE_BUCKET"; exit 1; }

# The overlay holds REGION/PROJECT_ID/IMAGE_TAG placeholders; sed fills them in.
gke-up: gke-check
	@start=$$(date +%s); set -e; \
	$(TF_INIT); \
	terraform -chdir=$(TF_DIR) apply -input=false -auto-approve $(TF_VARS); \
	gcloud auth configure-docker $(REGION)-docker.pkg.dev --quiet; \
	docker build -t $(GKE_IMAGE) .; \
	docker push $(GKE_IMAGE); \
	gcloud container clusters get-credentials vmaas --region $(REGION) --project $(PROJECT_ID); \
	kubectl kustomize k8s/overlays/gke | sed -e 's/REGION/$(REGION)/' -e 's/PROJECT_ID/$(PROJECT_ID)/' -e 's/IMAGE_TAG/$(TAG)/' | kubectl apply -f -; \
	$(MAKE) wait; \
	echo "gke-up took $$(( $$(date +%s) - start ))s"

gke-down: gke-check
	$(TF_INIT)
	terraform -chdir=$(TF_DIR) destroy -input=false -auto-approve $(TF_VARS)
