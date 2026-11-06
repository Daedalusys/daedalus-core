#!/usr/bin/env bash
# =============================================================================
# guest-driver.selftest.sh —— 驱动库自检(不真起 QEMU,用 stub 覆盖外部命令)
# =============================================================================
# 为什么需要
#   驱动库的判定逻辑(结论**累积**、任一 FAIL 即非零)与幂等(已有产物不重跑
#   ~7GB 派生构建)都是「改断言就不可验证」的那类代码:没有自检,把累积改成
#   「最后一次覆盖」或把幂等改瞎,不会有任何东西发现。
# =============================================================================
set -uo pipefail

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

export VERIFY_WORK="$TMP/work"
mkdir -p "$VERIFY_WORK"

# shellcheck source=guest-driver.sh
source "$(dirname "${BASH_SOURCE[0]}")/guest-driver.sh"

fail_n=0
ck() { # ck <说明> <期望> <实际>
    if [ "$2" = "$3" ]; then printf '  PASS  %s\n' "$1"
    else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$1" "$2" "$3"; fail_n=$((fail_n + 1)); fi
}

printf '== guest-driver.selftest ==\n'

# 1) FAIL 之后 driver_exit 非零
( DRIVER_FAIL_N=0; driver_verdict FAIL "x" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "FAIL → driver_exit 非零" 1 $?

# 2) 累积语义:先 PASS 再 FAIL 最终仍非零(不是最后一次覆盖)
( DRIVER_FAIL_N=0; driver_verdict PASS "a" >/dev/null; driver_verdict FAIL "b" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "PASS 后 FAIL 仍非零(累积)" 1 $?

# 3) 全 PASS → 零
( DRIVER_FAIL_N=0; driver_verdict PASS "a" >/dev/null; driver_verdict PASS "b" >/dev/null; driver_exit >/dev/null 2>&1 )
ck "全 PASS → driver_exit 零" 0 $?

# 4) 幂等:工作目录已有 qcow2 与派生镜像时 driver_init 不得重跑 podman build
build_n=0
podman() { case "${1:-}" in build) build_n=$((build_n + 1)) ;; esac; return 0; }
mkdir -p "$VERIFY_WORK/output/qcow2"
: > "$VERIFY_WORK/output/qcow2/disk.qcow2"
head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$VERIFY_WORK/.verify-pass"
driver_init >/dev/null 2>&1
ck "幂等 driver_init rc=0" 0 $?
ck "幂等:已有产物时不重跑 podman build" 0 "$build_n"
ck "幂等:qcow2 路径被识别" 1 "$([ -n "${DRIVER_QCOW2:-}" ] && echo 1 || echo 0)"

printf '\n'
if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
