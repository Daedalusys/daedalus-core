#!/usr/bin/env python3
"""确定性 MCP stdio 客户端 —— 给 tx 通道的真机 E2E 用。

为什么不用 AI 客户端:验收要量的是「档位判定与执行路径」,不是模型会不会说话。
这个客户端只做四件事:握手 → 列工具 → 调工具 → 打印结果,每次调用的返回都是可断言的 JSON。

用法:
  tx-e2e-client.py --bin <daedalus-tx-mcp 路径> [--env K=V]... --call <工具名> [--args <JSON 串>]
  tx-e2e-client.py --bin <路径> --list
  tx-e2e-client.py --bin <路径> --call <工具名> --args '<JSON>' --expect-rc 1
"""

import argparse
import json
import os
import subprocess
import sys
import threading

PROTOCOL_VERSION = "2025-06-18"


def _read_messages(stream, out: list) -> None:
    """按行读 JSON-RPC 消息。MCP stdio 用换行分隔的 JSON 对象。"""
    for line in stream:
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            # 服务器的日志行不是协议消息:忽略,但别把它当响应。
            continue


class MCPClient:
    def __init__(self, binary, env_extra=None):
        env = dict(os.environ)
        if env_extra:
            env.update(env_extra)
        self.proc = subprocess.Popen(
            [binary],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
            text=True,
            bufsize=1,
        )
        self._messages = []
        self._lock = threading.Lock()
        self._reader = threading.Thread(target=_read_messages, args=(self.proc.stdout, self._messages), daemon=True)
        self._reader.start()
        self._stderr = []
        threading.Thread(target=self._drain_stderr, daemon=True).start()
        self._id = 0

    def _drain_stderr(self) -> None:
        for line in self.proc.stderr:
            self._stderr.append(line.rstrip("\n"))

    def _send(self, payload: dict) -> None:
        assert self.proc.stdin is not None
        self.proc.stdin.write(json.dumps(payload, ensure_ascii=False) + "\n")
        self.proc.stdin.flush()

    def _request(self, method: str, params=None) -> dict:
        self._id += 1
        rid = self._id
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}})
        deadline_events = []
        while True:
            with self._lock:
                deadline_events = list(self._messages)
            for msg in deadline_events:
                if msg.get("id") == rid:
                    return msg
            if self.proc.poll() is not None:
                raise RuntimeError(
                    f"服务器进程已退出(rc={self.proc.returncode});stderr:\n" + "\n".join(self._stderr[-20:])
                )
            threading.Event().wait(0.05)

    def _notify(self, method: str, params=None) -> None:
        self._send({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def initialize(self) -> dict:
        resp = self._request(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "tx-e2e", "version": "0.1.0"},
            },
        )
        self._notify("notifications/initialized")
        return resp

    def list_tools(self) -> list[str]:
        resp = self._request("tools/list")
        return [t.get("name", "") for t in resp.get("result", {}).get("tools", [])]

    def call(self, name, args):
        return self._request("tools/call", {"name": name, "arguments": args})

    def close(self) -> None:
        try:
            if self.proc.stdin:
                self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()

    def stderr_text(self) -> str:
        return "\n".join(self._stderr)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", required=True, help="MCP 服务器二进制路径")
    ap.add_argument("--env", action="append", default=[], help="额外环境变量 K=V")
    ap.add_argument("--list", action="store_true", help="只列工具")
    ap.add_argument("--call", help="要调用的工具名")
    ap.add_argument("--args", default="{}", help="工具参数(JSON 串)")
    ap.add_argument("--expect-rc", type=int, default=None, help="期望的进程退出码")
    args = ap.parse_args()

    env_extra = {}
    for item in args.env:
        k, _, v = item.partition("=")
        env_extra[k] = v

    client = MCPClient(args.bin, env_extra)
    try:
        client.initialize()
        if args.list:
            print(json.dumps({"tools": client.list_tools()}, ensure_ascii=False))
            return 0
        if not args.call:
            print("需要 --call 或 --list", file=sys.stderr)
            return 2
        call_args = json.loads(args.args)
        resp = client.call(args.call, call_args)
        print(json.dumps(resp, ensure_ascii=False, indent=2))
        # MCP 层没有「进程退出码」这回事;工具被拒时返回 isError 的结构化结果。
        # 进程级退出码只在启动即失败时才有意义(比如拒绝启动)。
        return 0
    except Exception as exc:  # noqa: BLE001 —— E2E 脚本要看到失败原因
        print(f"E2E_CLIENT_ERROR: {exc}", file=sys.stderr)
        print(client.stderr_text(), file=sys.stderr)
        return 1
    finally:
        client.close()


if __name__ == "__main__":
    sys.exit(main())
