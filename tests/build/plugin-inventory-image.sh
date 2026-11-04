#!/usr/bin/env bash
# 镜像内插件清单断言:镜像里跑的 daedalus-host list 必须**恰好**是「CAPS + copilot」,
# 且无 degraded —— 「有 manifest 没二进制」的插件在此暴露(core#53 的原始形态)。
#
# 为什么独立成脚本:list 输出的解析(tabwriter 以**空格**对齐列、degraded 明细行缩进)
# 是本断言唯一易错处(历史上写成 -F'\t' 会让 degraded 判据恒为 0,门禁静默失明),
# 因此必须可离线自检 —— `--selftest` 喂合成输出,不依赖 podman / 镜像。
#
# 期望集合的唯一事实源是 76 构建脚本的 CAPS(不写死数量,不维护第二份名单);
# copilot 是预置的**非能力**插件(type=copilot,无 systemd 单元),它同样落在镜像
# /opt/daedalus/plugins 里,故期望集合 = CAPS + copilot。
#
# 用法:
#   plugin-inventory-image.sh              # 需 podman + 已构建的 localhost/daedalus-os:latest
#   plugin-inventory-image.sh --selftest   # 只验解析/比对逻辑,不碰 podman
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CAPS_SRC="$ROOT/files/scripts/76-daedalus-plugin-gen.sh"

# expected_caps —— 期望的插件 cap 集合(换行分隔、已排序)。
expected_caps() {
    { sed -n 's/^CAPS="\(.*\)"$/\1/p' "$CAPS_SRC" | head -n 1 | tr ' ' '\n' | sed '/^$/d'; echo copilot; } | sort
}

# actual_caps <list 输出> —— 从 daedalus-host list 输出抽实际 cap 集合。
# 输出形态:首行表头 + 每插件一行(行首即 id `daedalus.<cap>`,列以空格对齐,末列为状态);
# degraded 时表后另附缩进 2 空格的 `  <id>: <原因>` 明细行。按**行首**判定插件行 ——
# 明细行以空格起头,故不会被计入。切勿改用 -F'\t'(那是空格对齐,状态列取不中)。
actual_caps() {
    printf '%s\n' "$1" | awk '$0 ~ /^daedalus\./ {print $1}' | sed 's/^daedalus\.//' | sort
}

# degraded_caps <list 输出> —— 状态列为 degraded 的 cap(末字段即状态列,不被填充)。
degraded_caps() {
    printf '%s\n' "$1" | awk '$0 ~ /^daedalus\./ && $NF == "degraded" {print $1}' \
        | sed 's/^daedalus\.//' | sort
}

# check_list <list 输出> —— 集合必须等于「CAPS + copilot」且无 degraded。不等则打印差异,返回 1。
check_list() {
    local out="$1" exp act deg rc=0
    exp="$(expected_caps)"
    act="$(actual_caps "$out")"
    deg="$(degraded_caps "$out")"
    if [ "$act" != "$exp" ]; then
        echo "plugin-inventory-image: 镜像内插件集合与期望(CAPS + copilot)不等" >&2
        echo "  仅在镜像: $(comm -23 <(printf '%s\n' "$act") <(printf '%s\n' "$exp") | tr '\n' ' ')" >&2
        echo "  仅在期望: $(comm -13 <(printf '%s\n' "$act") <(printf '%s\n' "$exp") | tr '\n' ' ')" >&2
        rc=1
    fi
    if [ -n "$deg" ]; then
        echo "plugin-inventory-image: 镜像内有 degraded 插件: $(printf '%s' "$deg" | tr '\n' ' ')" >&2
        rc=1
    fi
    return "$rc"
}

# --- 自检:证明解析/比对逻辑没变瞎(不依赖 podman / 镜像) ----------------------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    rc() { "$@" >/dev/null 2>&1 && echo 0 || echo 1; }

    # mk_list <cap>[:degraded] ... —— 生成一份与 daedalus-host list 同构的合成输出
    # (表头 + 插件行 + degraded 时的缩进明细行)。
    mk_list() {
        printf 'ID  NAME  VERSION  TYPE  RUNTIME  I18N  STATUS\n'
        local item cap st
        for item in "$@"; do
            cap="${item%%:*}"
            st=ok
            case "$item" in *:degraded) st=degraded ;; esac
            printf 'daedalus.%s  Some Name  0.1.0  capability  native  -  %s\n' "$cap" "$st"
        done
        for item in "$@"; do
            case "$item" in *:degraded) printf '  daedalus.%s: 原因\n' "${item%%:*}" ;; esac
        done
    }

    printf '== plugin-inventory-image.sh --selftest ==\n'
    all="$(expected_caps)"
    st 0 "$(rc check_list "$(mk_list $(echo "$all"))")" "全 ok 的期望集合 → PASS"
    st 1 "$(rc check_list "$(mk_list $(echo "$all" | head -n -1))")" "少一个 cap → 判不等"
    st 1 "$(rc check_list "$(mk_list $(echo "$all") zzz)")" "多一个 cap(已移除的设备插件)→ 判不等"
    st 1 "$(rc check_list "$(mk_list "$(echo "$all" | sed 's/^fs$/fs:degraded/')")")" "某个 cap degraded → 判失败"
    # 明细行(缩进)不得污染集合:degraded 时集合仍应等于期望,只由状态列判失败。
    st 1 "$(rc check_list "$(mk_list "$(echo "$all" | sed 's/^copilot$/copilot:degraded/')")")" "copilot degraded → 判失败(且明细行不计入插件行)"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# --- 正式流程:进镜像跑一次 list,断言集合与状态 ---------------------------------
command -v podman >/dev/null \
    || { echo "plugin-inventory-image: 缺 podman,无法在镜像内断言(fail-closed)" >&2; exit 1; }
out="$(podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-host list)"
printf '%s\n' "$out"
check_list "$out" || { echo "ERROR: 镜像内插件清单断言失败"; exit 1; }
echo "OK: 镜像内插件清单 == CAPS + copilot($(printf '%s\n' "$(expected_caps)" | grep -c .) 个),0 degraded"
