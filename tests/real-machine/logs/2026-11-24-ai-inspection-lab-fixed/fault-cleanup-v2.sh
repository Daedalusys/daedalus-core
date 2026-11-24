#!/bin/bash
# fault-cleanup-v2.sh <port> —— 清掉 fault-injection-v2.sh 注入的 9 个故障。
# 顺序:先停/禁单元,再删文件,最后清 journal(否则 N2 的 2.58 MB 文本会留到下一轮)。
set -u
port=${1:-9601}
run() { ssh -T -o ProxyCommand='ssh -W %h:%p Masterwet_Server' -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$HOME/daedalus-lab/known_hosts" -o LogLevel=ERROR \
  -i "$HOME/daedalus-lab/keys/id_ed25519" -p "$port" daedalus@127.0.0.1 "sudo bash -s"; }

run <<'EOF'
for u in lab-fault-fail lab-fault-listen lab-fault-rootlisten lab-fault-crash; do
  systemctl disable --now "$u.service" >/dev/null 2>&1
  rm -f "/etc/systemd/system/$u.service"
done
systemctl daemon-reload
rm -f /usr/local/bin/lab-fault-crash
rm -f /tmp/lab-fault-big.bin /var/log/lab-fault-app.log
systemctl start chronyd.service
# N2 的批量 journal 行:旋转 + 清空归档段(等价于 journalctl --vacuum-time=1s 的两步)
journalctl --rotate >/dev/null 2>&1
sleep 2
journalctl --vacuum-time=1s 2>&1 | tail -1
echo "cleanup done: $(systemctl list-units --failed --no-pager --plain | grep -c lab-fault) lab-fault failed units remain"
EOF
