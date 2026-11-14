#!/usr/bin/env bash
#
# Plan C 异构被控 E2E:控制面(fleet 客户端 / node registry / 分桶调度器 / plan CLI)
# 对三台异构被控机发起并跟踪一次跨机 Plan。
#
# 用法:
#   tests/fleet/plan-c-e2e.sh --selftest
#       不 ssh、不依赖真机:校验夹具 + 跑 core 里的真 mTLS 端到端判据
#       (tests/integration/fleet_e2e_test.go:loopback TCP + 双向证书 + 真
#       Scheduler + 真夹具)。CI 与开发机都能跑。
#   tests/fleet/plan-c-e2e.sh --hosts ubuntu24=10.0.0.11 debian12=10.0.0.12 rhel9=10.0.0.13
#       真机矩阵:逐台登记 → 口径 C(声明版本不符)→ 低危 Plan → 高危 Plan
#       (含一台无回滚点)→ 人审恢复 → 混合 Plan(agent 落地 + atomic 停手)。
#       需要:各台机已按 daedalus-agent 的 install.sh 装好并 active;控制面本机
#       有 mTLS 客户端凭证(DAEDALUS_FLEET_CLIENT_CERT / _KEY / DAEDALUS_FLEET_CA)。
#
# 环境变量:
#   DAEDALUS_FLEET_GUEST_ALIASES
#       夹具机器名 → ssh 主机别名的映射(形如 "ubuntu24-a=vmu legacy-no-snapshot=vmc")。
#       控制面地址(`--hosts`)指的是 agent gRPC 端口,与"怎么登进那台机核实"是两件事;
#       口径 C 与混合两条腿要在目标机上独立核实(装没装上 / 收没收到 intent),
#       缺别名即判 FAIL —— 核实不了就不算绿,不拿控制面回报顶替。
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
    step "夹具结构(每份 Plan 必须能被 objectmodel.Plan 解进并通过校验)"
    for f in plan-low-nginx.json plan-high-kernel.json plan-high-container.json \
             plan-version-mismatch.json plan-mixed-atomic-agent.json; do
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
    # Plan 的 intents 是全局的(一套 intents 对所有机器),声明一个"三台都满足"的
    # 版本是不可能的 —— Ubuntu 24.04 是 1.24.0、Debian 12 是 1.22.1、RHEL 9 是
    # 1.20.1。低危腿因此不带任何版本诉求(口径 C 之后,声明版本就必须真能对上)。
    assert_jq "低危夹具不带版本诉求(否则必有一台如实失败)" \
        '(.intents[0] | has("spec") | not) and (.intents[0].expected_diff | contains("1.24.0") | not)' \
        "$(cat "$FIXTURES/plan-low-nginx.json")"
    assert_jq "口径 C 夹具:单台、低危、声明必然不满足的版本 9.9.9" \
        '(.machines | length) == 1 and .intents[0].kind == "package" and .intents[0].target == "nginx" and .intents[0].spec.version == "9.9.9" and .intents[0].risk_class == "low" and .blast_radius.count == 1 and .blast_radius.max_risk == "low" and .blast_radius.kind_breakdown.package == 1' \
        "$(cat "$FIXTURES/plan-version-mismatch.json")"
    assert_jq "混合夹具:agent 在前 atomic 在后、size=1、高危 package 且不带版本" \
        '.intents[0].kind == "package" and (.intents[0] | has("spec") | not) and .intents[0].risk_class == "high" and (.machines | length) == 2 and .machines[0].kind == "agent" and .machines[1].kind == "atomic" and .batching.strategy == "count" and .batching.size == 1 and .blast_radius.count == 2 and .blast_radius.max_risk == "high" and .blast_radius.kind_breakdown.package == 1' \
        "$(cat "$FIXTURES/plan-mixed-atomic-agent.json")"
    assert_jq "高危夹具:max_risk=high 且含 legacy-no-snapshot 目标机" \
        '.blast_radius.max_risk == "high" and ([.machines[].machine_id] | index("legacy-no-snapshot")) != null and (.intents[0].target == "kernel-core")' \
        "$(cat "$FIXTURES/plan-high-kernel.json")"
    assert_jq "container 高危夹具:既无内建回滚点也无执行器凭据,是 RF1 的落点" \
        '.intents[0].kind == "container" and .blast_radius.max_risk == "high" and .blast_radius.kind_breakdown.container == 1' \
        "$(cat "$FIXTURES/plan-high-container.json")"

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
    if render_plan "$FIXTURES/plan-version-mismatch.json" "$tmp/version.json"; then
        assert_jq "口径 C 夹具换成 node_id 且版本诉求原样保留" \
            '.machines[0].machine_id == "n-u24" and .intents[0].spec.version == "9.9.9"' \
            "$(cat "$tmp/version.json")"
    else
        bad "render_plan 口径 C 夹具失败"
    fi
    if render_plan "$FIXTURES/plan-mixed-atomic-agent.json" "$tmp/mixed.json"; then
        # 顺序是这条腿的前提:先让 agent 那台真落地,再让 atomic 那台停手;
        # 渲染只改 machine_id,不动先后 —— 顺序被改写则断言失去意义。
        assert_jq "混合夹具渲染后仍是 agent 在前、atomic 在后" \
            '.machines[0].machine_id == "n-u24" and .machines[1].machine_id == "n-nosnap" and (.machines|length) == 2' \
            "$(cat "$tmp/mixed.json")"
    else
        bad "render_plan 混合夹具失败"
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

    step "目标机别名:夹具机器名 → ssh 主机别名(独立核实用)"
    export DAEDALUS_FLEET_GUEST_ALIASES='ubuntu24-a=vmu legacy-no-snapshot=vmc'
    if [ "$(guest_alias ubuntu24-a)" = "vmu" ] && [ "$(guest_alias legacy-no-snapshot)" = "vmc" ]; then
        ok "别名映射按空白分隔解析(ubuntu24-a→vmu / legacy-no-snapshot→vmc)"
    else
        bad "别名映射解析不对:got $(guest_alias ubuntu24-a) / $(guest_alias legacy-no-snapshot)"
    fi
    if [ -z "$(guest_alias debian12-a)" ]; then
        ok "未登记的机器名解析为空(调用方据此判 FAIL,不静默跳过核实)"
    else
        bad "未登记的机器名不该解析出别名:got $(guest_alias debian12-a)"
    fi
    unset DAEDALUS_FLEET_GUEST_ALIASES

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
declare -A ADDRS=()

# ---------------------------------------------------- 目标机独立核实(ssh) ----
#
# 控制面回报的 tx_id / status 只证明"控制面认为成功了"。下面这组助手直连目标机,
# 用包管理器实况与 agent 自己的审计链核实 —— 控制面说谎或只是落后时才看得出来。

# guest_alias <夹具机器名>:从 DAEDALUS_FLEET_GUEST_ALIASES 取 ssh 主机别名。
guest_alias() {
    local name="$1" pair
    for pair in ${DAEDALUS_FLEET_GUEST_ALIASES:-}; do
        if [ "${pair%%=*}" = "$name" ]; then
            printf '%s' "${pair#*=}"
            return 0
        fi
    done
    printf ''
}

# guest_exec <夹具机器名> <远端命令>:别名缺失即返回非 0(调用方据此记 FAIL)。
guest_exec() {
    local name="$1" cmd="$2" alias
    alias="$(guest_alias "$name")"
    if [ -z "$alias" ]; then
        echo "guest_exec: $name 没有 ssh 别名(设 DAEDALUS_FLEET_GUEST_ALIASES)" >&2
        return 1
    fi
    ssh -o BatchMode=yes -o ConnectTimeout=10 "$alias" "$cmd"
}

# guest_audit_count <夹具机器名>:agent 本地哈希链的行数。
#
# 「这条 intent 到没到过这台机」在 agent 侧唯一可信的凭据就是这条链:链上没有
# 新行 = RPC 没到(fail-closed 的拒绝也会写一行,故"没有记录"本身就是证据)。
# 文件不存在(一次都没写过)按 0 行算。
guest_audit_count() {
    # 重定向必须由 root 那侧的 shell 做(`sudo wc -l < file` 会被登录用户的
    # shell 抢先重定向 → Permission denied),故改用 cat | wc -l。
    guest_exec "$1" 'sudo cat /var/lib/daedalus-agent/audit/chain.jsonl 2>/dev/null | wc -l' |
        tr -dc '0-9'
}

# reset_nginx_baseline <夹具机器名>:把 nginx 从目标机摘干净(幂等)。
#
# 要证明"这一次真的装上了",目标机上留着上一轮的 nginx 会把断言变成 no-op 的
# 空过。卸载完再复核一次现状:复核不通就返回非 0,绝不假装基线干净。
reset_nginx_baseline() {
    local name="$1" cmd
    case "$name" in
        ubuntu24*|debian12*) cmd='sudo DEBIAN_FRONTEND=noninteractive apt-get remove -y -qq nginx' ;;
        rhel9*|centos*|legacy-no-snapshot) cmd='sudo dnf -y remove nginx' ;;
        *) echo "reset_nginx_baseline: 未知发行版 $name" >&2; return 1 ;;
    esac
    guest_exec "$name" "$cmd" >/dev/null 2>&1 || true
    guest_exec "$name" 'dpkg-query -W -f="\${Status}" nginx 2>/dev/null | grep -q "install ok installed" && exit 1
rpm -q nginx >/dev/null 2>&1 && exit 1
exit 0'
}

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
            ADDRS["$name"]="$addr"
            ok "node add $name($addr) → node_id=${NODE_IDS[$name]}"
        else
            bad "node add $name($addr)"
        fi
    done
    build_node_id_map
}

# register_atomic <name> <addr>:把同一台机按 atomic 形态重新登记。
#
# 准入看的是注册表里的节点形态(intentHasRollbackPoint 读 n.Kind),不是 Plan 里
# 声明的 kind;要演示 atomic 分支就必须让注册行真的写着 atomic。同一 agent 进程
# 未重启 → node_id 不变,registry 行被覆盖,不会多出一台。
# support_level 故意不给 --distro:不知道发行版分级就不打 supported(与首轮登记同款)。
register_atomic() {
    local name="$1" addr="$2" out
    if out="$("$HOST_BIN" node add "$addr" --agent-id "$name" --kind atomic --hostname "$name")"; then
        NODE_IDS["$name"]="$(jq -r '.node.node_id' <<<"$out")"
        ADDRS["$name"]="$addr"
        ok "node add $name($addr) --kind atomic → node_id=${NODE_IDS[$name]}"
        build_node_id_map
    else
        bad "node add $name($addr) --kind atomic"
        return 1
    fi
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

# run_version_mismatch_leg <临时目录>:口径 C 的正面证据(daedalus-agent#32)。
#
# 声明 version=9.9.9 是必然不满足的诉求:agent 照常安装(Ubuntu 24.04 源里是
# 1.24.0,声明并不改变装什么),装完核对实际版本主干,不符就如实报
# PERSISTENT_FAILURE —— 不换源、不降级、不自动回滚。断言全部落在 agent 端
# (包管理器实况 + agent 审计链),不采信控制面的 status 文案。
run_version_mismatch_leg() {
    local tmp="$1" machine=ubuntu24-a json ver line
    step "口径 C 腿:声明版本与实际不符 → 如实 PERSISTENT(包仍被装上)"
    if [ -z "${NODE_IDS[$machine]:-}" ]; then
        bad "口径 C 腿缺少目标机 $machine(--hosts 少了它)"
        return 0
    fi
    # 先清基线:nginx 已装时这条腿会退化成 no-op,"装上了"就不再是证据。
    if ! reset_nginx_baseline "$machine"; then
        bad "口径 C 腿:清理 $machine 的 nginx 基线失败(断言会退化成空过,故记 FAIL)"
        return 0
    fi
    ok "基线:$machine 上 nginx 已确认未安装"

    if ! render_plan "$FIXTURES/plan-version-mismatch.json" "$tmp/version.json"; then
        bad "渲染口径 C 夹具失败"
        return 0
    fi
    if ! json="$("$HOST_BIN" plan submit "$tmp/version.json")"; then
        bad "plan-version-mismatch.json → plan submit 失败(控制面报错)"
        return 0
    fi
    LAST_PLAN_JSON="$json"
    echo "  plan submit plan-version-mismatch.json: $json"
    local pid
    pid="$(jq -r '.plan_id' <<<"$json")"
    assert_jq "口径 C 腿 → status=dirty(单台持久失败)" \
        '.status == "dirty"' "$json"
    assert_jq "口径 C 腿 → 结果 PERSISTENT_FAILURE(控制面词表 persistent_failure)" \
        '(.results | length) == 1 and .results[0].outcome == "persistent" and .results[0].result == "persistent_failure"' \
        "$json"
    assert_jq "口径 C 腿 → 文案含 declared=9.9.9 与 actual=" \
        '(.results[0].message | contains("declared=9.9.9")) and (.results[0].message | contains("actual="))' \
        "$json"

    # 独立核实①:包确实被装上了 —— 口径 C 是"装完再核对",失败不撤销安装。
    if ver="$(guest_exec "$machine" 'dpkg-query -W -f="\${Version}" nginx' 2>&1)" && [ -n "$ver" ]; then
        ok "目标机实况:$machine 的 nginx 版本 = $ver"
        case "$ver" in
            1.24.0*) ok "目标机实况:版本主干 1.24.0 = Ubuntu 24.04 源里的实际版本(≠ 声明的 9.9.9)" ;;
            *) bad "目标机实况:nginx 版本 $ver 与预期的 1.24.0* 不符" ;;
        esac
    else
        bad "目标机实况:nginx 没装上($ver)—— 那就不是「装完核对版本」的证据"
    fi
    # 独立核实②:agent 审计链上有这次失败,且原因就是版本比对。
    # 按本 Plan 的 id 过滤,旧轮次留下的同名文案不算数。
    if line="$(guest_exec "$machine" "sudo grep -h \"$pid\" /var/lib/daedalus-agent/audit/chain.jsonl | grep \"declared=9.9.9\" | tail -n 1")" &&
        [ -n "$line" ]; then
        ok "agent 审计链留痕(原文):$line"
    else
        bad "agent 审计链上没有本 Plan 的 declared=9.9.9 失败记录(控制面文案不足以证明是 agent 判的)"
    fi
}

# run_mixed_leg <临时目录>:混合 Plan —— agent 那台真落地,atomic 那台在下发前停手。
#
# 准入用的是**注册表里的节点形态**(intentHasRollbackPoint 读 n.Kind),所以先把
# vmc 按 atomic 重新登记一次;vmu 保持 agent。atomic 那台一次 RPC 都不该收到 ——
# 用 agent 侧审计链的行数证明,而不是只信控制面"没下发"的说法。
#
# 前提:agent 那台的 nginx 必须未装。高危 package 若是 no-op(包已到位),执行器
# 产不出回滚句柄,agent 会按"高危无句柄"拒绝 —— 那条腿就变成了另一件事。
run_mixed_leg() {
    local tmp="$1" agent_m=ubuntu24-a atomic_m=legacy-no-snapshot
    local agent atomic addr json before after agent_ver lines
    step "混合腿:agent 落地 + atomic 停手(整批 dirty,停在 atomic 那一批)"
    agent="${NODE_IDS[$agent_m]:-}"
    atomic="${NODE_IDS[$atomic_m]:-}"
    addr="${ADDRS[$atomic_m]:-}"
    if [ -z "$agent" ] || [ -z "$atomic" ] || [ -z "$addr" ]; then
        bad "混合腿缺少目标机(--hosts 需要 $agent_m 与 $atomic_m)"
        return 0
    fi
    register_atomic "$atomic_m" "$addr" || return 0
    if ! reset_nginx_baseline "$agent_m"; then
        bad "混合腿:清理 $agent_m 的 nginx 基线失败(高危 no-op 会被 agent 按无句柄拒绝)"
        return 0
    fi
    ok "基线:$agent_m 上 nginx 已确认未安装"

    before="$(guest_audit_count "$atomic_m")"
    if [ -z "$before" ]; then
        bad "混合腿:读不到 $atomic_m 的 agent 审计链行数(核实通道不通)"
        return 0
    fi
    ok "混合腿前置:$atomic_m 的 agent 审计链 = $before 行"

    if ! render_plan "$FIXTURES/plan-mixed-atomic-agent.json" "$tmp/mixed.json"; then
        bad "渲染混合夹具失败"
        return 0
    fi
    if ! json="$("$HOST_BIN" plan submit "$tmp/mixed.json")"; then
        bad "plan-mixed-atomic-agent.json → plan submit 失败(控制面报错)"
        return 0
    fi
    LAST_PLAN_JSON="$json"
    echo "  plan submit plan-mixed-atomic-agent.json: $json"
    local pid
    pid="$(jq -r '.plan_id' <<<"$json")"

    assert_jq "混合腿 → status=dirty" '.status == "dirty"' "$json"
    assert_jq "混合腿 → 两个子批、agent 那台在第一批" \
        "(.batches | length) == 2 and (.batches[0].node_ids == [\"$agent\"])" "$json"
    assert_jq "混合腿 → agent 那台真落地:成功 + 非空 tx_id(注册形态 agent)" \
        "any(.results[]; .node_id == \"$agent\" and .node_kind == \"agent\" and .outcome == \"success\" and .tx_id != \"\")" \
        "$json"
    assert_jq "混合腿 → atomic 那台停手:持久失败、无 tx_id、注册形态 atomic" \
        "any(.results[]; .node_id == \"$atomic\" and .node_kind == \"atomic\" and .outcome == \"persistent\" and .tx_id == \"\")" \
        "$json"
    assert_jq "混合腿 → 拒绝文案点名「无回滚点」" \
        "any(.results[]; .node_id == \"$atomic\" and (.message | contains(\"无回滚点\")))" \
        "$json"
    assert_jq "混合腿 → 只有这两条结果(没有第三个节点被动过)" \
        "(.results | length) == 2" "$json"

    # 独立核实①:atomic 那台 0 次 RPC —— agent 审计链行数不变,且链上找不到本 Plan。
    after="$(guest_audit_count "$atomic_m")"
    if [ "$before" = "$after" ]; then
        ok "atomic 那台 0 次 RPC:agent 审计链 $before → $after 行(未增长)"
    else
        bad "atomic 那台审计链从 $before 增到 $after 行:拒绝前不该有任何 RPC 到达"
    fi
    if lines="$(guest_exec "$atomic_m" "sudo grep -c \"$pid\" /var/lib/daedalus-agent/audit/chain.jsonl || true")" &&
        [ "${lines:-1}" = "0" ]; then
        ok "atomic 那台审计链里没有本 Plan($pid)的任何一行(0)"
    else
        bad "atomic 那台审计链里出现了本 Plan($pid)的记录(计数 ${lines:-?})"
    fi

    # 独立核实②:agent 那台真的装上了,且 agent 审计链有这次 apply。
    if agent_ver="$(guest_exec "$agent_m" 'dpkg-query -W -f="\${Version}" nginx' 2>&1)" && [ -n "$agent_ver" ]; then
        ok "agent 那台实况:nginx 已装 = $agent_ver"
    else
        bad "agent 那台实况:nginx 未装上($agent_ver)—— 控制面说成功不算数"
    fi
    if lines="$(guest_exec "$agent_m" "sudo grep -c \"$pid\" /var/lib/daedalus-agent/audit/chain.jsonl || true")" &&
        [ "${lines:-0}" -ge 1 ]; then
        ok "agent 那台审计链里有这次 apply 的记录($pid 共 $lines 行,身份即 intent_id)"
    else
        bad "agent 那台审计链里找不到本 Plan($pid)的记录(计数 ${lines:-?})"
    fi
}

run_matrix() {
    local hosts=("$@")
    register_hosts "${hosts[@]}"
    verify_node_list "${#hosts[@]}"

    local TMPD
    TMPD="$(mktemp -d)"

    # 口径 C 腿放在低危腿前面:此时 vmu 的 nginx 还是基线(未装),"装上后才核对
    # 版本"才是干净的证据;跑过一次之后 vmu 上已有 nginx,再做就只能算 no-op。
    run_version_mismatch_leg "$TMPD"

    # 低危腿不带版本诉求:Plan 的 intents 是**全局的**(一套 intents 对所有机器),
    # Ubuntu 24.04 是 1.24.0 / Debian 12 是 1.22.1 / RHEL 9 是 1.20.1 —— 声明任何一个
    # 版本都等于让其余两台在口径 C 下如实失败。要验版本口径就走上面那条专门的腿。
    step "低危腿:package.set nginx(low)"
    if render_plan "$FIXTURES/plan-low-nginx.json" "$TMPD/low.json"; then
        submit_and_check "$TMPD/low.json" complete \
            'all(.results[]; .tx_id != "")'
    else
        bad "渲染低危夹具失败"
    fi

    step "高危腿:container(high,该 kind 既无内建回滚点也无执行器凭据 → 下发前拒)"
    local nosnap="${NODE_IDS[legacy-no-snapshot]:-}"
    if [ -z "$nosnap" ]; then
        bad "没有名为 legacy-no-snapshot 的被控机(--hosts 少了它)"
    elif render_plan "$FIXTURES/plan-high-container.json" "$TMPD/high.json" "$nosnap"; then
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

    # 混合腿必须排在 container 腿之后:两条腿都用 vmc,前者要求它按 agent 登记,
    # 后者要把它重登记成 atomic(行按 node_id 覆盖,顺序反了 container 腿就换了分支)。
    run_mixed_leg "$TMPD"
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
