#!/bin/bash
# fault-injection-v2.sh <port> [fault...]
#
# 修复后复测用的注入集 = 上一轮 6 个故障(F1/F2/F3/F4a/F4b/F5)+ 3 个针对本轮修复的
# 新故障(N1/N2/N3)。默认注入全部;逐个故障幂等(重复注入不叠加新现象)。
#
#   F1  单元失败(状态轴)                    —— 上一轮已检出,防回归
#   F2  0.0.0.0:18080 多余监听(状态轴)      —— 上一轮已检出,防回归
#   F3  /tmp 1.2G 大文件(容量轴)             —— 上一轮已检出,防回归
#   F4a /var/log 250 行 ERROR/FATAL(日志文件轴) —— 上一轮已检出,防回归
#   F4b journal-only 磁盘错误(日志轴)        —— 上一轮 100% 漏检,本轮硬断言 1
#   F5  chronyd 停止(服务轴)                 —— 上一轮已检出,防回归
#   N1  journal-only 反复崩溃 + StartLimit   —— 只有读 journal 才能解释"为什么失败"
#   N2  journal 里 > 1 MiB 的可读文本        —— 单次 shell 输出超上限时必须截断而非断连
#   N3  root 进程监听 18081                  —— fd 不可读,归属只能靠 cmdline 线索
set -u
port=$1; shift
FAULTS=${*:-"F1 F2 F3 F4a F4b F5 N1 N2 N3"}
run() { ssh -T -o ProxyCommand='ssh -W %h:%p Masterwet_Server' -o StrictHostKeyChecking=no \
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
N1) run <<'EOF'
cat >/usr/local/bin/lab-fault-crash <<'S'
#!/bin/bash
echo "lab-fault-crash: cannot open /etc/lab-fault-crash.conf (No such file or directory)" >&2
echo "lab-fault-crash: configuration missing, aborting" >&2
exit 78
S
chmod 755 /usr/local/bin/lab-fault-crash
cat >/etc/systemd/system/lab-fault-crash.service <<'U'
[Unit]
Description=lab fault: crash loop with journal-only explanation
StartLimitIntervalSec=10
StartLimitBurst=3
[Service]
ExecStart=/usr/local/bin/lab-fault-crash
Restart=on-failure
[Install]
WantedBy=multi-user.target
U
systemctl daemon-reload
systemctl enable --now lab-fault-crash.service 2>/dev/null
sleep 5
echo "N1 injected: $(systemctl is-failed lab-fault-crash.service) (restarts=$(systemctl show -p NRestarts --value lab-fault-crash.service))"
EOF
;;
N2) run <<'EOF'
# 40 MiB 纯文本写进 journal:任何一次不设上限的 journalctl 读取都会撑爆客户端。
python3 - <<'P' | logger -t lab-fault-bulk --id=-
for i in range(260000):
    print(f"lab-fault-bulk line {i:06d} " + "x" * 100)
P
echo "N2 injected: $(journalctl -t lab-fault-bulk --no-pager 2>/dev/null | wc -c) bytes in journal"
EOF
;;
N3) run <<'EOF'
cat >/etc/systemd/system/lab-fault-rootlisten.service <<'U'
[Unit]
Description=lab fault: root-owned listener 18081
After=network.target
[Service]
User=root
ExecStart=/usr/bin/python3 -m http.server 18081 --bind 0.0.0.0
Restart=always
[Install]
WantedBy=multi-user.target
U
systemctl daemon-reload
systemctl enable --now lab-fault-rootlisten.service 2>/dev/null
sleep 2
echo "N3 injected: $(ss -ltn 2>/dev/null | grep -c 18081) listener(s) on 18081"
EOF
;;
esac
done
