#!/usr/bin/env bash
# =============================================================================
# try-release-asset.sh —— 用「用户会拿到的那份 qcow2」走一遍:下载 → 起机 → 登录 → 看东西
# =============================================================================
# 与 probe-first-run.sh 的分工:那个验的是**镜像**;本脚本验的是 **Release 资产本身**
# (下载、引导、试用账号能否登录、sudo 是否可用)。这是「第一版能不能上手」的最后一环。
#
# 下载慢的实测口径(2026-11-19):直连 GitHub 约 0.1 MB/s(1.24 GiB 要 6 小时以上),
# 走加速站约 1.3–1.9 MB/s(≈15 分钟)。本脚本默认直连,可用 DL_PREFIX 换成加速前缀;
# 两条路都下完要用 Release 上的 SHA256SUMS 校验,校验不过就不算拿到。
#
# 用法:
#   bash try-release-asset.sh <release-tag>          # 默认 v0.1.0
# 环境:
#   WORK=/path       工作目录(默认 ~/daedalus-release-try)
#   TRY_SSH_PORT     宿主端口(默认 2244)
#   DL_PREFIX=""     下载 URL 前缀;例:"https://ghfast.top/" 会拼成
#                    https://ghfast.top/https://github.com/...
#   SKIP_DOWNLOAD=1  跳过下载(本地已有该文件时用)
# =============================================================================
set -uo pipefail
TAG="${1:-v0.1.0}"
WORK="${WORK:-$HOME/daedalus-release-try}"
PORT="${TRY_SSH_PORT:-2244}"
REPO="Daedalusys/daedalus-core"
USER_="daedalus"
PASS_="daedalus"
mkdir -p "$WORK"; cd "$WORK" || exit 2

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
ok()  { printf '\033[1;32m   OK %s\033[0m\n' "$*"; }
bad() { printf '\033[1;31m   XX %s\033[0m\n' "$*"; }
FAILN=0
verdict() { printf '%s  %s\n' "$1" "$*"; [ "$1" = FAIL ] && FAILN=$((FAILN+1)); return 0; }

ASSET="qcow2-${TAG}.qcow2"
say "R1 · 从 Release 下载 $ASSET"
if [ "${SKIP_DOWNLOAD:-0}" = "1" ] && [ -s "$ASSET" ]; then
    ok "SKIP_DOWNLOAD=1 且文件已在位,跳过下载"
elif [ -s "$ASSET" ]; then
    ok "已存在,复用:$ASSET($(du -h "$ASSET" | cut -f1))"
else
    curl -fL --retry 3 -o "$ASSET.part" \
        "${DL_PREFIX:-}https://github.com/$REPO/releases/download/$TAG/$ASSET" \
        && mv "$ASSET.part" "$ASSET" && ok "下载完成:$(du -h "$ASSET" | cut -f1)" \
        || { bad "下载失败"; exit 2; }
fi
if qemu-img info "$ASSET" >/dev/null 2>&1; then
    verdict PASS "R1 Release 资产可下载且是合法 qcow2"
else
    verdict FAIL "R1 资产不是合法 qcow2"; exit 1
fi

say "R2 · 直接引导这份 qcow2(snapshot=on,不写原文件)"
pkill -f "hostfwd=tcp::${PORT}-:22" 2>/dev/null && sleep 1
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m 4096 -smp 2 \
    -nographic \
    -drive "file=$WORK/$ASSET,if=virtio,format=qcow2,snapshot=on" \
    -netdev "user,id=net0,hostfwd=tcp::${PORT}-:22" -device virtio-net,netdev=net0 \
    </dev/null >"$WORK/serial-$TAG.log" 2>&1 &
QPID=$!
trap 'kill $QPID 2>/dev/null' EXIT
ok "qemu pid=$QPID,串口 → $WORK/serial-$TAG.log"

cat > "$WORK/askpass.sh" <<EOF
#!/bin/sh
printf '%s\n' "$PASS_"
EOF
chmod 700 "$WORK/askpass.sh"
export SSH_ASKPASS="$WORK/askpass.sh" SSH_ASKPASS_REQUIRE=force DISPLAY=:0
SSH=(ssh -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
     -o ConnectTimeout=8 -o LogLevel=ERROR -o PreferredAuthentications=password \
     -o PubkeyAuthentication=no "$USER_@127.0.0.1")

UP=0
for _i in $(seq 1 90); do
    if "${SSH[@]}" true </dev/null >/dev/null 2>&1; then UP=1; break; fi
    sleep 3
done
if [ "$UP" = 1 ]; then
    verdict PASS "R2 引导后可用发布账号 $USER_ 登录(口令登录,无需注入任何东西)"
else
    verdict FAIL "R2 起机 270s 内登录不上;串口尾部:"; tail -15 "$WORK/serial-$TAG.log"; exit 1
fi

say "R3 · 登录之后能看到什么"
OUT="$("${SSH[@]}" 'set -e
  echo "host=$(hostname) user=$(id -un) uid=$(id -u)"
  grep -E "^(PRETTY_NAME|VARIANT_ID)=" /usr/lib/os-release
  echo "sudo=$(sudo -n true 2>/dev/null && echo yes || echo no)"
  echo "host_list_ok=$(sudo /usr/local/bin/daedalus-host list | grep -cE "[[:space:]]ok$")"
  echo "host_list_degraded=$(sudo /usr/local/bin/daedalus-host list | grep -c degraded)"
  echo "smoke=$(sudo /usr/local/bin/daedalus-smoke 2>&1 | tail -1)"
  echo "bootc=$(sudo /usr/bin/bootc status --json | head -c 60)"
' </dev/null 2>&1)"
printf '%s\n' "$OUT" | sed 's/^/      /'
if printf '%s' "$OUT" | grep -q 'sudo=yes' \
   && printf '%s' "$OUT" | grep -q 'host_list_ok=14' \
   && printf '%s' "$OUT" | grep -q 'host_list_degraded=0' \
   && printf '%s' "$OUT" | grep -q 'SMOKE OK'; then
    verdict PASS "R3 试用账号有免密 sudo,14 插件全 ok,smoke 通过"
else
    verdict FAIL "R3 登录后能力面不完整(见上)"
fi

say "R4 · package.set 在这份产物上的文案"
P="$("${SSH[@]}" 'T=$(sudo /usr/local/bin/daedalus-tx begin | sed -n "s/.*\"tx_id\":\"\([a-f0-9]*\)\".*/\1/p")
  sudo /usr/local/bin/daedalus-tx propose "$T" package.set "{\"name\":\"zsh\",\"desired_state\":\"present\"}" 2>&1 | head -c 400' </dev/null 2>&1)"
printf '%s\n' "$P" | sed 's/^/      /'
if printf '%s' "$P" | grep -q 'immutable(bootc)'; then
    verdict PASS "R4 package.set 在 immutable 节点上清晰 fail-closed(逐字文案)"
else
    verdict FAIL "R4 package.set 的拒绝文案不含边界说明"
fi

kill $QPID 2>/dev/null; trap - EXIT; sleep 1
printf '\n判定: %s\n' "$([ "$FAILN" -eq 0 ] && echo PASS || echo "FAIL($FAILN)")"
exit $([ "$FAILN" -eq 0 ] && echo 0 || echo 1)
