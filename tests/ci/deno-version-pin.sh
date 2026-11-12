#!/usr/bin/env bash
# Deno 运行时钉版本门禁:版本 / 校验和 / 支持窗口三者的单一事实源必须自洽,
# 且镜像脚本与 CI 都真的读它。任何一处漂移即拒。
#
# 判据(全部静态,不下载、不碰镜像):
#   1. deno-version.env 三项齐备且形态合法;
#   2. .tool-versions 的 deno 版本与 DENO_VERSION 相同(本地 asdf 不能跑另一个版本);
#   3. build.yml 不出现浮动版本(deps 由 mapping 解析,不在注释里误判);
#   4. 65-ai-safety.sh 读 env 文件,且不再出现 deno.land/install.sh;
#   5. 当天(UTC)不超过 DENO_LTS_END。
#
# 用法:
#   deno-version-pin.sh [--selftest]
set -euo pipefail

ROOT="${DENO_PIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
ENV_FILE="$ROOT/files/scripts/deno-version.env"
TOOL_VERSIONS="$ROOT/.tool-versions"
INSTALL_SH="$ROOT/files/scripts/65-ai-safety.sh"
WORKFLOW="$ROOT/.github/workflows/build.yml"

fail_n=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fail_n=$((fail_n + 1)); }
ok() { printf '  OK  %s\n' "$1"; }

# load_env <文件> —— 以子 shell 读取三项,缺失即空串。
load_env() {
    [ -f "$1" ] || return 0
    # shellcheck disable=SC1090
    ( set +u; . "$1"; printf '%s\t%s\t%s\n' \
        "${DENO_VERSION:-}" "${DENO_SHA256_X86_64:-}" "${DENO_LTS_END:-}" )
}

check_env_form() {
    local v s d
    if [ ! -f "$ENV_FILE" ]; then fail "缺少 files/scripts/deno-version.env"; return; fi
    # read 在空输入上返回 1,set -e 下会直接终止脚本;`|| true` 让空值走下面的形态判定。
    IFS=$'\t' read -r v s d <<<"$(load_env "$ENV_FILE")" || true
    [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "DENO_VERSION 形态非法: '$v'(需 x.y.z,不带 v 前缀)"
    [[ "$s" =~ ^[0-9a-f]{64}$ ]] || fail "DENO_SHA256_X86_64 形态非法(需 64 位小写 hex)"
    [[ "$d" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "DENO_LTS_END 形态非法(需 YYYY-MM-DD)"
    # 注意:不能写成 `[ ... ] && ok ...` —— set -e 下条件为假会让函数返回 1,整脚本误退。
    if [ "$fail_n" -eq 0 ]; then ok "deno-version.env 三项形态合法"; fi
}

check_tool_versions() {
    local v _ _ tv
    IFS=$'\t' read -r v _ _ <<<"$(load_env "$ENV_FILE")" || true
    tv="$(awk '$1=="deno"{print $2; exit}' "$TOOL_VERSIONS" 2>/dev/null || true)"
    if [ "$tv" != "$v" ]; then
        fail ".tool-versions 的 deno='$tv' 与 DENO_VERSION='$v' 不一致"
    else
        ok ".tool-versions 与事实源一致($v)"
    fi
}

check_workflow_no_floating() {
    # 只看 `deno-version:` 这个 deps 行的值,不看注释:注释里出现同名字样不是违规。
    local vals
    vals="$(awk '/^[[:space:]]*deno-version:/{sub(/^[[:space:]]*deno-version:[[:space:]]*/,""); print}' "$WORKFLOW" 2>/dev/null || true)"
    if [ -z "$vals" ]; then
        fail "build.yml 里找不到 deno-version: 行"
    elif printf '%s\n' "$vals" | grep -qE '^v?[0-9]+(\.[0-9x]+)*(\.[x])?$|^v2\.x$|^latest$|^lts$'; then
        fail "build.yml 仍是浮动版本:'$(printf '%s' "$vals" | head -1)'(必须取自事实源)"
    else
        ok "build.yml 的 deno-version 不再写死浮动版本"
    fi
}

check_install_script() {
    if grep -q 'deno.land/install.sh' "$INSTALL_SH" 2>/dev/null; then
        fail "65-ai-safety.sh 仍走 deno.land/install.sh(不可复现)"
    fi
    if ! grep -q 'deno-version.env' "$INSTALL_SH" 2>/dev/null; then
        fail "65-ai-safety.sh 未读取 deno-version.env"
    fi
    # 注意:不能写成 `[ ... ] && ok ...` —— set -e 下条件为假会让函数返回 1,整脚本误退。
    if [ "$fail_n" -eq 0 ]; then ok "65-ai-safety.sh 读事实源且不再走 install.sh"; fi
}

check_support_window() {
    local _ _ d today
    IFS=$'\t' read -r _ _ d <<<"$(load_env "$ENV_FILE")" || true
    [ -n "$d" ] || return 0
    today="${DENO_PIN_TODAY:-$(date -u +%F)}"
    if [[ "$today" > "$d" ]]; then
        fail "Deno 支持窗口已过期(今天 $today > DENO_LTS_END $d):需重新裁决运行时版本"
    else
        ok "支持窗口未过期($today ≤ $d)"
    fi
}

run_checks() {
    check_env_form; check_tool_versions; check_workflow_no_floating
    check_install_script; check_support_window
}

if [ "${1:-}" = "--selftest" ]; then
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    printf '== deno-version-pin.sh --selftest ==\n'
    st() { if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
           else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi; }

    mkfixture() { # mkfixture <dir> <version> <sha> <lts_end> <toolver> <wfval> <install_body>
        local d="$1"
        mkdir -p "$d/files/scripts" "$d/.github/workflows"
        printf 'DENO_VERSION=%s\nDENO_SHA256_X86_64=%s\nDENO_LTS_END=%s\n' "$2" "$3" "$4" >"$d/files/scripts/deno-version.env"
        printf 'deno %s\n' "$5" >"$d/.tool-versions"
        printf '      - uses: denoland/setup-deno@abc # v2\n        with:\n          deno-version: %s\n' "$6" >"$d/.github/workflows/build.yml"
        printf '%s\n' "$7" >"$d/files/scripts/65-ai-safety.sh"
    }

    GOOD_SHA="$(printf 'a%.0s' $(seq 64))"
    GOOD_INSTALL='. "$(dirname "$0")/deno-version.env"'

    # run_child <root> <today> —— 在 fixture 上跑一次门禁本体,回显其退出码。
    # 必须用 `|| rc=$?` 捕获:裸写 `( ... ); st 0 "$?"` 会让 set -e 在第一个
    # 预期失败用例上直接终止 selftest,后面五个用例根本跑不到。
    run_child() {
        local rc=0
        ( DENO_PIN_ROOT="$1" DENO_PIN_TODAY="$2" bash "$0" ) >/dev/null 2>&1 || rc=$?
        printf '%s' "$rc"
    }

    # 1) 全绿
    d="$tmp/good"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 '${{ steps.pin.outputs.version }}' "$GOOD_INSTALL"
    st 0 "$(run_child "$d" 2026-11-12)" "全绿输入通过"

    # 2) .tool-versions 漂移
    d="$tmp/tv"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.5 '${{ steps.pin.outputs.version }}' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "tool-versions 漂移被拒"

    # 3) CI 浮动版本
    d="$tmp/wf"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 'v2.x' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "CI 浮动版本被拒"

    # 4) 仍走 install.sh
    d="$tmp/sh"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 '${{ steps.pin.outputs.version }}' 'curl https://deno.land/install.sh | sh'
    st 1 "$(run_child "$d" 2026-11-12)" "install.sh 残留被拒"

    # 5) 支持窗口过期
    d="$tmp/exp"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 '${{ steps.pin.outputs.version }}' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2027-02-01)" "窗口过期被拒"

    # 6) 校验和形态非法
    d="$tmp/sha"; mkfixture "$d" 2.9.8 "deadbeef" 2027-01-31 2.9.8 '${{ steps.pin.outputs.version }}' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "sha256 形态非法被拒"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

printf '== deno-version-pin ==\n'
run_checks
if [ "$fail_n" -eq 0 ]; then echo "OK: Deno 运行时钉版本自洽"; exit 0; fi
echo "FAILED: $fail_n 处违规" >&2; exit 1
