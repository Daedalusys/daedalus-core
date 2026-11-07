#!/usr/bin/env bash
# =============================================================================
# probe-destructive-plugins.sh —— A5:破坏性插件回滚(仅 guest 内)
# =============================================================================
# 证伪什么
#   唯一会删用户数据的路径(diskclean / organize)与爆破半径最大的 blueprint
#   (/etc 写 + post_check + 回滚)在单测里从不 spawn 真 daedalus-tx、写目标永远
#   是临时目录。本探针在真 guest 内跑,破坏面**限制在 guest 的临时路径**。
#
# 三条
#   1. blueprint_apply 写 /etc/nginx/conf.d/<domain>.conf 后让 post_check 失败
#      (guest 无 nginx)→ 验证 rollback 把文件恢复到原 sha256;
#   2. diskclean(disk.clean)与 organize(organize.move)各跑完整
#      begin→propose→apply→rollback,记录文件清单前后 diff;
#   3. 宿主 lsblk 前后一致(证明没碰宿主盘)。
#
# 用法: sudo VERIFY_WORK=/tmp/probe-a5 bash tests/real-machine/probe-destructive-plugins.sh
# =============================================================================
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export VERIFY_WORK="${VERIFY_WORK:-/tmp/daedalus-probe-suite}"
export DRIVER_LOG="$VERIFY_WORK/probe-destructive-plugins.log"
# shellcheck source=lib/guest-driver.sh
source "$HERE/lib/guest-driver.sh"
DRIVER_QEMU_SNAPSHOT=1
# guest 是 snapshot 引导:破坏只落临时 overlay,退出即丢。

DOMAIN="probe-a5.example.com"
TARGET="/etc/nginx/conf.d/${DOMAIN}.conf"

driver_init
driver_boot

# 3) 宿主 lsblk 基线(证明没碰宿主盘)
HOST_LSBLK_BEFORE="$(lsblk -o NAME,SIZE,TYPE,MOUNTPOINT 2>&1)"

say "A5-1 · blueprint_apply 失败回滚(guest 无 nginx → post_check 必失败)"
gssh "sudo mkdir -p /etc/nginx/conf.d && printf '# probe-a5 original\n' | sudo tee $TARGET >/dev/null" >/dev/null 2>&1
SHA_BEFORE="$(gssh "sha256sum $TARGET 2>/dev/null | awk '{print \$1}'" 2>&1)"
info "原文件 sha256: $SHA_BEFORE"

# blueprint 的 plan store 是进程内的 → render 与 apply 必须在同一进程。用 bash
# coproc 与服务器交互读响应,不引入 python 依赖。
cat > "$DRIVER_WORK/a5-blueprint.sh" <<'BP'
#!/usr/bin/env bash
set -uo pipefail
BIN=/opt/daedalus/plugins/daedalus.blueprint/bin/daedalus-blueprint
DOMAIN="${1:-probe-a5.example.com}"
coproc BP { "$BIN" 2>/dev/null; }
send() { printf '%s\n' "$1" >&"${BP[1]}"; }
recv() { local l=""; IFS= read -r -t 15 l <&"${BP[0]}"; printf '%s' "$l"; }
send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}'
recv >/dev/null
send '{"jsonrpc":"2.0","method":"notifications/initialized"}'
send "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"blueprint_render\",\"arguments\":{\"name\":\"nginx-vhost\",\"params\":{\"domain\":\"$DOMAIN\"}}}}"
RENDER="$(recv)"
# 响应里 text 字段是转义过的 JSON(字段形如 \"plan_id\":\"<hex>\");plan_id 为
# 16 位 hex、confirm_token 为 64 位 hex,按十六进制串直接抽取。
PLAN_ID="$(printf '%s' "$RENDER" | sed -n 's/.*plan_id[^0-9a-f]*\([0-9a-f]\{16\}\).*/\1/p' | head -1)"
TOKEN="$(printf '%s' "$RENDER" | sed -n 's/.*confirm_token[^0-9a-f]*\([0-9a-f]\{16,\}\).*/\1/p' | head -1)"
echo "RENDER=$(printf '%s' "$RENDER" | head -c 400)"
echo "PLAN_ID=$PLAN_ID"
echo "TOKEN=$TOKEN"
send "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"blueprint_apply\",\"arguments\":{\"name\":\"nginx-vhost\",\"plan_id\":\"$PLAN_ID\",\"confirm_token\":\"$TOKEN\"}}}"
APPLY="$(recv)"
echo "APPLY=$(printf '%s' "$APPLY" | head -c 600)"
BP
gssh_scp "$DRIVER_WORK/a5-blueprint.sh" /tmp/a5-blueprint.sh >/dev/null 2>&1
BPOUT="$(gssh "sudo bash /tmp/a5-blueprint.sh $DOMAIN" 2>&1)"
printf '%s\n' "$BPOUT" | sed 's/^/      /' | tee -a "$DRIVER_LOG"

SHA_AFTER="$(gssh "sha256sum $TARGET 2>/dev/null | awk '{print \$1}'" 2>&1)"
info "回滚后 sha256: $SHA_AFTER"
if [ -n "$SHA_BEFORE" ] && [ "$SHA_BEFORE" = "$SHA_AFTER" ]; then
    driver_verdict PASS "A5-1 blueprint_apply 失败后回滚把 $TARGET 恢复到原 sha256($SHA_AFTER)"
else
    driver_verdict FAIL "A5-1 回滚未恢复到原 sha256(before=$SHA_BEFORE after=$SHA_AFTER)"
fi

say "A5-2a · diskclean 走完整 begin→propose→apply→rollback"
gssh "rm -rf /home/verify/a5-disk; mkdir -p /home/verify/a5-disk; for i in 1 2 3; do printf 'junk-%s' \$i > /home/verify/a5-disk/junk\$i.bin; done; printf keep > /home/verify/a5-disk/keep.txt" >/dev/null 2>&1
TX="$(gssh 'daedalus-tx begin' 2>&1 | sed -n 's/.*"tx_id":"\([a-f0-9]\{16\}\)".*/\1/p' | head -1)"
info "tx_id(disk.clean)=$TX"
info "propose: $(gssh "daedalus-tx propose $TX disk.clean '{\"paths\":[\"/home/verify/a5-disk/junk1.bin\",\"/home/verify/a5-disk/junk2.bin\",\"/home/verify/a5-disk/junk3.bin\"]}'" 2>&1 | head -c 300)"
info "apply: $(gssh "daedalus-tx apply $TX" 2>&1 | head -c 200)"
AFTER2="$(gssh "find /home/verify/a5-disk -type f | sort" 2>&1)"
info "apply 后清单: $(printf '%s' "$AFTER2" | tr '\n' ' ')"
gssh "daedalus-tx rollback $TX" >/dev/null 2>&1
RB2="$(gssh "find /home/verify/a5-disk -type f | sort" 2>&1)"
info "rollback 后清单: $(printf '%s' "$RB2" | tr '\n' ' ')"
if printf '%s' "$AFTER2" | grep -q 'keep.txt' && ! printf '%s' "$AFTER2" | grep -q 'junk1.bin'; then
    driver_verdict PASS "A5-2a disk.clean 真删除了目标(keep.txt 留存),清单 diff 已记录"
else
    driver_verdict FAIL "A5-2a disk.clean 未按预期删除: apply 后=[$(printf '%s' "$AFTER2" | tr '\n' ' ')]"
fi

say "A5-2b · organize 走完整 begin→propose→apply→rollback"
gssh "rm -rf /home/verify/a5-org; mkdir -p /home/verify/a5-org/src /home/verify/a5-org/dst; printf 'movable' > /home/verify/a5-org/src/m1.txt; printf 'movable2' > /home/verify/a5-org/src/m2.txt" >/dev/null 2>&1
TX2="$(gssh 'daedalus-tx begin' 2>&1 | sed -n 's/.*"tx_id":"\([a-f0-9]\{16\}\)".*/\1/p' | head -1)"
info "tx_id(organize.move)=$TX2"
M1='{"plan_id":"probe-a5","moves":[{"from":"/home/verify/a5-org/src/m1.txt","to":"/home/verify/a5-org/dst/m1.txt","size_bytes":7,"rule":"probe"},{"from":"/home/verify/a5-org/src/m2.txt","to":"/home/verify/a5-org/dst/m2.txt","size_bytes":8,"rule":"probe"}]}'
info "propose: $(gssh "daedalus-tx propose $TX2 organize.move '$M1'" 2>&1 | head -c 200)"
info "apply: $(gssh "daedalus-tx apply $TX2" 2>&1 | head -c 200)"
AFTER3="$(gssh "find /home/verify/a5-org -type f | sort" 2>&1)"
info "apply 后清单: $(printf '%s' "$AFTER3" | tr '\n' ' ')"
gssh "daedalus-tx rollback $TX2" >/dev/null 2>&1
RB3="$(gssh "find /home/verify/a5-org -type f | sort" 2>&1)"
info "rollback 后清单: $(printf '%s' "$RB3" | tr '\n' ' ')"
if printf '%s' "$AFTER3" | grep -q 'dst/m1.txt' && printf '%s' "$RB3" | grep -q 'src/m1.txt'; then
    driver_verdict PASS "A5-2b organize.move apply 生效且 rollback 归位(src↔dst 清单 diff 已记录)"
else
    driver_verdict FAIL "A5-2b organize.move 未按预期移动/归位: after=[$(printf '%s' "$AFTER3" | tr '\n' ' ')] rollback=[$(printf '%s' "$RB3" | tr '\n' ' ')]"
fi

say "A5-3 · 宿主 lsblk 前后一致"
HOST_LSBLK_AFTER="$(lsblk -o NAME,SIZE,TYPE,MOUNTPOINT 2>&1)"
if [ "$HOST_LSBLK_BEFORE" = "$HOST_LSBLK_AFTER" ]; then
    driver_verdict PASS "A5-3 宿主 lsblk 前后逐字节一致(未碰宿主盘)"
else
    driver_verdict FAIL "A5-3 宿主 lsblk 发生变化"
    diff <(printf '%s\n' "$HOST_LSBLK_BEFORE") <(printf '%s\n' "$HOST_LSBLK_AFTER") | sed 's/^/      /' | tee -a "$DRIVER_LOG"
fi

driver_exit
