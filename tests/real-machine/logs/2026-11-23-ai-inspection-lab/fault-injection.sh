#!/bin/bash
# inject.sh <port> [fault...]   默认注入全部
port=$1; shift
FAULTS=${*:-"F1 F2 F3 F4a F4b F5"}
run(){ ssh -T -o ProxyCommand='ssh -W %h:%p Masterwet_Server' -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$HOME/daedalus-lab/known_hosts" -o LogLevel=ERROR \
  -i "$HOME/daedalus-lab/keys/id_ed25519" -p "$port" daedalus@127.0.0.1 "sudo bash -s"; }

for f in $FAULTS; do
case $f in
F1) run <<'EOF'
cat >/etc/systemd/system/lab-fault-fail.service <<'U'
[Unit]
Description=lab fault: always fails
[Service]
Type=oneshot
ExecStart=/bin/false
[Install]
WantedBy=multi-user.target
U
systemctl daemon-reload
systemctl enable --now lab-fault-fail.service 2>/dev/null
systemctl start lab-fault-fail.service 2>/dev/null
echo "F1 injected: $(systemctl is-failed lab-fault-fail.service)"
EOF
;;
F2) run <<'EOF'
cat >/etc/systemd/system/lab-fault-listen.service <<'U'
[Unit]
Description=lab fault: rogue listener 18080
After=network.target
[Service]
ExecStart=/usr/bin/python3 -m http.server 18080 --bind 0.0.0.0
Restart=always
[Install]
WantedBy=multi-user.target
U
systemctl daemon-reload
systemctl enable --now lab-fault-listen.service 2>/dev/null
sleep 2
echo "F2 injected: $(ss -ltn 2>/dev/null | grep -c 18080) listener(s) on 18080"
EOF
;;
F3) run <<'EOF'
dd if=/dev/zero of=/tmp/lab-fault-big.bin bs=1M count=1200 status=none
echo "F3 injected: $(du -h /tmp/lab-fault-big.bin | cut -f1) in /tmp"
EOF
;;
F4a) run <<'EOF'
{ for i in $(seq 1 200); do echo "2026-11-23T03:2${i:0:1}:00Z ERROR app-worker[${i}] failed to commit transaction: connection reset by peer"; done
  for i in $(seq 1 50); do echo "2026-11-23T03:2${i:0:1}:01Z FATAL app-worker[${i}] crater: exiting"; done
} >/var/log/lab-fault-app.log
chmod 644 /var/log/lab-fault-app.log
echo "F4a injected: $(wc -l </var/log/lab-fault-app.log) lines, mode $(stat -c %a /var/log/lab-fault-app.log)"
EOF
;;
F4b) run <<'EOF'
logger -p daemon.err -t lab-fault "DISK FAILURE predicted on /dev/vdb, array degraded, 3 of 5 members offline"
logger -p kern.err  -t lab-fault "I/O error, dev vdb, sector 2048 op 0x0"
echo "F4b injected: journal-only errors written"
EOF
;;
F5) run <<'EOF'
systemctl stop chronyd.service
echo "F5 injected: chronyd=$(systemctl is-active chronyd.service)"
EOF
;;
esac
done
