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
# 环境变量可覆盖:仅供 --selftest 指向一份「存在但解析不出名单」的构造文件,
# 验证正式流程 fail-closed;生产路径恒为兄弟仓的 release 工作流。
PLUGINS_RELEASE_YML="${DAEDALUS_PLUGINS_RELEASE_YML:-$ROOT/../daedalus-plugins/.github/workflows/release.yml}"

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
#
# plugins 仓自 #72 起把清单收敛为 workflow 级 `env.CAPS` 单一事实源,release 步骤
# 里写成 `for cap in $CAPS; do`。取到 `$CAPS` 这类**引用**时必须回同一文件的 env
# 段解析出真实名单 —— 否则会把字面量 `$CAPS` 当成一个 cap,门禁报出根本不存在的
# 漂移(2026-11-04 起 core main 的 CI 就红在这个假漂移上)。
# 解析不出 → 返回空,由调用方 fail-closed:不得静默跳过第四份核对。
release_caps() {
    local f="$1" raw resolved var
    [ -f "$f" ] || return 1
    raw="$(sed -n 's/.*for cap in \(.*\); do.*/\1/p' "$f" | head -n 1)"
    [ -n "$raw" ] || return 1
    resolved="$raw"
    case "$raw" in
        '$'*|'${'*)
            var="${raw#\$\{}"; var="${var%\}}"; var="${var#\$}"
            resolved="$(awk -v var="$var" '
                /^[ \t]*#/ { next }
                $0 ~ "^[ \t]*" var ":" {
                    q = sprintf("%c", 39)
                    v = $0
                    sub("^[ \t]*" var ":[ \t]*", "", v)
                    gsub(/^[ \t]+|[ \t]+$/, "", v)
                    gsub(/^"|"$/, "", v)
                    gsub("^" q "|" q "$", "", v)
                    print v
                    exit
                }' "$f")"
            [ -n "$resolved" ] || return 1
            ;;
    esac
    printf '%s\n' "$resolved" | tr ' ' '\n' | sed '/^$/d' | sort
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

    # 5) 第四份来源「存在但解析不出名单」→ 正式流程必须 fail-closed(exit 1),
    #    不得静默跳过(跳过会让第四份核对凭空消失,门禁却仍报 OK)。
    doctored="$(mktemp)"
    printf 'name: daedalus-plugins\non: push\n' > "$doctored"
    rc=0
    msg="$(DAEDALUS_PLUGINS_RELEASE_YML="$doctored" bash "$0" 2>&1 >/dev/null)" || rc=$?
    st 1 "$rc" "release.yml 存在但无 'for cap in' 行 → 正式流程 exit 1(fail-closed)"
    # 诊断行必须在:fail-closed 静默退出(不打印原因)会让排障者只看到裸 rc=1。
    st 1 "$(printf '%s' "$msg" | grep -c '解析不出 cap 集合')" "同一场景必须打印「解析不出 cap 集合」的诊断"
    rm -f "$doctored"

    # 6) plugins 仓自 #72 起把清单收敛为 workflow 级 `env.CAPS` 单一事实源,
    #    release 步骤写成 `for cap in $CAPS; do`。解析器必须回 env 段取回真实名单,
    #    不能把字面量 `$CAPS` 当成一个 cap —— 那正是 2026-11-04 起 core main
    #    CI 报「集合不等」的假漂移。
    envform="$(mktemp)"
    cat > "$envform" <<'ENVEOF'
env:
  CAPS: "blueprint dupe"
jobs:
  release:
    steps:
      - run: |
          for cap in $CAPS; do
            echo "$cap"
          done
ENVEOF
    st $'blueprint\ndupe' "$(release_caps "$envform")" "release.yml 的 for cap in \$CAPS → 回 env.CAPS 解析出名单"
    rm -f "$envform"

    # 7) `$CAPS` 引用但 env 段解析不出 → 必须为空(fail-closed),
    #    不得把字面量 `$CAPS` 当成一个 cap 混进集合。
    noenv="$(mktemp)"
    printf 'jobs:\n  release:\n    steps:\n      - run: for cap in $CAPS; do echo x; done\n' > "$noenv"
    st "" "$(release_caps "$noenv")" "\$CAPS 解析不出 → 空(fail-closed)"
    rm -f "$noenv"

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
    # `|| true`:release_caps 解析失败会返回 1,而 set -e 会在赋值处直接退出 ——
    # 那样下面的诊断行永远打不出来,fail-closed 就只剩一个裸 rc=1。
    release="$(release_caps "$PLUGINS_RELEASE_YML")" || true
    # 文件在、却解析不出名单 = 该来源已不可用(格式漂移)。此时**不得**当作「跳过」:
    # 静默跳过会让第四份核对凭空消失而门禁仍报 OK —— fail-closed,直接拒。
    [ -n "$release" ] || {
        echo "plugin-inventory: release 工作流存在但解析不出 cap 集合(格式漂移?): $PLUGINS_RELEASE_YML" >&2
        exit 1
    }
else
    echo "NOTE: 兄弟仓 plugins 的 release.yml 不存在($PLUGINS_RELEASE_YML),跳过第四份(release 供料)核对" >&2
fi

if inventory_check "$caps" "$units" "$manifests" "$release"; then
    echo "OK: 插件清单四份一致($(printf '%s\n' "$caps" | grep -c .) 个)"
else
    exit 1
fi
