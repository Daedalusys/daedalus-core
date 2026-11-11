#!/usr/bin/env bash
#
# agent 侧 file 类 intent 的真机验收驱动(与 agent-e2e.sh 同构,但只跑 file kind)。
#
# 为什么另起一个驱动:agent-e2e.sh 只跑 package 类。spec §7 要求 file intent
# 在 **ext4 与 xfs** 上各跑一次真机用例,而 file 类特有的判据是"文件在目标机上
# 真的被写成了逐字相符的内容、且 mode 正确"——这条判据与 package 类的
# "包管理器说装上了"完全不同,塞进同一个脚本只会让两条判据互相稀释。
#
# 判定纪律与 agent-e2e.sh 相同:**不采信控制面 / agent 的自我汇报**
# (plan submit 的 status / result / tx_id 都不算数),只采信在目标机上跑出来的
# 文件系统事实(存在性 / sha256 / mode / 内容原文)。
#
# 用法:
#   tests/fleet/agent-file-e2e.sh --hosts ubuntu=<ssh别名>[@<可拨地址>] [debian12=... rhel9=...]
#       真机矩阵:逐台 node add → 造 file 类 Plan → plan submit → 回目标机
#       独立核实文件内容 / mode。三条腿:
#         1. ext4 宿主:低危路径 /tmp/daedalus-e2e-file.conf
#         2. xfs  宿主:低危路径 /tmp/daedalus-e2e-file.conf
#         3. ext4 宿主:高危路径 /etc/daedalus-e2e.conf(声明 low,运行时被
#            判定表判成 high;file 在 agent 节点上有内建单文件快照,故应放行)
#       需要:
#         - 控制面 mTLS 客户端凭证:DAEDALUS_FLEET_CLIENT_CERT / _KEY / DAEDALUS_FLEET_CA
#         - 目标机已装好 daedalus-agent 且控制面能拨通它的转发口
#         - 目标机 ssh 可达(用来独立核实与读 agent_id)
#
# 退出码:0 全部断言通过;非 0 表示有断言失败(逐条打印)。
#
# 证据:真机跑完把观察照实填进 tests/fleet/EVIDENCE/<日期>-agent-file-e2e.md。
# 与期望不符之处不隐瞒。

set -euo pipefail
# -E 让 ERR 线索进入函数体:没有它,函数内部的意外失败只会让整轮**静默中断**
# (真机上撞过一次:探一个不存在的文件,日志停在半截,没有任何线索)。
set -E

# 真机驱动最怕"静默中断":set -e 下任何意外失败都会让整轮以半截日志收场。
# 留一条 ERR 线索指名出错行,便于事后定位。
trap 'echo "agent-file-e2e: 第 $LINENO 行意外失败(exit=$?)" >&2' ERR

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOST_BIN="${DAEDALUS_HOST_BIN:-$ROOT/bin/daedalus-host}"
SSH="${DAEDALUS_SSH:-ssh}"
JQ="${DAEDALUS_JQ:-jq}"
AGENT_PORT="${DAEDALUS_AGENT_PORT:-9443}"
# 低危腿的落地路径(两台宿主各写自己的一份,内容互不相同)。
LOW_TARGET="/tmp/daedalus-e2e-file.conf"
# 高危路径腿:只写这一个文件,跑完删掉。
HIGH_TARGET="/etc/daedalus-e2e.conf"
# 新建文件期望的 mode(agent 的 file 执行器显式 chmod,不受 umask 影响)。
WANT_MODE="644"
POLICY_FILE="$ROOT/files/system/opt/daedalus/shared/policy.toml"

# 用仓内那份真实 policy.toml 判风险,而不是回退内置默认值 —— 判定表本身就是
# 单一事实源。操作者已显式指定 DAEDALUS_POLICY_PATH 时不覆盖。
if [ -z "${DAEDALUS_POLICY_PATH:-}" ] && [ -r "$POLICY_FILE" ]; then
    export DAEDALUS_POLICY_PATH="$POLICY_FILE"
fi

pass=0
fail=0

ok()   { echo "  ok   - $*"; pass=$((pass + 1)); }
bad()  { echo "  FAIL - $*" >&2; fail=$((fail + 1)); }
step() { echo; echo "== $*"; }

# ------------------------------------------------------------- 目标机事实 ----

# os_field <os-release 全文> <字段名>:取 /etc/os-release 的某个字段。
os_field() {
    printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1 | tr -d '"'
}

# remote_os_release <ssh>:读目标机的 /etc/os-release(原文,进证据文件)。
remote_os_release() {
    "$SSH" -n "$1" 'cat /etc/os-release' 2>/dev/null
}

# remote_fs_type <ssh>:目标机根文件系统的类型(ext4 / xfs …)。
# spec §7 要求 file intent 在 ext4 与 xfs 上各跑一次,故腿的划分按它来,
# 不按发行版来。
remote_fs_type() {
    "$SSH" -n "$1" "df -T / 2>/dev/null | awk 'NR==2 {print \$2}'" 2>/dev/null || true
}

# remote_agent_id <ssh>:agent 的身份来自它自己的配置(install.sh 的 postinstall
# 写 /etc/daedalus-agent/config.yaml);控制面发起 Register 时用同一个值。
remote_agent_id() {
    "$SSH" -n "$1" 'sudo sed -n "s/^agent_id:[[:space:]]*//p" /etc/daedalus-agent/config.yaml' 2>/dev/null
}

# remote_first_addr <ssh>:目标机第一个非回环 IPv4(可拨地址缺省时的取值)。
remote_first_addr() {
    "$SSH" -n "$1" "hostname -I 2>/dev/null | awk '{print \$1}'" 2>/dev/null || true
}

# remote_file_stat <ssh> <path>:目标机上文件的 mode / size / 类型。
# 读不到就是读不到(输出里带错误原文),不猜 —— 故末尾 `|| true`:探"文件不存在"
# 是本驱动的**正常**步骤,不能让它把整轮按意外失败打断。
remote_file_stat() {
    "$SSH" -n "$1" "sudo stat -c '%a %s %F' '$2' 2>&1" 2>&1 || true
}

# remote_file_sha <ssh> <path>:目标机上文件的 sha256(字节级判据)。
remote_file_sha() {
    "$SSH" -n "$1" "sudo sha256sum '$2' 2>&1 | cut -d' ' -f1" 2>&1 || true
}

# remote_file_raw <ssh> <path>:目标机上文件内容的原文(cat -A:行尾显示为 $,
# 能看出有没有多余的尾随换行 / 制表符)。
remote_file_raw() {
    "$SSH" -n "$1" "sudo cat -A '$2' 2>&1" 2>&1 || true
}

# remote_remove <ssh> <path>:删掉本驱动自己写下的文件(收尾用)。
remote_remove() {
    "$SSH" -n "$1" "sudo rm -f '$2' 2>&1; echo rc=\$?" 2>&1 || true
}

# ------------------------------------------------------------- Plan 生成 ----

# render_file_plan <plan-id> <node_id> <support_level> <target> <content>
#
# 形状照 tests/fleet/fixtures/plan-low-nginx.json,只把 intent 换成 file 类:
# spec 只有 content 一个键(agent 的 file 执行器用 DisallowUnknownFields 校验,
# 多一个键就整条被拒)。expected_diff 留空:file 类是整体覆盖,没有可比对的
# 差异基线。risk_class 一律声明 low —— 高危路径那条的**实际**风险由控制面的
# 判定表按路径前缀算出来(取声明与判定里更严的一侧),声明 low 不是绕过。
render_file_plan() {
    local plan_id="$1" node_id="$2" level="$3" target="$4" content="$5"
    "$JQ" -n --arg id "$plan_id" --arg nid "$node_id" --arg lv "$level" \
        --arg target "$target" --arg content "$content" '{
            id: $id,
            intents: [{
                kind: "file",
                target: $target,
                spec: {content: $content},
                expected_diff: "",
                risk_class: "low"
            }],
            machines: [{machine_id: $nid, kind: "agent", support_level: $lv}],
            batching: {strategy: "count", size: 1},
            blast_radius: {
                count: 1,
                kind_breakdown: {file: 1},
                max_downtime_ns: 30000000000,
                max_risk: "low"
            }
        }'
}

# expect_sha <content>:期望内容的 sha256(本地算,与目标机上的 sha256sum 同算法)。
expect_sha() {
    printf '%s' "$1" | sha256sum | cut -d' ' -f1
}

# ------------------------------------------------------- 一条腿的完整判定 ----

# run_file_leg <key> <ssh别名> <node_id> <support_level> <target> <content> <期望mode|"-">
#
# 判据全在目标机侧:存在 / 是普通文件 / sha256 逐字节相符 / mode 相符。
# plan submit 的返回只作上下文打印,不参与判定。
run_file_leg() {
    local key="$1" alias="$2" node_id="$3" level="$4" target="$5" content="$6" want_mode="$7"
    local planfile plan_json stat_out sha_out raw_out want_sha got_mode got_sha got_type rc=0

    echo "  [腿] ${key}: target=${target} content=$(printf '%s' "$content" | od -c | head -1 | sed 's/^0000000 //')"
    want_sha="$(expect_sha "$content")"

    # 基线:目标机上这个文件此刻在不在(本驱动只写自己的测试路径)。
    stat_out="$(remote_file_stat "$alias" "$target")"
    echo "    [基线] \$ sudo stat -c '%a %s %F' ${target}"
    printf '%s\n' "$stat_out" | sed 's/^/      /'
    if [[ "$stat_out" =~ ^[0-7]+[[:space:]]+[0-9]+[[:space:]]+regular ]]; then
        echo "    [基线] 已存在(上一轮残留),本腿会整体覆盖"
    else
        echo "    [基线] 不存在"
    fi

    planfile="$(mktemp)"
    render_file_plan "agent-file-e2e-${key}" "$node_id" "$level" "$target" "$content" > "$planfile"
    echo "    [计划] $(cat "$planfile")"
    plan_json="$("$HOST_BIN" plan submit "$planfile" 2>&1)" || rc=$?
    echo "    [控制面自报(exit=${rc},不作判据)] ${plan_json}"
    rm -f "$planfile"

    # ★ 独立核实:回目标机读文件,不信上面那行 status。
    stat_out="$(remote_file_stat "$alias" "$target")"
    sha_out="$(remote_file_sha "$alias" "$target")"
    raw_out="$(remote_file_raw "$alias" "$target")"
    echo "    [独立核实] \$ sudo stat -c '%a %s %F' ${target}"
    printf '%s\n' "$stat_out" | sed 's/^/      /'
    echo "    [独立核实] \$ sudo sha256sum ${target}"
    printf '%s\n' "$sha_out" | sed 's/^/      /'
    echo "    [独立核实] \$ sudo cat -A ${target}"
    printf '%s\n' "$raw_out" | sed 's/^/      /'

    got_mode="$(printf '%s' "$stat_out" | awk '{print $1}')"
    got_sha="$(printf '%s' "$sha_out" | head -1 | awk '{print $1}')"

    if [[ ! "$stat_out" =~ ^[0-7]+[[:space:]]+[0-9]+[[:space:]]+regular ]]; then
        bad "${key}: 目标机上 ${target} 不是普通文件(stat: ${stat_out})"
        return
    fi
    ok "${key}: 目标机上 ${target} 存在且是普通文件"
    if [ "$got_sha" = "$want_sha" ]; then
        ok "${key}: 目标机上 ${target} 内容逐字节相符(sha256 ${got_sha})"
    else
        bad "${key}: 目标机上 ${target} 内容不符(期望 sha256 ${want_sha},实际 ${got_sha})"
    fi
    if [ "$want_mode" = "-" ]; then
        echo "  info - ${key}: ${target} mode=${got_mode}(本腿只判存在与内容)"
    elif [ "$got_mode" = "$want_mode" ]; then
        ok "${key}: 目标机上 ${target} mode=${got_mode}"
    else
        bad "${key}: 目标机上 ${target} mode 不符(期望 ${want_mode},实际 ${got_mode})"
    fi
}

# -------------------------------------------------------------- 真机矩阵 ----

run_matrix() {
    local specs=("$@") n="${#specs[@]}"
    local -a keys=() sshs=() dials=() distros=() vers=() fs_types=() agent_ids=() node_ids=() levels=()
    local i spec key part alias addr osrel id ver
    local ext4_content xfs_content etc_content

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
        distros+=(""); vers+=(""); fs_types+=(""); agent_ids+=(""); node_ids+=(""); levels+=("")
    done

    step "被控机基础信息(目标机自己的 /etc/os-release 与根文件系统类型)"
    for ((i = 0; i < n; i++)); do
        if ! osrel="$(remote_os_release "${sshs[i]}")"; then
            bad "${keys[i]}: ssh ${sshs[i]} 不可达或读不到 /etc/os-release"
            continue
        fi
        id="$(os_field "$osrel" ID)"
        ver="$(os_field "$osrel" VERSION_ID)"
        distros[i]="${id,,}"
        vers[i]="$ver"
        fs_types[i]="$(remote_fs_type "${sshs[i]}")"
        ok "${keys[i]}(${sshs[i]}): ID=${id} VERSION_ID=${ver} 根文件系统=${fs_types[i]:-<未知>}"
    done

    step "登记被控机(daedalus-host node add → 拿 node_id)"
    local json
    for ((i = 0; i < n; i++)); do
        addr="${dials[i]}"
        if [ -z "$addr" ]; then
            addr="$(remote_first_addr "${sshs[i]}")"
        fi
        if [[ "$addr" != *:* ]]; then
            addr="${addr}:${AGENT_PORT}"
        fi
        dials[i]="$addr"
        agent_ids[i]="$(remote_agent_id "${sshs[i]}")" || agent_ids[i]=""
        if [ -z "${agent_ids[i]}" ]; then
            bad "${keys[i]}: 取不到 agent_id(检查 /etc/daedalus-agent/config.yaml),跳过登记"
            continue
        fi
        json="$("$HOST_BIN" node add "$addr" --agent-id "${agent_ids[i]}" \
            --distro "${distros[i]}" --distro-version "${vers[i]}" --hostname "${sshs[i]}" 2>&1)" || {
            bad "${keys[i]}: node add ${addr} 失败: ${json}"
            continue
        }
        node_ids[i]="$(printf '%s' "$json" | "$JQ" -r '.node.node_id // empty')"
        levels[i]="$(printf '%s' "$json" | "$JQ" -r '.node.support_level // "supported"')"
        if [ -z "${node_ids[i]}" ]; then
            bad "${keys[i]}: node add 回了但取不到 node_id: ${json}"
            continue
        fi
        ok "${keys[i]}: node add ${addr}(agent_id=${agent_ids[i]}) → node_id=${node_ids[i]} support_level=${levels[i]}"
        echo "    node add: ${json}"
    done

    step "选腿:ext4 与 xfs 各一条低危腿"
    local -i ext4_i=-1 xfs_i=-1
    for ((i = 0; i < n; i++)); do
        [ -z "${node_ids[i]}" ] && continue
        if [ "${fs_types[i]}" = "ext4" ] && [ "$ext4_i" -lt 0 ]; then ext4_i="$i"; fi
        if [ "${fs_types[i]}" = "xfs" ] && [ "$xfs_i" -lt 0 ]; then xfs_i="$i"; fi
    done
    if [ "$ext4_i" -lt 0 ]; then
        bad "没有 ext4 宿主,低危腿(spec §7 要求 ext4 与 xfs 各跑一次)无法执行"
    fi
    if [ "$xfs_i" -lt 0 ]; then
        bad "没有 xfs 宿主,低危腿(spec §7 要求 ext4 与 xfs 各跑一次)无法执行"
    fi

    if [ "$ext4_i" -ge 0 ]; then
        # 内容里带尾随换行,且**必须真的落进文件** —— 命令替换会吃掉尾随换行,
        # 故用 $'...' 拼接赋值,不用 $(printf ...)。
        ext4_content=$'e2e-file-'"${sshs[$ext4_i]}"$'\n'
        run_file_leg "ext4-${keys[$ext4_i]}" "${sshs[$ext4_i]}" "${node_ids[$ext4_i]}" \
            "${levels[$ext4_i]}" "$LOW_TARGET" "$ext4_content" "$WANT_MODE"
    fi
    if [ "$xfs_i" -ge 0 ]; then
        xfs_content=$'e2e-file-'"${sshs[$xfs_i]}"$'\n'
        run_file_leg "xfs-${keys[$xfs_i]}" "${sshs[$xfs_i]}" "${node_ids[$xfs_i]}" \
            "${levels[$xfs_i]}" "$LOW_TARGET" "$xfs_content" "$WANT_MODE"
    fi

    step "高危路径腿:${HIGH_TARGET}(file 在 agent 节点上有内建单文件快照,应放行)"
    if [ "$ext4_i" -lt 0 ]; then
        bad "没有可用的 ext4 宿主,高危路径腿无法执行"
    else
        etc_content=$'e2e-file-etc\n'
        run_file_leg "etc-${keys[$ext4_i]}" "${sshs[$ext4_i]}" "${node_ids[$ext4_i]}" \
            "${levels[$ext4_i]}" "$HIGH_TARGET" "$etc_content" "-"
    fi

    step "收尾:删掉本驱动写下的文件(高危路径那条必须删)"
    for ((i = 0; i < n; i++)); do
        [ -z "${node_ids[i]}" ] && continue
        echo "  ${keys[i]}: \$ sudo rm -f ${HIGH_TARGET} → $(remote_remove "${sshs[i]}" "$HIGH_TARGET")"
        echo "  ${keys[i]}: \$ sudo rm -f ${LOW_TARGET} → $(remote_remove "${sshs[i]}" "$LOW_TARGET")"
    done
}

# ------------------------------------------------------------------ main ----

mode=""
hosts=()
while [ $# -gt 0 ]; do
    case "$1" in
        --hosts)
            mode="matrix"
            shift
            while [ $# -gt 0 ] && [[ "$1" != --* ]]; do hosts+=("$1"); shift; done
            continue
            ;;
        -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "未知参数:$1" >&2; exit 2 ;;
    esac
    shift
done

case "$mode" in
    matrix)
        if [ "${#hosts[@]}" -eq 0 ]; then
            echo "用法:tests/fleet/agent-file-e2e.sh --hosts key=<ssh别名>[@<可拨地址>] ..." >&2
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
        # fleet 根:调用方指定就用调用方的(与 agent-e2e.sh 的"每次矩阵独立根"
        # 不同 —— 本驱动按需追加节点,复用同一个根更方便对照),没指定就临时造一个。
        if [ -z "${DAEDALUS_FLEET_DIR:-}" ]; then
            DAEDALUS_FLEET_DIR="$(mktemp -d)"
        fi
        export DAEDALUS_FLEET_DIR
        echo "fleet 根目录:$DAEDALUS_FLEET_DIR"
        run_matrix "${hosts[@]}"
        ;;
    *)
        sed -n '2,30p' "${BASH_SOURCE[0]}"
        exit 2
        ;;
esac

# 汇总之后就不是"意外失败"了:关掉 ERR 线索,免得把正常的非零退出也报成异常。
trap - ERR
echo
echo "agent-file-e2e: $pass 通过 / $fail 失败"
[ "$fail" -eq 0 ]
