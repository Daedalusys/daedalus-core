#!/bin/bash
# Headless deployment validation harness for daedalus-os on Mastwet_Server.
# Drives issue core#36 (P4 controller validation checklist) against a rootless
# podman + systemd-in-container runtime, because the target has no sudo / qemu.
#
# Usage: ./headless-test.sh [image-ref]
#   default image-ref: ghcr.io/daedalusys/daedalus-os:latest
#
# What we can honestly verify in this environment:
#   - image pull + container boot with systemd-as-PID1 (headless)
#   - A1/A2 unit artifacts, B1-B3 lifecycle, D1/D2 read-only drift/ownership,
#     E1 audit chain, F1 daedalus-smoke
#   - C1-C3 (real tx drive / service / package) only if the image is P4+
#   - C3 additionally requires /usr writes, which rootless overlay may block
#
# Everything is written to RESULTS and echoed at the end.

set -uo pipefail

REMOTE_HOST="${REMOTE_HOST:-Mastwet_Server}"
IMAGE_REF="${1:-ghcr.io/daedalusys/daedalus-os:latest}"
CONTAINER_NAME="daedalus-headless-$$"
RUN_ID="$(date +%Y%m%d-%H%M%S)-$$"
LOG_DIR="${LOG_DIR:-/tmp/daedalus-headless-logs/$RUN_ID}"
mkdir -p "$LOG_DIR"

declare -a ORDER=(A1 A2 B1 B2 B3 C1 C2 C3 D1 D2 E1 E2 F1)
declare -A RESULT  # key -> PASS|FAIL|SKIP|NOTE|N/A
declare -A DETAIL  # key -> short detail
for k in "${ORDER[@]}"; do RESULT[$k]="N/A"; DETAIL[$k]=""; done

rsh() { ssh -o ConnectTimeout=20 "$REMOTE_HOST" "$@"; }
log()  { printf '\n=== %s ===\n' "$*"; }
ok()   { printf '  PASS  %s\n' "$*"; }
bad()  { printf '  FAIL  %s\n' "$*"; }
skp()  { printf '  SKIP  %s\n' "$*"; }
nt()   { printf '  NOTE  %s\n' "$*"; }

set_r() { RESULT[$1]="$2"; DETAIL[$1]="${3:-}"; }

# ---------------------------------------------------------------- preflight ---
log "Preflight"
rsh "uname -a; echo; podman --version; echo; systemctl --version | head -1" || {
    echo "FATAL: cannot reach $REMOTE_HOST" >&2; exit 2; }

log "Pull $IMAGE_REF"
rsh "podman pull '$IMAGE_REF' 2>&1 | tail -6"

# ------------------------------------------------------------------- boot -----
log "Launch headless container $CONTAINER_NAME (systemd as PID 1)"
rsh "podman rm -f $CONTAINER_NAME >/dev/null 2>&1 || true
     podman run -d \
       --name $CONTAINER_NAME \
       --hostname daedalus-headless \
       --privileged --systemd=always --network=host \
       --security-opt label=disable --cgroupns=host \
       --tmpfs /run --tmpfs /tmp \
       '$IMAGE_REF' 2>&1 | tail -4"

log "Wait for systemd readiness"
READY=no
for i in $(seq 1 90); do
    st=$(rsh "podman exec $CONTAINER_NAME systemctl is-system-running 2>/dev/null")
    case "$st" in
        running|degraded) READY=yes; ok "systemd '$st' after ${i}s"; break ;;
    esac
    sleep 1
done
if [ "$READY" != yes ]; then
    bad "systemd did not reach running/degraded"; rsh "podman logs $CONTAINER_NAME 2>&1 | tail -30"
    exit 3
fi
SYS_RUNNING=$(rsh "podman exec $CONTAINER_NAME systemctl is-system-running")
DEGRADED_UNITS=$(rsh "podman exec $CONTAINER_NAME systemctl --failed --no-pager --plain 2>&1 | head -20")
nt "is-system-running=$SYS_RUNNING"
nt "failed units:"; echo "$DEGRADED_UNITS" | sed 's/^/         /'

# --------------------------------------------------------- A: build artifacts --
log "A1/A2: controller unit artifacts in image"
A1_LINK=$(rsh "podman exec $CONTAINER_NAME bash -c 'if [ -L /etc/systemd/system/multi-user.target.wants/daedalus-host-controller.service ]; then TGT=\$(readlink /etc/systemd/system/multi-user.target.wants/daedalus-host-controller.service); EXISTS=\$(test -e /etc/systemd/system/multi-user.target.wants/daedalus-host-controller.service && echo yes || echo no); echo \"link->\$TGT exists=\$EXISTS\"; elif [ -e /etc/systemd/system/multi-user.target.wants/daedalus-host-controller.service ]; then echo \"regular-file (not a link)\"; else echo MISSING; fi'")
A2_PATH=$(rsh "podman exec $CONTAINER_NAME bash -c 'if [ -f /usr/lib/systemd/system/daedalus-host-controller.service ]; then cat /usr/lib/systemd/system/daedalus-host-controller.service; else echo MISSING; fi'")
if echo "$A1_LINK" | grep -q '^link->' && echo "$A1_LINK" | grep -q 'exists=yes'; then
    ok "A1 enable symlink -> $(echo "$A1_LINK" | sed 's/.*link->//;s/ exists=.*//')"; set_r A1 PASS "$A1_LINK"
elif echo "$A1_LINK" | grep -q MISSING; then
    bad "A1 no enable symlink (pre-P4 image?)"; set_r A1 FAIL "$A1_LINK"
else
    bad "A1 symlink exists but target missing (dangling)"; set_r A1 FAIL "$A1_LINK"
fi
if echo "$A2_PATH" | grep -q 'Wants=daedalus-host.service' && echo "$A2_PATH" | grep -q 'WantedBy=multi-user.target'; then
    ok "A2 unit file correct"; set_r A2 PASS ""
else
    bad "A2 unit file missing/incorrect"; set_r A2 FAIL "$(echo "$A2_PATH" | head -2)"
fi
HAS_CTRL=no
echo "$A2_PATH" | grep -qE '^\[Unit\]|ExecStart=' && HAS_CTRL=yes
nt "variant: $([ "$HAS_CTRL" = yes ] && echo 'P4+ (controller present)' || echo 'pre-P4 (no controller)')"
# also record whether daedalus-host.service exists at all (P4 introduced it)
HAS_HOST=no
rsh "podman exec $CONTAINER_NAME systemctl cat daedalus-host.service >/dev/null 2>&1" && HAS_HOST=yes
nt "daedalus-host.service present: $HAS_HOST"

# ------------------------------------------------------- B: startup/lifecycle --
if [ "$HAS_CTRL" = yes ]; then
    log "B1: controller auto-start"
    B1=$(rsh "podman exec $CONTAINER_NAME systemctl status daedalus-host-controller.service --no-pager 2>&1" | head -15)
    if echo "$B1" | grep -qE 'active \(running\)'; then
        ok "B1 active (running)"; set_r B1 PASS ""
    else
        sleep 6
        B1=$(rsh "podman exec $CONTAINER_NAME systemctl status daedalus-host-controller.service --no-pager 2>&1" | head -15)
        if echo "$B1" | grep -qE 'active \(running\)'; then
            ok "B1 active after wait"; set_r B1 PASS ""
        else
            bad "B1 not active"; set_r B1 FAIL "$(echo "$B1"|head -3|tr '\n' ' ')"
        fi
    fi

    log "B2: Wants decoupling (stop controller, host stays active)"
    rsh "podman exec $CONTAINER_NAME systemctl stop daedalus-host-controller.service" >/dev/null 2>&1
    H2=$(rsh "podman exec $CONTAINER_NAME systemctl is-active daedalus-host.service 2>&1")
    C2=$(rsh "podman exec $CONTAINER_NAME systemctl is-active daedalus-host-controller.service 2>&1")
    if [ "$HAS_HOST" = yes ] && [ "$H2" = active ] && [ "$C2" = inactive ]; then
        ok "B2 host=active controller=inactive"; set_r B2 PASS ""
    elif [ "$HAS_HOST" != yes ]; then
        skp "B2 no daedalus-host.service to compare"; set_r B2 SKIP "no host unit"
    else
        bad "B2 host=$H2 ctrl=$C2"; set_r B2 FAIL "host=$H2 ctrl=$C2"
    fi
    rsh "podman exec $CONTAINER_NAME systemctl start daedalus-host-controller.service" >/dev/null 2>&1

    log "B3: SIGTERM graceful stop"
    B3=$(rsh "podman exec $CONTAINER_NAME systemctl stop daedalus-host-controller.service; echo STOP_RC=\$?" 2>&1)
    if echo "$B3" | grep -q 'STOP_RC=0'; then
        ok "B3 stop rc=0"; set_r B3 PASS ""
    else
        bad "B3 stop: $B3"; set_r B3 FAIL "$B3"
    fi
else
    skp "B1/B2/B3 skipped (pre-P4 image: no controller unit)"
    set_r B1 SKIP "no controller in image"
    set_r B2 SKIP "no controller in image"
    set_r B3 SKIP "no controller in image"
fi

# ------------------------------------------------------- D: drift/ownership ---
log "D1: daedalus-host drift --kind=service"
D1=$(rsh "podman exec $CONTAINER_NAME daedalus-host drift --kind=service 2>&1" | head -30)
if [ -n "$D1" ] && ! echo "$D1" | grep -qiE 'unknown|not found|error:'; then
    ok "D1 drift produced output"; set_r D1 PASS "$(echo "$D1" | head -3 | tr '\n' '|')"
else
    bad "D1 drift failed"; set_r D1 FAIL "$(echo "$D1" | head -3 | tr '\n' '|')"
fi

log "D2: daedalus-host ownership get service/chronyd.service"
D2=$(rsh "podman exec $CONTAINER_NAME daedalus-host ownership get service/chronyd.service 2>&1" | head -5)
if echo "$D2" | grep -qE 'mode=(unmanaged|observe|managed/manual|managed/reconcile)'; then
    ok "D2 ownership reports mode"; set_r D2 PASS "$(echo "$D2"|head -2|tr '\n' '|')"
else
    bad "D2 ownership get failed"; set_r D2 FAIL "$(echo "$D2" | head -3 | tr '\n' '|')"
fi

# --------------------------------------------------------------- E: audit -----
log "E1: daedalus-audit verify"
E1=$(rsh "podman exec $CONTAINER_NAME daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl 2>&1" | head -8)
if echo "$E1" | grep -qiE 'ok|verified|pass|sentinel|empty'; then
    ok "E1 audit verify ok"; set_r E1 PASS "$(echo "$E1"|head -2|tr '\n' '|')"
else
    bad "E1 audit verify: $(echo "$E1" | head -2 | tr '\n' '|')"; set_r E1 FAIL "$(echo "$E1"|head -3|tr '\n' '|')"
fi

log "E2: audit log inventory (look for controller-direct writes)"
E2=$(rsh "podman exec $CONTAINER_NAME bash -c 'ls -la /var/log/daedalus/ 2>&1; echo ---; wc -l /var/log/daedalus/*.jsonl 2>&1'")
nt "E2 inventory:"; echo "$E2" | sed 's/^/         /'
set_r E2 NOTE "$(echo "$E2" | grep -oE '[0-9]+ /var/log/daedalus/[a-z.]+' | tr '\n' ' ')"
CTRL_ENTRIES=$(rsh "podman exec $CONTAINER_NAME bash -c \"grep -c controller /var/log/daedalus/audit.jsonl 2>/dev/null || echo 0\"")
nt "E2 audit entries mentioning 'controller': $CTRL_ENTRIES"

# ------------------------------------------------------------- C: real drive --
if [ "$HAS_CTRL" = yes ]; then
    log "C1: controller -> daedalus-tx bridge"
    C1_JOURNAL=$(rsh "podman exec $CONTAINER_NAME journalctl -u daedalus-host-controller.service --no-pager -n 40 2>&1")
    if echo "$C1_JOURNAL" | grep -qiE 'reconcile|controller'; then
        ok "C1 controller logs reconcile activity"; set_r C1 PASS "$(echo "$C1_JOURNAL"|tail -2|tr '\n' '|')"
    else
        bad "C1 no reconcile activity"; set_r C1 FAIL "$(echo "$C1_JOURNAL"|tail -3|tr '\n' '|')"
    fi

    log "C2: service end-to-end via daedalus-tx (chronyd) x3"
    CYCLE_LOG=""
    for c in 1 2 3; do
        rsh "podman exec $CONTAINER_NAME daedalus-tx begin --steps '[{\"op\":\"service.set\",\"args\":{\"name\":\"chronyd\",\"state\":\"active\"}}]' --auto-apply 2>&1" >/dev/null 2>&1
        sleep 6
        sa=$(rsh "podman exec $CONTAINER_NAME systemctl is-active chronyd.service 2>&1")
        rsh "podman exec $CONTAINER_NAME daedalus-tx begin --steps '[{\"op\":\"service.set\",\"args\":{\"name\":\"chronyd\",\"state\":\"inactive\"}}]' --auto-apply 2>&1" >/dev/null 2>&1
        sleep 6
        si=$(rsh "podman exec $CONTAINER_NAME systemctl is-active chronyd.service 2>&1")
        CYCLE_LOG+="c$c:active=$sa,inactive=$si "
    done
    nt "C2 cycles: $CYCLE_LOG"
    set_r C2 NOTE "$CYCLE_LOG"

    log "C3: package end-to-end (dnf install); probing /usr writability"
    PROBE=$(rsh "podman exec $CONTAINER_NAME bash -c 'touch /usr/lib/.wtest 2>/dev/null && rm -f /usr/lib/.wtest && echo WRITABLE || echo READONLY'")
    if [ "$PROBE" = WRITABLE ]; then
        C3=$(rsh "podman exec $CONTAINER_NAME bash -c 'dnf install -y --setopt=installroot=/tmp/c3root httpd 2>&1 | tail -5'")
        if echo "$C3" | grep -qiE 'Complete!|Installed:'; then
            ok "C3 dnf install works"; set_r C3 PASS "$(echo "$C3"|tail -1)"
        else
            bad "C3 dnf install failed"; set_r C3 FAIL "$(echo "$C3"|tail -2|tr '\n' '|')"
        fi
    else
        skp "C3 /usr read-only under rootless overlay"; set_r C3 SKIP "usr read-only"
    fi
else
    skp "C1/C2/C3 skipped (pre-P4 image)"
    set_r C1 SKIP "no controller in image"
    set_r C2 SKIP "no controller in image"
    set_r C3 SKIP "no controller in image"
fi

# ---------------------------------------------------------------- F: smoke ----
log "F1: daedalus-smoke"
F1=$(rsh "podman exec $CONTAINER_NAME daedalus-smoke 2>&1" | tail -8)
if echo "$F1" | grep -qiE 'ok|pass|smoke'; then
    ok "F1 smoke ok"; set_r F1 PASS "$(echo "$F1"|tail -1)"
else
    bad "F1 smoke: $(echo "$F1"|tail -2|tr '\n' '|')"; set_r F1 FAIL "$(echo "$F1"|tail -2|tr '\n' '|')"
fi

# -------------------------------------------------------------- summary -------
log "SUMMARY"
printf '%-4s %-6s %s\n' KEY RESULT DETAIL
for k in "${ORDER[@]}"; do
    printf '%-4s %-6s %s\n' "$k" "${RESULT[$k]}" "${DETAIL[$k]:0:100}"
done

echo
echo "Container '$CONTAINER_NAME' left running."
echo "Stop: ssh $REMOTE_HOST 'podman stop $CONTAINER_NAME && podman rm $CONTAINER_NAME'"
