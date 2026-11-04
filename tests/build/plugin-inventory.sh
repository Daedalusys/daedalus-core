#!/usr/bin/env bash
# 插件清单集合相等门禁:四份清单必须严格相等 ———
#   ① 76 脚本的 CAPS          (镜像接线面的事实源)
#   ② 被追踪的插件 systemd 单元 (daedalus-<cap>.service,排除 core 自身 4 个)
#   ③ 被追踪的插件安装态 manifest (files/system/opt/daedalus/plugins/daedalus.<cap>/daedalus.plugin.json)
#   ④ plugins 仓 release 供料的 cap 集合(兄弟仓存在时校验;不存在则打印 NOTE)
# 任一份多出或少一个 cap 即 exit 1 —— 「有 manifest 没二进制」的插件不得再随镜像交付。
#
# 用法:plugin-inventory.sh [--selftest]
set -euo pipefail

# core 自身的 4 个单元不是插件,集合比对时必须排除,否则永远不等:
#   daedalus-audit / daedalus-audit-rotate / daedalus-env / daedalus-host-controller
CORE_UNITS="audit audit-rotate env host-controller"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CAPS_SRC="$ROOT/files/scripts/76-daedalus-plugin-gen.sh"
# 第四份来源:兄弟仓 plugins 的 release 工作流供料名单。兄弟仓缺失(如插件仓自身的
# CI 里只有 core 一个仓)时降级为 NOTE,不失败 —— 前三份仍是硬门禁。
PLUGINS_RELEASE_YML="$ROOT/../daedalus-plugins/.github/workflows/release.yml"

# caps_from_76 —— 从 76 脚本读 CAPS(单一事实源,不另维护一份名单)。
caps_from_76() {
    sed -n 's/^CAPS="\(.*\)"$/\1/p' "$CAPS_SRC" | head -n 1 | tr ' ' '\n' | sed '/^$/d' | sort
}

# units_from_tree —— 仓里被追踪的插件单元(cap 名)。
units_from_tree() {
    local d="$ROOT/files/system/usr/lib/systemd/system"
    local f base cap cu skip
    for f in "$d"/daedalus-*.service; do
        [ -e "$f" ] || continue
        base="$(basename "$f" .service)"
        cap="${base#daedalus-}"
        skip=0
        for cu in $CORE_UNITS; do [ "$cap" = "$cu" ] && skip=1; done
        [ "$skip" -eq 1 ] && continue
        echo "$cap"
    done | sort
}

# manifests_from_tree —— 仓里被追踪的插件 manifest(cap 名)。安装态目录 files/system/
# opt/daedalus/plugins/ 同时容纳**被追踪**的能力插件 manifest 与 gitignore 的构建产物
# (daedalus.copilot 的安装态),后者不属于「能力插件清单」,按 gitignore 过滤掉 ——
# 与第 ② 份(单元)里的 CORE_UNITS 排除同理。
manifests_from_tree() {
    local d="$ROOT/files/system/opt/daedalus/plugins"
    local f cap
    for f in "$d"/daedalus.*/daedalus.plugin.json; do
        [ -e "$f" ] || continue
        if git -C "$ROOT" check-ignore -q -- "$f" 2>/dev/null; then continue; fi
        cap="$(basename "$(dirname "$f")")"
        echo "${cap#daedalus.}"
    done | sort
}

# release_caps —— 兄弟仓 plugins 的 release.yml 供料 cap 集合(可选来源)。
release_caps() {
    local f="$1"
    [ -f "$f" ] || return 1
    sed -n 's/.*for cap in \(.*\); do.*/\1/p' "$f" | head -n 1 \
        | tr ' ' '\n' | sed '/^$/d' | sort
}

# compare_sets <标签A> <集合A> <标签B> <集合B> —— 两个集合(换行分隔、已排序)不等即打印
# 差异并返回 1。comm 要求输入已排序,调用方保证。
compare_sets() {
    local la="$1" a="$2" lb="$3" b="$4"
    [ "$a" = "$b" ] && return 0
    echo "plugin-inventory: 集合不等 —— ${la} vs ${lb}" >&2
    echo "  仅在 ${la}: $(comm -23 <(printf '%s\n' "$a") <(printf '%s\n' "$b") | tr '\n' ' ')" >&2
    echo "  仅在 ${lb}: $(comm -13 <(printf '%s\n' "$a") <(printf '%s\n' "$b") | tr '\n' ' ')" >&2
    return 1
}

# inventory_check <CAPS> <单元> <manifest> [<release 供料>] —— 逐对比较四份集合。
# 拆成独立函数是为了 --selftest 能用合成输入驱动它(不碰真实文件树)。
inventory_check() {
    local caps="$1" units="$2" manifests="$3" release="${4:-}"
    local rc=0
    compare_sets "CAPS" "$caps" "插件单元" "$units" || rc=1
    compare_sets "CAPS" "$caps" "插件 manifest" "$manifests" || rc=1
    if [ -n "$release" ]; then
        compare_sets "CAPS" "$caps" "plugins release 供料" "$release" || rc=1
    fi
    return "$rc"
}

# --- 自检:证明门禁没变瞎 ---------------------------------------------------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    # rc <命令...> —— 只回退出码的 0/1,屏蔽被驱动命令的 stderr。
    rc() { "$@" >/dev/null 2>&1 && echo 0 || echo 1; }
    printf '== plugin-inventory.sh --selftest ==\n'

    good=$'blueprint\ndupe'
    extra=$'blueprint\ndupe\nzzz'
    short=$'blueprint'

    st 0 "$(rc inventory_check "$good" "$good" "$good" "$good")" "干净输入三方集合相等 → PASS"
    st 1 "$(rc inventory_check "$good" "$extra" "$good" "$good")" "多出一个单元 → 判不等"
    st 1 "$(rc inventory_check "$good" "$good" "$extra" "$good")" "多出一个 manifest → 判不等"
    st 1 "$(rc inventory_check "$short" "$good" "$good" "$good")" "CAPS 少一个 → 判不等"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# --- 正式流程:算出四方集合,逐对比较 -----------------------------------------
caps="$(caps_from_76)"
units="$(units_from_tree)"
manifests="$(manifests_from_tree)"
if [ -z "$caps" ]; then
    echo "plugin-inventory: 无法从 $CAPS_SRC 解析 CAPS(单一事实源缺失)" >&2
    exit 1
fi

release=""
if [ -f "$PLUGINS_RELEASE_YML" ]; then
    release="$(release_caps "$PLUGINS_RELEASE_YML")"
else
    echo "NOTE: 兄弟仓 plugins 的 release.yml 不存在($PLUGINS_RELEASE_YML),跳过第四份(release 供料)核对" >&2
fi

if inventory_check "$caps" "$units" "$manifests" "$release"; then
    echo "OK: 插件清单四份一致($(printf '%s\n' "$caps" | grep -c .) 个)"
else
    exit 1
fi
