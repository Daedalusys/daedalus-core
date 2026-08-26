"""Daedalus Copilot CLI 契约与安全拒绝一致性集成测试。

测试验证：
1. 通过子进程调用指向 daedalus/ 源树的 audit-log.py 验证哈希链审计日志 CLI 契约。
2. 验证 Python shell 服务器验证规则与 Daedalus Copilot 策略之间的安全拒绝一致性（强制执行 ALLOW_COMMANDS、受阻路径和 returncode 126）。

双重兼容 pytest 与 python3 -m unittest。
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, List

REPO_ROOT = Path(__file__).resolve().parent.parent
SERVERS_DIR = REPO_ROOT / "daedalus" / "files" / "system" / "opt" / "daedalus" / "servers"
AUDIT_SCRIPT = REPO_ROOT / "daedalus" / "files" / "system" / "opt" / "daedalus" / "audit-log.py"

# 将服务器目录添加到 sys.path，以便可以直接导入模块
if str(SERVERS_DIR) not in sys.path:
    sys.path.insert(0, str(SERVERS_DIR))
if str(AUDIT_SCRIPT.parent) not in sys.path:
    sys.path.insert(0, str(AUDIT_SCRIPT.parent))


def run_python_shell_logic():
    """从 daedalus/ 源目录树导入并返回 Python shell 服务器函数。"""
    import importlib.util

    # 如果当前测试环境中缺少 mcp 包，则提供模拟 mcp
    if "mcp" not in sys.modules:
        class DummyFastMCP:
            def __init__(self, name: str):
                self.name = name
                self.tools = {}

            def tool(self, *args, **kwargs):
                def decorator(fn):
                    self.tools[fn.__name__] = fn
                    return fn
                return decorator

            def run(self):
                pass

        class DummyTypes:
            class ToolAnnotations:
                def __init__(self, **kwargs):
                    pass

        import types
        dummy_mcp_pkg = types.ModuleType("mcp")
        dummy_mcp_server = types.ModuleType("mcp.server")
        dummy_mcp_fastmcp = types.ModuleType("mcp.server.fastmcp")
        dummy_mcp_fastmcp.FastMCP = DummyFastMCP
        dummy_mcp_types = types.ModuleType("mcp.types")
        dummy_mcp_types.ToolAnnotations = DummyTypes.ToolAnnotations

        sys.modules["mcp"] = dummy_mcp_pkg
        sys.modules["mcp.server"] = dummy_mcp_server
        sys.modules["mcp.server.fastmcp"] = dummy_mcp_fastmcp
        sys.modules["mcp.types"] = dummy_mcp_types

    spec = importlib.util.spec_from_file_location("py_shell_server", str(SERVERS_DIR / "shell_server.py"))
    if spec is None or spec.loader is None:
        raise ImportError(f"Could not load spec from {SERVERS_DIR / 'shell_server.py'}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# ---------------------------------------------------------------------------
# 测试 1: Copilot 审计 CLI 契约
# ---------------------------------------------------------------------------

def test_copilot_audit_cli_contract():
    """验证 audit-log.py CLI 调用、创世哈希和 SHA-256 链的连续性。"""
    assert AUDIT_SCRIPT.exists(), f"Audit script not found at {AUDIT_SCRIPT}"

    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as tf:
        audit_file_path = tf.name

    try:
        # 1. 条目 1: copilot_translate 事件
        args_1 = '{"query":"check disk","round":0}'
        cmd_1 = [
            sys.executable,
            str(AUDIT_SCRIPT),
            "--identity", "daedalus-copilot",
            "--tool", "copilot_translate",
            "--args", args_1,
            "--outcome", "success",
            "--log-path", audit_file_path,
        ]
        proc_1 = subprocess.run(cmd_1, capture_output=True, text=True, check=True)
        assert proc_1.returncode == 0, f"Entry 1 CLI failed: {proc_1.stderr}"

        # 2. 条目 2: copilot_confirm 事件
        args_2 = '{"command":"df","args":["-h"]}'
        cmd_2 = [
            sys.executable,
            str(AUDIT_SCRIPT),
            "--identity", "daedalus-copilot",
            "--tool", "copilot_confirm",
            "--args", args_2,
            "--outcome", "success",
            "--log-path", audit_file_path,
        ]
        proc_2 = subprocess.run(cmd_2, capture_output=True, text=True, check=True)
        assert proc_2.returncode == 0, f"Entry 2 CLI failed: {proc_2.stderr}"

        # 3. 读回条目并验证加密哈希链的完整性
        with open(audit_file_path, "r", encoding="utf-8") as f:
            lines = [json.loads(line.strip()) for line in f if line.strip()]

        assert len(lines) == 2, f"Expected 2 audit log entries, got {len(lines)}"

        entry1, entry2 = lines[0], lines[1]

        # 验证条目 1 的创世哈希
        assert entry1["identity"] == "daedalus-copilot"
        assert entry1["tool"] == "copilot_translate"
        assert entry1["outcome"] == "success"
        assert entry1["args"] == {"query": "check disk", "round": 0}
        assert entry1["prev_hash"] == "0" * 64, f"Entry 1 prev_hash {entry1['prev_hash']} is not genesis"
        assert len(entry1["entry_hash"]) == 64

        # 验证条目 2 的哈希连续性
        assert entry2["identity"] == "daedalus-copilot"
        assert entry2["tool"] == "copilot_confirm"
        assert entry2["outcome"] == "success"
        assert entry2["args"] == {"command": "df", "args": ["-h"]}
        assert entry2["prev_hash"] == entry1["entry_hash"], (
            f"Hash chain broken: entry 2 prev_hash {entry2['prev_hash']} != entry 1 entry_hash {entry1['entry_hash']}"
        )
        assert len(entry2["entry_hash"]) == 64

        # 独立为两个条目重新计算 SHA-256 哈希
        for idx, rec in enumerate([entry1, entry2]):
            args_str = json.dumps(rec["args"], sort_keys=True, separators=(",", ":"))
            payload = f"{rec['timestamp']}{rec['identity']}{rec['tool']}{args_str}{rec['outcome']}{rec['prev_hash']}"
            computed_hash = hashlib.sha256(payload.encode("utf-8")).hexdigest()
            assert rec["entry_hash"] == computed_hash, (
                f"Entry hash mismatch at index {idx}: recorded {rec['entry_hash']} != computed {computed_hash}"
            )

    finally:
        if os.path.exists(audit_file_path):
            os.remove(audit_file_path)


# ---------------------------------------------------------------------------
# 测试 2: 安全拒绝一致性
# ---------------------------------------------------------------------------

def test_copilot_security_rejection_parity():
    """验证 Python shell 服务器中的安全验证规则和拒绝行为。"""
    py_shell = run_python_shell_logic()

    # 1. 禁止命令 'rm' 抛出包含 'not in ALLOW_COMMANDS' 的异常
    try:
        py_shell.validate_command("rm")
        assert False, "Expected PermissionError for disallowed command 'rm'"
    except (PermissionError, ValueError) as e:
        assert "not in ALLOW_COMMANDS" in str(e), f"Expected 'not in ALLOW_COMMANDS' in error: {e}"

    # 2. 参数中的受阻路径 '/etc/shadow' 抛出包含 'forbidden' 或 'blocked path' 的异常
    try:
        py_shell.validate_arg("/etc/shadow")
        assert False, "Expected PermissionError for blocked argument '/etc/shadow'"
    except (PermissionError, ValueError) as e:
        err_msg = str(e).lower()
        assert "forbidden" in err_msg or "blocked path" in err_msg or "outside allowed" in err_msg, (
            f"Expected forbidden/blocked message for /etc/shadow: {e}"
        )

    # 3. 允许的命令 'df' 和允许的参数 '/tmp' 通过验证且不抛出异常
    cmd_validated = py_shell.validate_command("df")
    assert cmd_validated == "df"
    py_shell.validate_arg("-h")
    py_shell.validate_arg("/tmp")

    # 4. 与 copilot returncode 126 拒绝行为的一致性
    async def verify_shell_exec_parity():
        # 禁止命令 rm 返回 returncode 126
        res_rm = await py_shell.shell_exec("rm", ["-rf", "/tmp/test"])
        assert res_rm["returncode"] == 126
        assert "not in ALLOW_COMMANDS" in res_rm["stderr"] or "validation failed" in res_rm["stderr"]

        # 禁止路径访问返回 returncode 126
        res_cat = await py_shell.shell_exec("cat", ["/etc/shadow"])
        assert res_cat["returncode"] == 126
        assert "forbidden" in res_cat["stderr"] or "Argument validation failed" in res_cat["stderr"]

        # 有效的允许命令成功执行，返回 returncode 0
        res_df = await py_shell.shell_exec("df", ["-h", "/tmp"])
        assert res_df["returncode"] == 0
        assert len(res_df["stdout"]) > 0

    asyncio.run(verify_shell_exec_parity())


# ---------------------------------------------------------------------------
# 标准 unittest 运行器兼容性
# ---------------------------------------------------------------------------

class TestCopilotIntegration(unittest.TestCase):
    def test_copilot_audit_cli_contract(self):
        test_copilot_audit_cli_contract()

    def test_copilot_security_rejection_parity(self):
        test_copilot_security_rejection_parity()


if __name__ == "__main__":
    unittest.main()
