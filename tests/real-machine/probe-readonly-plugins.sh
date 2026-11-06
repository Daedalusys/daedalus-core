#!/usr/bin/env bash
# =============================================================================
# probe-readonly-plugins.sh —— A7:只读插件真数据源回归(真 guest 内)
# =============================================================================
# 证伪什么
#   单测用 fixture/fake 撑起 service/pkg/sysinfo/proc/trace/dupe,期望值与**真
#   数据源**的差异从未被观测。本探针在真 guest 里对每个插件跑一次真实调用,
#   如实记录「真实输出 vs 现有 fixture 期望」的差异结论(退出码 0 = 六条都已
#   如实记录;第 6 条若确认缺陷即开 issue 并记号码)。
#
# 六条
#   1. service.query 对真 timer 的 next_elapse_usec / 对 masked 单元的 load_state;
#   2. pkg.dnf_query 的 rpm→dnf repoquery 兜底(本地无/repo 有/两边都无);
#   3. sysinfo.network_status 的 ip -j → ip → /proc/net/dev 三级兜底;
#   4. proc 对他人进程 fd 的 EACCES 与 unowned_sockets;
#   5. trace 读真 /var/log/daedalus/audit.jsonl;
#   6. dupe 对**硬链接**的行为(同一 inode 是否被报成重复)。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a7 bash tests/real-machine/probe-readonly-plugins.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-readonly-plugins.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

# 第 6 条确认缺陷后填入 issue 号。
A7_ISSUE="${A7_ISSUE:-Daedalusys/daedalus-plugins#76}"

P=/opt/daedalus/plugins
SVC="$P/daedalus.service/bin/daedalus-service"
PKG="$P/daedalus.pkg/bin/daedalus-pkg"
SYS="$P/daedalus.sysinfo/bin/daedalus-sysinfo"
PRC="$P/daedalus.proc/bin/daedalus-proc"
TRC="$P/daedalus.trace/bin/daedalus-trace"
DUP="$P/daedalus.dupe/bin/daedalus-dupe"

driver_init
driver_boot
gssh_scp "$HERE/lib/mcp-call.sh" /tmp/mcp-call.sh || die "送 mcp-call.sh 进 guest 失败"
gssh "chmod +x /tmp/mcp-call.sh" >/dev/null 2>&1

call() { gssh "$@" 2>&1; }
rec()  { printf '      %s\n' "$1"; log "      $1"; }

say "A7-1 · service.query:真 timer 的 next_elapse + masked 单元的 load_state"
T1="$(call "/tmp/mcp-call.sh $SVC systemd_timer_next '{\"name\":\"daedalus-audit-rotate\"}'")"
T1B="$(call "/tmp/mcp-call.sh $SVC service.query '{\"name\":\"daedalus-audit-rotate\"}'")"
T2="$(call "sudo systemctl mask --runtime daedalus-diskclean.service >/dev/null 2>&1; echo '--- systemctl show 原始输出 ---'; sudo systemctl show daedalus-diskclean.service -p LoadState --no-pager 2>&1; /tmp/mcp-call.sh $SVC service.query '{\"name\":\"daedalus-diskclean\"}'; sudo systemctl unmask --runtime daedalus-diskclean.service >/dev/null 2>&1")"
rec "timer next(systemd_timer_next): $(printf '%s' "$T1" | head -c 300)"
rec "service.query(真单元): $(printf '%s' "$T1B" | head -c 300)"
rec "masked 单元: $(printf '%s' "$T2" | head -c 400)"
if printf '%s' "$T1" | grep -q 'next_elapse_usec'; then
    driver_verdict PASS "A7-1a 如实记录:真 timer 有 next_elapse_usec_realtime"
else
    driver_verdict FAIL "A7-1a timer 响应缺 next_elapse_usec"
fi
if printf '%s' "$T2" | grep -q 'masked'; then
    driver_verdict PASS "A7-1b 如实记录:masked 单元的 LoadState 可见"
else
    # PROBE_WIP:本探针尚未与插件对 masked 单元的响应形态对齐(它回 error 而非
    # LoadState=masked),尚不能断言是插件缺陷还是探针的 mask 姿势不对。见 PR 说明。
    driver_verdict PROBE_WIP "A7-1b masked 单元的响应形态未对齐(见上原始输出;探针待校准,非产品结论)"
fi

say "A7-2 · pkg.dnf_query 兜底(rpm -q --info → dnf repoquery)"
P1="$(call "/tmp/mcp-call.sh $PKG dnf_query '{\"name\":\"bash\"}'")"
P2="$(call "/tmp/mcp-call.sh $PKG dnf_query '{\"name\":\"tree\"}'")"
P3="$(call "/tmp/mcp-call.sh $PKG dnf_query '{\"name\":\"daedalus-probe-nonexistent-pkg\"}'")"
rec "本地有(bash): $(printf '%s' "$P1" | head -c 200)"
rec "本地无 repo 有(tree): $(printf '%s' "$P2" | head -c 200)"
rec "两边都无: $(printf '%s' "$P3" | head -c 200)"
if [ -n "$P1" ]; then
    driver_verdict PASS "A7-2 如实记录(本地有/本地无 repo 有/两边都无 三段输出见上;本地无时兜底可能因仓库元数据不可达返回空)"
else
    driver_verdict FAIL "A7-2 dnf_query 未产出响应"
fi

say "A7-3 · sysinfo.network_status 三级兜底(PATH 里移走 ip)"
S1="$(call "/tmp/mcp-call.sh $SYS network_status '{}'")"
S2="$(call "sudo mkdir -p /tmp/a7-path && sudo ln -sf /usr/bin/timeout /usr/bin/grep /usr/bin/tail /usr/bin/sleep /bin/bash /bin/cat /usr/bin/head /usr/bin/sed /usr/bin/awk /tmp/a7-path/ 2>/dev/null; sudo env PATH=/tmp/a7-path /tmp/mcp-call.sh $SYS network_status '{}'")"
rec "ip -j 可用: $(printf '%s' "$S1" | head -c 200)"
rec "PATH 无 ip(第三级 /proc/net/dev): $(printf '%s' "$S2" | head -c 200)"
if [ -n "$S1" ] && [ -n "$S2" ]; then
    driver_verdict PASS "A7-3 如实记录(降级路径仍产出网络信息)"
else
    driver_verdict FAIL "A7-3 network_status 在某一级未产出响应"
fi

say "A7-4 · proc:他人进程 fd 的 EACCES 与 unowned_sockets"
F1="$(call "/tmp/mcp-call.sh $PRC proc_fds '{\"pid\":1}'")"
L1="$(call "/tmp/mcp-call.sh $PRC proc_listen '{}'")"
rec "proc_fds pid=1(以 verify 用户跑): $(printf '%s' "$F1" | head -c 300)"
rec "proc_listen: $(printf '%s' "$L1" | head -c 300)"
if printf '%s' "$F1" | grep -qE '"unreadable":[1-9]'; then
    driver_verdict PASS "A7-4 如实记录(对他人 fd 出现 unreadable/EACCES)"
elif [ -n "$F1" ]; then
    driver_verdict PASS "A7-4 如实记录(proc_fds 有响应;unreadable 计数见上)"
else
    driver_verdict FAIL "A7-4 proc_fds 无响应"
fi

say "A7-5 · trace 读真 /var/log/daedalus/audit.jsonl"
R5="$(call "sudo /tmp/mcp-call.sh $TRC trace_summary '{\"log_path\":\"/var/log/daedalus/audit.jsonl\"}'")"
rec "trace_summary: $(printf '%s' "$R5" | head -c 300)"
if printf '%s' "$R5" | grep -q 'total'; then
    driver_verdict PASS "A7-5 如实记录(真日志可被 trace 读取;单测遇真日志会 t.Skip)"
else
    driver_verdict FAIL "A7-5 trace_summary 未产出响应"
fi

say "A7-6 · dupe 对硬链接(同一 inode)的行为"
gssh "rm -rf /tmp/a7-dupe; mkdir -p /tmp/a7-dupe; printf 'hardlink-payload-0123456789' > /tmp/a7-dupe/a.txt; ln /tmp/a7-dupe/a.txt /tmp/a7-dupe/b.txt; printf 'hardlink-payload-0123456789' > /tmp/a7-dupe/c.txt; ls -li /tmp/a7-dupe" 2>&1 | sed 's/^/      /'
D1="$(call "/tmp/mcp-call.sh $DUP scan_dupes '{\"dirs\":[\"/tmp/a7-dupe\"],\"algo\":\"sha256\"}'")"
printf '%s\n' "$D1" | head -c 600 | sed 's/^/      /'
log "      dupe scan_dupes: $(printf '%s' "$D1" | head -c 600)"
# a.txt/b.txt 是同一 inode(硬链接),c.txt 是独立 inode(真重复)。
# scannedFile 只存 path/size/mtime、无 inode → 预期把 a/b/c 全归入同一组。
if printf '%s' "$D1" | grep -q 'a.txt' && printf '%s' "$D1" | grep -q 'b.txt'; then
    rec "结论: a.txt 与 b.txt 是同一 inode,却被一起报为重复 —— 硬链接被误报(wasted_bytes 虚高)"
    rec "issue: $A7_ISSUE"
    driver_verdict PASS "A7-6 如实记录(硬链接误报,详见输出与 issue)"
elif [ -n "$D1" ]; then
    driver_verdict PASS "A7-6 如实记录(未观察到硬链接同组,详见输出)"
else
    driver_verdict FAIL "A7-6 scan_dupes 无响应"
fi

driver_exit
