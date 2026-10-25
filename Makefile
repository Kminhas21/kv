# Local Kubernetes workflow. Needs docker, kind, kubectl (and Git Bash on Windows).
CLUSTER := vmaas
IMAGE   := vmaas-sim:dev
NS      := vmaas
# Git Bash rewrites /unix/paths in arguments; this turns that off for docker/kubectl.
export MSYS_NO_PATHCONV := 1

.PHONY: test bench kind-up kind-down image load deploy redeploy up status logs forward wait demo gke-check gke-up gke-down

test:
	go vet ./...
	go test ./...

# In-process benchmarks; the replay one builds a 1M-record WAL (takes a while).
bench:
	KV_BENCH_REPLAY_N=1000000 go test -run '^$$' -bench Replay -benchtime 3x -timeout 30m ./internal/kv
	go test -run '^$$' -bench 'Get|Put' -count 3 ./internal/kv

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
	kubectl -n $(NS) logs -f -l app=vmctl --prefix --max-log-requests 5

# Makes vmctl, kv and mockhv reachable on localhost for vmcli, plus Grafana
# (:3000) and Prometheus (:9090). Runs until Ctrl+C.
forward:
	kubectl -n $(NS) port-forward svc/vmctl 8090:8090 & \
	kubectl -n $(NS) port-forward svc/kv 8080:8080 & \
	kubectl -n $(NS) port-forward svc/mockhv 8081:8081 & \
	kubectl -n $(NS) port-forward svc/grafana 3000:3000 & \
	kubectl -n $(NS) port-forward svc/prometheus 9090:9090 & \
	echo "Grafana http://localhost:3000 (anonymous viewer)  Prometheus http://localhost:9090"; \
	wait

# ---- GKE Autopilot (COSTS MONEY: always run gke-down when done) ----
# Usage: make gke-up PROJECT_ID=my-project TF_STATE_BUCKET=my-state-bucket
REGION          ?= us-central1
PROJECT_ID      ?=
TF_STATE_BUCKET ?=
# Unique per run: changed (even uncommitted) source gets a new tag, so pods roll.
TAG             := $(shell git rev-parse --short HEAD)-$(shell date +%s)
GKE_REPO        := $(REGION)-docker.pkg.dev/$(PROJECT_ID)/vmaas/vmaas-sim
TF_DIR          := infra/envs/dev
TF_INIT         := terraform -chdir=$(TF_DIR) init -input=false -backend-config="bucket=$(TF_STATE_BUCKET)"
TF_VARS         := -var project_id=$(PROJECT_ID) -var region=$(REGION)

gke-check:
	@test -n "$(PROJECT_ID)" -a -n "$(TF_STATE_BUCKET)" || { echo "set PROJECT_ID and TF_STATE_BUCKET"; exit 1; }

# The overlay holds __GKE_IMAGE__/__GKE_TAG__ tokens; sed fills them in.
gke-up: gke-check
	@start=$$(date +%s); set -e; \
	$(TF_INIT); \
	terraform -chdir=$(TF_DIR) apply -input=false -auto-approve $(TF_VARS); \
	gcloud auth configure-docker $(REGION)-docker.pkg.dev --quiet; \
	docker build --platform linux/amd64 -t $(GKE_REPO):$(TAG) .; \
	docker push $(GKE_REPO):$(TAG); \
	gcloud container clusters get-credentials vmaas --region $(REGION) --project $(PROJECT_ID); \
	kubectl kustomize k8s/overlays/gke | sed -e 's#__GKE_IMAGE__#$(GKE_REPO)#g' -e 's#__GKE_TAG__#$(TAG)#g' | kubectl apply -f -; \
	$(MAKE) wait; \
	echo "gke-up took $$(( $$(date +%s) - start ))s"

gke-down: gke-check
	$(TF_INIT)
	terraform -chdir=$(TF_DIR) destroy -input=false -auto-approve $(TF_VARS)
