#!/usr/bin/env bash
# =============================================================================
# tx_service_roundtrip.sh —— daedalus-tx 端到端集成测试(AIOS 对象模型计划 todo 23)
#
# 全链路只走 CLI 表面(计划钉死):
#   daedalus-tx begin
#     → propose <id> service.set '{"name":"daedalus-itest","desired_state":"started"}' --unit-dir ~/.config/systemd/user
#     → apply <id> → status <id> → rollback <id> → status <id>
#
# 断言(计划 todo 23 (a)-(d), 逐条命名):
#   (a) apply 后 status 报 applied 且步骤 OpResult.returncode == 0
#   (b) rollback 后 status 报 rolled_back
#   (c) daedalus-audit verify 对测试专属日志退出码 0(双链完好)
#   (d) 测试日志里恰有 3 条同 tx_id 条目(begin/apply/rollback, 盖章规则见 todo 15;
#       propose/status 为空 TxID 外围条目)且事务子链逐条串接
#   外加 ActiveState 实感断言: apply 前 inactive → apply 后 active → rollback 后 inactive
#   (夹具带 RemainAfterExit=yes, 否则 oneshot 跑完即回落 inactive, active 无从断言)。
#
# 门控与隔离:
#   - 用户 systemd 会话探测失败 → 打印 "SKIP: no user manager" 并退 77
#     (headless CI 无用户会话时不得把 SKIP 读成 PASS 或实现失败);
#   - 日志(journal)与审计各自独立 mktemp -d, 经 DAEDALUS_TX_DIR /
#     DAEDALUS_AUDIT_LOG_PATH 环境变量注入, 绝不触碰 /var 与 $HOME 状态目录;
#   - 夹具复制到 ~/.config/systemd/user/ 前**先预清**历史残留; EXIT trap 强制
#     teardown(stop + 删除夹具 + daemon-reload + 清临时目录), 失败路径同样执行。
#
# 用法:
#   bash tests/integration/tx_service_roundtrip.sh                  # 快乐路径, 期望 exit 0
#   bash tests/integration/tx_service_roundtrip.sh --tamper-step-args
#       # 失败夹具: 完整往返后篡改一条 in-tx 条目的 args 值, 断言 verify 非零退出
#       # 且 stderr 给出具名断链诊断, 脚本按计划传播非零(固定 exit 1)。
# =============================================================================
set -uo pipefail

# ──── 路径与常量 ────
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)   # 仓库根(脚本住在 tests/integration/ 下)
CORE="$ROOT/daedalus/core"
BIN="$CORE/bin"
UNIT="daedalus-itest.service"
USER_UNIT_DIR="$HOME/.config/systemd/user"
FIXTURE="$ROOT/tests/integration/units/$UNIT"
TX_BIN="$BIN/daedalus-tx"
AUDIT_BIN="$BIN/daedalus-audit"

# roundtrip 产物(供 (c)(d) 断言与篡改模式引用)
TXID=""
AUDIT_LOG=""

# 本次进程创建的全部临时隔离目录(逐个登记, teardown 统一 rm -rf)
TDIRS=()

# ──── 输出助手 ────
ok()   { echo "PASS: $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

# ──── 强制 teardown(EXIT trap; 失败路径同样经过) ────
# 计划钉死: stop(容错) → 删夹具 → daemon-reload → 清临时目录;
# 快乐路径(rc=0)下 teardown 自身失败必须把退出码翻成非零("脚本仅在 teardown
# 成功后才退 0"); 原本已非零则保留原始失败码不被覆盖。
teardown() {
    local rc=$? d
    systemctl --user stop "$UNIT" >/dev/null 2>&1 || true
    rm -f "$USER_UNIT_DIR/$UNIT"
    if ! systemctl --user daemon-reload; then
        echo "TEARDOWN: systemctl --user daemon-reload 失败(用户单元目录可能有残留)" >&2
        (( rc == 0 )) && rc=1
    fi
    for d in "${TDIRS[@]:-}"; do
        [[ -n "$d" ]] && rm -rf "$d"
    done
    exit "$rc"
}

# ──── 二进制就绪(缺失或过期才构建; 与 just go-build 同旗标) ────
# 过期判据: cmd/ 或 internal/ 下任一 .go 比目标二进制新 → 重建。
is_stale() {
    local target=$1
    [[ ! -x "$target" ]] && return 0
    [[ -n "$(find "$CORE/cmd" "$CORE/internal" -name '*.go' -newer "$target" -print -quit)" ]]
}

ensure_binaries() {
    local name pkg
    for pair in "daedalus-tx ./cmd/daedalus-tx" "daedalus-audit ./cmd/daedalus-audit"; do
        name=${pair%% *}; pkg=${pair##* }
        if is_stale "$BIN/$name"; then
            echo "==> 构建 $name(缺失或已过期)"
            ( cd "$CORE" && CGO_ENABLED=0 GOTOOLCHAIN=local go build -o "bin/$name" "$pkg" ) \
                || fail "go build $name 失败"
        fi
    done
}

# ──── 夹具 setup ────
setup_fixture() {
    mkdir -p "$USER_UNIT_DIR" || fail "无法创建 $USER_UNIT_DIR"
    # 预清历史残留(上次异常中断泄漏时, 保证本次落位与入库夹具逐字节一致)
    rm -f "$USER_UNIT_DIR/$UNIT"
    cp "$FIXTURE" "$USER_UNIT_DIR/$UNIT" || fail "夹具复制失败"
    systemctl --user daemon-reload || fail "setup: daemon-reload 失败"
    # 状态归零: 若上次运行把单元留在 active(RemainAfterExit 驻留), propose 的
    # BeforeState 快照就不是 inactive, 回滚动词与 ActiveState 断言全部失真。
    systemctl --user stop "$UNIT" >/dev/null 2>&1 || true
}

# 读当前 ActiveState(systemctl show 单行 KEY=VALUE; bash 参数展开剥前缀)
active_state() {
    local out
    out=$(systemctl --user show "$UNIT" --property=ActiveState) \
        || { echo "<查询失败>"; return 1; }
    out=${out#ActiveState=}
    echo "${out%$'\n'}"
}

# ──── 事务往返内核(happy 与 tamper 模式共用) ────
# 每次调用一套全新隔离目录: $iso/tx(日志根) + $iso/audit(审计专属)。
run_roundtrip() {
    local iso out rc
    iso=$(mktemp -d) || fail "mktemp -d 失败"
    TDIRS+=("$iso")
    mkdir -p "$iso/tx" "$iso/audit"
    export DAEDALUS_TX_DIR="$iso/tx"
    export DAEDALUS_AUDIT_LOG_PATH="$iso/audit/audit.jsonl"
    AUDIT_LOG="$DAEDALUS_AUDIT_LOG_PATH"

    # begin: stdout 恰一份 {"tx_id":"<16hex>"}
    out=$("$TX_BIN" begin); rc=$?
    (( rc == 0 )) || fail "begin 退出码 $rc: $out"
    TXID=$(jq -r '.tx_id // empty' <<<"$out")
    [[ "$TXID" =~ ^[a-f0-9]{16}$ ]] || fail "begin 未产出合法 tx_id: $out"
    ok "begin → tx_id=$TXID"

    # propose(参数逐字取计划钉死的两键 JSON; --unit-dir = 集成测试逃生舱)
    out=$("$TX_BIN" propose "$TXID" service.set \
        '{"name":"daedalus-itest","desired_state":"started"}' \
        --unit-dir "$USER_UNIT_DIR"); rc=$?
    (( rc == 0 )) || fail "propose 退出码 $rc: $out"
    ok "propose service.set(started) 已登记"

    # apply 前基线: 夹具必须是 inactive(setup 已归零)
    local st; st=$(active_state)
    [[ "$st" == "inactive" ]] || fail "apply 前 ActiveState 应为 inactive, 实得 $st"

    # apply: 动词直发 systemctl --user start
    out=$("$TX_BIN" apply "$TXID" --unit-dir "$USER_UNIT_DIR"); rc=$?
    (( rc == 0 )) || fail "apply 退出码 $rc: $out"
    st=$(active_state)
    [[ "$st" == "active" ]] || fail "apply 后 ActiveState 应变 active, 实得 $st"
    ok "apply 后 ActiveState=active"

    # (a) status = applied 且步骤 OpResult.returncode == 0
    out=$("$TX_BIN" status "$TXID"); rc=$?
    (( rc == 0 )) || fail "status(apply 后) 退出码 $rc: $out"
    [[ "$(jq -r '.status' <<<"$out")" == "applied" ]] \
        || fail "(a) apply 后 journal status 应为 applied, 实得 $(jq -r '.status' <<<"$out")"
    [[ "$(jq -r '.steps[0].op_result.returncode' <<<"$out")" == "0" ]] \
        || fail "(a) 步骤 OpResult.returncode 应为 0, 实得 $(jq -r '.steps[0].op_result.returncode' <<<"$out")"
    ok "(a) status=applied 且 OpResult.returncode=0"

    # rollback: 恢复语义 → stop(见 T22 逆动词裁决), 状态机 applied → rolled_back
    out=$("$TX_BIN" rollback "$TXID" --unit-dir "$USER_UNIT_DIR"); rc=$?
    (( rc == 0 )) || fail "rollback 退出码 $rc: $out"

    # (b) status = rolled_back; ActiveState 回落 inactive
    out=$("$TX_BIN" status "$TXID"); rc=$?
    (( rc == 0 )) || fail "status(rollback 后) 退出码 $rc: $out"
    [[ "$(jq -r '.status' <<<"$out")" == "rolled_back" ]] \
        || fail "(b) rollback 后 journal status 应为 rolled_back, 实得 $(jq -r '.status' <<<"$out")"
    st=$(active_state)
    [[ "$st" == "inactive" ]] || fail "rollback 后 ActiveState 应恢复 inactive, 实得 $st"
    ok "(b) status=rolled_back 且 ActiveState 恢复 inactive"
}

# ──── (c) 双链校验 + (d) 恰 3 条同 tx 条目且子链串接 ────
assert_chain_happy() {
    if ! "$AUDIT_BIN" verify --log-path "$AUDIT_LOG"; then
        fail "(c) daedalus-audit verify 对测试专属日志非零退出(链断了?)"
    fi
    ok "(c) verify 退出 0(全局链 + 事务子链完好)"

    # (d)-1 计数: 恰 3 条同 tx_id 盖章条目, 且工具名集合 = begin/apply/rollback
    local -a tools
    mapfile -t tools < <(jq -r --arg id "$TXID" \
        'select(.tx_id == $id) | .tool' "$AUDIT_LOG" | sort)
    (( ${#tools[@]} == 3 )) || fail "(d) 同 tx_id 条目应恰 3 条, 实得 ${#tools[@]} 条: ${tools[*]}"
    [[ "${tools[0]}" == "daedalus_tx_apply" && "${tools[1]}" == "daedalus_tx_begin" \
        && "${tools[2]}" == "daedalus_tx_rollback" ]] \
        || fail "(d) 条目工具集应 begin/apply/rollback, 实得 ${tools[*]}"

    # (d)-2 子链逐条串接: apply.tx_prev_hash == begin.entry_hash 且
    # rollback.tx_prev_hash == apply.entry_hash(verify 已整体走过, 这里再点名核对)
    local hb ha ra hr
    # (jq 合取关键字是 `and`, 不存在 `&&` 算子 —— 1.6 编译期直接拒)
    hb=$(jq -r  --arg id "$TXID" 'select(.tx_id == $id and .tool == "daedalus_tx_begin")   | .entry_hash'   "$AUDIT_LOG")
    ha=$(jq -r  --arg id "$TXID" 'select(.tx_id == $id and .tool == "daedalus_tx_apply")    | .tx_prev_hash' "$AUDIT_LOG")
    ra=$(jq -rs --arg id "$TXID" 'map(select(.tx_id == $id and .tool == "daedalus_tx_apply"))    | .[0].entry_hash'   "$AUDIT_LOG")
    hr=$(jq -r  --arg id "$TXID" 'select(.tx_id == $id and .tool == "daedalus_tx_rollback")  | .tx_prev_hash' "$AUDIT_LOG")
    [[ -n "$hb" && "$ha" == "$hb" ]] || fail "(d) apply 的 tx_prev_hash 未指向 begin 的 entry_hash"
    [[ -n "$ra" && "$hr" == "$ra" ]] || fail "(d) rollback 的 tx_prev_hash 未指向 apply 的 entry_hash"
    ok "(d) 恰 3 条同 tx_id 条目(begin/apply/rollback)且事务子链逐条串接"
}

# ──── 失败夹具模式: 篡改一条 in-tx 条目的 args 值 → verify 必须拒 ────
# 篡改落点对计划钉死: 只动 args 值(适配器名末位加一个字符), 顶层 tx_id/
# entry_hash/prev_hash 一概原样 —— 重算哈希必与记录哈希失配, verify 给出
# 具名断链诊断(全局链"哈希不符"/"链断裂"文案族)。篡改在**日志副本**上做,
# 原始临时日志保持自洽, 清理仍归 trap。
run_tamper() {
    local tampered="$AUDIT_LOG.tampered" err rc
    jq -c --arg id "$TXID" \
        'if type == "object" and .tx_id == $id and .tool == "daedalus_tx_apply"
         then .args.adapter = (.args.adapter + "t")
         else . end' \
        "$AUDIT_LOG" > "$tampered" || fail "篡改副本生成失败"

    err=$("$AUDIT_BIN" verify --log-path "$tampered" 2>&1 >/dev/null); rc=$?
    (( rc != 0 )) || fail "(tamper) 篡改 in-tx args 后 verify 竟退出 0 —— 篡改不可见, 证据边界失守"
    [[ "$err" == *"哈希不符"* || "$err" == *"链断裂"* ]] \
        || fail "(tamper) verify 非零但 stderr 无具名断链诊断: $err"
    ok "(tamper) verify 退出 $rc, 具名诊断: $err"

    # 计划 Failure QA: 检测成功后脚本传播非零退出 1(trap 照常完成 teardown)
    echo "RESULT: 篡改检测达成, 按计划传播非零退出"
    exit 1
}

# ──── 主流程 ────
main() {
    local mode=${1:-}
    case "$mode" in
        "")                  : ;; # 快乐路径
        --tamper-step-args)  : ;; # 失败夹具
        *)  echo "用法: $0 [--tamper-step-args]" >&2; exit 2 ;;
    esac

    # 用户 systemd 会话探测(计划 review round 1): 失败即 SKIP 77, 先于任何构建/落位,
    # 且此刻尚未登记 trap —— 无残留可清, 77 码必须原样出去。
    if ! systemctl --user show-environment >/dev/null 2>&1; then
        echo "SKIP: no user manager"
        exit 77
    fi

    trap teardown EXIT
    ensure_binaries
    setup_fixture
    run_roundtrip

    if [[ "$mode" == "--tamper-step-args" ]]; then
        run_tamper    # 永不返回(内部 exit 1)
    fi

    assert_chain_happy
    echo "RESULT: tx_service_roundtrip 全链路 PASS"
    exit 0
}

main "$@"
