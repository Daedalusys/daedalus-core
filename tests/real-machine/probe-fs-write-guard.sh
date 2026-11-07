#!/usr/bin/env bash
# =============================================================================
# probe-fs-write-guard.sh —— A6:fs.write_file 二档写守卫缺陷复现(真 guest 内)
# =============================================================================
# 这是**复现任务**,不是判据任务:本脚本退出码恒 0,结论写进输出与证据。
#
# 已知背景(供复核)
#   daedalus-sdk/pathguard 有两档:
#     · ValidatePath(path, write)      —— 目录白名单(AllowedDirs 前缀边界);
#     · ValidateWritePath(path)        —— 二档:额外拒写只读系统目录(/proc /sys
#                                         /boot /usr /etc /var/lib/rpm)与敏感文件
#                                         (/etc/shadow、/etc/sudoers.d/ 等)。
#   fs 的写路径(write_file / move_file)只调 `ValidatePath(path, true)`,而该函数
#   里 `_ = write`,**write 形参被丢弃** —— 于是两档守卫实际是同一套规则,二档
#   对 fs.write_file 不生效。
#
# 复现实验设计
#   1. 默认策略(allowed_dirs=/home /var/log /tmp):写 /etc/shadow、/proc/... 预期
#      被**一档**白名单拒(它们本来就不在白名单里)—— 如实记录拒绝来源;
#   2. 隔离实验:把白名单临时放宽到含 /etc /proc(**guest 内临时副本**,
#      不碰镜像里的 policy.toml),再写 /etc/shadow —— 若仍被拒 = 二档生效;
#      若写成功 = 二档被绕过,缺陷成立。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a6 bash tests/real-machine/probe-fs-write-guard.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-fs-write-guard.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

# 复现成功后在下方填入 issue 号(计划要求把号码写进证据段)。
A6_ISSUE="${A6_ISSUE:-Daedalusys/daedalus-core#91}"

FS_BIN="/opt/daedalus/plugins/daedalus.fs/bin/daedalus-fs"
POLICY="/opt/daedalus/shared/policy.toml"

driver_init
driver_boot
gssh_scp "$HERE/lib/mcp-call.sh" /tmp/mcp-call.sh || die "送 mcp-call.sh 进 guest 失败"
gssh "chmod +x /tmp/mcp-call.sh" >/dev/null 2>&1

# 从工具响应里抽 text 与 isError。
resp_text() { printf '%s' "$1" | sed -n 's/.*"text":"\(.*\)".*/\1/p' | head -1; }
resp_err()  { printf '%s' "$1" | grep -q '"isError":true' && echo yes || echo no; }
# 拒绝来源分类:一档是 AllowedDirs 白名单,二档是 ValidateWritePath 的只读目录/
# 敏感文件清单。两者文案不同,据此判定「是哪一档拒的」。
guard_source() {
    case "$1" in
        *"is outside allowed directories"*) echo "一档 pathguard(AllowedDirs 白名单)" ;;
        *"read-only system director"*|*"sensitive"*|*"forbidden"*|*"Refusing to write"*) echo "二档 ValidateWritePath(只读目录/敏感文件)" ;;
        "") echo "无响应(工具调用未完成)" ;;
        *) echo "其它:$(printf '%s' "$1" | head -c 120)" ;;
    esac
}
show() { # show <标签> <响应>
    printf '      %-32s isError=%s source=%s text=%s\n' "$1" "$(resp_err "$2")" "$(guard_source "$(resp_text "$2")")" "$(resp_text "$2" | head -c 200)"
    log "      $1 isError=$(resp_err "$2") text=$(resp_text "$2" | head -c 200)"
}

say "A6-1 · 默认策略下写敏感路径(期望被一档白名单拒)"
# 一律以 root 跑:审计链落链需要 root(日志 0640 root:+a),否则响应会被
# "审计证据未能写入哈希链" 覆盖,看不到真正的路径守卫结论。
R_SHADOW="$(gssh "sudo /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/shadow\",\"content\":\"probe-a6\"}'" 2>&1)"
R_PROC="$(gssh "sudo /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/proc/sys/kernel/hostname\",\"content\":\"probe-a6\"}'" 2>&1)"
show "/etc/shadow (默认策略)" "$R_SHADOW"
show "/proc/sys/kernel/hostname (默认策略)" "$R_PROC"

say "A6-2 · 隔离实验:白名单临时放宽到含 /etc /proc(guest 内临时副本)"
gssh "sed 's|^allowed_dirs = .*|allowed_dirs = [\"/home\",\"/var/log\",\"/tmp\",\"/etc\",\"/proc\"]|' $POLICY > /tmp/a6-policy.toml && grep '^allowed_dirs' /tmp/a6-policy.toml" 2>&1 | sed 's/^/      /'
# 无害敏感目标:/etc/pam.d/ 在二档的 forbiddenWritePatterns 里,写一个**新**文件名
# 不会打断 sudo/SSH(没有任何配置引用它;写 /etc/sudoers.d/ 会让 sudo 解析失败、
# 后续 sudo 全挂,写 /etc/shadow 会立刻打断 SSH 口令认证——后者放最后且不回读)。
R_PAM="$(gssh "sudo env DAEDALUS_POLICY_PATH=/tmp/a6-policy.toml /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/pam.d/probe-a6-guard\",\"content\":\"probe-a6-widened\"}'" 2>&1)"
show "/etc/pam.d/probe-a6-guard (白名单含 /etc)" "$R_PAM"
PAM_READ="$(gssh "sudo cat /etc/pam.d/probe-a6-guard 2>&1" 2>&1)"
info "落盘回读 /etc/pam.d/probe-a6-guard: $(printf '%s' "$PAM_READ" | head -c 80)"
gssh "sudo rm -f /etc/pam.d/probe-a6-guard" >/dev/null 2>&1
# 计划点名的 /etc/shadow:只记工具响应(写成功即会打断本次 SSH,不做任何后续读)。
R_WIDE="$(gssh "sudo env DAEDALUS_POLICY_PATH=/tmp/a6-policy.toml /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/shadow\",\"content\":\"probe-a6-widened\"}'" 2>&1)"
show "/etc/shadow (白名单含 /etc)" "$R_WIDE"

say "A6-3 · 结论"
if printf '%s' "$R_PAM" | grep -q 'Successfully wrote'; then
    CONCL="二档写守卫对 fs.write_file 不生效:白名单含 /etc 时 /etc/pam.d/ 下的写入成功(该前缀在 ValidateWritePath 的 forbiddenWritePatterns 里,write 形参被丢弃)"
elif [ -z "$(printf '%s' "$R_PAM" | tr -d '[:space:]')" ]; then
    CONCL="工具调用无响应(MCP 会话未完成),本轮未取得判定"
elif [ "$(resp_err "$R_PAM")" = "yes" ]; then
    CONCL="二档写守卫在放宽白名单后仍拒绝了写入(拒绝来源:$(guard_source "$(resp_text "$R_PAM")"))—— 与已知背景不符,需人工复核"
else
    CONCL="判定不确定:/etc/pam.d 写入响应=$(printf '%s' "$R_PAM" | head -c 200)"
fi
printf '      结论: %s\n' "$CONCL"
printf '      issue: %s\n' "$A6_ISSUE"
log "      结论: $CONCL"
log "      issue: $A6_ISSUE"

# 复现任务:恒退 0。
driver_cleanup
printf '\n判定: REPRO_DONE(复现任务不计 PASS/FAIL)\n'
exit 0
