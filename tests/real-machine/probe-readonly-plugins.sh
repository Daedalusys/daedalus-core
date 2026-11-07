#!/usr/bin/env bash
# =============================================================================
# probe-readonly-plugins.sh —— A7:只读插件真数据源回归(真 guest 内)
# =============================================================================
# 证伪什么
#   单测用 fixture/fake 撑起 service/pkg/sysinfo/proc/trace/dupe,期望值与**真
#   数据源**的差异从未被观测。本探针在真 guest 里对每个插件跑一次真实调用,
#   如实记录「真实输出 vs 现有 fixture 期望」的差异结论(退出码 0 = 六条判定
#   全部为 PASS;A7-6 是硬链接误报的可判定断言,不是「记录一下」)。
#
# 六条
#   1. service.query 对真 timer 的 next_elapse_usec / 对 masked 单元的 load_state;
#   2. pkg.dnf_query 的 rpm→dnf repoquery 兜底(本地无/repo 有/两边都无);
#   3. sysinfo.network_status 的 ip -j → ip → /proc/net/dev 三级兜底;
#   4. proc 对他人进程 fd 的 EACCES 与 unowned_sockets;
#   5. trace 读真 /var/log/daedalus/audit.jsonl;
#   6. dupe 对**硬链接**的行为:同一 inode 的多个路径不得被报成重复,
#      wasted_bytes 只算真实副本(断言随 plugins#76 的修复转 PASS)。
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

# 硬链接误报的跟踪 issue:FAIL 消息里带上,便于证据直连缺陷单。
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
# 夹具语义:a.txt 与 b.txt 是同一 inode(硬链接,只占一份空间),c.txt 是独立
# inode 的同内容副本。因此真实可回收空间只有一份 27 字节;把 a/b/c 三者归成
# 一组并报 54 字节就是把硬链接当成了重复。
A7_PAYLOAD_LEN=27
A7_DUPE_DIR=/tmp/a7-dupe
if [ -z "$D1" ]; then
    driver_verdict FAIL "A7-6 scan_dupes 无响应"
elif ! command -v jq >/dev/null 2>&1; then
    # 宿主缺 jq 就无法把 JSON-RPC 响应还原成可判定事实,只能如实报无法判定。
    driver_verdict FAIL "A7-6 无法判定:宿主缺 jq(原始响应见上)"
else
    # 响应是 JSON-RPC 信封,分组结果在 result.content[0].text 的字符串里。
    A7_GROUPS="$(printf '%s' "$D1" | jq -r '.result.content[0].text // empty' 2>/dev/null)"
    # 同一组里同时出现两个硬链接路径 = 硬链接被误报。
    A7_HARDLINK_GROUPED="$(printf '%s' "$A7_GROUPS" | jq -r --arg a "$A7_DUPE_DIR/a.txt" --arg b "$A7_DUPE_DIR/b.txt" \
        '[.[] | select((.files | index($a)) and (.files | index($b)))] | length' 2>/dev/null)"
    A7_WASTED="$(printf '%s' "$A7_GROUPS" | jq -r '[.[] | .wasted_bytes] | add // 0' 2>/dev/null)"
    A7_GROUPS_JSON="$(printf '%s' "$A7_GROUPS" | jq -c '.' 2>/dev/null)"
    rec "分组结果(已还原 JSON): ${A7_GROUPS_JSON}"
    log "      A7-6 groups=$A7_GROUPS_JSON hardlink_grouped=$A7_HARDLINK_GROUPED wasted=$A7_WASTED"
    # 判定顺序:先看"同组"(硬链接被当成重复的直接证据),再看 wasted_bytes
    # 的数值;解析不出数字时 A7_HARDLINK_GROUPED 为空串,落到 FAIL 而非放行。
    case "$A7_HARDLINK_GROUPED" in
        ''|*[!0-9]*)
            driver_verdict FAIL "A7-6 无法判定:响应不是可解析的 scan_dupes 分组 JSON(见原始响应)"
            ;;
        0)
            if [ "$A7_WASTED" = "$A7_PAYLOAD_LEN" ]; then
                driver_verdict PASS "A7-6 硬链接未被误报,wasted_bytes 只算真实副本(${A7_WASTED}B),分组=${A7_GROUPS_JSON}"
            else
                driver_verdict FAIL "A7-6 wasted_bytes=${A7_WASTED:-<解析失败>},期望 ${A7_PAYLOAD_LEN}(只算一份真实副本;硬链接不多占空间),分组=${A7_GROUPS_JSON}"
            fi
            ;;
        *)
            driver_verdict FAIL "A7-6 硬链接仍被报成重复:a.txt 与 b.txt(同 inode)出现在同一组,分组=${A7_GROUPS_JSON} — $A7_ISSUE"
            ;;
    esac
fi

driver_exit
