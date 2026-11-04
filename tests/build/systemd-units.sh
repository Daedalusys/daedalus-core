#!/usr/bin/env bash
# systemd 单元静态门禁:单元/drop-in 的**解析级**错误即拒。
#
# 解析级 = systemd 认不出键名或值,例如:
#   - SystemCallFilter 的取反前缀放在列表中间 → "Failed to parse system call, ignoring"
#   - 键写错节(如 ConditionPathExists 写进 [Service]) → "Unknown key name ... in section"
#
# 故意不检 "Command ... is not executable":源码树里插件二进制本就不存在,
# CI 上该告警对每个单元恒真、零信号。ExecStart 路径/旗标的正确性由构建期
# 76 脚本(render-unit 自校验)与 cmd/daedalus-host 的单测各自覆盖。
#
# 用法:
#   systemd-units.sh            校验仓库里的单元(有解析级错误即 exit 1)
#   systemd-units.sh --selftest 门禁自检:证明它没变瞎(见下)
set -euo pipefail

# 单元类型全集:新增类型必须一并纳入,否则门禁对新单元类型静默失明。
# (仓内现有 .service / .timer,其余为将来可能引入的类型。)
readonly UNIT_PATTERNS=(
    '*.service' '*.socket' '*.target' '*.timer' '*.path'
    '*.mount' '*.automount' '*.swap' '*.slice'
)

# collect_units <dir> —— 列出该目录下所有单元文件(绝对路径,按名排序)。
# 用绝对路径传给 systemd-analyze,报错行才带 <dir> 前缀,过滤才精确。
collect_units() {
    local dir="$1"
    local expr=()
    local first=1
    local p
    for p in "${UNIT_PATTERNS[@]}"; do
        if [ "$first" -eq 1 ]; then first=0; else expr+=("-o"); fi
        expr+=("-name" "$p")
    done
    find "$dir" -maxdepth 1 -type f \( "${expr[@]}" \) | sort
}

# parse_errors <dir> <verify-output> —— 只保留属于 <dir> 的解析级告警。
# systemd-analyze verify 会顺带解析宿主单元(依赖拉取),宿主单元的行不带 <dir>
# 前缀。按路径过滤而不是按文件名猜 —— 后者会对非 daedalus- 前缀的单元失明。
#
# 措辞匹配必须跨 systemd 版本,同一个错误各版本写法不同:
#   systemd 252: "Failed to parse system call, ignoring: ~@privileged"
#   systemd 255: "System call ~@privileged is not known, ignoring."
# 两类都是"配置项被丢弃"(ignoring),都必须算错。--selftest 是这条匹配的守门:
# 若将来某版本再换措辞,selftest 会先红,而不是门禁静默失明。
parse_errors() {
    printf '%s\n' "$2" \
        | grep -E 'Failed to parse system call|is not known, ignoring|Unknown key name' \
        | grep -F "$1" || true
}

# verify_units <dir> —— 对 <dir> 内的单元跑 systemd-analyze verify,
# 把解析级错误逐行打到 stdout(空 = 无错)。<dir> 必须是**快照目录**:
# 仓内单元目录的路径尾部恰好是 /usr/lib/systemd/system,直接传会被 systemd
# 误认成宿主单元搜索路径下的单元而加载宿主版本。
verify_units() {
    local dir="$1"
    local units=()
    local f
    while IFS= read -r f; do units+=("$f"); done < <(collect_units "$dir")
    if [ "${#units[@]}" -eq 0 ]; then
        echo "systemd-units: $dir 下没有单元文件" >&2
        return 1
    fi
    local output
    output="$(systemd-analyze verify "${units[@]}" 2>&1 || true)"
    parse_errors "$dir" "$output"
}

# --- 自检:证明门禁没变瞎 ---------------------------------------------------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n+1)); fi
    }
    printf '== systemd-units.sh --selftest ==\n'

    command -v systemd-analyze >/dev/null || {
        echo "systemd-units: systemd-analyze 不可用,无法自检" >&2
        exit 1
    }

    s="$(mktemp -d)"
    trap 'rm -rf "$s"' EXIT

    # 1) 干净单元 → 不产生解析级错误。
    printf '[Unit]\nDescription=x\n[Service]\nExecStart=/bin/true\n' > "$s/clean.service"
    st 0 "$(verify_units "$s" | wc -l)" "干净单元不产生解析级错误"

    # 2) 非 daedalus- 前缀的单元,seccomp 取反语法错 → 必须被抓。
    #    (门禁若按文件名前缀过滤,这一条会漏。)
    printf '[Unit]\nDescription=x\n[Service]\nExecStart=/bin/true\nSystemCallFilter=@system-service ~@privileged\n' \
        > "$s/zz-notdaedalus.service"
    st 1 "$(verify_units "$s" | grep -c 'zz-notdaedalus')" "非 daedalus- 前缀单元的 seccomp 解析错必须被抓"

    # 3) 条件键写进 [Timer] 节 → 必须被抓(键放错节这一类)。
    printf '[Unit]\nDescription=x\n[Timer]\nOnCalendar=daily\nConditionPathExists=/tmp/x\n' > "$s/zz-badsection.timer"
    st 1 "$(verify_units "$s" | grep -c 'zz-badsection')" "条件键放错节必须被抓"

    # 4) 新单元类型(.target)必须被纳入检查范围。
    printf '[Unit]\nDescription=x\n[Install]\nWantedBy=multi-user.target\n' > "$s/zz-newtype.target"
    st 1 "$(collect_units "$s" | grep -c 'zz-newtype.target')" ".target 类型必须纳入检查范围"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# --- 正式流程:校验仓库单元 --------------------------------------------------
command -v systemd-analyze >/dev/null || {
    echo "systemd-units: systemd-analyze 不可用,无法执行门禁" >&2
    exit 1
}

unit_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../files/system/usr/lib/systemd/system" && pwd)"
# 拷到临时目录再验:仓内单元目录路径尾部是 /usr/lib/systemd/system,
# 直接传会被 systemd 当作宿主单元搜索路径下的单元而加载宿主版本。
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
cp -a "$unit_dir/." "$scratch/"

bad="$(verify_units "$scratch")"
if [ -n "$bad" ]; then
    echo "systemd-units: 发现单元解析级错误:" >&2
    printf '%s\n' "$bad" >&2
    exit 1
fi
echo "OK: systemd 单元无解析级错误"
