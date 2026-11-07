#!/usr/bin/env bash
# =============================================================================
# probe-audit-inode.sh —— A4:audit 真 inode 的 chattr +a 与轮转(真 guest 内)
# =============================================================================
# 证伪什么
#   集成测试把 chattr +a 抽象成 noopAttrs{} 注入,FS_IOC_SETFLAGS 从未碰过真
#   inode。「证据层防篡改」这条主张因此全是纸面的。本探针在真 guest 里对真
#   inode 观测:属性位、内核拒绝码、哈希链校验。
#
# 断言链
#   1. /var/log/daedalus/audit.jsonl 存在且 lsattr 含 `a`;
#   2. 写一条 entry 后 `truncate -s 0` 必须失败(EPERM,Operation not permitted);
#   3. `daedalus-audit verify` 输出含 hash chain intact;
#   4. 轮转后 /var/log/daedalus/archive/ 出现 .gz,且 verify 仍通过。
#
# 与计划的一处偏差(如实记录):`daedalus-audit-rotate.service` 不带 --force,
# 阈值来自 policy 的 256MB —— 新 guest 上日志远小于阈值,服务正确地 no-op。
# 故本探针既跑一次服务(记录其真实输出),又用 `rotate --force` 强制走轮转路径
# 验证「rename→gzip→链仍完好」这条机制本身。两者都如实记录。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a4 bash tests/real-machine/probe-audit-inode.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-audit-inode.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

LOG_PATH="/var/log/daedalus/audit.jsonl"
ARCHIVE_DIR="/var/log/daedalus/archive"

driver_init
driver_boot

# 1) 文件存在 + append-only 属性位
# 审计日志是 root 域(0640 root),且 lsattr 读 inode 属性也要 root —— 一律 sudo。
ATTRS="$(gssh "sudo lsattr $LOG_PATH 2>&1; sudo wc -l < $LOG_PATH 2>/dev/null" 2>&1)"
info "$(printf '%s' "$ATTRS" | tr '\n' ' ')"
if printf '%s' "$ATTRS" | grep -qE '^[a-]*a[a-]*[[:space:]]'; then
    driver_verdict PASS "A4-1 $LOG_PATH 存在且 lsattr 含 a: $(printf '%s' "$ATTRS" | head -1)"
else
    driver_verdict FAIL "A4-1 $LOG_PATH 的 append-only 属性位缺失: $(printf '%s' "$ATTRS" | head -1)"
fi

# 2) 写一条 entry,再试 truncate —— 必须 EPERM
WRITE_OUT="$(gssh "sudo /usr/local/bin/daedalus-audit --identity probe-a4 --tool a4_probe --args '{}' --outcome success" 2>&1)"
info "写入 entry: $(printf '%s' "$WRITE_OUT" | tr '\n' ' ' | head -c 200)"
TRUNC="$(gssh "sudo truncate -s 0 $LOG_PATH 2>&1; echo TRUNC_RC=\$?" 2>&1)"
info "truncate 输出: $(printf '%s' "$TRUNC" | tr '\n' ' ')"
TRUNC_RC="$(printf '%s' "$TRUNC" | sed -n 's/^TRUNC_RC=//p' | tail -1)"
if [ "${TRUNC_RC:-0}" != "0" ] && printf '%s' "$TRUNC" | grep -qi 'Operation not permitted'; then
    driver_verdict PASS "A4-2 truncate -s 0 被拒(rc=$TRUNC_RC, Operation not permitted)"
else
    driver_verdict FAIL "A4-2 truncate -s 0 未被拒(rc=${TRUNC_RC:-?},输出=$(printf '%s' "$TRUNC" | head -1))"
fi

# 3) 哈希链校验
VERIFY="$(gssh "sudo /usr/local/bin/daedalus-audit verify --log-path $LOG_PATH 2>&1" 2>&1)"
info "verify: $(printf '%s' "$VERIFY" | tr '\n' ' ')"
if printf '%s' "$VERIFY" | grep -qi 'hash chain intact'; then
    driver_verdict PASS "A4-3 verify: $(printf '%s' "$VERIFY" | grep -i 'hash chain intact' | head -1)"
else
    driver_verdict FAIL "A4-3 verify 未见 hash chain intact: $(printf '%s' "$VERIFY" | head -1)"
fi

say "A4-4 · 轮转(服务口径 + --force 强制口径)"
# 根因线索:append-only 文件必须以 O_APPEND 打开才可写;O_RDWR 不开 O_APPEND 会 EPERM。
# ClearAppend/SetAppend 走 os.OpenFile(path, os.O_RDWR, 0) —— 这决定轮转能否清属性。
RDWR="$(gssh "sudo bash -c 'exec 3<> $LOG_PATH' 2>&1; echo RDWR_RC=\$?" 2>&1)"
APPEND="$(gssh "sudo bash -c 'exec 3>> $LOG_PATH' 2>&1; echo APPEND_RC=\$?" 2>&1)"
info "O_RDWR 打开(无 O_APPEND): $(printf '%s' "$RDWR" | tr '\n' ' ')"
info "O_APPEND 打开          : $(printf '%s' "$APPEND" | tr '\n' ' ')"
# 服务本身(阈值未达 → 应 no-op,如实记录)
SVC="$(gssh "sudo systemctl start daedalus-audit-rotate.service 2>&1; echo SVC_RC=\$?; sudo systemctl show daedalus-audit-rotate.service -p ExecMainStatus --value" 2>&1)"
info "rotate.service: $(printf '%s' "$SVC" | tr '\n' ' ')"
ROT_OUT="$(gssh "sudo /usr/local/bin/daedalus-audit rotate --force --log-path $LOG_PATH 2>&1" 2>&1)"
info "rotate --force: $(printf '%s' "$ROT_OUT" | tr '\n' ' ')"
ARCH="$(gssh "sudo ls -1 $ARCHIVE_DIR 2>&1" 2>&1)"
info "archive: $(printf '%s' "$ARCH" | tr '\n' ' ')"
REV="$(gssh "sudo /usr/local/bin/daedalus-audit verify --log-path $LOG_PATH 2>&1" 2>&1)"
info "轮转后 verify: $(printf '%s' "$REV" | tr '\n' ' ')"
if printf '%s' "$ARCH" | grep -q '\.gz$' && printf '%s' "$REV" | grep -qi 'hash chain intact'; then
    driver_verdict PASS "A4-4 轮转后 archive 出现 .gz 且 verify 仍 intact($(printf '%s' "$ARCH" | tr '\n' ' '))"
elif printf '%s' "$ROT_OUT" | grep -qi '清除 append-only 失败'; then
    driver_verdict FAIL "A4-4 轮转在清 append-only 属性时中止(Daedalusys/daedalus-core#92):$(printf '%s' "$ROT_OUT" | head -1)"
else
    driver_verdict FAIL "A4-4 轮转未产出 .gz 或链损坏: archive=$(printf '%s' "$ARCH" | tr '\n' ' ') rotate=$(printf '%s' "$ROT_OUT" | head -1)"
fi

driver_exit
