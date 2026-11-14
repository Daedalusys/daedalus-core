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

    step "运行期渲染:夹具按 agent 自报的 node_id 提交(不等机器名)"
    local tmp
    tmp="$(mktemp -d)"
    export NODE_ID_MAP='{"ubuntu24-a":"n-u24","debian12-a":"n-d12","rhel9-a":"n-r9","legacy-no-snapshot":"n-nosnap"}'
    if render_plan "$FIXTURES/plan-low-nginx.json" "$tmp/low.json"; then
        assert_jq "低危夹具三台都换成 node_id" \
            '([.machines[].machine_id] == ["n-u24","n-d12","n-r9"])' "$(cat "$tmp/low.json")"
    else
        bad "render_plan 低危夹具失败"
    fi
    if render_plan "$FIXTURES/plan-high-kernel.json" "$tmp/high.json" n-nosnap; then
        assert_jq "高危夹具把无回滚点排第一批,台数不变" \
            '.machines[0].machine_id == "n-nosnap" and (.machines|length) == 2' "$(cat "$tmp/high.json")"
    else
        bad "render_plan 高危夹具失败"
    fi
    export NODE_ID_MAP='{"ubuntu24-a":"n-u24"}'
    if render_plan "$FIXTURES/plan-low-nginx.json" "$tmp/bad.json" 2>/dev/null; then
        bad "反证:映射不全时 render_plan 必须拒"
    else
        ok "反证:映射不全时 render_plan 拒绝提交"
    fi

    export NODE_ID_MAP='{"ubuntu24-a":"n-u24","legacy-no-snapshot":"n-nosnap"}'
    if render_plan "$FIXTURES/plan-high-kernel.json" "$tmp/nope.json" n-absent 2>/dev/null; then
        bad "反证:首批机器不在夹具里时必须拒(否则批次顺序守卫静默失效)"
    else
        ok "反证:首批机器不在夹具里时 render_plan 拒绝"
    fi

    step "失败路径:控制面报错时逐条记 FAIL,不整轮中断"
    local stub before
    stub="$tmp/stub-host"
    cat > "$stub" <<'STUB'
#!/usr/bin/env bash
# 桩控制面:只用于校验驱动自身的失败路径,不连任何真机
sub="$1"; shift
case "$sub" in
  node)
    case "$1" in
      add)
        shift; id=""
        while [ $# -gt 0 ]; do [ "$1" = "--agent-id" ] && id="$2"; shift; done
        printf '{"node":{"node_id":"n-%s"}}\n' "$id"
        ;;
      list) echo '{"nodes":[]}' ;;
    esac
    ;;
  plan)
    case "$1" in
      submit)
        if [ "${STUB_SUBMIT_FAIL:-}" = 1 ]; then
            echo "plan submit: 桩故意失败" >&2
            exit 1
        fi
        echo '{"plan_id":"p1","status":"complete","results":[{"node_id":"n-u24","tx_id":"tx-1"}]}'
        ;;
      advance) echo "${STUB_ADVANCE_JSON:-{\}}" ;;
    esac
    ;;
esac
STUB
    chmod +x "$stub"

    out="$( ( export STUB_SUBMIT_FAIL=1; HOST_BIN="$stub"
            submit_and_check "$tmp/plan.json" complete 'true'
          ) 2>&1)"
    case "$out" in
        *"plan submit 失败"*) ok "plan submit 失败 → 记 1 条 FAIL 并继续(不整轮中断)" ;;
        *) bad "plan submit 失败未按预期记 FAIL: $out" ;;
    esac

    out="$( ( LAST_PLAN_JSON=""
            HOST_BIN="$stub" run_recovery_skip n-nosnap "$tmp/adv.json"
          ) 2>&1)"
    case "$out" in
        *"高危腿未提交"*) ok "高危腿未提交时,人审恢复步直接记 FAIL 而不是拿旧 Plan 演练" ;;
        *) bad "高危腿未提交时未按预期 FAIL: $out" ;;
    esac

    out="$( ( LAST_PLAN_JSON='{"plan_id":"p1","status":"dirty"}'
            export HOST_BIN="$stub"
            export STUB_ADVANCE_JSON='{"batches":[{"index":0,"node_ids":["n-u24"]}],"results":[{"node_id":"n-nosnap","message":"skipped-by-human: x"}]}'
            run_recovery_skip n-nosnap "$tmp/adv.json"
          ) 2>&1)"
    if [ "$(grep -c '^  bad' <<<"$out")" -eq 0 ] && [ "$(grep -c '^  ok' <<<"$out")" -eq 4 ]; then
        ok "高危腿已提交时,--skip 三条断言全过(含剩余批次只剩另一台)"
    else
        bad "safe path 断言不全绿: $out"
    fi
    rm -rf "$tmp"
}

# -------------------------------------------------------------- 真机矩阵 ----

declare -A NODE_IDS=()

# render_plan <src> <dst> [snapshotless_first]
# 夹具是模板:机器名由夹具自定,真 node_id 由 agent 在 Register 时自报
# (控制面 Registry.Get 只认 node_id,不认机器名)。映射不全时拒提交 ——
# 把未映射的原名交上去只会换来一条"未注册",掩盖真正的拼写错误。
render_plan() {
    local src="$1" dst="$2" first="${3:-}" map missing
    map="${NODE_ID_MAP:-}"
    [ -n "$map" ] || map='{}'
    missing="$(jq -r --argjson m "$map" \
        '.machines[].machine_id | select(. as $x | ($m|has($x))|not)' "$src")"
    if [ -n "$missing" ]; then
        echo "render_plan: 夹具机器 $(echo "$missing" | tr '\n' ' ')未在 --hosts 中登记" >&2
        return 1
    fi
    jq --argjson m "$map" --arg first "$first" '
        .machines |= map(.machine_id = ($m[.machine_id]))
        | if $first == "" then .
          else .machines |= (map(select(.machine_id == $first)) + map(select(.machine_id != $first)))
          end' "$src" > "$dst" || return 1
    # 替换后才校验:调用方传的是 node_id,夹具里写的是模板名,只有渲染后两者才对得上。
    if [ -n "$first" ] && ! jq -e --arg f "$first" \
        '[.machines[].machine_id] | index($f) != null' "$dst" >/dev/null; then
        echo "render_plan: 要排第一批的机器 $first 不在夹具里" >&2
        return 1
    fi
}

# build_node_id_map 把登记回读到的 node_id 拼成 jq 用的映射:机器名 → node_id。
build_node_id_map() {
    NODE_ID_MAP="$(for k in "${!NODE_IDS[@]}"; do
        jq -cn --arg k "$k" --arg v "${NODE_IDS[$k]}" '{($k):$v}'
    done | jq -s 'add // {}')"
    export NODE_ID_MAP
}

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
        local out
        if out="$("$HOST_BIN" node add "$addr" --agent-id "$name" \
            --distro "$distro" --distro-version "$version" --hostname "$name")"; then
            NODE_IDS["$name"]="$(jq -r '.node.node_id' <<<"$out")"
            ok "node add $name($addr) → node_id=${NODE_IDS[$name]}"
        else
            bad "node add $name($addr)"
        fi
    done
    build_node_id_map
}

# verify_node_list:node list 的台数与 support_level 诚实度。
verify_node_list() {
    step "node list 契约"
    local want="$1" json
    # 控制面报错时记 FAIL 并继续:整轮中断会让后面的断言一条都不跑,红跑看不出卡在哪。
    if ! json="$("$HOST_BIN" node list)"; then
        bad "node list 失败(控制面报错)"
        return 0
    fi
    assert_jq "节点数 = $want" ".nodes | length == $want" "$json"
    assert_jq "每行都带 support_level 且取值合法" \
        'all(.nodes[]; .support_level == "supported" or .support_level == "experimental")' "$json"
    echo "  node list: $json"
}

# submit_and_check <已渲染的 Plan 文件> <期望 status> <额外 jq 断言>
submit_and_check() {
    local planfile="$1" want="$2" extra="$3" json name
    name="$(basename "$planfile")"
    if ! json="$("$HOST_BIN" plan submit "$planfile")"; then
        bad "$name → plan submit 失败(控制面报错)"
        return 0
    fi
    assert_jq "$name → status=$want" ".status == \"$want\"" "$json"
    assert_jq "$name → $extra" "$extra" "$json"
    echo "  plan submit $name: $json"
    LAST_PLAN_JSON="$json"
}

# run_recovery_skip <被 skip 的 node_id> <advance 输出落盘路径>
# 人审恢复腿:高危腿没提交就没什么可 skip 的 —— 此时直接记 FAIL,
# 不能拿上一次的 PlanRun 接着演练(会把"低危腿的 plan_id"当成高危腿的)。
run_recovery_skip() {
    local legacy="$1" out="$2" plan_id
    if [ -z "${LAST_PLAN_JSON:-}" ]; then
        bad "高危腿未提交,跳过 --skip 演练(没有可推进的 Plan)"
        return 0
    fi
    plan_id="$(jq -r '.plan_id' <<<"$LAST_PLAN_JSON")"
    if "$HOST_BIN" plan advance "$plan_id" --skip "$legacy" | tee "$out" | jq -e . >/dev/null; then
        ok "plan advance --skip $legacy"
        assert_jq "被 skip 的节点已从待跑批次摘掉" \
            "[.batches[].node_ids[]] | index(\"$legacy\") == null" "$(cat "$out")"
        assert_jq "剩余批次只剩另一台(不是\"本来就没有\"的空过)" \
            "(.batches | length) == 1" "$(cat "$out")"
        assert_jq "被 skip 的结果标成人已接受" \
            "any(.results[]; .node_id == \"$legacy\" and (.message | startswith(\"skipped-by-human:\")))" \
            "$(cat "$out")"
    else
        bad "plan advance --skip $legacy"
    fi
}

run_matrix() {
    local hosts=("$@")
    register_hosts "${hosts[@]}"
    verify_node_list "${#hosts[@]}"

    local TMPD
    TMPD="$(mktemp -d)"

    step "低危腿:package.set nginx(low)"
    if render_plan "$FIXTURES/plan-low-nginx.json" "$TMPD/low.json"; then
        submit_and_check "$TMPD/low.json" complete \
            'all(.results[]; .tx_id != "")'
    else
        bad "渲染低危夹具失败"
    fi

    step "高危腿:package.set kernel-core(high,含一台无回滚点)"
    local nosnap="${NODE_IDS[legacy-no-snapshot]:-}"
    if [ -z "$nosnap" ]; then
        bad "没有名为 legacy-no-snapshot 的被控机(--hosts 少了它)"
    elif render_plan "$FIXTURES/plan-high-kernel.json" "$TMPD/high.json" "$nosnap"; then
        submit_and_check "$TMPD/high.json" dirty \
            'any(.results[]; (.message | ascii_downcase | contains("snapshot")))'
    else
        bad "渲染高危夹具失败"
    fi

    step "人审恢复(spec §4.3 三选一;本脚本演练 --skip)"
    # --skip 收的是 Plan 里的 machine_id,渲染后它就是被控机的 node_id。
    run_recovery_skip "${nosnap:-}" "$TMPD/advance.json"
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
