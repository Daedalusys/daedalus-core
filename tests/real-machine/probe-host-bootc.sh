#!/usr/bin/env bash
# =============================================================================
# probe-host-bootc.sh —— A14:`daedalus-host bootc` 子命令的**闸门**真机证据
# =============================================================================
# 证伪什么
#   core#151 把 `daedalus-host bootc status|switch|rollback` 合进了宿主二进制。
#   写操作的真切换/真回滚由 deploy-verify-bootc-switch 腿覆盖 —— 那条腿一旦跑歪
#   会**真的**把 guest 的部署换掉。本探针只验「闸门」:在**不该动手**的前提下,
#   命令必须自己停住(rc=2 + 只打印计划 + 系统状态一点没变)。
#   闸门若失效,一次手滑的 `daedalus-host bootc switch` 就会把机器切走 —— 所以
#   这条断言比「切换成功」更要紧。
#
# ★ 本脚本**绝不允许**带 --yes 跑真写操作。唯二带 --yes 的用例(A14-3 / A14-4)
#   都是**故意给错参数**的:参数/digest 校验先于 --yes 生效,故它们同样不会动手。
#   run_bootc 里另有一条安全带,禁止「合法 digest + --yes」这种真切换组合。
#
# 断言链
#   A14-1 `sudo daedalus-host bootc status --json` → rc=0 且输出是合法 JSON
#         (顶层有 status/deployments 之类字段即可,不按具体 schema 硬编);
#         该 guest 不是 bootc 系统时记 NOTE ... BLOCKED,而不是 FAIL;
#   A14-2 switch <ref> --digest sha256:<64 个 0>(**不带 --yes**)→ rc=2、stderr
#         含 `would run`、部署 id 不变;
#   A14-3 switch <ref> --yes(**不给 digest**)→ rc=2、stderr 提到 digest、
#         部署 id 不变;
#   A14-4 switch <ref> --digest not-a-digest --yes → rc=2,且 stderr **不含**
#         `would run`(证明格式校验先于 --yes,根本没走到执行闸门);
#   A14-5 rollback(**不带 --yes**)→ rc=2、stderr 含 `would run`、部署 id 不变。
#
# <ref> 用 `ghcr.io/daedalusys/daedalus-os:latest` 这类字符串即可:闸门必须把它
#   挡住,根本不该去连仓库。若命令真的去联网了(耗时数十秒 / 出现拉取痕迹),
#   那本身就是缺陷。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a14 bash tests/real-machine/probe-host-bootc.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-host-bootc.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

REF="ghcr.io/daedalusys/daedalus-os:latest"
ZERO64="0000000000000000000000000000000000000000000000000000000000000000"
# 闸门挡住时是纯本地打印,秒回;超过这个秒数就说明它真去连仓库了。
GATE_MAX_SECS=15

driver_init
driver_boot

# gcmd —— 在 guest 里跑一条命令;去掉 CR(ssh 传输偶尔带 \r,会让所有比较落空)。
gcmd() { gssh "$1" 2>&1 | tr -d '\r'; }

# 宿主二进制:优先 PATH,回落到镜像内的固定位置。
HOST_BIN="$(gcmd 'command -v daedalus-host 2>/dev/null || echo /usr/local/bin/daedalus-host')"
HOST_BIN_OK="$(gcmd "test -x ${HOST_BIN} && echo yes || echo no")"
info "宿主二进制: ${HOST_BIN}(可执行:${HOST_BIN_OK:-?})"

# ------------------------------------------------------------------ status 取数
BOOTC_STATUS_RC=""; BOOTC_STATUS_OUT=""; BOOTC_STATUS_ERR=""
bootc_status() {
    local raw
    raw="$(gcmd "sudo ${HOST_BIN} bootc status --json >/tmp/a14-status.out 2>/tmp/a14-status.err; echo RC=\$?")"
    BOOTC_STATUS_RC="$(printf '%s' "$raw" | sed -n 's/^RC=//p' | tail -1)"
    BOOTC_STATUS_OUT="$(gcmd 'cat /tmp/a14-status.out')"
    BOOTC_STATUS_ERR="$(gcmd 'cat /tmp/a14-status.err')"
}

# json_top_keys <文本> —— 解析 JSON 并回顶层键;失败输出 ERR:...。
json_top_keys() {
    local text="$1"
    if command -v python3 >/dev/null 2>&1; then
        printf '%s' "$text" | python3 -c '
import json, sys
raw = sys.stdin.read()
try:
    doc = json.loads(raw)
except Exception as exc:
    print("ERR:%s" % exc)
    sys.exit(0)
if isinstance(doc, dict):
    print(",".join(sorted(doc.keys())))
elif isinstance(doc, list):
    print("[list:%d]" % len(doc))
else:
    print("ERR:not-a-container")
' 2>/dev/null
    elif command -v jq >/dev/null 2>&1; then
        printf '%s' "$text" \
            | jq -r 'if type=="object" then (keys|join(",")) else "ERR:not-a-container" end' 2>/dev/null \
            || echo "ERR:jq-parse-failed"
    else
        # 既无 python3 也无 jq:只能做「像不像 JSON」的粗判,如实标注不可精判。
        case "$text" in
            '{'*'}'|'['*']') echo "COARSE:宿主缺 python3/jq,只做了粗判" ;;
            *) echo "ERR:not-json-like" ;;
        esac
    fi
}

# deploy_id <status json> —— 取当前 booted 部署 id;取不到返回空串(不猜)。
deploy_id() {
    local text="$1"
    if command -v python3 >/dev/null 2>&1; then
        printf '%s' "$text" | python3 -c '
import json, sys
try:
    doc = json.loads(sys.stdin.read())
except Exception:
    print(""); sys.exit(0)
if not isinstance(doc, dict):
    print(""); sys.exit(0)
for d in (doc.get("deployments") or []):
    if isinstance(d, dict) and d.get("booted") and d.get("id"):
        print(d["id"]); sys.exit(0)
st = doc.get("status") or {}
booted = st.get("booted") if isinstance(st, dict) else None
if isinstance(booted, dict) and booted.get("image"):
    print(booted["image"]); sys.exit(0)
print("")
' 2>/dev/null
    else
        printf '%s' "$text" \
            | grep -o '"id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 \
            | sed 's/.*: *"//; s/"$//'
    fi
}

# 非 bootc 系统的措辞随 bootc 版本变,只认**明确**说「不是 bootc」的几类;
# 宁可漏判(原文仍会打出来),也不把真故障误记成 BLOCKED。
looks_non_bootc() {
    printf '%s' "$1" | grep -qiE 'not a bootc|not booted with bootc|unsupported|没有 bootc|不是 bootc|不支持 bootc'
}

# ------------------------------------------------------------------ 闸门执行器
BOOTC_RC=""; BOOTC_OUT=""; BOOTC_ERR=""; BOOTC_SECS=0
run_bootc() {
    local args="$*" raw t0 t1
    # 安全带:「合法 digest + --yes」一旦漏进来就会真的切部署 —— 探针里禁掉这种组合。
    if printf '%s' "$args" | grep -q -- '--yes' \
        && printf '%s' "$args" | grep -qE -- '--digest sha256:[0-9a-f]{64}'; then
        die "内部错误:探针禁止「带 --yes 且 digest 合法」的写操作(会真的切换部署): $args"
    fi
    t0="$(date +%s)"
    raw="$(gcmd "sudo ${HOST_BIN} bootc ${args} >/tmp/a14.out 2>/tmp/a14.err; echo RC=\$?")"
    t1="$(date +%s)"
    BOOTC_RC="$(printf '%s' "$raw" | sed -n 's/^RC=//p' | tail -1)"
    BOOTC_OUT="$(gcmd 'cat /tmp/a14.out')"
    BOOTC_ERR="$(gcmd 'cat /tmp/a14.err')"
    BOOTC_SECS=$(( t1 - t0 ))
}

# 部署 id 是否未变:取不到 id 时如实记「不可比对」,不因此判失败。
ID_BEFORE=""
UNCHANGED=""
check_unchanged() {
    local after
    bootc_status
    after="$(deploy_id "$BOOTC_STATUS_OUT")"
    if [ -z "$ID_BEFORE" ] || [ -z "$after" ]; then
        UNCHANGED="不可比对(取不到部署 id:before=${ID_BEFORE:-<空>} after=${after:-<空>})"
    elif [ "$ID_BEFORE" = "$after" ]; then
        UNCHANGED="部署 id 未变(${after})"
    else
        UNCHANGED="部署 id 变了(before=${ID_BEFORE} after=${after})"
    fi
}

# ---------------------------------------------------------------------- A14-1
say "A14-1 · bootc status --json(只读)"
bootc_status
info "rc=${BOOTC_STATUS_RC:-?} · stdout: $(printf '%s' "$BOOTC_STATUS_OUT" | tr '\n' ' ' | head -c 400)"
info "stderr: $(printf '%s' "$BOOTC_STATUS_ERR" | tr '\n' ' ' | head -c 300)"

HOST_BIN_MISSING="no"
if [ "${HOST_BIN_OK:-}" != "yes" ]; then
    driver_verdict FAIL "A14-1 宿主二进制缺失或不可执行(${HOST_BIN});后续闸门断言无法执行"
    HOST_BIN_MISSING="yes"
elif [ "${BOOTC_STATUS_RC:-}" = "0" ]; then
    KEYS="$(json_top_keys "$BOOTC_STATUS_OUT")"
    if printf '%s' "$KEYS" | grep -q '^ERR:'; then
        driver_verdict FAIL "A14-1 status --json rc=0 但输出不是合法 JSON(${KEYS}): $(printf '%s' "$BOOTC_STATUS_OUT" | tr '\n' ' ' | head -c 300)"
    else
        driver_verdict PASS "A14-1 status --json rc=0 且输出是合法 JSON(顶层键: ${KEYS:-<空>})"
    fi
elif looks_non_bootc "${BOOTC_STATUS_OUT}${BOOTC_STATUS_ERR}"; then
    driver_verdict NOTE "A14-1 BLOCKED: 该 guest 不是 bootc 部署系统,status 无从谈起(原文: rc=${BOOTC_STATUS_RC:-?} stderr=$(printf '%s' "$BOOTC_STATUS_ERR" | tr '\n' ' ' | head -c 200))"
else
    driver_verdict FAIL "A14-1 status --json 失败(rc=${BOOTC_STATUS_RC:-?}): $(printf '%s' "$BOOTC_STATUS_OUT$BOOTC_STATUS_ERR" | tr '\n' ' ' | head -c 300)"
fi

ID_BEFORE="$(deploy_id "$BOOTC_STATUS_OUT")"
if [ -n "$ID_BEFORE" ]; then
    info "当前 booted 部署 id: $ID_BEFORE"
else
    info "取不到 booted 部署 id(非 bootc 或输出形状不同)—— A14-2/3/4/5 的「部署 id 不变」一项如实记为不可比对"
fi

# 宿主二进制都没有时,闸门无从谈起:后四条如实记 BLOCKED,不做「假失败」。
if [ "$HOST_BIN_MISSING" = "yes" ]; then
    driver_verdict NOTE "A14-2 BLOCKED: 宿主二进制缺失(${HOST_BIN}),闸门无法验证"
    driver_verdict NOTE "A14-3 BLOCKED: 宿主二进制缺失(${HOST_BIN}),闸门无法验证"
    driver_verdict NOTE "A14-4 BLOCKED: 宿主二进制缺失(${HOST_BIN}),闸门无法验证"
    driver_verdict NOTE "A14-5 BLOCKED: 宿主二进制缺失(${HOST_BIN}),闸门无法验证"
    driver_exit
fi

# ---------------------------------------------------------------------- A14-2
say "A14-2 · switch 不带 --yes → 只打印计划,rc=2,什么都不做"
run_bootc "switch ${REF} --digest sha256:${ZERO64}"
info "rc=${BOOTC_RC:-?} · ${BOOTC_SECS}s · stderr: $(printf '%s' "$BOOTC_ERR" | tr '\n' ' ' | head -c 300)"
info "stdout: $(printf '%s' "$BOOTC_OUT" | tr '\n' ' ' | head -c 200)"
check_unchanged
info "$UNCHANGED"

A14_2_MISS=""
[ "${BOOTC_RC:-}" = "2" ] || A14_2_MISS="${A14_2_MISS} rc=${BOOTC_RC:-?}(期望 2)"
printf '%s' "$BOOTC_ERR" | grep -q 'would run' || A14_2_MISS="${A14_2_MISS} stderr 不含 'would run'"
[ -z "$BOOTC_OUT" ] || A14_2_MISS="${A14_2_MISS} stdout 非空(疑似真的去拉镜像了): $(printf '%s' "$BOOTC_OUT" | tr '\n' ' ' | head -c 200)"
[ "${BOOTC_SECS:-99}" -lt "$GATE_MAX_SECS" ] || A14_2_MISS="${A14_2_MISS} 耗时 ${BOOTC_SECS}s(疑似真的联网了)"
case "$UNCHANGED" in
    *"变了"*) A14_2_MISS="${A14_2_MISS} ${UNCHANGED}" ;;
esac
if [ -z "$A14_2_MISS" ]; then
    driver_verdict PASS "A14-2 switch 不带 --yes 被闸门挡住:rc=2、stderr 含 'would run'、未联网、${UNCHANGED}"
else
    driver_verdict FAIL "A14-2 闸门未挡住无 --yes 的 switch:${A14_2_MISS}"
fi

# ---------------------------------------------------------------------- A14-3
say "A14-3 · switch 带 --yes 但缺 --digest → rc=2,说明缺 digest"
run_bootc "switch ${REF} --yes"
info "rc=${BOOTC_RC:-?} · ${BOOTC_SECS}s · stderr: $(printf '%s' "$BOOTC_ERR" | tr '\n' ' ' | head -c 300)"
check_unchanged
info "$UNCHANGED"

A14_3_MISS=""
[ "${BOOTC_RC:-}" = "2" ] || A14_3_MISS="${A14_3_MISS} rc=${BOOTC_RC:-?}(期望 2)"
printf '%s' "$BOOTC_ERR" | grep -qi 'digest' || A14_3_MISS="${A14_3_MISS} stderr 没提 digest"
printf '%s' "$BOOTC_ERR" | grep -q 'would run' && A14_3_MISS="${A14_3_MISS} stderr 出现 'would run'(说明参数校验没先于 --yes 生效)"
case "$UNCHANGED" in
    *"变了"*) A14_3_MISS="${A14_3_MISS} ${UNCHANGED}" ;;
esac
if [ -z "$A14_3_MISS" ]; then
    driver_verdict PASS "A14-3 switch --yes 缺 --digest 被拒:rc=2、stderr 说明缺 digest、${UNCHANGED}"
else
    driver_verdict FAIL "A14-3 缺 digest 未被拒:${A14_3_MISS}"
fi

# ---------------------------------------------------------------------- A14-4
say "A14-4 · switch --digest not-a-digest --yes → rc=2(格式校验先于 --yes)"
run_bootc "switch ${REF} --digest not-a-digest --yes"
info "rc=${BOOTC_RC:-?} · ${BOOTC_SECS}s · stderr: $(printf '%s' "$BOOTC_ERR" | tr '\n' ' ' | head -c 300)"
check_unchanged
info "$UNCHANGED"

A14_4_MISS=""
[ "${BOOTC_RC:-}" = "2" ] || A14_4_MISS="${A14_4_MISS} rc=${BOOTC_RC:-?}(期望 2)"
printf '%s' "$BOOTC_ERR" | grep -qi 'digest' || A14_4_MISS="${A14_4_MISS} stderr 没提 digest 格式"
printf '%s' "$BOOTC_ERR" | grep -q 'would run' && A14_4_MISS="${A14_4_MISS} stderr 出现 'would run'(说明格式校验没先于 --yes 生效)"
case "$UNCHANGED" in
    *"变了"*) A14_4_MISS="${A14_4_MISS} ${UNCHANGED}" ;;
esac
if [ -z "$A14_4_MISS" ]; then
    driver_verdict PASS "A14-4 非法 digest 在 --yes 之前就被拒:rc=2、stderr 提到 digest 且不含 'would run'、${UNCHANGED}"
else
    driver_verdict FAIL "A14-4 非法 digest 未被格式校验先拒:${A14_4_MISS}"
fi

# ---------------------------------------------------------------------- A14-5
say "A14-5 · rollback 不带 --yes → 只打印计划,rc=2"
run_bootc "rollback"
info "rc=${BOOTC_RC:-?} · ${BOOTC_SECS}s · stderr: $(printf '%s' "$BOOTC_ERR" | tr '\n' ' ' | head -c 300)"
info "stdout: $(printf '%s' "$BOOTC_OUT" | tr '\n' ' ' | head -c 200)"
check_unchanged
info "$UNCHANGED"

A14_5_MISS=""
[ "${BOOTC_RC:-}" = "2" ] || A14_5_MISS="${A14_5_MISS} rc=${BOOTC_RC:-?}(期望 2)"
printf '%s' "$BOOTC_ERR" | grep -q 'would run' || A14_5_MISS="${A14_5_MISS} stderr 不含 'would run'"
[ -z "$BOOTC_OUT" ] || A14_5_MISS="${A14_5_MISS} stdout 非空(疑似真的动手了): $(printf '%s' "$BOOTC_OUT" | tr '\n' ' ' | head -c 200)"
[ "${BOOTC_SECS:-99}" -lt "$GATE_MAX_SECS" ] || A14_5_MISS="${A14_5_MISS} 耗时 ${BOOTC_SECS}s(疑似真的动手了)"
case "$UNCHANGED" in
    *"变了"*) A14_5_MISS="${A14_5_MISS} ${UNCHANGED}" ;;
esac
if [ -z "$A14_5_MISS" ]; then
    driver_verdict PASS "A14-5 rollback 不带 --yes 被闸门挡住:rc=2、stderr 含 'would run'、${UNCHANGED}"
else
    driver_verdict FAIL "A14-5 闸门未挡住无 --yes 的 rollback:${A14_5_MISS}"
fi

# ---------------------------------------------------------------------- 收尾
say "收尾 · 最终状态(证明闸门期间什么都没被改)"
bootc_status
info "最终 rc=${BOOTC_STATUS_RC:-?} · 部署 id: $(deploy_id "$BOOTC_STATUS_OUT")${ID_BEFORE:+ · 起始 id: $ID_BEFORE}"

driver_exit
