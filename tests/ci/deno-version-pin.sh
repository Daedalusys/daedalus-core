#!/usr/bin/env bash
# Deno 运行时钉版本门禁:版本 / 校验和 / 支持窗口只有 deno-version.env 一个事实源,
# 镜像脚本与 CI 都必须从它取值,任何一处漂移即拒(全静态,不下载、不碰镜像)。
# 用法: deno-version-pin.sh [--selftest]
set -euo pipefail

ROOT="${DENO_PIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
ENV_FILE="$ROOT/files/scripts/deno-version.env"
TOOL_VERSIONS="$ROOT/.tool-versions"
INSTALL_SH="$ROOT/files/scripts/65-ai-safety.sh"
WORKFLOW="$ROOT/.github/workflows/build.yml"

fail_n=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fail_n=$((fail_n + 1)); }
ok() { printf '  OK  %s\n' "$1"; }

# non_comment_lines <文件> —— 去掉整行注释与空行后的有效行。
# 「字符串出现过」不等于「代码真在」:注释掉的 source 行、注释里的同名字样都曾让
# 门禁假绿,故凡是断言"某动作存在"的判据一律只看非注释行。
non_comment_lines() {
    awk '!/^[[:space:]]*#/ && !/^[[:space:]]*$/' "$1" 2>/dev/null || true
}

# load_env <文件> —— 以子 shell 读取三项,缺失即空串。
load_env() {
    [ -f "$1" ] || return 0
    # shellcheck disable=SC1090
    ( set +u; . "$1"; printf '%s\t%s\t%s\n' \
        "${DENO_VERSION:-}" "${DENO_SHA256_X86_64:-}" "${DENO_LTS_END:-}" )
}

# 形态先立:下游每个消费点都拿这三项当输入,形态错会让后续判据与镜像构建一起失真。
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

# 本地 asdf 必须跑与镜像同一个版本,否则本地验过的行为在镜像里毫无保证。
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

# CI 若让 setup-deno 自行解析版本,镜像内实际运行时与事实源脱钩,钉版本当场作废。
# 判据是白名单而非黑名单:黑名单只认识"浮动版本"的写法,带引号的字面量、指向别的
# step 的表达式都能溜过去;而"表达式写对了但生产端 step 被删"会让 outputs 解析成空串,
# setup-deno 回落默认版本 —— 同样是浮动,却是黑名单永远看不见的形态。
check_workflow_no_floating() {
    local expected wfbody vals v
    expected='${{ steps.deno-pin.outputs.version }}'
    wfbody="$(non_comment_lines "$WORKFLOW")"
    # 只取非注释的 `deno-version:` 行的值:注释里出现同名字样不是违规。
    vals="$(awk '/^[[:space:]]*deno-version:/{
                    sub(/^[[:space:]]*deno-version:[[:space:]]*/, "");
                    sub(/[[:space:]]+$/, "");
                    print
                }' <<<"$wfbody")"
    if ! grep -qE '^[[:space:]]*deno-version:' <<<"$wfbody"; then
        fail "build.yml 里找不到 deno-version: 行"
    elif [ -z "$vals" ]; then
        fail "build.yml 的 deno-version 值为空(空值会让 setup-deno 回落默认版本)"
    else
        while IFS= read -r v; do
            if [ "$v" != "$expected" ]; then
                fail "build.yml 的 deno-version 不是 '$expected',而是 '$v'(版本必须取自事实源;字面量、浮动版本、指向别的 step 都不算)"
            fi
        done <<<"$vals"
    fi
    # 取值链的生产端必须真实存在:step 消失时表达式本身仍然"合法",只是永远解析成空串。
    if ! awk '/^[[:space:]]*id:[[:space:]]*deno-pin[[:space:]]*$/{found=1} END{exit !found}' <<<"$wfbody"; then
        fail "build.yml 里没有 'id: deno-pin' 的 step(取值表达式的生产端不存在,setup-deno 会回落默认版本)"
    fi
    # 注意:不能写成 `[ ... ] && ok ...` —— set -e 下条件为假会让函数返回 1,整脚本误退。
    if [ "$fail_n" -eq 0 ]; then ok "build.yml 的 deno-version 取自事实源,且生产端 step 在位"; fi
}

# install.sh 拉"当时最新"且不经校验和,既不可复现又绕过 DENO_SHA256 的完整性保证。
# 同理,只断言"文件里出现过 deno-version.env"挡不住被注释掉的 source 行,也不保证
# 下载后真的校验过 —— 故三条动作各自单独断言,且都只看非注释行。
check_install_script() {
    local body
    body="$(non_comment_lines "$INSTALL_SH")"
    if grep -q 'deno.land/install.sh' "$INSTALL_SH" 2>/dev/null; then
        fail "65-ai-safety.sh 仍走 deno.land/install.sh(不可复现)"
    fi
    if ! grep -qE '^[[:space:]]*\.[[:space:]].*deno-version\.env' <<<"$body"; then
        fail "65-ai-safety.sh 没有非注释的 source 行读取 deno-version.env(注释里的同名字样不算)"
    fi
    if ! grep -qF 'sha256sum -c' <<<"$body"; then
        fail "65-ai-safety.sh 没有非注释的 'sha256sum -c'(下载后不校验等于 CDN 给什么就装什么)"
    fi
    if ! grep -qF '${DENO_SHA256_X86_64}' <<<"$body"; then
        fail "65-ai-safety.sh 的校验行没引用 \${DENO_SHA256_X86_64}(校验和必须取自事实源)"
    fi
    # 注意:不能写成 `[ ... ] && ok ...` —— set -e 下条件为假会让函数返回 1,整脚本误退。
    if [ "$fail_n" -eq 0 ]; then ok "65-ai-safety.sh 读事实源、按事实源校验和校验且不走 install.sh"; fi
}

# 支持窗口结束即不再有安全补丁,继续发版等于把无补丁运行时固化进镜像,必须重新裁决。
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

    mkfixture() { # mkfixture <dir> <version> <sha> <lts_end> <toolver> <wfval> <install_body> [<with_pin_step:1|0>]
        local d="$1" with_pin="${8:-1}"
        mkdir -p "$d/files/scripts" "$d/.github/workflows"
        printf 'DENO_VERSION=%s\nDENO_SHA256_X86_64=%s\nDENO_LTS_END=%s\n' "$2" "$3" "$4" >"$d/files/scripts/deno-version.env"
        printf 'deno %s\n' "$5" >"$d/.tool-versions"
        # 结构照抄真实 build.yml:一个产出 steps.deno-pin.outputs.version 的 step,
        # 一个消费它的 setup-deno step。三行 YAML 表达不了"取值链断开",而漏检正是
        # 断在这种结构上。
        {
            if [ "$with_pin" = 1 ]; then
                printf '      - name: Read pinned Deno version\n        id: deno-pin\n        run: |\n          echo "version=1" >> "$GITHUB_OUTPUT"\n'
            fi
            printf '      - uses: denoland/setup-deno@abc # v2\n        with:\n          deno-version: %s\n' "$6"
        } >"$d/.github/workflows/build.yml"
        printf '%s\n' "$7" >"$d/files/scripts/65-ai-safety.sh"
    }

    GOOD_SHA="$(printf 'a%.0s' $(seq 64))"
    GOOD_WF='${{ steps.deno-pin.outputs.version }}'
    # 夹具必须带全三个动作(source / 按事实源校验和校验 / 解包),否则"全绿"用例
    # 本身就会红,负例也就失去意义。
    GOOD_INSTALL="$(cat <<'EOF'
# Deno 版本 / 校验和的唯一事实源。
# shellcheck source=/dev/null
. "$(dirname "$0")/deno-version.env"
curl -fsSL -o "$zip" "$url"
printf '%s  %s\n' "${DENO_SHA256_X86_64}" "$zip" | sha256sum -c -
unzip -o -d /usr/local/bin "$zip"
EOF
)"
    NO_SUM_INSTALL="$(printf '%s\n' "$GOOD_INSTALL" | grep -v 'sha256sum -c')"
    COMMENTED_SRC_INSTALL="$(printf '%s\n' "$GOOD_INSTALL" | sed 's/^\. /#. /')"

    # run_child <root> <today> —— 在 fixture 上跑一次门禁本体,回显其退出码。
    # 必须用 `|| rc=$?` 捕获:裸写 `( ... ); st 0 "$?"` 会让 set -e 在第一个
    # 预期失败用例上直接终止 selftest,后面五个用例根本跑不到。
    run_child() {
        local rc=0
        ( DENO_PIN_ROOT="$1" DENO_PIN_TODAY="$2" bash "$0" ) >/dev/null 2>&1 || rc=$?
        printf '%s' "$rc"
    }

    # 1) 全绿
    d="$tmp/good"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" "$GOOD_INSTALL"
    st 0 "$(run_child "$d" 2026-11-12)" "全绿输入通过"

    # 2) .tool-versions 漂移
    d="$tmp/tv"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.5 "$GOOD_WF" "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "tool-versions 漂移被拒"

    # 3) CI 浮动版本
    d="$tmp/wf"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 'v2.x' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "CI 浮动版本被拒"

    # 4) 仍走 install.sh
    d="$tmp/sh"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" 'curl https://deno.land/install.sh | sh'
    st 1 "$(run_child "$d" 2026-11-12)" "install.sh 残留被拒"

    # 5) 支持窗口过期
    d="$tmp/exp"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2027-02-01)" "窗口过期被拒"

    # 6) 校验和形态非法
    d="$tmp/sha"; mkfixture "$d" 2.9.8 "deadbeef" 2027-01-31 2.9.8 "$GOOD_WF" "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "sha256 形态非法被拒"

    # 7) 校验行被删:文件里仍出现 deno-version.env,但下载后不再校验
    d="$tmp/nosum"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" "$NO_SUM_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "缺 sha256sum -c 被拒"

    # 8) source 行被注释掉:字符串仍在文件里,但代码里没有
    d="$tmp/cmtsrc"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" "$COMMENTED_SRC_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "source 行被注释被拒"

    # 9) 生产端 step 消失:表达式看着没变,实际永远解析成空串
    d="$tmp/nopin"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 "$GOOD_WF" "$GOOD_INSTALL" 0
    st 1 "$(run_child "$d" 2026-11-12)" "缺 deno-pin step 被拒"

    # 10) 带引号的字面量:黑名单只看无引号写法,这条曾静默放行
    d="$tmp/quoted"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 '"2.9.8"' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "带引号字面量被拒"

    # 11) 值指向别的 step id:表达式形态对,但取值链指错
    d="$tmp/otherid"; mkfixture "$d" 2.9.8 "$GOOD_SHA" 2027-01-31 2.9.8 '${{ steps.other.outputs.version }}' "$GOOD_INSTALL"
    st 1 "$(run_child "$d" 2026-11-12)" "指向别的 step 被拒"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

printf '== deno-version-pin ==\n'
run_checks
if [ "$fail_n" -eq 0 ]; then echo "OK: Deno 运行时钉版本自洽"; exit 0; fi
echo "FAILED: $fail_n 处违规" >&2; exit 1
