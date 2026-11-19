#!/usr/bin/env bash
# =============================================================================
# probe-copilot-keyed.sh —— copilot 真 provider + watchdog(A11-1/2/4 的有 key 版)
# =============================================================================
# 与 probe-copilot.sh 的差别只有三点:
#   1. key 从**文件**送进 guest(/root/.llmkey,0600),不在任何命令行上出现,
#      也就不会被 ps 看到、不会进日志;
#   2. provider / model / base_url 由 guest 侧的小 helper(/root/llmrun)导出,
#      自建 OpenAI 兼容网关时必需;
#   3. key 文件是**裸 token**还是 `VAR=VALUE` 形式都能吃。
#
# 用法:
#   sudo VERIFY_WORK=/tmp/probe-copilot-keyed \
#        VERIFY_IMAGE=ghcr.io/daedalusys/daedalus-os:latest \
#        DAEDALUS_LLM_KEY_FILE=$HOME/.daedalus-llm-key.clean \
#        DAEDALUS_LLM_MODEL=code-fast \
#        DAEDALUS_LLM_BASE_URL=http://10.66.0.6/v1 \
#        bash probe-copilot-keyed.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-copilot-keyed}"
export DRIVER_LOG="$VERIFY_WORK/probe-copilot-keyed.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1

: "${DAEDALUS_LLM_KEY_FILE:?需要 DAEDALUS_LLM_KEY_FILE(宿主上 key 文件路径)}"
[ -s "$DAEDALUS_LLM_KEY_FILE" ] || { echo "key 文件不存在或为空: $DAEDALUS_LLM_KEY_FILE" >&2; exit 2; }
MODEL="${DAEDALUS_LLM_MODEL:-code-fast}"
BASE_URL="${DAEDALUS_LLM_BASE_URL:-}"
PROV="${DAEDALUS_LLM_PROVIDER:-openai}"

# guest 侧 helper:读 key → 导出环境 → exec 目标命令
HELPER="$(mktemp)"
cat > "$HELPER" <<EOF
#!/bin/bash
# 由 probe-copilot-keyed.sh 放进 guest:把 key 与网关配置导进环境再 exec。
K="\$(cat /root/.llmkey)"
if printf '%s' "\$K" | grep -q '='; then
    . /root/.llmkey
else
    export DAEDALUS_LLM_API_KEY="\$K"
fi
export DAEDALUS_LLM_PROVIDER="$PROV"
export DAEDALUS_LLM_MODEL="$MODEL"
export DAEDALUS_LLM_BASE_URL="$BASE_URL"
exec "\$@"
EOF
chmod 755 "$HELPER"

driver_init
driver_boot

say "K0 · 把 key 与 helper 送进 guest(key 是文件,0600;不进命令行)"
gssh_scp "$DAEDALUS_LLM_KEY_FILE" /tmp/.llmkey
gssh_scp "$HELPER" /tmp/llmrun
gssh "sudo install -m 600 -o root -g root /tmp/.llmkey /root/.llmkey; sudo install -m 755 -o root -g root /tmp/llmrun /root/llmrun; rm -f /tmp/.llmkey /tmp/llmrun; sudo ls -l /root/.llmkey /root/llmrun" 2>&1 | sed 's/^/      /'
ok "key 已就位(内容不回显)"

say "K1 · guest 到网关的连通性"
gssh "curl -s -m 8 -o /dev/null -w 'http=%{http_code} ip=%{remote_ip}\n' ${BASE_URL%/}/models 2>&1 || true" | sed 's/^/      /'

say "A11-1 · --dry-run 翻译(有 key)"
D1="$(gssh "sudo /root/llmrun /usr/local/bin/daedalus --dry-run 'show os version'; echo RC=\$?" 2>&1)"
printf '%s\n' "$D1" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$D1" | grep -q 'RC=0' && printf '%s' "$D1" | grep -qiE 'command|mode|uname|cat|hostname|os-release'; then
    driver_verdict PASS "A11-1 --dry-run 经真 provider 产出翻译(rc=0)"
else
    driver_verdict FAIL "A11-1 未产出可判定翻译: $(printf '%s' "$D1" | tail -2 | tr '\n' ' ')"
fi

say "A11-2 · watchdog(rc=124 + 无残留进程)"
W1="$(gssh "sudo env DAEDALUS_WATCHDOG_TIMEOUT_MS=1000 /root/llmrun /usr/local/bin/daedalus -y 'ping 127.0.0.1 five times slowly'; echo RC=\$?" 2>&1)"
printf '%s\n' "$W1" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
PSRES="$(gssh "ps -ef | grep -c '[p]ing 127.0.0.1'" 2>&1 | tr -d '\r')"
printf '      残留 ping 进程数: %s\n' "${PSRES:-?}" | tee -a "$DRIVER_LOG"
if printf '%s' "$W1" | grep -q 'RC=124' && [ "${PSRES:-1}" = "0" ]; then
    driver_verdict PASS "A11-2 watchdog rc=124 且无残留 ping 进程"
else
    driver_verdict FAIL "A11-2 watchdog 未如期(rc/ps 见上)"
fi

say "A11-4 · 真 provider 执行 + 审计链"
R4="$(gssh "sudo /root/llmrun /usr/local/bin/daedalus -y 'show os version'; echo RC=\$?" 2>&1)"
printf '%s\n' "$R4" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
T4="$(gssh "sudo tail -3 /var/log/daedalus/audit.jsonl 2>&1; sudo /usr/local/bin/daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl 2>&1" 2>&1)"
printf '%s\n' "$T4" | sed 's/^/      /' | tee -a "$DRIVER_LOG"
if printf '%s' "$R4" | grep -q 'RC=0' && printf '%s' "$T4" | grep -qi 'hash chain intact'; then
    driver_verdict PASS "A11-4 真 provider 返回且审计链 intact"
else
    driver_verdict FAIL "A11-4 真 provider 路径未通过(见上)"
fi

# 收尾:把 key 从 guest 抹掉(overlay 落盘也在本次 QEMU 生命期内)
gssh "sudo shred -u /root/.llmkey 2>/dev/null || sudo rm -f /root/.llmkey" >/dev/null 2>&1
rm -f "$HELPER"
driver_exit
