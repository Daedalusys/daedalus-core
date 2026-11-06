#!/usr/bin/env bash
# =============================================================================
# probe-plugin-inventory.sh —— A12:镜像内插件 degraded 盘点(真 guest 内)
# =============================================================================
# 证伪什么
#   「有 manifest 没二进制」的插件会以 degraded 形态随镜像交付(core#53)。
#   单测读的是仓内清单文件,盘不到**镜像里装出来的那一份**。
#
# 期望集合从构建产物派生,不写死名单
#   期望 = 76-daedalus-plugin-gen.sh 的 CAPS + copilot。注意 core#53 已 CLOSED,
#   不得再拿「预期 8 个 degraded」当判据;判据是「集合恰为 CAPS + copilot 且无
#   degraded」。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a12 bash tests/real-machine/probe-plugin-inventory.sh
#   CAPS 来源: 96 脚本(tests/real-machine/../../files/scripts/76-daedalus-plugin-gen.sh)
#              或环境变量 DAEDALUS_EXPECTED_CAPS(脚本不在仓内时的显式入口)。
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-plugin-inventory.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

# 非 root 口径发现 degraded 时填入 issue 号。
A12_ISSUE="${A12_ISSUE:-Daedalusys/daedalus-core#89}"

CAPS_SRC="$HERE/../../files/scripts/76-daedalus-plugin-gen.sh"
if [ -n "${DAEDALUS_EXPECTED_CAPS:-}" ]; then
    CAPS="$DAEDALUS_EXPECTED_CAPS"
elif [ -f "$CAPS_SRC" ]; then
    CAPS="$(sed -n 's/^CAPS="\(.*\)"$/\1/p' "$CAPS_SRC" | head -n 1)"
else
    die "拿不到期望 CAPS:既无 $CAPS_SRC 也无 DAEDALUS_EXPECTED_CAPS(不写死名单)"
fi
[ -n "$CAPS" ] || die "从 76 脚本解析 CAPS 为空"

EXPECTED="$( { for c in $CAPS; do echo "daedalus.$c"; done; echo "daedalus.copilot"; } | sort | tr '\n' ' ')"
info "期望插件集合(从 76 CAPS 派生 + copilot): $EXPECTED"

driver_init
driver_boot

LIST="$(gssh "sudo /usr/local/bin/daedalus-host list 2>&1" 2>&1)"
say "A12-0 · guest 内 daedalus-host list 原始输出(root 口径:与仓内契约一致)"
printf '%s\n' "$LIST" | sed 's/^/      /'

# 表格形态:<id> <name> <version> <type> <runtime> <i18n> <status>;status ∈ ok/degraded。
ACTUAL="$(printf '%s\n' "$LIST" | awk '/^daedalus\./ {print $1}' | sort | tr '\n' ' ')"
DEGRADED="$(printf '%s\n' "$LIST" | awk '/^daedalus\./ && $NF=="degraded" {print $1}' | sort | tr '\n' ' ')"
info "实际插件集合: $ACTUAL"
info "degraded      : ${DEGRADED:-（无）}"

if [ "$ACTUAL" = "$EXPECTED" ]; then
    driver_verdict PASS "A12-1 插件集合恰为 CAPS + copilot($(printf '%s' "$ACTUAL" | wc -w) 个)"
else
    driver_verdict FAIL "A12-1 插件集合与期望不等: 实际=[$ACTUAL] 期望=[$EXPECTED]"
fi

if [ -z "${DEGRADED// /}" ]; then
    driver_verdict PASS "A12-2 无 degraded 插件(root 口径)"
else
    driver_verdict FAIL "A12-2 存在 degraded 插件(root 口径): $DEGRADED"
fi

# 非 root 口径:普通用户跑宿主 CLI 是产品的真实使用姿势(copilot CLI 由用户
# 发起),故单独记录 —— 不并入上面的判定,但发现异常即记 issue 号码。
say "A12-3 · 非 root 口径(普通用户跑宿主 CLI)的可读性"
MODES="$(gssh "stat -c '%a %n' /opt/daedalus/plugins/*/ 2>&1" 2>&1)"
printf '%s\n' "$MODES" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
LIST_U="$(gssh "/usr/local/bin/daedalus-host list 2>&1" 2>&1)"
DEGRADED_U="$(printf '%s\n' "$LIST_U" | awk '/^daedalus\./ && $NF=="degraded" {print $1}' | sort | tr '\n' ' ')"
if [ -z "${DEGRADED_U// /}" ]; then
    driver_verdict PASS "A12-3 非 root 口径亦无 degraded"
else
    driver_verdict NOTE "A12-3 非 root 口径出现 degraded: $DEGRADED_U(目录权限见上;issue $A12_ISSUE)"
    printf '      目录权限明细: %s\n' "$(printf '%s' "$MODES" | tr '\n' ' ')"
    printf '      /sysroot 侧权限: %s\n' "$(gssh "sudo ls -ld /sysroot/opt/daedalus/plugins/daedalus.copilot /sysroot/ostree/deploy/*/deploy/*/opt/daedalus/plugins/daedalus.copilot 2>&1 | head -3" 2>&1 | tr '\n' ' ')"
fi


driver_exit
