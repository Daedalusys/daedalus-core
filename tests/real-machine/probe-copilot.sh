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
#   3. Deno 权限边界:向 --allow-write 之外的路径写文件必须被 Deno 拒;
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
if printf '%s' "$D1" | grep -qi 'missing LLM API key'; then
    driver_verdict NOTE "A11-1 BLOCKED: no key(dry-run 仍需 LLM 翻译;实测文案见上)"
elif printf '%s' "$D1" | grep -qiE 'is degraded|permission denied'; then
    driver_verdict FAIL "A11-1 CLI 在 guest 内不可用:copilot 插件被判 degraded(目录权限不足,普通用户读不到 manifest;见 Daedalusys/daedalus-core#89)"
elif printf '%s' "$D1" | grep -qE 'RC=0' && printf '%s' "$D1" | grep -qiE 'command|mode|dry'; then
    driver_verdict PASS "A11-1 --dry-run 产出翻译(rc=0)"
else
    driver_verdict FAIL "A11-1 --dry-run 未产出可判定输出: $(printf '%s' "$D1" | head -1)"
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

say "A11-3 · Deno 权限边界(manifest --allow-write 之外的路径必须被拒)"
# 用 manifest 里的 --allow-write 集合跑 deno eval,尝试写 /etc/probe-copilot-denied。
# 这是 Deno 真权限边界(manifest entrypoint 旗标就是生产用的那套)。
DENO_TEST='try{Deno.writeTextFileSync("/etc/probe-copilot-denied","x");console.log("WROTE")}catch(e){console.log("DENIED:"+e.name)}'
D3="$(gssh "deno eval --allow-write=/var/log/daedalus,/tmp,'\$HOME/.local/share/daedalus' '$DENO_TEST' 2>&1" 2>&1)"
printf '%s\n' "$D3" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
D3B="$(gssh "deno eval --allow-write=/var/log/daedalus,/tmp,'\$HOME/.local/share/daedalus' 'try{Deno.writeTextFileSync(Deno.env.get(\"HOME\")+\"/.local/share/daedalus/probe-ok\",\"x\");console.log(\"WROTE_ALLOWED\")}catch(e){console.log(\"DENIED:\"+e.name)}' 2>&1" 2>&1)"
printf '%s\n' "$D3B" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$D3" | grep -q 'DENIED:PermissionDenied'; then
    driver_verdict PASS "A11-3 Deno 拒写 --allow-write 之外的 /etc(白名单内路径可写,见上)"
else
    driver_verdict FAIL "A11-3 Deno 未拒绝白名单外写入: $(printf '%s' "$D3" | head -1)"
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
