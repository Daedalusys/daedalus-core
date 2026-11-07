#!/bin/bash
# =============================================================================
# mcp-call.sh —— guest 内使用的 MCP stdio 调用助手
# =============================================================================
# 能力服务器是 stdio JSON-RPC(MCP)。本助手把 initialize + initialized 通知 +
# tools/call 一次性写进服务器 stdin,再取出 id=2 的响应行(唯一判据来源:
# 服务器原始响应,不是我们的猜测)。
#
# 为什么请求写完还要 sleep:sdk 的 stdio transport 在读到 EOF 时会关连接,
# 若请求瞬间灌完,处理中的 tools/call 可能在写回响应前就被取消(实测输出为空)。
# 多留 3 秒让服务器把响应写出来,再关 stdin。
#
# 用法: mcp-call.sh <server-binary> <tool-name> <json-args>
#   例: mcp-call.sh /opt/daedalus/plugins/daedalus.fs/bin/daedalus-fs \
#         write_file '{"path":"/etc/shadow","content":"x"}'
#
# 退出码:服务器自身的退出码;无 id=2 响应时输出为空(调用方据此判"未响应")。
# 用 #!/bin/bash(而非 /usr/bin/env bash):调用方可收紧 PATH 来演练降级路径。
# =============================================================================
set -uo pipefail
bin="$1"; tool="$2"; args="$3"
{
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"daedalus-probe","version":"1.0.0"}}}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
    printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"${tool}\",\"arguments\":${args}}}"
    sleep 3
} | timeout 60 "$bin" 2>/dev/null | grep -E '"id"[[:space:]]*:[[:space:]]*2([^0-9]|$)' | tail -1
