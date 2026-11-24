#!/bin/bash
# probe-capabilities.sh <port> —— 直连 guest 的 MCP 插件,逐条验证本轮修复的硬断言。
#
# 与巡检报告的分工:巡检报告只能说"模型没报错";这里量的是协议层字节数与
# outcome 字段——「回包有多大」「rc!=0 是不是 MCP error」必须由原始回包判定。
set -u
port=${1:-9601}
LAB=${LAB:-$HOME/daedalus-lab}
MCP=$LAB/mcpcheck.py
JOURNAL=$(ssh -T -o ProxyCommand='ssh -W %h:%p Masterwet_Server' -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$LAB/known_hosts" -o LogLevel=ERROR -i "$LAB/keys/id_ed25519" \
  -p "$port" daedalus@127.0.0.1 'ls /var/log/journal/*/system.journal | head -1' | tr -d '\r')

echo "== 1. journalctl 已在白名单 =="
python3 "$MCP" "$port" shell shell_exec '{"command":"journalctl","args":["-p","err","-n","5","--no-pager"]}' --fields
echo
echo "== 2. 非零退出码不再是 MCP error(is-system-running=1) =="
python3 "$MCP" "$port" shell shell_exec '{"command":"systemctl","args":["is-system-running"]}' --fields
echo
echo "== 3. 单元不存在(rc=4)不再是 MCP error =="
python3 "$MCP" "$port" shell shell_exec '{"command":"systemctl","args":["status","no-such-unit"]}' --fields
echo
echo "== 4. 真正该拒的仍然拒(journalctl -f)=="
python3 "$MCP" "$port" shell shell_exec '{"command":"journalctl","args":["-f"]}' --fields
echo
echo "== 5. 单次 shell 输出 > 1 MiB:截断而非断连 =="
python3 "$MCP" "$port" shell shell_exec '{"command":"journalctl","args":["-t","lab-fault-bulk","--no-pager"]}' --fields
echo
echo "== 6. 读 8 MiB journal:回包必须 < 16 MiB =="
python3 "$MCP" "$port" fs read_file "$(python3 -c 'import json,sys;print(json.dumps({"path":sys.argv[1]}))' "$JOURNAL")"
echo
echo "== 7. 端口归属线索(data 进程 fd 不可读时) =="
python3 "$MCP" "$port" proc proc_listen '{"port":18081,"proto":"tcp"}'
echo
echo "== 8. proc_fds 权限不足:部分结果 + denied =="
python3 "$MCP" "$port" proc proc_fds '{"pid":1}'
