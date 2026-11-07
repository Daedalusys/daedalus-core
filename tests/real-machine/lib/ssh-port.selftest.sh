#!/usr/bin/env bash
# ssh-port.sh 的判定逻辑自检:三种分支各一条断言,不碰 qemu、不需要 KVM。
set -euo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/ssh-port.sh"

fail=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
    else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail=$((fail + 1)); fi
}

# 1) 未设 VERIFY_SSH_PORT → 用默认值
unset VERIFY_SSH_PORT
if out="$(ssh_port_resolve 2222)"; then rc=0; else rc=$?; fi
st 0 "$rc" "未设开关 → 返回 0"
st 2222 "$out" "未设开关 → 用默认 2222"

# 2) 非法值 → 明确失败,不静默回落
VERIFY_SSH_PORT=abc
if ssh_port_resolve 2222 >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 2 "$rc" "非法值 abc → 退出码 2"

VERIFY_SSH_PORT=70000
if ssh_port_resolve 2222 >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 2 "$rc" "越界值 70000 → 退出码 2"

# 3) 合法覆盖 → 原样返回
VERIFY_SSH_PORT=2223
st 2223 "$(ssh_port_resolve 2222)" "合法覆盖 2223 → 原样返回"

# 4) 端口被占 → 退出码 3
# 端口号由监听者自己报出来(而不是从 ss 输出里猜),否则可能测到别人的端口。
BUSY_PORT_FILE="$(mktemp)"
python3 - "$BUSY_PORT_FILE" <<'PY' &
import socket, sys, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 0)); s.listen(1)
with open(sys.argv[1], "w") as f:
    f.write(str(s.getsockname()[1]))
time.sleep(30)
PY
BUSY_PID=$!
for _ in $(seq 1 50); do
    [ -s "$BUSY_PORT_FILE" ] && break
    sleep 0.1
done
BUSY_PORT="$(cat "$BUSY_PORT_FILE")"
rm -f "$BUSY_PORT_FILE"
if ssh_port_guard "$BUSY_PORT" >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 3 "$rc" "端口被占 → 退出码 3"
kill "$BUSY_PID" 2>/dev/null || true

# 5) 端口空闲 → 退出码 0
if ssh_port_guard 65000 >/dev/null 2>&1; then rc=0; else rc=$?; fi
st 0 "$rc" "端口空闲 → 退出码 0"

# 6) ss 不可用 → 明确降级但仍放行
if PATH=/nonexistent ssh_port_guard 65000 2>/dev/null; then rc=0; else rc=$?; fi
st 0 "$rc" "ss 不可用 → 降级放行(0)"

if [ "$fail" -eq 0 ]; then echo "RESULT: SSHPORT SELFTEST PASS"; exit 0; fi
echo "RESULT: SSHPORT SELFTEST FAIL ($fail)"; exit 1
