#!/usr/bin/env python3
"""把 opencode 的 JSON 事件流压成「这个 AI agent 实际做了什么」的摘要。

用法:
    python3 tests/mcp/spike-ai-agent-report.py t0 t1 t2 t3 t4
    WS=/path/in/guest python3 tests/mcp/spike-ai-agent-report.py t2
"""
import json
import os
import pathlib
import sys

WS = os.environ.get("WS", "/var/tmp/daedalus-ai-spike")
LOG = pathlib.Path(WS) / "logs"


def load(name):
    tools, texts = [], []
    p = LOG / f"{name}.json"
    if not p.exists():
        return tools, texts
    for line in p.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            ev = json.loads(line)
        except Exception:
            continue
        part = ev.get("part") or {}
        if ev.get("type") == "text" and part.get("text"):
            texts.append(part["text"])
        if part.get("type") != "tool":
            continue
        st = part.get("state") or {}
        calls = ((st.get("metadata") or {}).get("metadata") or {}).get("toolCalls") or []
        inp = st.get("input") or {}
        tools.append({
            "tool": part.get("tool"),
            "status": st.get("status"),
            "mcp_calls": [f"{c.get('tool')}[{c.get('status')}]" for c in calls],
            "input": str(inp.get("code", ""))[:400].replace("\n", " "),
            "output": str(st.get("output", ""))[:400],
        })
    return tools, texts


def main(names):
    for name in names:
        tools, texts = load(name)
        print(f"\n########## {name} ##########")
        print(f"-- 工具调用 {len(tools)} 次 --")
        for t in tools:
            label = ("MCP:" + ",".join(t["mcp_calls"])) if t["mcp_calls"] \
                else f"本地工具:{t['tool']}"
            print(f"  [{t['status']}] {label}")
            if t["input"]:
                print(f"      in : {t['input'][:260]}")
            if t["output"]:
                print(f"      out: {t['output'][:260]}")
        print("-- 最终输出 --")
        if texts:
            print(texts[-1][:3000])


if __name__ == "__main__":
    main(sys.argv[1:] or ["t0", "t1", "t2", "t3", "t4"])
