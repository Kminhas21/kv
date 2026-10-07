# Local Kubernetes workflow. Needs docker, kind, kubectl (and Git Bash on Windows).
CLUSTER := vmaas
IMAGE   := vmaas-sim:dev
NS      := vmaas
# Git Bash rewrites /unix/paths in arguments; this turns that off for docker/kubectl.
export MSYS_NO_PATHCONV := 1

.PHONY: test kind-up kind-down image load deploy redeploy up status logs forward wait demo

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
	kubectl apply -k k8s/

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
