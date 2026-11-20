#!/usr/bin/env bash
# =============================================================================
# probe-copilot.sh —— copilot:dry-run / watchdog / Deno 权限边界(真 guest 内)
# =============================================================================
# 证伪什么
#   copilot 的翻译走外部 LLM,Deno 权限来自 manifest 的 entrypoint 旗标 —— 两者
#   都只有真 guest 才跑得起来。
#
# 四条(缺 key 的条目记 BLOCKED: no key,不算失败也不算通过)
#   1. `daedalus --dry-run "<一句话>"` 能否在不依赖 key 的情况下产出翻译;
#   2. watchdog:DAEDALUS_WATCHDOG_TIMEOUT_MS 生效 → rc=124 且无残留进程;
#   3. Deno 权限边界:deno run 下向 --allow-write 之外的路径写文件必须被 Deno 以
#      NotCapable 拒,且白名单内可写(正向对照;PermissionDenied 是 OS 拒绝,不算);
#   4. 有 DAEDALUS_LLM_API_KEY 时才跑真 provider,并核对审计链仍 intact。
#
# 已知事实(实测口径,与计划的默认假设不同,如实记录):
#   · 翻译(translateFn)在 dry-run 判断**之前**发生,且 readConfigFn 在无 key 时
#     直接抛 "missing LLM API key" —— 故 `--dry-run` 不是「不依赖 key」的路径;
#   · 计划设想的「配一个 sleep 5 的 allowlisted 命令」不成立:sleep 不在 15 条白
#     名单里,且要被执行必须经 LLM 翻译 —— 无 key 时 watchdog 无法驱动。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-copilot [DAEDALUS_LLM_API_KEY=...] \
#         bash tests/real-machine/probe-copilot.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-copilot.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

HAVE_KEY=0
[ -n "${DAEDALUS_LLM_API_KEY:-}" ] && HAVE_KEY=1

driver_init
driver_boot

say "A11-0 · copilot 安装态与宿主构造的启动命令"
gssh "ls -la /usr/local/bin/daedalus /opt/daedalus/plugins/daedalus.copilot/main.ts 2>&1 | head -5" 2>&1 | sed 's/^/      /'
LAUNCH="$(gssh "daedalus-host run-plugin daedalus.copilot -dir /opt/daedalus/plugins -- probe" 2>&1)"
printf '%s\n' "$LAUNCH" | sed 's/^/      /' | tee -a "$DRIVER_LOG"

say "A11-1 · --dry-run 是否能在无 key 下翻译"
D1="$(gssh "daedalus --dry-run 'show os version' 2>&1; echo RC=\$?" 2>&1)"
printf '%s\n' "$D1" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$D1" | grep -q 'RC=2' && printf '%s' "$D1" | grep -qi 'missing LLM API key'; then
    driver_verdict NOTE "A11-1 BLOCKED: no key(dry-run 仍需 LLM 翻译;缺 key 属前置不满足 rc=2;实测文案见上)"
elif printf '%s' "$D1" | grep -qiE 'is degraded|permission denied'; then
    driver_verdict FAIL "A11-1 CLI 在 guest 内不可用:copilot 插件被判 degraded(目录权限不足,普通用户读不到 manifest;见 Daedalusys/daedalus-core#89)"
elif printf '%s' "$D1" | grep -qE 'RC=0' && printf '%s' "$D1" | grep -qiE 'command|mode|dry'; then
    driver_verdict PASS "A11-1 --dry-run 产出翻译(rc=0)"
else
    driver_verdict FAIL "A11-1 缺 key 未按前置不满足拒绝(期望 rc=2 且文案含 'missing LLM API key'): $(printf '%s' "$D1" | head -1)"
fi

say "A11-2 · watchdog(rc=124 + 无残留进程)"
if [ "$HAVE_KEY" = 0 ]; then
    driver_verdict NOTE "A11-2 BLOCKED: no key(watchdog 需经 LLM 翻译产出可执行命令;sleep 亦不在 15 条白名单内)"
else
    W1="$(gssh "DAEDALUS_WATCHDOG_TIMEOUT_MS=1000 daedalus -y 'ping 127.0.0.1 five times slowly' 2>&1; echo RC=\$?" 2>&1)"
    printf '%s\n' "$W1" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    PSRES="$(gssh "ps -ef | grep -c '[p]ing 127.0.0.1'" 2>&1)"
    if printf '%s' "$W1" | grep -q 'RC=124' && [ "${PSRES:-1}" = "0" ]; then
        driver_verdict PASS "A11-2 watchdog rc=124 且无残留 ping 进程"
    else
        driver_verdict FAIL "A11-2 watchdog 未如期(rc/ps 见上)"
    fi
fi

say "A11-3 · Deno 权限边界(deno run + manifest 的 --allow-write 集合)"
# ★ 判据只能用 `deno run`,不能用 `deno eval`(本探针旧版写法,已证实是假绿):
#   · `deno eval` 是"代码片段"入口,不施加 Deno 权限边界:白名单外的写入在那里只会
#     撞 OS 的 EACCES(/etc 属 root),而 e.name 恰好也是 PermissionDenied。于是
#     "看到 PermissionDenied 就算被 Deno 拦住了"证明的是文件系统权限,与 manifest
#     旗标完全无关;且 2.9.x 起 `deno eval` 连 --allow-write 旗标都不接受(usage 错),
#     那条命令根本没有跑起来过。
#   · 真边界抛的是 NotCapable(Deno 2.x 的权限异常名),不是 PermissionDenied。
#   · 必须配正向对照(白名单内可写),否则"全拒"也能让负对照通过。
# 复现证据(同一台机、同一 deno):probe-copilot-sandbox.sh 的 S5 —— deno eval 加
# --deny-write 仍写成功、query 白名单外返回 granted;deno run 才抛 NotCapable。
GUEST_HOME="$(gssh 'echo $HOME' 2>&1 | tr -d '\r' | sed -n '/^\//p' | tail -n 1)"
# 生产旗标只认 host 构造的那份(LAUNCH),不在这里另抄一份 allow-write 清单 ——
# 抄一份就会与 manifest 漂移,而漂移后的探针测的是假旗标。
ALLOW_WRITE="$(printf '%s\n' "$LAUNCH" | sed -n "s/.*--allow-write=\([^ ']*\).*/\1/p" | head -n 1)"
if [ -z "$GUEST_HOME" ] || [ -z "$ALLOW_WRITE" ]; then
    driver_verdict FAIL "A11-3 取不到 guest HOME 或 manifest 的 --allow-write(host 构造命令里没有该旗标,上限 120 字): ${LAUNCH:0:120}"
else
    # 生产侧的 $HOME 占位由 wrapper 在 exec 前展开;两边指的是同一个目录,这里用
    # guest 的真实 HOME 展开同一位。
    ALLOW_WRITE="${ALLOW_WRITE//\$HOME/"$GUEST_HOME"}"
    A11_3_OUTSIDE="$GUEST_HOME/.daedalus-probe-outside"       # OS 层可写,但不在白名单
    A11_3_INSIDE="$GUEST_HOME/.local/share/daedalus/probe-ok" # 白名单内(正向对照)
    # 脚本经 base64 过 ssh:gssh 把 stdin 接成 /dev/null,heredoc 传不进去;
    # 而把 JS 直接拼进远端命令行会被远程 shell 的引号规则撕碎。
    A11_3_JS='const [outside, inside] = Deno.args;
function attempt(p) {
  try { Deno.writeTextFileSync(p, "x"); return "WROTE"; }
  catch (e) { return "DENIED:" + e.name; }
}
console.log("outside=" + attempt(outside));
console.log("inside=" + attempt(inside));'
    A11_3_B64="$(printf '%s' "$A11_3_JS" | base64 | tr -d '\n')"
    gssh "printf '%s' '$A11_3_B64' | base64 -d > /tmp/probe-a11-3.ts" >/dev/null 2>&1
    gssh "mkdir -p '$GUEST_HOME/.local/share/daedalus' && rm -f '$A11_3_OUTSIDE' '$A11_3_INSIDE'" >/dev/null 2>&1
    D3="$(gssh "deno run --no-prompt --allow-write='$ALLOW_WRITE' /tmp/probe-a11-3.ts '$A11_3_OUTSIDE' '$A11_3_INSIDE' 2>&1" 2>&1)"
    printf '%s\n' "$D3" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    A11_3_OUT_LINE="$(printf '%s\n' "$D3" | grep -m1 '^outside=' || true)"
    A11_3_IN_LINE="$(printf '%s\n' "$D3" | grep -m1 '^inside=' || true)"
    A11_3_LEFTOVER=0
    gssh "test -e '$A11_3_OUTSIDE'" >/dev/null 2>&1 && A11_3_LEFTOVER=1
    if [ "$A11_3_OUT_LINE" = "outside=DENIED:NotCapable" ] &&
        [ "$A11_3_IN_LINE" = "inside=WROTE" ] &&
        [ "$A11_3_LEFTOVER" -eq 0 ]; then
        driver_verdict PASS "A11-3 manifest 的 --allow-write 是真边界:白名单外 NotCapable 且文件未生成,白名单内可写(正向对照)"
    else
        driver_verdict FAIL "A11-3 边界判据不成立:outside=[$A11_3_OUT_LINE] inside=[$A11_3_IN_LINE] 白名单外文件存在=$A11_3_LEFTOVER(NotCapable=Deno 拦;PermissionDenied=OS 拒,后者不算)"
    fi
    gssh "rm -f /tmp/probe-a11-3.ts '$A11_3_INSIDE'" >/dev/null 2>&1 || true
fi

say "A11-4 · 真 provider(有 key 才跑)"
if [ "$HAVE_KEY" = 0 ]; then
    driver_verdict NOTE "A11-4 BLOCKED: no key(未跑真 provider)"
else
    R4="$(gssh "DAEDALUS_LLM_API_KEY='$DAEDALUS_LLM_API_KEY' daedalus -y 'show os version' 2>&1; echo RC=\$?" 2>&1)"
    printf '%s\n' "$R4" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    TAIL4="$(gssh "sudo tail -3 /var/log/daedalus/audit.jsonl 2>&1; sudo daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl 2>&1" 2>&1)"
    printf '%s\n' "$TAIL4" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
    if printf '%s' "$R4" | grep -q 'RC=0' && printf '%s' "$TAIL4" | grep -qi 'hash chain intact'; then
        driver_verdict PASS "A11-4 真 provider 返回且审计链 intact"
    else
        driver_verdict FAIL "A11-4 真 provider 路径未通过(见上)"
    fi
fi

driver_exit
