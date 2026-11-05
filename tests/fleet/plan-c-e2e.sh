#!/usr/bin/env bash
#
# Plan C 异构被控 E2E:控制面(fleet 客户端 / node registry / 分桶调度器 / plan CLI)
# 对三台异构被控机发起并跟踪一次跨机 Plan。
#
# 用法:
#   tests/fleet/plan-c-e2e.sh --selftest
#       不 ssh、不依赖真机:校验两份夹具 + 跑 core 里的真 mTLS 端到端判据
#       (tests/integration/fleet_e2e_test.go:loopback TCP + 双向证书 + 真
#       Scheduler + 真夹具)。CI 与开发机都能跑。
#   tests/fleet/plan-c-e2e.sh --hosts ubuntu24=10.0.0.11 debian12=10.0.0.12 rhel9=10.0.0.13
#       真机矩阵:逐台登记 → 低危 Plan → 高危 Plan(含一台无回滚点)→ 人审恢复。
#       需要:三台机已按 daedalus-agent 的 install.sh 装好并 active;控制面本机
#       有 mTLS 客户端凭证(DAEDALUS_FLEET_CLIENT_CERT / _KEY / DAEDALUS_FLEET_CA)。
#
# 退出码:0 全部断言通过;非 0 表示某一步断言失败(逐条打印)。
#
# 证据:真机跑完请把观察填进 tests/fleet/EVIDENCE/ 下的模板文件(照实写,
# 含与期望不符之处与对应 issue 号)。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURES="$ROOT/tests/fleet/fixtures"
HOST_BIN="${DAEDALUS_HOST_BIN:-$ROOT/bin/daedalus-host}"

pass=0
fail=0

ok()   { echo "  ok   - $*"; pass=$((pass + 1)); }
bad()  { echo "  FAIL - $*" >&2; fail=$((fail + 1)); }
step() { echo; echo "== $*"; }

# assert_jq <描述> <jq 表达式> <stdin 的 JSON>
assert_jq() {
    local desc="$1" expr="$2" json="$3"
    if echo "$json" | jq -e "$expr" >/dev/null 2>&1; then
        ok "$desc"
    else
        bad "$desc(jq: $expr)"
        echo "    实际输出: $json" >&2
    fi
}

# ---------------------------------------------------------------- selftest ----

run_selftest() {
    step "夹具结构(两份 Plan 必须能被 objectmodel.Plan 解进并通过校验)"
    for f in plan-low-nginx.json plan-high-kernel.json; do
        if [ -f "$FIXTURES/$f" ]; then
            ok "夹具存在:$f"
        else
            bad "夹具缺失:$f"
        fi
    done
    # 结构断言:低危夹具 3 台机 / low;高危夹具含 1 台 experimental 目标机 / high。
    assert_jq "低危夹具:machines 3 台且 max_risk=low" \
        '.blast_radius.max_risk == "low" and (.machines | length) == 3 and (.intents | length) == 1 and .batching.strategy == "count"' \
        "$(cat "$FIXTURES/plan-low-nginx.json")"
    assert_jq "高危夹具:max_risk=high 且含 legacy-no-snapshot 目标机" \
        '.blast_radius.max_risk == "high" and ([.machines[].machine_id] | index("legacy-no-snapshot")) != null and (.intents[0].target == "kernel-core")' \
        "$(cat "$FIXTURES/plan-high-kernel.json")"

    step "端到端判据(真 mTLS + 真 Scheduler + 真夹具;loopback,不需要真机)"
    export GOMODCACHE="${GOMODCACHE:-/tmp/opencode/go-modcache}"
    export TMPDIR="${TMPDIR:-/tmp/opencode}"
    if (cd "$ROOT" && go test ./tests/integration/ -run TestFleetE2E -count=1 -v 2>&1 | sed 's/^/  /'); then
        ok "TestFleetE2E_*:低危跑完(complete + 每台有 tx_id)+ 高危遇无回滚点当场 dirty 且文案点名 snapshot"
    else
        bad "TestFleetE2E_* 未通过"
    fi
}

# -------------------------------------------------------------- 真机矩阵 ----

# register_hosts <name=addr>...:逐台 node add(控制面主动 Register)。
register_hosts() {
    step "登记被控机(daedalus-host node add)"
    for pair in "$@"; do
        local name="${pair%%=*}" addr="${pair#*=}"
        # agent_id 用机器名:真机上应换成本机 /var/lib/daedalus-agent/state/agent_id 的值。
        # support_level 需要操作者给发行版(proto 的 Register 响应不回带 OsInfo)。
        local distro version
        case "$name" in
            ubuntu24*) distro=ubuntu; version=24.04 ;;
            debian12*) distro=debian; version=12 ;;
            rhel9*)    distro=rhel;   version=9 ;;
            *)         distro="";     version="" ;;
        esac
        if "$HOST_BIN" node add "$addr" --agent-id "$name" \
            --distro "$distro" --distro-version "$version" --hostname "$name"; then
            ok "node add $name($addr)"
        else
            bad "node add $name($addr)"
        fi
    done
}

# verify_node_list:node list 的台数与 support_level 诚实度。
verify_node_list() {
    step "node list 契约"
    local want="$1" json
    json="$("$HOST_BIN" node list)"
    assert_jq "节点数 = $want" ".nodes | length == $want" "$json"
    assert_jq "每行都带 support_level 且取值合法" \
        'all(.nodes[]; .support_level == "supported" or .support_level == "experimental")' "$json"
    echo "  node list: $json"
}

# submit_and_check <夹具> <期望 status> <额外 jq 断言>
submit_and_check() {
    local fixture="$1" want="$2" extra="$3" json
    json="$("$HOST_BIN" plan submit "$FIXTURES/$fixture")"
    assert_jq "$fixture → status=$want" ".status == \"$want\"" "$json"
    assert_jq "$fixture → $extra" "$extra" "$json"
    echo "  plan submit $fixture: $json"
    LAST_PLAN_JSON="$json"
}

run_matrix() {
    local hosts=("$@")
    register_hosts "${hosts[@]}"
    verify_node_list "${#hosts[@]}"

    step "低危腿:package.set nginx(low)"
    submit_and_check plan-low-nginx.json complete \
        'all(.results[]; .tx_id != "")'

    step "高危腿:package.set kernel-core(high,含一台无回滚点)"
    submit_and_check plan-high-kernel.json dirty \
        'any(.results[]; (.message | ascii_downcase | contains("snapshot")))'

    step "人审恢复(spec §4.3 三选一;本脚本演练 --skip)"
    local plan_id legacy
    plan_id="$(echo "$LAST_PLAN_JSON" | jq -r '.plan_id')"
    legacy="legacy-no-snapshot"
    if "$HOST_BIN" plan advance "$plan_id" --skip "$legacy" | tee /tmp/plan-c-advance.json | jq -e . >/dev/null; then
        ok "plan advance --skip $legacy"
        assert_jq "被 skip 的节点已从待跑批次摘掉" \
            "[.batches[].node_ids[]] | index(\"$legacy\") == null" "$(cat /tmp/plan-c-advance.json)"
        assert_jq "被 skip 的结果标成人已接受" \
            "any(.results[]; .node_id == \"$legacy\" and (.message | startswith(\"skipped-by-human:\")))" \
            "$(cat /tmp/plan-c-advance.json)"
    else
        bad "plan advance --skip $legacy"
    fi
    echo
    echo "  提示:回滚路径由人执行(控制面不代跑远程回滚)。dirty 时 plan status 会给出"
    echo "        每台已成功节点的本地命令:daedalus-tx rollback <tx_id>。"
}

# ------------------------------------------------------------------ main ----

mode=""
hosts=()
while [ $# -gt 0 ]; do
    case "$1" in
        --selftest) mode=selftest ;;
        --hosts)
            mode=matrix
            shift
            while [ $# -gt 0 ] && [[ "$1" != --* ]]; do hosts+=("$1"); shift; done
            continue
            ;;
        -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "未知参数:$1" >&2; exit 2 ;;
    esac
    shift
done

case "$mode" in
    selftest)
        run_selftest
        ;;
    matrix)
        if [ "${#hosts[@]}" -eq 0 ]; then
            echo "用法:tests/fleet/plan-c-e2e.sh --hosts name=addr [name=addr ...]" >&2
            exit 2
        fi
        if [ ! -x "$HOST_BIN" ]; then
            echo "找不到 daedalus-host 二进制($HOST_BIN);先 just go-build,或用 DAEDALUS_HOST_BIN 指定。" >&2
            exit 2
        fi
        run_matrix "${hosts[@]}"
        ;;
    *)
        sed -n '2,20p' "${BASH_SOURCE[0]}"
        exit 2
        ;;
esac

echo
echo "plan-c-e2e: $pass 通过 / $fail 失败"
[ "$fail" -eq 0 ]
