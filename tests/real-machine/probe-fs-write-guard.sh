#!/usr/bin/env bash
# =============================================================================
# probe-fs-write-guard.sh —— A6:fs.write_file 二档写守卫(真 guest 内判据任务)
# =============================================================================
# 本脚本是**判据任务**:断言「白名单被放宽到含 /etc 后,写 /etc 下的敏感目标仍
# 必须被二档拒」,按 driver_verdict 记 PASS/FAIL 并决定退出码。
#
# 已知背景(供复核)
#   daedalus-sdk/pathguard 有两档:
#     · ValidatePath(path, write)      —— 目录白名单(AllowedDirs 前缀边界);
#     · ValidateWritePath(path)        —— 二档:额外拒写只读系统目录(/proc /sys
#                                         /boot /usr /etc /var/lib/rpm)与敏感文件
#                                         (/etc/shadow、/etc/sudoers.d/ 等)。
#   若 fs 的写路径只调 ValidatePath,二档就完全不参与判定:白名单一旦放宽到含
#   /etc,这些敏感路径立刻可写。
#
# 判据设计
#   1. 默认策略(allowed_dirs=/home /var/log /tmp):写 /etc/shadow、/proc/...
#      预期被**一档**白名单拒 —— 仅作证据记录,不作判据(它们本就不在白名单);
#   2. 隔离实验(判据):白名单临时放宽到含 /etc /proc(**guest 内临时副本**,
#      不碰镜像里的 policy.toml),再写敏感目标 —— 必须被拒,且拒绝理由必须来自
#      二档(只读系统目录 / 敏感文件清单)。
#
#   判定陷阱:二档强制完整 realpath,"cannot resolve realpath" 也是 "write denied"。
#   若只看这两个词,一个把新文件直接丢给二档的实现会因为「目标不存在」而拒,
#   在探针里显示成 PASS —— 那是假绿,它并没有拦住敏感路径。故判据额外要求拒绝
#   理由**不是** realpath 解析失败,且必须点名只读系统目录或敏感文件清单。
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

A6_ISSUE="${A6_ISSUE:-Daedalusys/daedalus-core#91}"

FS_BIN="/opt/daedalus/plugins/daedalus.fs/bin/daedalus-fs"
POLICY="/opt/daedalus/shared/policy.toml"

driver_init
driver_boot
gssh_scp "$HERE/lib/mcp-call.sh" /tmp/mcp-call.sh || die "送 mcp-call.sh 进 guest 失败"
gssh "chmod +x /tmp/mcp-call.sh" >/dev/null 2>&1

# 响应是单行 JSON-RPC,消息正文原样在里面(只有引号被转义,关键字不受影响)。
# 判据一律在**原始响应**上匹配:从 JSON 里抠字符串要处理转义引号,sed 的非贪婪
# 不可用,抠歪了会让判据失灵;正文摘录只用于人读证据。
resp_has()  { printf '%s' "$1" | grep -q "$2"; }
resp_err()  { resp_has "$1" '"isError":true' && echo yes || echo no; }
resp_text() { printf '%s' "$1" | sed -n 's/.*"text":"//; s/"}].*//; p' | head -1; }
# 拒绝来源分类:一档是 AllowedDirs 白名单,二档是 ValidateWritePath 的只读目录/
# 敏感文件清单。两者文案不同,据此判定「是哪一档拒的」。
guard_source() {
    local raw="$1"
    if resp_has "$raw" 'cannot resolve realpath'; then
        echo "二档 realpath 解析失败(目标不存在,非守卫判定)"
    elif resp_has "$raw" 'read-only system dir'; then
        echo "二档 ValidateWritePath(只读系统目录)"
    elif resp_has "$raw" 'forbidden write'; then
        echo "二档 ValidateWritePath(敏感文件清单)"
    elif resp_has "$raw" 'is outside allowed directories'; then
        echo "一档 pathguard(AllowedDirs 白名单)"
    elif [ -z "$(printf '%s' "$raw" | tr -d '[:space:]')" ]; then
        echo "无响应(工具调用未完成)"
    else
        echo "其它:$(resp_text "$raw" | head -c 120)"
    fi
}
show() { # show <标签> <原始响应>
    printf '      %-46s isError=%s source=%s text=%s\n' "$1" "$(resp_err "$2")" "$(guard_source "$2")" "$(resp_text "$2" | head -c 200)"
    log "      $1 isError=$(resp_err "$2") text=$(resp_text "$2" | head -c 200)"
}

# guard_verdict <标签> <原始响应> —— 判据:被拒 + 理由点名为只读系统目录或敏感文件。
# 特意把"目标不存在"这种 realpath 拒绝判成 FAIL:它同样写着 write denied,却完全
# 没拦住敏感路径,只按关键字判会得到一个假的 PASS。
guard_verdict() {
    local label="$1" raw="$2" src
    src="$(guard_source "$raw")"
    if [ -z "$(printf '%s' "$raw" | tr -d '[:space:]')" ]; then
        driver_verdict FAIL "$label 工具调用无响应(MCP 会话未完成),本轮未取得判定"
        return
    fi
    if resp_has "$raw" 'Successfully wrote'; then
        driver_verdict FAIL "A6 二档写守卫仍未生效:$label 在白名单含 /etc 时写入成功($(resp_text "$raw" | head -c 160))"
        return
    fi
    if resp_has "$raw" 'cannot resolve realpath'; then
        driver_verdict FAIL "A6 判定不可信:$label 被拒的理由是 realpath 解析失败而非敏感路径守卫 —— 该实现没有拦住敏感路径,只是拒绝了不存在的落点($(resp_text "$raw" | head -c 160))"
        return
    fi
    case "$src" in
        二档\ ValidateWritePath*) driver_verdict PASS "A6 二档写守卫生效:$label 在白名单含 /etc 时仍被拒($src)" ;;
        *) driver_verdict FAIL "A6 拒绝来源不符:$label 期望由二档拒绝,实得 $src($(resp_text "$raw" | head -c 160))" ;;
    esac
}

say "A6-1 · 默认策略下写敏感路径(证据,不作判据)"
# 一律以 root 跑:审计链落链需要 root(日志 0640 root:+a),否则响应会被
# "审计证据未能写入哈希链" 覆盖,看不到真正的路径守卫结论。
R_SHADOW="$(gssh "sudo /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/shadow\",\"content\":\"probe-a6\"}'" 2>&1)"
R_PROC="$(gssh "sudo /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/proc/sys/kernel/hostname\",\"content\":\"probe-a6\"}'" 2>&1)"
show "/etc/shadow (默认策略)" "$R_SHADOW"
show "/proc/sys/kernel/hostname (默认策略)" "$R_PROC"

say "A6-2 · 隔离实验:白名单临时放宽到含 /etc /proc(guest 内临时副本)"
gssh "sed 's|^allowed_dirs = .*|allowed_dirs = [\"/home\",\"/var/log\",\"/tmp\",\"/etc\",\"/proc\"]|' $POLICY > /tmp/a6-policy.toml && grep '^allowed_dirs' /tmp/a6-policy.toml" 2>&1 | sed 's/^/      /'
# 判据点用 /etc/pam.d/:该前缀在二档的敏感文件清单里,写一个**新**文件名不会打断
# sudo/SSH(没有任何配置引用它),可安全反复跑。
R_PAM="$(gssh "sudo env DAEDALUS_POLICY_PATH=/tmp/a6-policy.toml /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/pam.d/probe-a6-guard\",\"content\":\"probe-a6-widened\"}'" 2>&1)"
show "/etc/pam.d/probe-a6-guard (白名单含 /etc)" "$R_PAM"
PAM_READ="$(gssh "sudo cat /etc/pam.d/probe-a6-guard 2>&1" 2>&1)"
info "落盘回读 /etc/pam.d/probe-a6-guard: $(printf '%s' "$PAM_READ" | head -c 80)"
gssh "sudo rm -f /etc/pam.d/probe-a6-guard" >/dev/null 2>&1

say "A6-3 · 结论"
guard_verdict "/etc/pam.d/probe-a6-guard" "$R_PAM"

# 计划点名的 /etc/sudoers.d 形态:放在最后且不做任何后续 sudo 调用 —— 一旦它真的
# 落了盘,sudo 解析会失败,后续所有 sudo 命令都会挂。snapshot 引导,退出即丢。
R_SUDOERS="$(gssh "sudo env DAEDALUS_POLICY_PATH=/tmp/a6-policy.toml /tmp/mcp-call.sh $FS_BIN write_file '{\"path\":\"/etc/sudoers.d/probe-a6-guard\",\"content\":\"# probe-a6\"}'" 2>&1)"
show "/etc/sudoers.d/probe-a6-guard (白名单含 /etc)" "$R_SUDOERS"
guard_verdict "/etc/sudoers.d/probe-a6-guard" "$R_SUDOERS"

info "issue: $A6_ISSUE"
log "issue: $A6_ISSUE"

driver_exit
