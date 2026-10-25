#!/usr/bin/env bash
# End-to-end demo of the VM control plane running in the kind cluster.
#
#   make demo                 interactive: pauses before each step
#   DEMO_AUTO=1 make demo     runs straight through (e.g. as a smoke test)
#
# Each step prints what is about to happen and what to look for, then checks
# the result itself and prints PASS or FAIL. Exit code = number of failures.
set -uo pipefail
export MSYS_NO_PATHCONV=1 # Git Bash: don't rewrite /usr/local/bin/... paths

NS=vmaas
RUN=$(date +%H%M%S) # suffix so repeated runs get fresh VM names
FAILURES=0

# vmcli runs inside the cluster (in the mockhv pod, which this demo never
# kills), so killing vmctl or kv cannot break the connection we watch from.
vmcli() {
	kubectl -n "$NS" exec deploy/mockhv -- /usr/local/bin/vmcli \
		-vmctl http://vmctl:8090 -kv http://kv:8080 -hv http://mockhv:8081 "$@"
}

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
say() { printf '   %s\n' "$*"; }
run() {
	printf '\033[36m$ vmcli %s\033[0m\n' "$*"
	vmcli "$@"
}
pause() {
	if [[ "${DEMO_AUTO:-0}" != 1 ]]; then
		read -rp $'\n   [press Enter to continue] '
	fi
}
check() { # check "description" <command...>
	local what=$1
	shift
	if "$@"; then
		printf '   \033[32mPASS\033[0m %s\n' "$what"
	else
		printf '   \033[31mFAIL\033[0m %s\n' "$what"
		FAILURES=$((FAILURES + 1))
	fi
}

# create NAME [extra vmcli flags...] -> sets VM and OP
create() {
	local out
	out=$(vmcli vm create -name "$1" -cpu 2 -mem 2048 "${@:2}" 2>/dev/null)
	echo "$out"
	VM=$(awk '{print $2}' <<<"$out")
	OP=$(sed -E 's/.*operation ([^)]*)\).*/\1/' <<<"$out")
}

hv_count() { vmcli hv vms -external-id "vmaas-$1" 2>/dev/null | grep -c "vmaas-$1"; }
op_field() { vmcli op get "$1" | tr -s ' ' '\n' | sed -n "s/^$2=//p"; }
phase() { vmcli vm get "$1" | awk '/^phase:/{print $2}'; }
pod_of() { kubectl -n "$NS" get pods -l "app=$1" -o jsonpath='{.items[0].metadata.name}'; }
wait_ready() {
	kubectl -n "$NS" rollout status "$1" --timeout=120s >/dev/null &&
		kubectl -n "$NS" wait --for=condition=Ready pod -l "app=$2" --timeout=120s >/dev/null
}

# ---------------------------------------------------------------------------
bold "0. Starting point"
say "kv runs as a StatefulSet (kv-0 + a persistent volume), vmctl and mockhv as"
say "Deployments. vmcli runs inside the cluster and reaches them by Service DNS."
kubectl -n "$NS" get pods,pvc -o wide
run hosts
pause

# ---------------------------------------------------------------------------
bold "1. Request a VM and watch it converge"
say "POST /vms only records desired state and returns 202. The reconciler then"
say "schedules it (first-fit), creates it on the hypervisor, powers it on, and"
say "observes it until desired == observed. Watch the phase move."
create "demo-web-$RUN"
WEB=$VM
vmcli vm watch -until Running -timeout 60s "$WEB" 2>/dev/null
check "VM reached Running" test "$(phase "$WEB")" = Running
check "exactly 1 VM on the hypervisor" test "$(hv_count "$WEB")" -eq 1
pause

# ---------------------------------------------------------------------------
bold "2. Retry the same request (same Idempotency-Key)"
say "A client whose response got lost retries with the same key. The"
say "idempotency record (claimed create-only, If-Match: 0) returns the original"
say "IDs instead of creating a second VM."
create "demo-dup-$RUN" -key "demo-key-$RUN"
FIRST_VM=$VM FIRST_OP=$OP
create "demo-dup-$RUN" -key "demo-key-$RUN"
check "retry returned the same VM ID" test "$VM" = "$FIRST_VM"
check "retry returned the same operation ID" test "$OP" = "$FIRST_OP"
say "Same key, different body (cpu 4 instead of 2) must be refused:"
# Capture first, then match: the command is SUPPOSED to fail here, and with
# pipefail a failing command poisons the whole `cmd | grep` pipeline status.
REJECTION=$(vmcli vm create -name "demo-dup-$RUN" -cpu 4 -mem 2048 -key "demo-key-$RUN" 2>&1)
say "${REJECTION//$'\n'/ }"
check "reused key with a different body rejected (422)" grep -q "422.*\|different request" <<<"$REJECTION"
vmcli vm watch -until Running -timeout 60s "$FIRST_VM" >/dev/null 2>&1
check "exactly 1 VM on the hypervisor for the retried request" test "$(hv_count "$FIRST_VM")" -eq 1
pause

# ---------------------------------------------------------------------------
bold "3. Lost response: the hypervisor creates the VM, but vmctl never hears back"
say "mockhv will do the next create, then hold the HTTP response for 10s while"
say "vmctl gives up after 3s (HV_TIMEOUT). vmctl must mark the operation UNKNOWN,"
say "NOT failed, then look the VM up by external ID and adopt it."
run hv faults -lost-responses 1
create "demo-lost-$RUN"
LOST=$VM
vmcli vm watch -until Running -timeout 60s "$LOST" 2>/dev/null
check "VM reached Running" test "$(phase "$LOST")" = Running
check "exactly 1 VM on the hypervisor (no duplicate)" test "$(hv_count "$LOST")" -eq 1
check "only 1 create attempt was needed" test "$(op_field "$OP" attempts)" = 1
say "What vmctl logged for this VM:"
kubectl -n "$NS" logs deploy/vmctl | grep "$LOST" | grep -E "unknown|adopting" | cut -c21- | sed -E 's/ (external_id|op_id)=[^ ]+//g'
pause

# ---------------------------------------------------------------------------
bold "4. Crash the control plane in the middle of provisioning"
say "We slow the hypervisor down (create takes ~10s), request a VM, wait until a"
say "create task is in flight, then force-kill the vmctl pod (no graceful exit,"
say "like a node crash). Kubernetes starts a replacement pod. Because every step"
say "was saved in kv, the new pod polls the saved task instead of creating again."
run hv faults -create-delay 8s
create "demo-crash-$RUN"
CRASH=$VM CRASH_OP=$OP
for _ in $(seq 1 30); do
	TASK=$(op_field "$CRASH_OP" task)
	[[ -n "$TASK" && "$TASK" != - ]] && break
	sleep 1
done
say "create task in flight: $TASK"
OLD_POD=$(pod_of vmctl)
printf '\033[36m$ kubectl -n %s delete pod %s --grace-period=0 --force\033[0m\n' "$NS" "$OLD_POD"
kubectl -n "$NS" delete pod "$OLD_POD" --grace-period=0 --force 2>/dev/null
wait_ready deployment/vmctl vmctl
NEW_POD=$(pod_of vmctl)
say "old pod: $OLD_POD   new pod: $NEW_POD"
check "Kubernetes replaced the vmctl pod" test "$NEW_POD" != "$OLD_POD"
vmcli vm watch -until Running -timeout 90s "$CRASH" 2>/dev/null
check "VM reached Running after the crash" test "$(phase "$CRASH")" = Running
check "exactly 1 VM on the hypervisor" test "$(hv_count "$CRASH")" -eq 1
check "still only 1 create attempt (the new pod polled the saved task)" test "$(op_field "$CRASH_OP" attempts)" = 1
say "What the NEW vmctl pod logged for this VM (no 'create submitted' line):"
kubectl -n "$NS" logs "$NEW_POD" | grep "$CRASH" | cut -c21- | sed -E 's/ (external_id|op_id)=[^ ]+//g'
run hv faults -create-delay 0s >/dev/null
pause

# ---------------------------------------------------------------------------
bold "5. Crash the database (kv-0)"
say "kv is a StatefulSet: the replacement pod gets the SAME name (kv-0) and the"
say "SAME PersistentVolumeClaim, so the WAL is replayed and nothing is lost."
BEFORE=$(vmcli vm list | grep -c "demo-")
PV_BEFORE=$(kubectl -n "$NS" get pvc data-kv-0 -o jsonpath='{.spec.volumeName}')
UID_BEFORE=$(kubectl -n "$NS" get pod kv-0 -o jsonpath='{.metadata.uid}')
printf '\033[36m$ kubectl -n %s delete pod kv-0 --grace-period=0 --force\033[0m\n' "$NS"
kubectl -n "$NS" delete pod kv-0 --grace-period=0 --force 2>/dev/null
wait_ready statefulset/kv kv
UID_AFTER=$(kubectl -n "$NS" get pod kv-0 -o jsonpath='{.metadata.uid}')
PV_AFTER=$(kubectl -n "$NS" get pvc data-kv-0 -o jsonpath='{.spec.volumeName}')
say "kv-0 pod uid: $UID_BEFORE -> $UID_AFTER"
say "volume:       $PV_BEFORE -> $PV_AFTER"
kubectl -n "$NS" logs kv-0 | grep "WAL replayed" | cut -c21-
check "a brand new kv-0 pod was created" test "$UID_AFTER" != "$UID_BEFORE"
check "it reattached the same volume" test "$PV_AFTER" = "$PV_BEFORE"
check "every VM record survived ($BEFORE demo VMs)" test "$(vmcli vm list | grep -c "demo-")" -eq "$BEFORE"
run vm list
run hosts
pause

# ---------------------------------------------------------------------------
bold "6. Timed self-heal: crash and hang kv-0"
say "The kind overlay sets FAULTS=1, so kv has POST /debug/crash (process exits)"
say "and POST /debug/hang (every handler, /healthz included, blocks forever)."
say "The image has no shell, so we trigger them through a temporary port-forward."
say "Probes: every 2s, 2 failures, 1s timeout. Crash: kubelet sees the exit at once."
say "Hang: liveness needs 2 failed probes (~4-5s), then SIGTERM. The hung handlers"
say "wait on their request context, which the timed-out probe connections cancel,"
say "so the drain finishes fast; then restart, startup and readiness probes."
now() { date +%s.%N; }
# fault_recovery ENDPOINT -> sets RECOVERY (seconds) once kv-0 is Ready with restartCount+1
fault_recovery() {
	local before pf t0 t1 st
	before=$(kubectl -n "$NS" get pod kv-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')
	kubectl -n "$NS" port-forward pod/kv-0 18080:8080 >/dev/null 2>&1 &
	pf=$!
	for _ in $(seq 1 50); do curl -s -o /dev/null -m 1 localhost:18080/healthz && break; sleep 0.2; done
	t0=$(now)
	curl -s -o /dev/null -m 2 -X POST "localhost:18080/debug/$1"
	kill "$pf" 2>/dev/null
	for _ in $(seq 1 600); do
		st=$(kubectl -n "$NS" get pod kv-0 -o jsonpath='{.status.containerStatuses[0].restartCount} {.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
		[[ "$st" == "$((before + 1)) True" ]] && break
		sleep 0.2
	done
	t1=$(now)
	RESTARTS="${st% *}"
	RECOVERY=$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.1f", b-a}')
	say "$1: restartCount $before -> $RESTARTS, Ready again after ${RECOVERY}s"
}
under10() { awk -v d="$1" 'BEGIN{exit !(d <= 10)}'; }
fault_recovery crash
CRASH_S=$RECOVERY
check "kv-0 restarted and became Ready after crash" test "$RESTARTS" -ge 1
check "crash recovery ${CRASH_S}s <= 10s" under10 "$CRASH_S"
wait_ready statefulset/kv kv
fault_recovery hang
HANG_S=$RECOVERY
check "kv-0 restarted and became Ready after hang" test "$RESTARTS" -ge 1
check "hang recovery ${HANG_S}s <= 10s" under10 "$HANG_S"
wait_ready statefulset/kv kv
say "Summary: crash ${CRASH_S}s, hang ${HANG_S}s"
check "VM records survived both restarts" test "$(vmcli vm list | grep -c "demo-")" -eq "$BEFORE"
pause

# ---------------------------------------------------------------------------
bold "Done"
if [[ $FAILURES -eq 0 ]]; then
	printf '   \033[32mAll checks passed.\033[0m\n'
else
	printf '   \033[31m%d check(s) failed.\033[0m\n' "$FAILURES"
fi
exit "$FAILURES"
