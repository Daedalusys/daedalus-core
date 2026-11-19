#!/usr/bin/env bash
# iso-verdict-test.sh —— ISO 验证腿的**判定表**行为自检。
#
# 为什么必须有:2026-11-19 的实际事故 —— `iso-verify.sh` 的判定块里
# `elif live_ok -eq 1 → LIVE_OK` 排在 `install_state = BLOCKED_EXTERNAL` 之前,
# 于是「装机因外部网络没装完」被判成 LIVE_OK,而 REQUIRE_INSTALL=1 又把 LIVE_OK
# 判红 —— #198 想消灭的那条假红原封不动地留着,而且从代码上一眼看不出问题
# (两个分支都在,只是后者不可达)。静态 grep「有没有 BLOCKED_EXTERNAL 这个词」
# 完全挡不住这种退化。
#
# 做法:把 iso-verify.sh 里的两块判定逻辑**抽出来真跑**,喂全部输入组合:
#   块 A —— 输入(live_ok / installed_state / install_state)→ verdict
#   块 B —— 输入(verdict / REQUIRE_INSTALL)→ 退出码
#
# 用法: bash tests/ci/iso-verdict-test.sh    # 全绿打印 RESULT: SELFTEST PASS
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERIFY="$ROOT/tests/real-machine/iso-verify.sh"

PASS_N=0
FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}

[ -f "$VERIFY" ] || { printf '  FAIL  找不到 %s\n' "$VERIFY"; printf 'RESULT: SELFTEST FAIL (1)\n'; exit 1; }

# extract_block <起始整行> <结束整行> —— 按**整行字符串**取第一段区间(含首尾)。
# 不用正则也不用 awk -v:那两条路都会把 `[` / `$` / `\n` 解释掉(实测 `-v` 还会把
# `printf '%s\n'` 里的 `\n` 变成真换行),于是永远匹配不上。ENVIRON 不做转义。
extract_block() {
    S="$1" E="$2" awk '
        !f && $0 == ENVIRON["S"] { f = 1 }
        f { print }
        f && $0 == ENVIRON["E"] { exit }
    ' "$VERIFY"
}

BLOCK_A="$(extract_block 'if [ "$live_ok" -eq 1 ] && [ "$installed_state" = "INSTALLED_LOGIN" ]; then' \
                       'printf '"'"'%s\n'"'"' "$verdict" > "$WORK/iso-verdict.txt"')"
BLOCK_B="$(extract_block 'if [ "$verdict" = "BLOCKED_EXTERNAL" ]; then' 'esac')"

printf '== iso-verdict-test.sh ==\n'

st 1 "$([ -n "$BLOCK_A" ] && echo 1 || echo 0)" "抽得出判定块 A(输入 → verdict)"
st 1 "$(printf '%s' "$BLOCK_A" | grep -c 'iso-verdict.txt')" "块 A 确实来自 iso-verify.sh(写出判定文件)"
st 1 "$([ -n "$BLOCK_B" ] && echo 1 || echo 0)" "抽得出判定块 B(verdict → 退出码)"
st 1 "$([ "$(printf '%s' "$BLOCK_B" | grep -c 'REQUIRE_INSTALL')" -ge 1 ] && echo 1 || echo 0)" \
    "块 B 确实是退出码映射"

WORK_BIN="$(mktemp -d)"
trap 'rm -rf "$WORK_BIN"' EXIT

# verdict_of <live_ok> <installed_state> <install_state> —— 真跑判定块 A。
verdict_of() {
    ( set +u
      live_ok="$1"; installed_state="$2"; install_state="$3"; WORK="$WORK_BIN"
      # shellcheck disable=SC2086
      eval "$BLOCK_A" >/dev/null 2>&1
      cat "$WORK_BIN/iso-verdict.txt" 2>/dev/null )
}

# rc_of_verdict <verdict> <REQUIRE_INSTALL> —— 真跑判定块 B(warn/bad 打桩)。
rc_of_verdict() {
    ( set +u
      verdict="$1"; REQUIRE_INSTALL="$2"
      warn() { :; }; bad() { :; }
      # shellcheck disable=SC2086
      eval "$BLOCK_B" >/dev/null 2>&1 )
}

# --- 块 A:四种结局一个都不能错 ---
st "INSTALLED_LOGIN" "$(verdict_of 1 INSTALLED_LOGIN DONE)" \
    "live 通过 + 装机完成 → INSTALLED_LOGIN"
st "BLOCKED_EXTERNAL" "$(verdict_of 1 SKIPPED BLOCKED_EXTERNAL)" \
    "live 通过 + 装机全 transient(外因)→ BLOCKED_EXTERNAL(2026-11-19 事故的回归点)"
st "LIVE_OK" "$(verdict_of 1 SKIPPED NOT_DONE)" \
    "live 通过 + 装机逻辑失败 → LIVE_OK(装机段自己已判红)"
st "LIVE_OK" "$(verdict_of 1 SKIPPED FAILED)" \
    "live 通过 + 装机单元 failed → LIVE_OK"
st "LIVE_UNREACHABLE" "$(verdict_of 0 SKIPPED BLOCKED_EXTERNAL)" \
    "live 不通 → LIVE_UNREACHABLE(外因不能盖过 live 判据)"

# --- 块 B:外因以 0 退出且不晋级;缺装机仍以非 0 退出 ---
st "0" "$(rc_of_verdict BLOCKED_EXTERNAL 1; echo $?)" \
    "BLOCKED_EXTERNAL → 退出码 0(外因不判红)"
st "1" "$(rc_of_verdict LIVE_OK 1; echo $?)" \
    "LIVE_OK + REQUIRE_INSTALL=1 → 退出码 1(没装机就是失败)"
st "0" "$(rc_of_verdict LIVE_OK 0; echo $?)" \
    "LIVE_OK + REQUIRE_INSTALL=0 → 退出码 0(仅本地排查放宽)"
st "0" "$(rc_of_verdict INSTALLED_LOGIN 1; echo $?)" \
    "INSTALLED_LOGIN → 退出码 0"
st "1" "$(rc_of_verdict LIVE_UNREACHABLE 0; echo $?)" \
    "LIVE_UNREACHABLE → 退出码 1(放宽装机也要 live 自证)"

printf -- '---------------- %d 条通过 / %d 条失败 ----------------\n' "$PASS_N" "$FAIL_N"
if [ "$FAIL_N" -eq 0 ]; then
    printf 'RESULT: SELFTEST PASS\n'
    exit 0
fi
printf 'RESULT: SELFTEST FAIL (%d)\n' "$FAIL_N"
exit 1
