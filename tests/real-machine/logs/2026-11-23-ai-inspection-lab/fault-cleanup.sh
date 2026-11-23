#!/bin/bash
# 撤销 fault-injection.sh 注入的 6 个故障,恢复到注入前状态。
#
# 用法:./fault-cleanup.sh <port>
set -u
port=${1:?用法: fault-cleanup.sh <port>}

run(){ ssh -T \
  -o ProxyCommand='ssh -W %h:%p Masterwet_Server' \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$HOME/daedalus-lab/known_hosts" \
  -o LogLevel=ERROR \
  -i "$HOME/daedalus-lab/keys/id_ed25519" \
  -p "$port" daedalus@127.0.0.1 "sudo bash -s"; }

run <<'EOF'
systemctl disable --now lab-fault-listen.service 2>/dev/null
systemctl disable --now lab-fault-fail.service 2>/dev/null
rm -f /etc/systemd/system/lab-fault-listen.service /etc/systemd/system/lab-fault-fail.service
systemctl daemon-reload
systemctl reset-failed 2>/dev/null
rm -f /tmp/lab-fault-big.bin /var/log/lab-fault-app.log
systemctl start chronyd.service
EOF

echo "=== after cleanup ==="
echo "units:      $(run <<'EOF'
systemctl list-unit-files 'lab-fault*' --no-legend | wc -l
EOF
)"
echo "state:      $(run <<'EOF'
systemctl is-system-running 2>&1
EOF
)"
echo "failed:     $(run <<'EOF'
systemctl list-units --state=failed --no-legend | wc -l
EOF
)"
echo "chronyd:    $(run <<'EOF'
systemctl is-active chronyd
EOF
)"
echo "tmpfs /tmp: $(run <<'EOF'
df -h /tmp | tail -1
EOF
)"
