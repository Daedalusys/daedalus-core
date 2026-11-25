#!/bin/bash
# 在靶场 guest(真实 systemd 252 + root)上冒烟验证 daedalus-patrol.service 的
# StateDirectory / UMask / ExecStartPre 链 / BindReadOnlyPaths / LoadCredential 语义。
# 单元结构与真实单元逐行一致,只把 ExecStart 换成探针(镜像里没有 opencode)。
set -u

UNIT=patrol-unit-smoke
OUT=/tmp/patrol-unit-smoke
rm -rf "$OUT"; mkdir -p "$OUT"

mkdir -p /etc/credstore
printf 'smoke-user' > /etc/credstore/smtp-username
printf 'smoke-pass' > /etc/credstore/smtp-password
chmod 600 /etc/credstore/smtp-username /etc/credstore/smtp-password

cat > /usr/local/bin/patrol-probe <<'PROBEEOF'
#!/bin/sh
# 模拟 AI 客户端:在 HOME 下建数据目录、写日志/会话库。
set -u
echo "uid=$(id -u) umask=$(umask) HOME=$HOME"
mkdir -p "$HOME/.local/share/opencode/log"
echo "probe" > "$HOME/.local/share/opencode/log/2026-11-25.log"
echo "db"    > "$HOME/.local/share/opencode/storage.db"
echo "--- credentials ---"
ls -l "$CREDENTIALS_DIRECTORY" 2>&1
echo "--- modes ---"
stat -c '%a %U:%G %n' \
    /var/lib/daedalus-patrol /var/lib/daedalus-patrol/home \
    "$HOME/.local/share/opencode" \
    "$HOME/.local/share/opencode/log" "$HOME/.local/share/opencode/log/2026-11-25.log" \
    "$HOME/.local/share/opencode/storage.db" 2>&1
if [ -f "$HOME/.local/share/opencode/auth.json" ]; then
    stat -c '%a %n' "$HOME/.local/share/opencode/auth.json"
else
    echo "auth.json=absent"
fi
echo "--- host config mounted read-only ---"
stat -c '%a %U:%G %n' "$HOME/.config/opencode/opencode.json" 2>&1
cat "$HOME/.config/opencode/opencode.json" 2>&1
echo "--- state dir writable (fleet sentinel) ---"
touch /var/lib/daedalus/.smoke-sentinel && echo "state_rw=yes" || echo "state_rw=no"
rm -f /var/lib/daedalus/.smoke-sentinel
echo "--- host config NOT writable from sandbox ---"
touch /root/.config/opencode/.smoke-probe 2>/dev/null && echo "host_cfg_rw=yes" || echo "host_cfg_rw=no"
echo "--- stale marker from previous round ---"
cat "$HOME/.local/share/opencode/log/stale.log" 2>&1 || true
echo "probe done"
PROBEEOF
chmod 0755 /usr/local/bin/patrol-probe

cat > /etc/systemd/system/$UNIT.service <<'UNITEOF'
[Unit]
Description=patrol unit smoke (mirror of daedalus-patrol.service sandbox)
[Service]
Type=oneshot
EnvironmentFile=-/etc/daedalus/patrol.env
Environment=PATH=/root/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=HOME=/var/lib/daedalus-patrol/home
StateDirectory=daedalus-patrol daedalus
UMask=0077
ExecStartPre=/bin/install -d -m 0700 /var/lib/daedalus-patrol/home
ExecStartPre=/bin/rm -rf /var/lib/daedalus-patrol/home/.local/share/opencode
ExecStartPre=/bin/mkdir -p /var/lib/daedalus-patrol/home/.config /var/lib/daedalus-patrol/home/.local/share/opencode
BindReadOnlyPaths=-/root/.config/opencode:/var/lib/daedalus-patrol/home/.config/opencode
ExecStartPre=/bin/sh -c 'if [ -f /root/.local/share/opencode/auth.json ]; then /bin/cp -f /root/.local/share/opencode/auth.json /var/lib/daedalus-patrol/home/.local/share/opencode/auth.json || exit 1; /bin/chmod 600 /var/lib/daedalus-patrol/home/.local/share/opencode/auth.json; fi'
LoadCredential=smtp-username:/etc/credstore/smtp-username
LoadCredential=smtp-password:/etc/credstore/smtp-password
TimeoutStartSec=15min
TimeoutStopSec=30s
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
NoNewPrivileges=yes
ProtectControlGroups=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources @obsolete
ExecStart=/usr/local/bin/patrol-probe
UNITEOF
systemctl daemon-reload

run() {   # run <tag> <描述>
    local tag=$1 desc=$2 since
    echo "########## $tag: $desc ##########"
    systemctl reset-failed $UNIT.service 2>/dev/null
    since=$(date '+%Y-%m-%d %H:%M:%S')
    systemctl start $UNIT.service
    echo "start_exit=$?"
    systemctl show $UNIT.service -p Result -p ExecMainStatus -p StateDirectory \
        -p UMask -p ProtectSystem -p ProtectHome -p BindReadOnlyPaths
    journalctl -u $UNIT.service --since "$since" --no-pager -o cat
    echo
} 

# A:宿主没有 opencode 配置目录(bind 源缺失,`-` 前缀跳过,home 也没被 systemd 预建)
if [ -e /root/.config/opencode ]; then mv /root/.config/opencode /root/.config/opencode.smoke-bak; fi
rm -rf /var/lib/daedalus-patrol
run A "host has NO /root/.config/opencode (bind source missing)"

# B:宿主有配置 + auth.json;数据目录里预置上轮残留,验证每轮重建
mkdir -p /root/.config/opencode
printf '{"marker":"host-config"}\n' > /root/.config/opencode/opencode.json
mkdir -p /root/.local/share/opencode
printf '{"smoke":{"type":"api","key":"sk-smoke-FAKE"}}\n' > /root/.local/share/opencode/auth.json
chmod 600 /root/.local/share/opencode/auth.json
mkdir -p /var/lib/daedalus-patrol/home/.local/share/opencode/log
echo "stale-from-previous-round" > /var/lib/daedalus-patrol/home/.local/share/opencode/log/stale.log
run B "host HAS config + auth.json, data dir has leftovers"

# C:凭据复制失败必须让单元失败(N3);源不存在必须不算错
echo "########## C: auth.json copy failure must fail the unit ##########"
mkdir -p /root/.local/share/opencode
printf '{}\n' > /root/.local/share/opencode/auth.json
# 用不可写目标(/proc 下不存在可建目录)模拟 ENOSPC/EACCES:片段必须 exit 1
/bin/sh -c 'if [ -f /root/.local/share/opencode/auth.json ]; then /bin/cp -f /root/.local/share/opencode/auth.json /proc/daedalus-patrol/auth.json || exit 1; fi' 2>/dev/null
echo "cp_failure_exit=$?  (期望非 0:复制失败要让单元失败)"
rm -f /root/.local/share/opencode/auth.json
# 源不存在:必须静默通过(用内联 provider 凭据的部署没有 auth.json)
/bin/sh -c 'if [ -f /root/.local/share/opencode/auth.json ]; then /bin/cp -f /root/.local/share/opencode/auth.json /var/lib/daedalus-patrol/home/.local/share/opencode/auth.json || exit 1; fi'
echo "cp_source_missing_exit=$?  (期望 0)"
echo

# 清理
systemctl stop $UNIT.service 2>/dev/null
rm -f /etc/systemd/system/$UNIT.service /usr/local/bin/patrol-probe
rm -rf /etc/credstore /root/.config/opencode /root/.local/share/opencode/auth.json /var/lib/daedalus-patrol
systemctl daemon-reload
if [ -e /root/.config/opencode.smoke-bak ]; then mv /root/.config/opencode.smoke-bak /root/.config/opencode; fi
echo "=== cleanup done ==="
