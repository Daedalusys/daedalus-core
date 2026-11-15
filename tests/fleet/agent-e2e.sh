#!/usr/bin/env bash
#
# agent 侧异构 E2E 驱动:把 daedalus-agent 装到 Ubuntu / Debian / RHEL 三类
# 宿主上,经控制面发一次低危 Plan,并**回到目标机上独立核实**那次变更真的发生了。
#
# 为什么"独立核实"是硬要求:agent 侧曾经的 apply 是个只打日志就返回 nil 的桩
# (applyStub),它让 ApplyIntent 回报 SUCCESS 而机器上什么都没变,控制面因此
# 把 Plan 判成 complete。所以本脚本**不采信**控制面 / agent 的任何自我汇报
# (status / result / tx_id 都不算数),只采信在目标机上跑出来的包管理器事实
# (dpkg-query / rpm -q)。判定与 plan-c-e2e.sh 互补:那个验"控制面编排对了",
# 本脚本验"被控机上那件事真的发生了"。
#
# 用法:
#   tests/fleet/agent-e2e.sh --selftest
#       不 ssh、不碰真机。复用 core 既有的本地 fake agent 端到端判据
#       (tests/integration 的 TestFleetE2E:真 mTLS + 真 Scheduler + 真夹具),
#       再单测本脚本自己的判定原语,含一个"说谎的 agent"负对照。
#   tests/fleet/agent-e2e.sh --hosts ubuntu=<ssh别名>[@<可拨地址>] [debian12=... rhel9=...]
#       真机矩阵:逐台装 agent → node add → node list → 低危 Plan + 目标机独立
#       核实 → 高危 Plan(container:既无内建回滚点也无执行器凭据)当场 dirty,
#       且目标机 agent 审计链在提交前后零增长(证明一个 RPC 都没发)。
#       需要:
#         - 控制面 mTLS 客户端凭证:DAEDALUS_FLEET_CLIENT_CERT / _KEY / DAEDALUS_FLEET_CA
#         - agent 出货包(二选一,**都缺或都给即退出 1**):
#             DAEDALUS_AGENT_RELEASE=vX.Y.Z  从 GitHub Release 取件并核对
#                                            SHA256SUMS(发布态正路;装完逐台断言
#                                            被控端自报 `<版本> commit=`)
#             DAEDALUS_AGENT_TARBALL=<path>  本地 tarball(开发态旁路,内含 bin/ 与
#                                            scripts/;不校验,也不断言版本)
#           取件跑在驱动机上,故驱动机需有可用的 gh(已 gh auth login);
#           目标机不需要 gh —— 包由本驱动经 ssh 送过去。
#         - 目标机有 systemd 与免密 sudo(install.sh 的前置)
#         - **各目标机的 agent 必须共用同一张 CA**:agent 首启会自举自己的 CA,而
#           控制面对一个 Plan 里的所有节点只持有一份客户端证书,三台各一套 CA 时
#           plan submit 谁都连不上。故须在 install 之前把同一张 CA 与各自签发的
#           服务端证书铺进 /etc/daedalus-agent/tls/(覆盖 ca.pem/agent.pem/agent.key,
#           配置里 cert_path/key_path/ca_path/ca_key_path 四个路径都非空才会开 mTLS)。
#           服务端证书的 SAN 必须是控制面拨号地址的 host 段(本驱动的用法是 localhost)。
#         - 目标机需已可用的包索引:cloud image 预置的索引可能指向归档里已被撤下
#           的版本,不刷新会让 package.set 因"取不到包"这种与被测对象无关的原因失败。
#
# 退出码:0 全部断言通过;非 0 表示有断言失败(逐条打印)。
#
# 证据:真机跑完把观察照实填进 tests/fleet/EVIDENCE/<日期>-agent-e2e.md。
# 与期望不符之处不隐瞒;发现缺陷开 issue 并把编号写进证据文件。

set -euo pipefail

# 真机驱动最怕"静默中断":set -e 下任何意外失败都会让整轮以半截日志收场,现场
# 看不出是哪一步。留一条 ERR 线索指名出错行,便于事后定位。
trap 'echo "agent-e2e: 第 $LINENO 行意外失败(exit=$?)" >&2' ERR

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
FIXTURES="$HERE/fixtures"
HOST_BIN="${DAEDALUS_HOST_BIN:-$ROOT/bin/daedalus-host}"
SSH="${DAEDALUS_SSH:-ssh}"
JQ="${DAEDALUS_JQ:-jq}"
AGENT_PORT="${DAEDALUS_AGENT_PORT:-9443}"
WANT_PKG="${DAEDALUS_E2E_PACKAGE:-nginx}"
POLICY_FILE="$ROOT/files/system/opt/daedalus/shared/policy.toml"

# 取件层:release 资产名推导 / 下载 + 校验和 / 装完的版本断言。三个函数只在这一处
# 定义,真机矩阵与自检共用同一份判定代码。
# shellcheck source=tests/fleet/lib/agent-release.sh
. "$HERE/lib/agent-release.sh"

# 目标机审计链取证(数行数):与 plan-c 混合腿共用同一份实现,高危腿的"零 RPC"
# 判据就建在它之上。
# shellcheck source=tests/fleet/lib/agent-audit.sh
. "$HERE/lib/agent-audit.sh"

# 本轮解析出的 agent 出货 tarball(绝对路径),由 resolve_agent_tarball 填。全轮
# 只解析一次:release 模式下重复下载是纯浪费,且每台机器各下一份会让"哪个包装的"
# 出现多个版本。
AGENT_TARBALL_RESOLVED=""

# 用仓内那份真实 policy.toml 判风险,而不是回退内置默认值 —— 判定表本身就是
# 单一事实源,用副本会掩盖"策略改动影响 Plan 判定"这类问题。操作者已显式
# 指定 DAEDALUS_POLICY_PATH 时不覆盖。
if [ -z "${DAEDALUS_POLICY_PATH:-}" ] && [ -r "$POLICY_FILE" ]; then
    export DAEDALUS_POLICY_PATH="$POLICY_FILE"
fi

pass=0
fail=0

ok()   { echo "  ok   - $*"; pass=$((pass + 1)); }
bad()  { echo "  FAIL - $*" >&2; fail=$((fail + 1)); }
step() { echo; echo "== $*"; }

# assert_jq <描述> <jq 表达式> <JSON>
assert_jq() {
    local desc="$1" expr="$2" json="$3"
    if printf '%s' "$json" | "$JQ" -e "$expr" >/dev/null 2>&1; then
        ok "$desc"
    else
        bad "$desc(jq: $expr)"
        echo "    实际输出: $json" >&2
    fi
}

# ------------------------------------------------------------- 判定原语 ----
#
# 全部是纯函数:不 ssh、不打印、不计数。真机矩阵与 --selftest 走同一份判定
# 代码 —— 自测过的判定就是真跑时用的判定,不存在两套逻辑漂移。

# plan_is_complete <plan-status-json>:控制面回报的 Plan 是否收尾为 complete。
plan_is_complete() {
    printf '%s' "$1" | "$JQ" -e '.status == "complete"' >/dev/null 2>&1
}

# pkg_probe_cmd <distro> <pkg>:目标机上用于**独立**观测包状态的最小命令。
# 按发行版选宿主原生包管理器(Ubuntu/Debian 走 dpkg,其余走 rpm),且只查不装,
# 与 agent 侧声称做了什么完全解耦。
#
# 两处易踩的坑,都在真机上撞过:
#   - dpkg-query 的字段语法是 `${Status}`,不是 rpm 的 `%{Status}`。写错时未装
#     的包会报 "no packages found"(看不出问题),装了之后才原样吐出 `%{Status}`
#     —— 判定因此 fail-closed 地判"没装上",把真装好的机器误报成没装。
#   - rpm/dpkg 的提示语是 gettext 本地化的,zh_CN 宿主上 "package nginx is not
#     installed" 会变成"未安装软件包 nginx"。判定虽对两种文本都 fail-closed,
#     但把输出钉成英文能去掉一整类"换个 locale 就变脸"的不确定性,也让证据文件
#     里的输出可跨宿主比对。
pkg_probe_cmd() {
    local distro="$1" pkg="$2"
    case "$distro" in
        ubuntu|debian)
            printf "LC_ALL=C dpkg-query -W -f='\${Status} \${Version}' %s 2>&1 || true" "$pkg"
            ;;
        *)
            printf 'LC_ALL=C rpm -q %s 2>&1 || true' "$pkg"
            ;;
    esac
}

# pkg_installed_from_output <distro> <pkg> <probe-output>:把观测输出判成
# "确证已装"。dpkg 认 "install ok installed" 这一行;rpm 认首行是
# "<pkg>-<数字>"(rpm -q 命中时的输出形状)。任何缺证(空输出、探测报错、
# 形状不认识)一律判**没装上** —— 与 fail-closed 同向:证不出来就是没装上。
pkg_installed_from_output() {
    local distro="$1" pkg="$2" out="$3"
    case "$distro" in
        ubuntu|debian)
            [[ "$out" == *"install ok installed"* ]]
            return
            ;;
    esac
    local first="${out%%$'\n'*}"
    [[ "$first" == "$pkg"-* ]] || return 1
    local rest="${first#"$pkg"-}"
    [[ "${rest:0:1}" == [0-9] ]]
}

# low_risk_verdict <plan-json> <distro> <pkg> <probe-output>:低危腿的**合取**
# 判定 —— 控制面说 complete **且** 目标机上确证已装。目标机那一侧证不出来
# 时,不论控制面怎么报,判定就是不通过(applyStub 教训的钉子)。
low_risk_verdict() {
    plan_is_complete "$1" || return 1
    pkg_installed_from_output "$2" "$3" "$4"
}

# 高危腿的选点**不再**由本驱动回答"哪个 kind 在这个节点上没有回滚点":那张
# 本地表曾经把 package 判成无回滚点(与控制面相反),于是高危 intent 被真的
# 下发(内核包真被装上过,core#141)。判定唯一事实源在 SDK 的
# objectmodel.AdmitsHighRisk(控制面 internal/fleet 调它),驱动只负责选一台
# 按夹具口径能确证"拒在下发之前"的机器 —— 口径见 run_matrix 的高危腿段。

# ------------------------------------------------------------ Plan 生成 ----

# render_machines <node_id:support_level>...:Plan 的 machines 数组。
#
# machine_id 必须等于注册表里的 node_id。agent 侧 Register 返回的 node_id 是
# "<agent_id>-<随机后缀>",不是 node add 时给的 agent_id,所以静态夹具里的
# machine_id 在真机上对不上 —— 必须按 node list 的实测值现场生成。注册形态恒为
# agent:本驱动只用 node add 注册 agent 形态。
render_machines() {
    local machines="[]" m
    for m in "$@"; do
        machines="$(printf '%s' "$machines" | "$JQ" --arg nid "${m%%:*}" --arg lv "${m##*:}" \
            '. + [{machine_id: $nid, kind: "agent", support_level: $lv}]')"
    done
    printf '%s' "$machines"
}

# render_low_plan <pkg> <node_id:support_level>...:低危腿的 Plan。
render_low_plan() {
    local pkg="$1"; shift
    render_plan "agent-e2e-low" low "$pkg" "$@"
}

# render_high_plan <node_id:support_level>...:高危腿的 Plan。
#
# intents 与 blast_radius 原样取夹具 fixtures/plan-high-container.json,只把
# machines 换成本轮 node list 的实测值。
#
# 为什么 kind 是 container:它既没有内建回滚点、agent 侧也没有它的执行器凭据,
# 所以控制面在**下发之前**就拒,目标机一个 RPC 都收不到。旧实现拼的是
# package + 内核包(自带执行器,高危会被真的执行),core#141 的真机日志
# 就是 agent 真去 apt 装内核包。
render_high_plan() {
    local fixture="$FIXTURES/plan-high-container.json" machines n
    machines="$(render_machines "$@")"
    n="$(printf '%s' "$machines" | "$JQ" 'length')"
    "$JQ" --argjson machines "$machines" --argjson n "$n" --arg id "agent-e2e-high" '
        .id = $id
        | .machines = $machines
        | .blast_radius.count = $n' "$fixture"
}

# render_plan <plan-id> <risk> <target> <node_id:support_level>...
render_plan() {
    local plan_id="$1" risk="$2" target="$3"; shift 3
    local machines n
    machines="$(render_machines "$@")"
    n="$(printf '%s' "$machines" | "$JQ" 'length')"
    "$JQ" -n --argjson machines "$machines" --arg id "$plan_id" \
        --arg risk "$risk" --arg target "$target" --argjson n "$n" '{
            id: $id,
            intents: [{
                kind: "package",
                target: $target,
                spec: {desired_state: "present"},
                expected_diff: ("+ " + $target + " (present)"),
                risk_class: $risk
            }],
            machines: $machines,
            batching: {strategy: "count", size: 1},
            blast_radius: {
                count: $n,
                kind_breakdown: {package: 1},
                max_downtime_ns: 30000000000,
                max_risk: $risk
            }
        }'
}

# ------------------------------------------------------------- 目标机侧 ----

# os_field <os-release 全文> <字段名>:取 /etc/os-release 的某个字段。
os_field() {
    printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1 | tr -d '"'
}

# remote_os_release <ssh>:读目标机的 /etc/os-release(原文,进证据文件)。
remote_os_release() {
    "$SSH" -n "$1" 'cat /etc/os-release' 2>/dev/null
}

# remote_agent_id <ssh>:agent 的身份来自它自己的配置(install.sh 的 postinstall
# 写 /etc/daedalus-agent/config.yaml);控制面发起 Register 时用同一个值。
#
# 远端命令整体单引号包住、内部只用双引号,避免嵌套引号被本地 shell 先吃掉一层
# —— 早先那种 `tr -d ' "'` 写法在远端留下一个未闭合的单引号,ssh 直接以 2 退出,
# 又撞上 set -e 把整个矩阵静默打断。
remote_agent_id() {
    "$SSH" -n "$1" 'sudo sed -n "s/^agent_id:[[:space:]]*//p" /etc/daedalus-agent/config.yaml' 2>/dev/null
}

# remote_first_addr <ssh>:目标机第一个非回环 IPv4(可拨地址缺省时的取值)。
remote_first_addr() {
    "$SSH" -n "$1" "hostname -I 2>/dev/null | awk '{print \$1}'" 2>/dev/null
}

# resolve_agent_tarball:决定本轮从哪儿取 agent 出货包,返回 0 则
# AGENT_TARBALL_RESOLVED 是一个可读 tarball 的绝对路径。
#
# 两种来源**二选一**,都缺或都给都算配置错误(退出 1):猜一个来源比直接报错更
# 危险 —— 猜成 release 会拉不出东西,猜成本地包会把开发机上的裸构建当成发布版
# 装上机器,而那正是本改造要退役的做法。
resolve_agent_tarball() {
    local rel="${DAEDALUS_AGENT_RELEASE:-}" tb="${DAEDALUS_AGENT_TARBALL:-}"
    if [ -n "$rel" ] && [ -n "$tb" ]; then
        echo "DAEDALUS_AGENT_RELEASE 与 DAEDALUS_AGENT_TARBALL 只能给一个(两个来源并存无法判断该信谁)" >&2
        return 1
    fi
    if [ -n "$rel" ]; then
        local dir
        dir="$(mktemp -d)"
        if ! AGENT_TARBALL_RESOLVED="$(fetch_agent_release "$rel" "$dir")"; then
            echo "从 release ${rel} 取件失败(见上一条);本地自造包可改走 DAEDALUS_AGENT_TARBALL" >&2
            return 1
        fi
        echo "  取件来源:release ${rel} → ${AGENT_TARBALL_RESOLVED}"
        return 0
    fi
    if [ -n "$tb" ]; then
        if [ ! -r "$tb" ]; then
            echo "DAEDALUS_AGENT_TARBALL 指定的包不可读:${tb}" >&2
            return 1
        fi
        AGENT_TARBALL_RESOLVED="$(cd "$(dirname "$tb")" && pwd)/$(basename "$tb")"
        echo "  取件来源:本地 tarball(开发态旁路,不校验来源、不断言版本)→ ${AGENT_TARBALL_RESOLVED}"
        return 0
    fi
    echo "缺少 agent 取件来源,二选一:" >&2
    echo "  DAEDALUS_AGENT_RELEASE=vX.Y.Z  从 GitHub Release 取件并核对 SHA256SUMS(发布态正路)" >&2
    echo "  DAEDALUS_AGENT_TARBALL=<path>  用本地 tarball(开发态旁路,内含 bin/ 与 scripts/)" >&2
    return 1
}

# install_agent_on <key> <ssh>:把**已解析好的**出货 tarball 送到目标机,解出它
# 自带的 scripts/ 后跑 install.sh(install.sh 以自身所在目录为 SCRIPT_DIR 找同级
# 脚本)。取件来源的解析只在 resolve_agent_tarball 做一次,这里不重复下载。
install_agent_on() {
    local key="$1" alias="$2" tarball="${AGENT_TARBALL_RESOLVED:-}"
    if [ -z "$tarball" ] || [ ! -r "$tarball" ]; then
        echo "  ${key}: 没有可读的 agent 出货 tarball(先跑 resolve_agent_tarball)" >&2
        return 1
    fi
    local work base
    work="/tmp/daedalus-agent-e2e-$$"
    base="$(basename "$tarball")"
    "$SSH" "$alias" "mkdir -p '$work'" || return 1
    "$SSH" "$alias" "cat > '$work/$base'" < "$tarball" || return 1
    "$SSH" "$alias" bash -s -- "$work" "$base" <<'EOS'
set -euo pipefail
work="$1"; base="$2"
mkdir -p "$work"
tar -xzf "$work/$base" -C "$work"
inst="$(find "$work" -maxdepth 3 -type f -name install.sh -path '*/scripts/*' | head -1)"
[ -n "$inst" ] || { echo "tarball 内找不到 scripts/install.sh" >&2; exit 1; }
sudo bash "$inst" "$work/$base"
EOS
}

# assert_pkg_installed_on_target <key> <ssh> <distro> <pkg>:在目标机上跑最小
# 探测,按输出判定,并把**命令原文与原始输出**都打出来(证据文件要原文)。
assert_pkg_installed_on_target() {
    local key="$1" alias="$2" distro="$3" pkg="$4"
    local cmd out rc=0
    cmd="$(pkg_probe_cmd "$distro" "$pkg")"
    out="$("$SSH" -n "$alias" "$cmd" 2>&1)" || rc=$?
    echo "  [独立核实] ${key}: \$ ${cmd}"
    printf '%s\n' "$out" | sed 's/^/    /'
    if pkg_installed_from_output "$distro" "$pkg" "$out"; then
        ok "${key}: 目标机上确证 ${pkg} 已装(独立于控制面/agent 的自我汇报)"
    else
        bad "${key}: 目标机上并未确证 ${pkg} 已装 —— 控制面报 complete 也不能算数"
    fi
}

# ---------------------------------------------------------------- selftest ----

# check_probe <描述> <distro> <pkg> <期望 installed|absent> <探测输出>
check_probe() {
    local desc="$1" distro="$2" pkg="$3" want="$4" out="$5" got=absent
    if pkg_installed_from_output "$distro" "$pkg" "$out"; then
        got=installed
    fi
    if [ "$got" = "$want" ]; then
        ok "$desc"
    else
        bad "$desc(期望 $want,实际 $got)"
    fi
}

# build_host_bin:确保有一份 daedalus-host 可执行(没有就地构建一份临时的)。
build_host_bin() {
    if [ -x "$HOST_BIN" ]; then
        echo "$HOST_BIN"
        return 0
    fi
    local tmp
    tmp="$(mktemp -d)/daedalus-host"
    if ! (cd "$ROOT" && go build -o "$tmp" ./cmd/daedalus-host); then
        return 1
    fi
    echo "$tmp"
}

run_selftest() {
    step "控制面判定(复用 core 既有本地 fake agent 判据,不重复实现控制面半)"
    if (cd "$ROOT" && go test ./tests/integration/ -run TestFleetE2E -count=1 -v 2>&1 | sed 's/^/  /'); then
        ok "低危 Plan 全量落地 + 高危遇无回滚点节点当场 dirty(真 mTLS + 真 Scheduler)"
    else
        bad "TestFleetE2E 未通过(见上方输出)"
    fi

    step "目标机独立核实:包管理器输出判定(不采信任何自我汇报)"
    check_probe "dpkg 报告 install ok installed → 判已装" \
        ubuntu "$WANT_PKG" installed "$(printf 'install ok installed\n1.24.0-2ubuntu7\n')"
    check_probe "dpkg 单行格式 install ok installed <版本> → 判已装" \
        ubuntu "$WANT_PKG" installed "$(printf 'install ok installed 1.24.0-2ubuntu7.18')"
    check_probe "dpkg 字段占位符没被展开(写成 rpm 的 %{Status})→ 判没装上" \
        ubuntu "$WANT_PKG" absent "$(printf '%%{Status}\n%%{Version}\n')"
    check_probe "dpkg 报告 no packages found → 判没装上" \
        ubuntu "$WANT_PKG" absent "$(printf 'dpkg-query: no packages found matching nginx\n')"
    check_probe "rpm 报告 nginx-1.24.0-2.el9.x86_64 → 判已装" \
        rhel "$WANT_PKG" installed "$(printf 'nginx-1.24.0-2.el9.x86_64\n')"
    check_probe "rpm 报告 package nginx is not installed → 判没装上" \
        rhel "$WANT_PKG" absent "$(printf 'package nginx is not installed\n')"
    check_probe "rpm 报本地化提示(zh_CN 的\"未安装软件包\")→ 仍判没装上" \
        rhel "$WANT_PKG" absent "$(printf '未安装软件包 nginx \n')"
    check_probe "探测命令失败(空输出)→ 判没装上(fail-closed,不猜)" \
        ubuntu "$WANT_PKG" absent ""
    check_probe "rpm 输出形状不认识 → 判没装上" \
        rhel "$WANT_PKG" absent "$(printf 'nginx-devel is not a package\n')"

    step "高危腿选点口径:夹具 kind 必须是 container(既无内建回滚点也无执行器凭据)"
    # 选 package 就是 core#141 的复发:agent 有它的执行器,高危 intent 会被真的
    # 下发(实测跑了 apt/dnf)。判定表的唯一事实源在 SDK 的
    # objectmodel.AdmitsHighRisk,驱动不自持第二张表 —— 这里只钉夹具的 kind。
    if grep -qE '"kind"[[:space:]]*:[[:space:]]*"container"' "$FIXTURES/plan-high-container.json"; then
        ok "高危夹具 kind=container(无内建回滚点/无执行器凭据)"
    else
        bad "高危夹具不是 container —— 高危腿会真的下发(见 core#141)"
    fi
    # 哨兵:驱动本身不许再出现真包夹具(内核包在 RHEL 源里真实存在,会被真的
    # 装上,不是"一定会失败"的安全选择)。模式拆成两段拼起来 —— 整串写在这里
    # 会让哨兵命中自己这一行,永远红。
    local banned_pkg="kernel""-core"
    if grep -qF "$banned_pkg" "$HERE/agent-e2e.sh"; then
        bad "驱动里不得再出现内核包高危夹具(会真装内核包)"
    else
        ok "驱动里没有内核包高危夹具"
    fi
    # 夹具对而渲染路径改回 package 时,上面两条都拦不住:所以真渲染一遍再验。
    local hi_rendered
    hi_rendered="$(render_high_plan "selftest-node-a:supported")"
    assert_jq "渲染出的高危 Plan:intent kind=container、单台目标机、高危" \
        '.intents[0].kind == "container" and .blast_radius.max_risk == "high" and (.machines | length) == 1 and .blast_radius.count == 1' \
        "$hi_rendered"

    step "负对照:说谎的 agent(控制面报 complete,目标机上并没有装上)"
    local lying='{"plan_id":"selftest","status":"complete","results":[{"node_id":"n1","outcome":"success","result":"success","tx_id":"tx-n1","message":""}]}'
    if low_risk_verdict "$lying" ubuntu "$WANT_PKG" \
        "$(printf 'dpkg-query: no packages found matching nginx\n')"; then
        bad "Plan complete + 目标机无此包,判定却通过 —— 独立核实形同虚设"
    else
        ok "Plan complete 但目标机无此包 → 判定不通过(applyStub 的教训已钉住)"
    fi
    if low_risk_verdict "$lying" ubuntu "$WANT_PKG" \
        "$(printf 'install ok installed\n1.24.0-2\n')"; then
        ok "正对照:Plan complete + 目标机确证已装 → 判定通过"
    else
        bad "正对照失败:目标机确证已装却判不通过"
    fi

    step "生成的 Plan 过真校验器(空注册表 ⇒ 只应在\"节点未注册\"处停)"
    local host_bin tmp plan rc err
    if ! host_bin="$(build_host_bin)"; then
        bad "构建 daedalus-host 失败(先 just go-build)"
        return
    fi
    tmp="$(mktemp -d)"
    plan="$(render_low_plan "$WANT_PKG" "selftest-node-a:supported" "selftest-node-b:experimental")"
    printf '%s' "$plan" > "$tmp/low.json"
    rc=0
    err="$(DAEDALUS_FLEET_DIR="$tmp/fleet" "$host_bin" plan submit "$tmp/low.json" 2>&1 >/dev/null)" || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$err" == *"未注册"* ]]; then
        ok "生成的低危 Plan 通过 Plan 校验(在\"节点未注册\"处才停,说明结构合法)"
    else
        bad "生成的低危 Plan 未按预期被拒(rc=$rc, stderr=$err)"
    fi
    # 反证:把爆炸半径改坏,校验器必须拒 —— 否则上一条只是碰巧没走到校验。
    "$JQ" '.blast_radius.count = 99' "$tmp/low.json" > "$tmp/bad.json"
    rc=0
    err="$(DAEDALUS_FLEET_DIR="$tmp/fleet" "$host_bin" plan submit "$tmp/bad.json" 2>&1 >/dev/null)" || rc=$?
    if [[ "$err" == *"校验失败"* ]]; then
        ok "反证:改坏 blast_radius.count 后校验器确实拒(说明上一条不是空过)"
    else
        bad "反证失败:坏 Plan 未被校验器拒(rc=$rc, stderr=$err)"
    fi
    # 高危腿的 Plan 现在整份来自夹具(intent 无 spec、kind_breakdown 是
    # container,形状与低危那条不同),所以单独过一遍校验器:夹具被改坏时应当
    # 在这里红,而不是等到真机 submit 才报一句看不懂的错。
    render_high_plan "selftest-node-a:supported" > "$tmp/high.json"
    rc=0
    err="$(DAEDALUS_FLEET_DIR="$tmp/fleet" "$host_bin" plan submit "$tmp/high.json" 2>&1 >/dev/null)" || rc=$?
    if [ "$rc" -ne 0 ] && [[ "$err" == *"未注册"* ]]; then
        ok "高危腿渲染出的 Plan 通过 Plan 校验(同样只在\"节点未注册\"处停)"
    else
        bad "高危腿渲染出的 Plan 未按预期被拒(rc=$rc, stderr=$err)"
    fi
    rm -rf "$tmp"
}

# -------------------------------------------------------------- 真机矩阵 ----

run_matrix() {
    local specs=("$@")
    local n="${#specs[@]}"
    local -a keys=() sshs=() dials=() distros=() vers=() agent_ids=()
    local i spec key part alias addr osrel id ver

    for ((i = 0; i < n; i++)); do
        spec="${specs[i]}"
        key="${spec%%=*}"
        part="${spec#*=}"
        if [ "$key" = "$spec" ] || [ -z "$part" ]; then
            echo "主机规格需为 <key>=<ssh别名>[@<可拨地址>]:$spec" >&2
            exit 2
        fi
        alias="${part%%@*}"
        addr=""
        if [[ "$part" == *@* ]]; then
            addr="${part##*@}"
        fi
        keys+=("$key"); sshs+=("$alias"); dials+=("$addr")
        distros+=(""); vers+=(""); agent_ids+=("")
    done

    step "被控机基础信息(目标机自己的 /etc/os-release;不采信 agent 自报)"
    for ((i = 0; i < n; i++)); do
        if ! osrel="$(remote_os_release "${sshs[i]}")"; then
            bad "${keys[i]}: ssh ${sshs[i]} 不可达或读不到 /etc/os-release"
            continue
        fi
        id="$(os_field "$osrel" ID)"
        ver="$(os_field "$osrel" VERSION_ID)"
        distros[i]="${id,,}"
        vers[i]="$ver"
        ok "${keys[i]}(${sshs[i]}): ID=${id} VERSION_ID=${ver} PRETTY_NAME=\"$(os_field "$osrel" PRETTY_NAME)\""
        echo "    uname -r: $("$SSH" -n "${sshs[i]}" 'uname -r' 2>/dev/null)"
    done

    step "逐台安装 daedalus-agent(出货 install.sh)"
    echo "  agent 包:${AGENT_TARBALL_RESOLVED}"
    # 装成功的下标集合:版本断言只对装成功的台子跑,"装失败"与"版本不对"是两种
    # 缺陷,不该混成一条。
    local -a installed=()
    for ((i = 0; i < n; i++)); do
        installed[i]=0
        if install_agent_on "${keys[i]}" "${sshs[i]}"; then
            installed[i]=1
            ok "${keys[i]}: install.sh 退出 0"
            # 单台取不到 agent_id 不该把整个矩阵打断(set -e 下命令替换失败会直接
            # 退出):记成失败继续,让后面的台子照样跑到。
            agent_ids[i]="$(remote_agent_id "${sshs[i]}")" || agent_ids[i]=""
            if [ -z "${agent_ids[i]}" ]; then
                bad "${keys[i]}: install.sh 成功但取不到 agent_id(检查 /etc/daedalus-agent/config.yaml)"
            fi
        else
            bad "${keys[i]}: install.sh 失败"
        fi
    done

    # 版本断言:装完必须能自证"机器上跑的就是这个 tag 的发布产物"。判据是二进制
    # 自己吐的 version(不采信包名、不采信 install.sh 的回显):裸构建只会自报
    # `0.1.0-dev`,拼不出 `commit=`,所以这一条同时钉住"版本对"与"是发布产物"。
    # 单台失败记 bad 而不中断:整轮的价值在于"哪几台不对",不是第一台不对就收工。
    step "被控端版本断言(release 模式:须自报 <版本> commit=)"
    if [ -z "${DAEDALUS_AGENT_RELEASE:-}" ]; then
        echo "  跳过:本轮走 DAEDALUS_AGENT_TARBALL 开发态旁路,没有可断言的发布版本"
    else
        local expect="${DAEDALUS_AGENT_RELEASE#v}" checked=0
        for ((i = 0; i < n; i++)); do
            if [ "${installed[i]}" -ne 1 ]; then
                echo "  ${keys[i]}: 安装没成功,版本断言无从谈起(已在上一步记 FAIL)"
                continue
            fi
            checked=$((checked + 1))
            if assert_agent_version "${sshs[i]}" "$expect"; then
                ok "${keys[i]}: 自报版本含 ${expect} commit=(确是 release ${DAEDALUS_AGENT_RELEASE} 的产物)"
            else
                bad "${keys[i]}: 自报版本不是 ${expect} 的发布构建(装错包,或装成了裸构建)"
            fi
        done
        if [ "$checked" -eq 0 ]; then
            bad "没有任何一台装成功,版本断言一台也没跑成"
        fi
    fi

    step "登记被控机(daedalus-host node add)"
    for ((i = 0; i < n; i++)); do
        local addr
        addr="${dials[i]}"
        if [ -z "$addr" ]; then
            addr="$(remote_first_addr "${sshs[i]}")"
        fi
        # 显式给的 <可拨地址> 可能自带端口(localhost:9471),那就原样用;只有没带
        # 端口的裸地址(含 hostname -I 取回的那类)才补默认端口。
        if [[ "$addr" != *:* ]]; then
            addr="${addr}:${AGENT_PORT}"
        fi
        dials[i]="$addr"
        if [ -z "${agent_ids[i]}" ]; then
            bad "${keys[i]}: 取不到 agent_id,跳过登记"
            continue
        fi
        if "$HOST_BIN" node add "${dials[i]}" --agent-id "${agent_ids[i]}" \
            --distro "${distros[i]}" --distro-version "${vers[i]}" --hostname "${sshs[i]}"; then
            ok "${keys[i]}: node add ${dials[i]}(agent_id=${agent_ids[i]})"
        else
            bad "${keys[i]}: node add ${dials[i]} 失败"
        fi
    done

    step "node list 契约"
    local json
    json="$("$HOST_BIN" node list)"
    assert_jq "节点数 = $n" ".nodes | length == $n" "$json"
    assert_jq "每行 support_level ∈ {supported, experimental}" \
        'all(.nodes[]; .support_level == "supported" or .support_level == "experimental")' "$json"
    echo "  node list: $json"

    # 节点形态按 node.addr 对齐回主机下标:kind 只用于高危腿选点(见下),快照
    # 能力只进证据输出 —— 准入判定在控制面(objectmodel.AdmitsHighRisk),驱动
    # 不复制那张表。
    local -a caps=() node_kinds=()
    for ((i = 0; i < n; i++)); do
        local row
        row="$("$JQ" -r --arg a "${dials[i]}" \
            '[.nodes[] | select(.addr == $a) | "\(.kind) \(.snapshot_capability)"] | first // ""' <<<"$json")"
        node_kinds[i]="${row%% *}"
        caps[i]="${row##* }"
        echo "  node ${keys[i]}: addr=${dials[i]} kind=${node_kinds[i]:-<无此节点>} snapshot_capability=${caps[i]:-<无>}"
    done

    step "低危腿:package.set ${WANT_PKG}(low)→ 目标机独立核实"
    # node list 里的节点**全部**进低危腿:低危 intent 不因缺回滚点被拒,回滚点
    # 按 intent kind 判是 agent 内部的事,不是低危腿的选点条件。
    local -a low_pairs=() low_keys=() low_sshs=() low_distros=()
    local low_json
    for ((i = 0; i < n; i++)); do
        local pair
        pair="$("$JQ" -r --arg a "${dials[i]}" \
            '[.nodes[] | select(.addr == $a) | "\(.node_id):\(.support_level)"] | first // ""' <<<"$json")"
        if [ -z "$pair" ]; then
            bad "${keys[i]}: 不在 node list 里(登记失败?),低危腿跳过该节点"
            continue
        fi
        low_pairs+=("$pair")
        low_keys+=("${keys[i]}"); low_sshs+=("${sshs[i]}"); low_distros+=("${distros[i]}")
    done

    # 基线闸门:本轮要证明"装上了",所以执行前必须确证**没装**。上一轮留下的包
    # 会让"独立核实已装"变成空过 —— 真机上撞过:nginx 是上一轮装的,本轮明明
    # dirty、tx_id 全空,独立核实照样报 ok。基线不干净的节点不下发,记 FAIL 并
    # 提示换 --package。
    local -a base_pairs=() base_keys=() base_sshs=() base_distros=()
    for ((i = 0; i < ${#low_pairs[@]}; i++)); do
        local pre
        pre="$("$SSH" -n "${low_sshs[i]}" "$(pkg_probe_cmd "${low_distros[i]}" "$WANT_PKG")" 2>/dev/null || true)"
        if pkg_installed_from_output "${low_distros[i]}" "$WANT_PKG" "$pre"; then
            bad "${low_keys[i]}: ${WANT_PKG} 执行前就装着(基线不干净),本轮无法证明落地 —— 用 --package 换个未装包"
            continue
        fi
        base_pairs+=("${low_pairs[i]}"); base_keys+=("${low_keys[i]}")
        base_sshs+=("${low_sshs[i]}"); base_distros+=("${low_distros[i]}")
    done
    if [ "${#base_pairs[@]}" -gt 0 ]; then
        low_pairs=("${base_pairs[@]}"); low_keys=("${base_keys[@]}")
        low_sshs=("${base_sshs[@]}"); low_distros=("${base_distros[@]}")
    else
        low_pairs=(); low_keys=(); low_sshs=(); low_distros=()
    fi

    if [ "${#low_pairs[@]}" -eq 0 ]; then
        bad "低危腿没有可执行的节点(未登记,或被基线闸门拦下)"
    else
        local planfile
        planfile="$(mktemp)"
        render_low_plan "$WANT_PKG" ${low_pairs[@]+"${low_pairs[@]}"} > "$planfile"
        low_json="$("$HOST_BIN" plan submit "$planfile")"
        assert_jq "低危 Plan status=complete" '.status == "complete"' "$low_json"
        # "零目标机的 Plan" 也会报 complete,所以只断 status 是空过:必须断言真的
        # 每台目标机都有一条落地记录,且带非空 tx_id。
        assert_jq "低危 Plan 每条目标机都有落地记录" ".results | length == ${#low_pairs[@]}" "$low_json"
        assert_jq "每条落地记录都有非空 tx_id(事务 id,不等于\"有回滚点\")" \
            'all(.results[]; .tx_id != "")' "$low_json"
        echo "  plan submit(package.set ${WANT_PKG}): $low_json"
        # ★ 不采信上面的 complete:回到每台目标机上,用包管理器独立核实。
        for ((i = 0; i < ${#low_pairs[@]}; i++)); do
            assert_pkg_installed_on_target "${low_keys[i]}" "${low_sshs[i]}" "${low_distros[i]}" "$WANT_PKG"
        done
        rm -f "$planfile"
    fi

    step "高危腿:container(既无内建回滚点也无执行器凭据)→ 期望当场 dirty 且一个 RPC 都不发"
    # 选点口径(不是"回滚点判定表"):从 --hosts 里挑**第一台**已登记、且注册
    # 形态是 agent 的目标机。本驱动只用 node add 注册 agent 形态(render_plan 的
    # machines[].kind 恒为 "agent"),而 agent 节点上 container 既无内建回滚点、
    # 也没有它的执行器凭据 —— 准入由控制面按 objectmodel.AdmitsHighRisk 判。
    # 只命中 1 台(夹具 batching.size=1),避免"第一台失败掩盖后面"。
    #
    # 挑不到就记偏差并停(fail-closed):宁可不打高危,也不猜一台"大概会被拒"的
    # 机器 —— 猜错的代价是把危险动作真的下发出去(core#141)。
    local hi_i="" hi_pair=""
    for ((i = 0; i < n; i++)); do
        if [ -z "${node_kinds[i]}" ]; then
            continue # 不在 node list 里的节点已在低危腿处记为失败
        fi
        if [ "${node_kinds[i]}" != "agent" ]; then
            echo "  ${keys[i]}: 注册形态是 ${node_kinds[i]}(非 agent),无法确证 container 高危会被拒,跳过"
            continue
        fi
        hi_pair="$("$JQ" -r --arg a "${dials[i]}" \
            '[.nodes[] | select(.addr == $a) | "\(.node_id):\(.support_level)"] | first // ""' <<<"$json")"
        if [ -z "$hi_pair" ]; then
            continue
        fi
        hi_i="$i"
        break
    done
    if [ -z "$hi_pair" ]; then
        bad "没有符合 container 高危夹具口径的节点(需已登记且注册形态为 agent);记为偏差,不下发高危变更"
    else
        local hfile high_json audit_before audit_after
        # 零 RPC 的硬判据是目标机 agent 审计链**行数不增长**(与 plan-c 混合腿
        # 同一条取证路径):失败时控制面同样不给 tx_id,所以 tx_id 为空证明不了
        # "没下发"。
        audit_before="$(agent_audit_count "${sshs[hi_i]}" "$SSH" -n)" || audit_before=""
        if [ -z "$audit_before" ]; then
            # 取证通道不通就不提交:没有基线,"零增长"无从证明,空过比记 FAIL 更糟。
            bad "高危腿:读不到 ${keys[hi_i]} 的 agent 审计链行数(零 RPC 无从证明),本轮不下发"
        else
            hfile="$(mktemp)"
            render_high_plan "$hi_pair" > "$hfile"
            high_json="$("$HOST_BIN" plan submit "$hfile")"
            assert_jq "高危腿:plan 当场 dirty" '.status == "dirty"' "$high_json"
            # 文案以控制面实际输出为准:它把 kind 原样插进"…无回滚点…"那句里。
            # 英文 "no rollback point for kind=..." 是 agent 侧**下发之后**的回滚
            # 失败文案,正是这条腿要杜绝的事,不能拿来当断言。
            assert_jq "高危腿:文案点名无回滚点(container)" \
                'any(.results[]; (.message | contains("无回滚点")) and (.message | contains("container")))' "$high_json"
            audit_after="$(agent_audit_count "${sshs[hi_i]}" "$SSH" -n)" || audit_after=""
            if [ -z "$audit_after" ]; then
                bad "高危腿:提交后读不到 ${keys[hi_i]} 的 agent 审计链行数(零增长无从证明)"
            elif [ "$audit_after" = "$audit_before" ]; then
                ok "高危腿:${keys[hi_i]} 的 agent 审计链 $audit_before → $audit_after 行(零增长 ⇒ 一个 RPC 都没发)"
            else
                bad "高危腿:${keys[hi_i]} 的 agent 审计链从 $audit_before 增到 $audit_after 行 —— 高危 intent 被真下发了"
            fi
            echo "  plan submit(container,high): $high_json"
            rm -f "$hfile"
        fi
    fi
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
        --package) shift; WANT_PKG="${1:-nginx}" ;;
        -h|--help) sed -n '2,42p' "${BASH_SOURCE[0]}"; exit 0 ;;
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
            echo "用法:tests/fleet/agent-e2e.sh --hosts key=<ssh别名>[@<可拨地址>] ..." >&2
            exit 2
        fi
        if [ ! -x "$HOST_BIN" ]; then
            echo "找不到 daedalus-host 二进制($HOST_BIN);先 just go-build,或用 DAEDALUS_HOST_BIN 指定。" >&2
            exit 2
        fi
        for v in DAEDALUS_FLEET_CLIENT_CERT DAEDALUS_FLEET_CLIENT_KEY DAEDALUS_FLEET_CA; do
            if [ -z "${!v:-}" ]; then
                echo "缺少控制面 mTLS 客户端凭证:$v" >&2
                exit 2
            fi
        done
        # 取件来源必须在**动手装之前**定下来:配错(两个都给 / 都不给)时先报错退出 1,
        # 而不是装到一半才发现没包可用,留下半轮被改过的机器。
        if ! resolve_agent_tarball; then
            exit 1
        fi
        # 每次矩阵用独立的 fleet 根:node_id 每次 Register 都会轮换,复用同一个
        # 注册表会把上一轮的僵尸行算进"节点数"断言里。
        export DAEDALUS_FLEET_DIR
        DAEDALUS_FLEET_DIR="$(mktemp -d)"
        echo "fleet 根目录:$DAEDALUS_FLEET_DIR"
        run_matrix "${hosts[@]}"
        ;;
    *)
        sed -n '2,42p' "${BASH_SOURCE[0]}"
        exit 2
        ;;
esac

# 汇总之后就不是"意外失败"了:关掉 ERR 线索,免得把正常的非零退出也报成异常。
trap - ERR
echo
echo "agent-e2e: $pass 通过 / $fail 失败"
[ "$fail" -eq 0 ]
