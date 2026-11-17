#!/usr/bin/env bash
# 跨仓制品边钉版本门禁:SDK 与 plugins 的版本只有 files/scripts/cross-repo-pins.env
# 一个事实源,四个消费点都必须与它一致,且任何一处都不得回落到「默认分支 / latest」。
# 全静态:不下载、不联网、不碰镜像。
#
# 为什么要有这条门禁:2026-11-17 之前 go.mod 是占位版本(CI 回退 SDK 默认分支)、
# 插件取 `gh release download` 不带 tag(永远 latest)。同一个 commit 隔天重跑会拿到
# 不同的 SDK / 插件,「某个 sha 构建出的镜像」无法回查 —— 证据链在这里断掉。
#
# 用法: cross-repo-pin.sh [--selftest]
set -euo pipefail

ROOT="${CROSS_REPO_PIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
ENV_FILE="$ROOT/files/scripts/cross-repo-pins.env"
GOMOD="$ROOT/go.mod"
GOWORK_EXAMPLE="$ROOT/go.work.example"
WORKFLOW="$ROOT/.github/workflows/build.yml"
FETCH="$ROOT/scripts/fetch-plugins.sh"

fail_n=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fail_n=$((fail_n + 1)); }
ok() { printf '  OK  %s\n' "$1"; }

# non_comment_lines <文件> —— 去掉整行注释与空行后的有效行。
# 「字符串出现过」不等于「代码真在」:注释掉的 source 行、注释里的同名字样都曾让
# 门禁假绿,故凡是断言"某动作存在"的判据一律只看非注释行。
non_comment_lines() {
    awk '!/^[[:space:]]*#/ && !/^[[:space:]]*$/' "$1" 2>/dev/null || true
}

# load_pins —— 以子 shell 读取两项,缺失即空串。
load_pins() {
    [ -f "$ENV_FILE" ] || return 0
    # shellcheck disable=SC1090
    ( set +u; . "$ENV_FILE"; printf '%s\t%s\n' \
        "${SDK_VERSION:-}" "${PLUGINS_VERSION:-}" )
}

read_pins() {
    IFS=$'\t' read -r SDK_VERSION PLUGINS_VERSION <<<"$(load_pins)" || true
}

# 形态先立:下游每个消费点都拿这两项当输入,形态错会让后续判据与构建一起失真。
check_env_form() {
    if [ ! -f "$ENV_FILE" ]; then
        fail "缺少 files/scripts/cross-repo-pins.env(跨仓版本唯一事实源)"
        return
    fi
    read_pins
    [[ "$SDK_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || fail "SDK_VERSION 形态非法: '$SDK_VERSION'(需 x.y.z,不带 v 前缀)"
    [[ "$PLUGINS_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || fail "PLUGINS_VERSION 形态非法: '$PLUGINS_VERSION'(需 x.y.z,不带 v 前缀)"
    if [ "$fail_n" -eq 0 ]; then ok "cross-repo-pins.env 两项形态合法(SDK $SDK_VERSION / plugins $PLUGINS_VERSION)"; fi
}

# go.mod 是 Go 工具链唯一认的版本位;它与事实源不一致时,CI 的 checkout ref 会
# 悄悄变成另一个版本 —— 那正是「占位版本回退默认分支」这条老漏洞的入口。
check_gomod() {
    if [ ! -f "$GOMOD" ]; then fail "缺少 go.mod"; return; fi
    if grep -qF 'v0.0.0-00010101000000-000000000000' "$GOMOD"; then
        fail "go.mod 的 SDK require 仍是占位伪版本(CI 会回退 SDK 默认分支)"
    fi
    local got
    got="$(sed -n 's/.*Daedalusys\/daedalus-sdk v\([0-9.]*\).*/\1/p' "$GOMOD" | head -n1)"
    if [ "$got" != "$SDK_VERSION" ]; then
        fail "go.mod require SDK '$got',但事实源是 '$SDK_VERSION'"
    else
        ok "go.mod require SDK v$got,与事实源一致"
    fi
}

# 平级布局的 workspace replace 必须跟着 require 走:漏了它,本机 go build 会去解析
# 一个不存在的 ../daedalus-plugins/daedalus-sdk 路径,表现为「改了依赖本地编不过」。
check_gowork_example() {
    if [ ! -f "$GOWORK_EXAMPLE" ]; then fail "缺少 go.work.example"; return; fi
    if ! non_comment_lines "$GOWORK_EXAMPLE" \
        | grep -qE "replace github\.com/Daedalusys/daedalus-sdk v${SDK_VERSION} => \.\./daedalus-sdk"; then
        fail "go.work.example 缺少 'replace ... daedalus-sdk v${SDK_VERSION} => ../daedalus-sdk' 行"
    else
        ok "go.work.example 的 workspace replace 覆盖 v${SDK_VERSION}"
    fi
}

# CI 的取值链:版本必须从一个 step 的输出流到 checkout ref / 下载 tag,
# 且生产端 step 必须在位(step 消失后表达式仍"合法",只是永远解析成空串 → 浮动)。
check_workflow() {
    if [ ! -f "$WORKFLOW" ]; then fail "缺少 .github/workflows/build.yml"; return; fi
    local body
    body="$(non_comment_lines "$WORKFLOW")"

    if ! grep -qE '^[[:space:]]*(-[[:space:]]+)?id:[[:space:]]*cross-repo-pins[[:space:]]*$' <<<"$body"; then
        fail "build.yml 里没有 'id: cross-repo-pins' 的 step(版本取值链的生产端不存在)"
    fi

    # SDK checkout 的 ref:必须是 pins 输出的表达式,不许留空、不许写字面量。
    # 取「repository: Daedalusys/daedalus-sdk」之后的第一个 ref: 行。
    local sdk_ref
    sdk_ref="$(awk '
        /repository:[[:space:]]*Daedalusys\/daedalus-sdk[[:space:]]*$/ { want=1; next }
        want && /^[[:space:]]*ref:/ {
            sub(/^[[:space:]]*ref:[[:space:]]*/, ""); sub(/[[:space:]]+$/, ""); print; exit
        }' <<<"$body")"
    if [ -z "$sdk_ref" ]; then
        fail "build.yml 的 daedalus-sdk checkout 没有 ref:(会吃默认分支)"
    elif [ "$sdk_ref" != 'v${{ steps.cross-repo-pins.outputs.sdk_version }}' ]; then
        fail "build.yml 的 daedalus-sdk ref 是 '$sdk_ref',期望 pins 输出的表达式"
    else
        ok "build.yml 的 SDK checkout 钉在 pins 输出的 tag 上"
    fi

    # 插件下载:凡是从 daedalus-plugins 的 release 取 zip,必须带上钉死的版本。
    # gh release download 的 tag 是**位置参数**(没有 --tag 这个 flag —— 写过
    # `--tag` 的版本在 CI 上直接 "unknown flag: --tag" 断掉),故判据是
    # 「下载行里带版本表达式」。
    local dl
    dl="$(grep 'gh release download' <<<"$body" | grep 'Daedalusys/daedalus-plugins' || true)"
    if [ -z "$dl" ]; then
        fail "build.yml 里找不到从 daedalus-plugins release 取料的步骤"
    elif ! grep -qF 'steps.cross-repo-pins.outputs.plugins_version' <<<"$dl"; then
        fail "build.yml 的插件下载没带钉死的版本(取的是 latest,历史镜像无法重建)"
    elif grep -q -- '--tag' <<<"$dl"; then
        fail "build.yml 的插件下载用了 --tag:gh release download 没有这个 flag(tag 是位置参数)"
    else
        ok "build.yml 的插件下载钉在 pins 输出的版本上"
    fi
}

# 本机腿必须与 CI 同源:否则本机验过的镜像和 CI 出的镜像是两套依赖。
check_fetch_script() {
    if [ ! -f "$FETCH" ]; then fail "缺少 scripts/fetch-plugins.sh"; return; fi
    local body dl
    body="$(non_comment_lines "$FETCH")"
    dl="$(grep 'gh release download' <<<"$body" || true)"
    if [ -z "$dl" ]; then
        fail "fetch-plugins.sh 里找不到 gh release download"
    elif ! grep -qF 'PLUGINS_VERSION' <<<"$dl"; then
        fail "fetch-plugins.sh 的下载没带钉死的版本(本机腿会取 latest)"
    elif grep -q -- '--tag' <<<"$dl"; then
        fail "fetch-plugins.sh 的下载用了 --tag:gh release download 没有这个 flag(tag 是位置参数)"
    else
        ok "fetch-plugins.sh 的下载钉在 PLUGINS_VERSION 上"
    fi
    if ! grep -qE '^[[:space:]]*(\.|source)[[:space:]]+.*cross-repo-pins\.env' <<<"$body"; then
        fail "fetch-plugins.sh 没有非注释的 source 行读取 cross-repo-pins.env"
    else
        ok "fetch-plugins.sh 从事实源取值"
    fi
}

run_checks() {
    fail_n=0
    check_env_form
    read_pins
    check_gomod
    check_gowork_example
    check_workflow
    check_fetch_script
    return "$fail_n"
}

# 自检:门禁必须能对一份已知不合格的树变红,否则它只是一段永远打印 OK 的脚本。
selftest() {
    local tmp good bad rc=0
    tmp="$(mktemp -d)"
    trap 'rm -rf "${tmp:-}"' EXIT

    mk_good_tree() {
        mkdir -p "$1/files/scripts" "$1/scripts" "$1/.github/workflows"
        cat >"$1/files/scripts/cross-repo-pins.env" <<'EOF'
SDK_VERSION=9.9.9
PLUGINS_VERSION=8.8.8
EOF
        cat >"$1/go.mod" <<'EOF'
module example
require github.com/Daedalusys/daedalus-sdk v9.9.9
replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk
EOF
        cat >"$1/go.work.example" <<'EOF'
replace github.com/Daedalusys/daedalus-sdk v9.9.9 => ../daedalus-sdk
EOF
        cat >"$1/.github/workflows/build.yml" <<'EOF'
jobs:
  t:
    steps:
      - id: cross-repo-pins
        run: echo ok
      - uses: actions/checkout
        with:
          repository: Daedalusys/daedalus-sdk
          path: daedalus-sdk
          ref: v${{ steps.cross-repo-pins.outputs.sdk_version }}
      - run: gh release download "v${{ steps.cross-repo-pins.outputs.plugins_version }}" --repo Daedalusys/daedalus-plugins --pattern '*.plugin.zip'
EOF
        cat >"$1/scripts/fetch-plugins.sh" <<'EOF'
. "$(dirname "$0")/../files/scripts/cross-repo-pins.env"
gh release download "v${PLUGINS_VERSION}" --repo Daedalusys/daedalus-plugins --pattern '*.plugin.zip'
EOF
    }

    good="$tmp/good"; mk_good_tree "$good"
    if CROSS_REPO_PIN_ROOT="$good" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        echo "PASS selftest/good-tree"
    else
        echo "FAIL selftest/good-tree(合格树被判红)" >&2
        rc=1
    fi

    bad="$tmp/bad"; mk_good_tree "$bad"
    printf 'require github.com/Daedalusys/daedalus-sdk v0.0.0-00010101000000-000000000000\n' \
        >>"$bad/go.mod"
    if CROSS_REPO_PIN_ROOT="$bad" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        echo "FAIL selftest/placeholder-go.mod(占位伪版本没被拒)" >&2
        rc=1
    else
        echo "PASS selftest/placeholder-go.mod"
    fi

    bad="$tmp/bad2"; mk_good_tree "$bad"
    sed -i 's|gh release download "v${{ steps.cross-repo-pins.outputs.plugins_version }}" |gh release download |' \
        "$bad/.github/workflows/build.yml"
    if CROSS_REPO_PIN_ROOT="$bad" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        echo "FAIL selftest/latest-plugins(不带 tag 没被拒)" >&2
        rc=1
    else
        echo "PASS selftest/latest-plugins"
    fi

    bad="$tmp/bad3"; mk_good_tree "$bad"
    sed -i 's|v9\.9\.9|v7.7.7|' "$bad/go.work.example"
    if CROSS_REPO_PIN_ROOT="$bad" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        echo "FAIL selftest/gowork-drift(workspace replace 漂移没被拒)" >&2
        rc=1
    else
        echo "PASS selftest/gowork-drift"
    fi

    # --tag 是真实踩过的坑:gh release download 没有这个 flag,CI 上直接
    # "unknown flag: --tag"。门禁必须把这种写法判红,否则它又会溜进 workflow。
    bad="$tmp/bad4"; mk_good_tree "$bad"
    sed -i 's|gh release download "v${{ steps.cross-repo-pins.outputs.plugins_version }}" |gh release download --tag "v${{ steps.cross-repo-pins.outputs.plugins_version }}" |' \
        "$bad/.github/workflows/build.yml"
    if CROSS_REPO_PIN_ROOT="$bad" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        echo "FAIL selftest/gh-flag(不存在的 --tag 没被拒)" >&2
        rc=1
    else
        echo "PASS selftest/gh-flag"
    fi

    echo "RESULT: SELFTEST $([ "${rc:-0}" -eq 0 ] && echo PASS || echo FAIL)(5)"
    return "${rc:-0}"
}

case "${1:-}" in
    --selftest) selftest ;;
    "") run_checks ;;
    *) echo "用法: $0 [--selftest]" >&2; exit 2 ;;
esac
