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
# make gke-up leaves kubectl pointed at GKE; never run the kill/restart steps there.
if [[ "$(kubectl config current-context)" != kind-vmaas ]]; then
	echo "refusing to run: kubectl context is $(kubectl config current-context), want kind-vmaas" >&2
	exit 1
fi
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
# The vmctl leader: the pod name stored in the lease key (empty if none).
leader() { vmcli kv get leases/vmctl 2>/dev/null; }
# wait_new_leader OLD TIMEOUT_SECONDS -> sets NEW_LEADER once someone else holds the lease
wait_new_leader() {
	local i
	NEW_LEADER=
	for ((i = 0; i < $2 * 2; i++)); do
		NEW_LEADER=$(leader)
		[[ -n "$NEW_LEADER" && "$NEW_LEADER" != "$1" ]] && return 0
		sleep 0.5
	done
	return 1
}
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
kubectl -n "$NS" logs "$(leader)" | grep "$LOST" | grep -E "unknown|adopting" | cut -c21- | sed -E 's/ (external_id|op_id)=[^ ]+//g'
pause

# ---------------------------------------------------------------------------
bold "4. Crash the control plane in the middle of provisioning"
say "We slow the hypervisor down (create takes ~10s), request a VM, wait until a"
say "create task is in flight, then SIGKILL the vmctl LEADER's process on its node"
say "(a real crash: no SIGTERM, so it never releases its lease; kubectl's force-"
say "delete would still send SIGTERM). The lease must expire first (LEASE_TTL 10s),"
say "then a replica takes it: another pod, or the restarted one. Because every step"
say "was saved in kv, the new leader polls the saved task instead of creating again."
run hv faults -create-delay 8s
create "demo-crash-$RUN"
CRASH=$VM CRASH_OP=$OP
for _ in $(seq 1 30); do
	TASK=$(op_field "$CRASH_OP" task)
	[[ -n "$TASK" && "$TASK" != - ]] && break
	sleep 1
done
say "create task in flight: $TASK"
OLD_POD=$(leader)
NODE=$(kubectl -n "$NS" get pod "$OLD_POD" -o jsonpath='{.spec.nodeName}')
CID=$(kubectl -n "$NS" get pod "$OLD_POD" -o jsonpath='{.status.containerStatuses[0].containerID}')
PID=$(docker exec "$NODE" crictl inspect --output go-template --template '{{.info.pid}}' "${CID#containerd://}")
printf '\033[36m$ docker exec %s kill -9 %s   # vmctl in %s\033[0m\n' "$NODE" "$PID" "$OLD_POD"
docker exec "$NODE" kill -9 "$PID"
START=$SECONDS
NEW_POD=
for _ in $(seq 1 120); do # first "became leader" logged by any replica since the kill
	LINE=$(kubectl -n "$NS" logs -l app=vmctl --prefix --max-log-requests 5 --since="$((SECONDS - START + 1))s" 2>/dev/null | grep -m1 "became leader")
	[[ -n "$LINE" ]] && NEW_POD=$(sed -E 's#^\[pod/([^/]+)/.*#\1#' <<<"$LINE") && break
	sleep 0.5
done
TOOK=$((SECONDS - START))
say "old leader: $OLD_POD   new leader: $NEW_POD (after ~${TOOK}s)"
check "a replica took the lease after the crash" test -n "$NEW_POD"
check "it had to wait for the TTL (a crashed leader releases nothing)" test "$TOOK" -ge 5
wait_ready deployment/vmctl vmctl
# vmcli's watch exits on the first connection error (vmctl just restarted), so
# poll the phase ourselves instead of trusting a single watch.
wait_phase() { # wait_phase VM PHASE TIMEOUT_SECONDS
	local i
	for ((i = 0; i < $3; i += 2)); do
		[[ "$(phase "$1" 2>/dev/null)" == "$2" ]] && return 0
		sleep 2
	done
	return 1
}
vmcli vm watch -until Running -timeout 90s "$CRASH" 2>/dev/null
check "VM reached Running after the crash" wait_phase "$CRASH" Running 90
check "exactly 1 VM on the hypervisor" test "$(hv_count "$CRASH")" -eq 1
check "still only 1 create attempt (the new leader polled the saved task)" test "$(op_field "$CRASH_OP" attempts)" = 1
say "What the NEW leader logged for this VM (no 'create submitted' line):"
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
PF_PID=
# Git Bash's kill may not reach the native kubectl.exe, so also taskkill its Windows pid.
stop_pf() {
	[[ -n "$PF_PID" ]] || return 0
	local win
	win=$(cat "/proc/$PF_PID/winpid" 2>/dev/null)
	kill "$PF_PID" 2>/dev/null
	[[ -n "$win" ]] && taskkill //F //PID "$win" >/dev/null 2>&1
	PF_PID=
	return 0
}
trap stop_pf EXIT
# fault_recovery ENDPOINT -> sets RECOVERY (seconds) and RESTARTS once kv-0 is Ready with restartCount+1.
# If the trigger never reached kv it says why and leaves RESTARTS=0, RECOVERY=999 so the checks fail.
fault_recovery() {
	local before t0 t1 st rc up=
	RESTARTS=0 RECOVERY=999 RESTARTS_WANT=-1
	before=$(kubectl -n "$NS" get pod kv-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')
	RESTARTS_WANT=$((before + 1))
	kubectl -n "$NS" port-forward pod/kv-0 18080:8080 >/dev/null 2>&1 &
	PF_PID=$!
	# Redirect, never curl -o /dev/null: with MSYS_NO_PATHCONV=1 the native curl.exe
	# cannot open /dev/null and exits 23 even though the request succeeded.
	for _ in $(seq 1 50); do
		if curl -s -m 1 localhost:18080/healthz >/dev/null; then up=1; break; fi
		sleep 0.2
	done
	if [[ -z "$up" ]]; then
		say "FAULT NOT TRIGGERED: port-forward to kv-0 never came up"
		stop_pf
		return 0
	fi
	t0=$(now)
	curl -sf -m 2 -X POST "localhost:18080/debug/$1" >/dev/null
	rc=$?
	stop_pf
	# crash kills the process mid-request (curl 52 empty reply / 56 reset is expected);
	# hang answers 200 first, so only 0 is acceptable there.
	if [[ $rc -ne 0 ]] && ! [[ "$1" == crash && ($rc -eq 52 || $rc -eq 56) ]]; then
		say "FAULT NOT TRIGGERED: POST /debug/$1 failed (curl exit $rc); is FAULTS=1 set?"
		return 0
	fi
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
check "kv-0 restarted and became Ready after crash" test "$RESTARTS" -eq "$RESTARTS_WANT"
check "crash recovery ${CRASH_S}s <= 10s" under10 "$CRASH_S"
wait_ready statefulset/kv kv
# A container's 2nd restart within 10 minutes waits out CrashLoopBackOff (10s),
# which would be timed as part of the hang. A fresh pod resets the backoff.
say "Recreating kv-0 so the hang is timed without the kubelet's restart backoff."
kubectl -n "$NS" delete pod kv-0 >/dev/null
wait_ready statefulset/kv kv
fault_recovery hang
HANG_S=$RECOVERY
check "kv-0 restarted and became Ready after hang" test "$RESTARTS" -eq "$RESTARTS_WANT"
check "hang recovery ${HANG_S}s <= 10s" under10 "$HANG_S"
wait_ready statefulset/kv kv
say "Summary: crash ${CRASH_S}s, hang ${HANG_S}s"
check "VM records survived both restarts" test "$(vmcli vm list | grep -c "demo-")" -eq "$BEFORE"
pause

# ---------------------------------------------------------------------------
bold "7. Kill the leader mid-provisioning"
say "vmctl runs 3 replicas. All serve the API; only the leader, the pod named in"
say "the kv key leases/vmctl, runs the reconciler. We start a slow create, delete"
say "the leader pod, and watch another replica take the lease and finish the VM."
say "A graceful delete makes the leader release the lease on its way out, so the"
say "handoff is quick; a crash would mean waiting out the 10s LEASE_TTL instead."
run hv faults -create-delay 8s
create "demo-leader-$RUN"
LVM=$VM LOP=$OP
for _ in $(seq 1 30); do
	TASK=$(op_field "$LOP" task)
	[[ -n "$TASK" && "$TASK" != - ]] && break
	sleep 1
done
say "create task in flight: $TASK"
OLD_LEADER=$(leader)
say "leader: $OLD_LEADER"
printf '\033[36m$ kubectl -n %s delete pod %s\033[0m\n' "$NS" "$OLD_LEADER"
kubectl -n "$NS" delete pod "$OLD_LEADER" --wait=false >/dev/null
START=$SECONDS
wait_new_leader "$OLD_LEADER" 30
say "new leader: $NEW_LEADER (after ~$((SECONDS - START))s)"
check "another replica became leader" test -n "$NEW_LEADER" -a "$NEW_LEADER" != "$OLD_LEADER"
check "VM reached Running under the new leader" wait_phase "$LVM" Running 90
check "exactly 1 VM on the hypervisor" test "$(hv_count "$LVM")" -eq 1
check "still only 1 create attempt" test "$(op_field "$LOP" attempts)" = 1
say "Lease log lines from the old and new leader:"
for p in "$OLD_LEADER" "$NEW_LEADER"; do
	kubectl -n "$NS" logs "$p" 2>/dev/null | grep -E "leader|leadership" | cut -c21- | sed "s/^/[$p] /"
done
run hv faults -create-delay 0s >/dev/null
pause

# ---------------------------------------------------------------------------
bold "Done"
if [[ $FAILURES -eq 0 ]]; then
	printf '   \033[32mAll checks passed.\033[0m\n'
else
	printf '   \033[31m%d check(s) failed.\033[0m\n' "$FAILURES"
fi
exit "$FAILURES"
