#!/usr/bin/env bash
# 插件清单集合相等门禁:四份清单必须相等 ———
#   ① 76 脚本的 CAPS          (镜像接线面的事实源,硬门禁)
#   ② 被追踪的插件 systemd 单元 (daedalus-<cap>.service,排除 core 自身 4 个,硬门禁)
#   ③ 兄弟仓 plugins 的作者态 manifest (daedalus-plugins/<cap>/daedalus.plugin.json)
#   ④ 兄弟仓 plugins 的 release 供料 cap 集合(其 release 工作流的 env.CAPS)
# 任一份多出或少一个 cap 即 exit 1 —— 「有 manifest 没二进制」的插件不得再随镜像交付。
# ③④ 依赖兄弟仓:来源不可用(兄弟仓缺失)时各自打印 NOTE 并跳过该对(单仓 clone 场景),
# 不得静默当作通过;core 自己的 CI 四仓平级检出,两份都在,故 CI 里恒为硬核对。
#
# 用法:plugin-inventory.sh [--selftest]
set -euo pipefail

# core 自身的 4 个单元不是插件,集合比对时必须排除,否则永远不等:
#   daedalus-audit / daedalus-audit-rotate / daedalus-env / daedalus-host-controller
CORE_UNITS="audit audit-rotate env host-controller"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CAPS_SRC="$ROOT/files/scripts/76-daedalus-plugin-gen.sh"
# 第三/四份来源的兄弟仓根。兄弟仓缺失(如单仓 clone)时两份各自降级为 NOTE,不失败。
PLUGINS_ROOT="${DAEDALUS_PLUGINS_ROOT:-$ROOT/../daedalus-plugins}"
# 第四份来源:兄弟仓 plugins 的 release 工作流供料名单。
# 环境变量可覆盖:仅供 --selftest 指向一份「存在但解析不出名单」的构造文件,
# 验证正式流程 fail-closed;生产路径恒为兄弟仓的 release 工作流。
PLUGINS_RELEASE_YML="${DAEDALUS_PLUGINS_RELEASE_YML:-$PLUGINS_ROOT/.github/workflows/release.yml}"

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

# manifests_from_sibling —— 兄弟仓 plugins 里**作者态**的插件 manifest(cap 名)。
#
# 第③份为什么不再读本仓安装态:files/system/opt/daedalus/plugins/ 是构建产物目录
# (just build 链的 fetch-plugins / just plugin-pack 把 release zip 解进去),其 manifest
# 带 Pack 注入的**编译产物 sha256**。编译产物不可跨机器/跨提交复现(实测同一份源码在
# 默认旗标 / -buildvcs=false / 入库值三种条件下三个不同哈希),那份 manifest 一旦入库,
# 任何一次 plugin-pack 都会把它改脏且无从判断是真漂移还是本机工具链差异。故该目录整体
# 不入库(见 .gitignore 与 AGENTS「镜像安装态策略」),第③份回到作者态事实源:插件仓里
# 声明了 manifest 的 cap 目录。核对语义不变 —— 有 manifest 的插件必须被镜像交付。
manifests_from_sibling() {
    local d="$PLUGINS_ROOT"
    local f cap
    [ -d "$d" ] || return 1
    for f in "$d"/*/daedalus.plugin.json; do
        [ -e "$f" ] || continue
        cap="$(basename "$(dirname "$f")")"
        echo "$cap"
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
# ③④ 传空集 = 该来源不可用(兄弟仓缺失),跳过该对;调用方负责打印 NOTE ——
# 门禁静默变窄正是它要消灭的假绿,所以"跳过"必须留痕,且不得被当成"通过"。
inventory_check() {
    local caps="$1" units="$2" manifests="$3" release="${4:-}"
    local rc=0
    compare_sets "CAPS" "$caps" "插件单元" "$units" || rc=1
    if [ -n "$manifests" ]; then
        compare_sets "CAPS" "$caps" "插件 manifest" "$manifests" || rc=1
    fi
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
    # ③ 来源不可用(空集)→ 跳过该对,不得判不等;也不得把它当作"已核对"。
    st 0 "$(rc inventory_check "$good" "$good" "" "$good")" "manifest 来源不可用(空集)→ 跳过该对"
    st 0 "$(rc inventory_check "$good" "$good" "$good" "")" "release 来源不可用(空集)→ 跳过该对"

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
# `|| true` 同 release_caps:manifests_from_sibling 在兄弟仓缺失时返回 1,
# set -e 会在赋值处直接退出,那样下面的 NOTE 永远打不出来。
manifests="$(manifests_from_sibling)" || true
if [ -z "$caps" ]; then
    echo "plugin-inventory: 无法从 $CAPS_SRC 解析 CAPS(单一事实源缺失)" >&2
    exit 1
fi
if [ -z "$manifests" ]; then
    echo "NOTE: 兄弟仓 plugins 的作者态 manifest 不可用($PLUGINS_ROOT),跳过第三份(manifest)核对" >&2
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
    # 报"几份"如实数:③④ 不可用时只比 2 份,不能仍写"四份一致"。
    n_src=2
    [ -n "$manifests" ] && n_src=$((n_src + 1))
    [ -n "$release" ] && n_src=$((n_src + 1))
    echo "OK: 插件清单 ${n_src} 份一致($(printf '%s\n' "$caps" | grep -c .) 个 cap)"
else
    exit 1
fi
