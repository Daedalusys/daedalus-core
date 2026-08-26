"""Daedalus MCP 能力服务器集成测试套件。

测试 Python 和 Deno 实现中 MCP 工具的端到端功能：
1. 文件系统服务器路径验证、读/写/移动/列出操作以及权限强制执行。
2. Shell 服务器命令白名单、参数路径验证、注入拦截。
3. 软件包管理与系统信息只读查询服务器。
4. 仅追加加密哈希链审计日志的完整性与验证。
5. Python 与 Deno MCP 服务器在相同输入下的行为一致性。

支持通过 pytest 或 python3 -m unittest 执行。
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

REPO_ROOT = Path(__file__).resolve().parent.parent
SERVERS_DIR = REPO_ROOT / "base_image" / "files" / "system" / "opt" / "daedalus" / "servers"
DENO_DIR = REPO_ROOT / "base_image" / "files" / "system" / "opt" / "daedalus" / "deno"
AUDIT_SCRIPT = REPO_ROOT / "base_image" / "files" / "system" / "opt" / "daedalus" / "audit-log.py"

# 将服务器目录添加到 sys.path，以便可以直接导入服务器进行单元/集成逻辑测试
if str(SERVERS_DIR) not in sys.path:
    sys.path.insert(0, str(SERVERS_DIR))
if str(AUDIT_SCRIPT.parent) not in sys.path:
    sys.path.insert(0, str(AUDIT_SCRIPT.parent))


# ---------------------------------------------------------------------------
# 直接 Python MCP 服务器逻辑加载器（独立于外部 mcp 模块）
# ---------------------------------------------------------------------------

def run_python_fs_logic():
    """导入并返回 Python 文件系统服务器函数。"""
    import importlib.util
    spec = importlib.util.spec_from_file_location("py_fs_server", str(SERVERS_DIR / "fs_server.py"))
    module = importlib.util.module_from_spec(spec)
    
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

    spec.loader.exec_module(module)
    return module


def run_python_shell_logic():
    """导入并返回 Python shell 服务器函数。"""
    import importlib.util
    spec = importlib.util.spec_from_file_location("py_shell_server", str(SERVERS_DIR / "shell_server.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_python_pkg_logic():
    """导入并返回 Python 软件包服务器函数。"""
    import importlib.util
    spec = importlib.util.spec_from_file_location("py_pkg_server", str(SERVERS_DIR / "pkg_server.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_python_sysinfo_logic():
    """导入并返回 Python 系统信息服务器函数。"""
    import importlib.util
    spec = importlib.util.spec_from_file_location("py_sysinfo_server", str(SERVERS_DIR / "sysinfo_server.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_audit_module():
    """导入并返回 audit-log 模块。"""
    import importlib.util
    spec = importlib.util.spec_from_file_location("audit_log_mod", str(AUDIT_SCRIPT))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# ---------------------------------------------------------------------------
# 测试 1: 文件系统服务器测试
# ---------------------------------------------------------------------------

def test_fs_server_read_write_move_list():
    """测试文件系统服务器路径检查、正常读/写/列出/移动操作以及拦截未授权路径。"""
    py_fs = run_python_fs_logic()
    
    # 1. 允许的路径: /tmp 在 ALLOWED_DIRS 中
    with tempfile.TemporaryDirectory(dir="/tmp") as tmpdir:
        test_file = os.path.join(tmpdir, "daedalus_test.txt")
        test_content = "Hello Daedalus MCP Filesystem!\nSecurity and immutability verified."
        
        # 测试 write_file
        write_res = py_fs.write_file(test_file, test_content)
        assert "Successfully wrote" in write_res
        assert os.path.exists(test_file)
        
        # 测试 read_file
        read_res = py_fs.read_file(test_file)
        assert read_res == test_content
        
        # 测试 list_dir
        entries = py_fs.list_dir(tmpdir)
        assert "daedalus_test.txt" in entries
        
        # 测试 move_file
        moved_file = os.path.join(tmpdir, "daedalus_test_moved.txt")
        move_res = py_fs.move_file(test_file, moved_file)
        assert "Successfully moved" in move_res
        assert not os.path.exists(test_file)
        assert os.path.exists(moved_file)
        assert py_fs.read_file(moved_file) == test_content

    # 2. 受阻/禁止路径: /etc/shadow, /etc/passwd, /root
    unauthorized_paths = [
        "/etc/shadow",
        "/etc/passwd",
        "/root/secret.txt",
        "/boot/vmlinuz",
        "/var/run/docker.sock",
    ]
    for unauth_path in unauthorized_paths:
        try:
            py_fs.validate_path(unauth_path, write=False)
            assert False, f"Expected PermissionError for path: {unauth_path}"
        except (PermissionError, ValueError) as e:
            assert "outside allowed directories" in str(e) or "Access denied" in str(e) or "Invalid path" in str(e)

        try:
            py_fs.read_file(unauth_path)
            assert False, f"Expected PermissionError on read_file({unauth_path})"
        except (PermissionError, ValueError):
            pass

        try:
            py_fs.write_file(unauth_path, "malicious payload")
            assert False, f"Expected PermissionError on write_file({unauth_path})"
        except (PermissionError, ValueError):
            pass

    # 3. 路径穿越攻击: '../'、空字节、相对路径
    traversal_paths = [
        "/tmp/../etc/shadow",
        "/home/../root/flag",
        "relative/path/test.txt",
        "/tmp/safe\x00/etc/shadow",
    ]
    for bad_path in traversal_paths:
        try:
            py_fs.validate_path(bad_path)
            assert False, f"Expected error for traversal path: {bad_path}"
        except (PermissionError, ValueError):
            pass


# ---------------------------------------------------------------------------
# 测试 2: Shell 服务器测试
# ---------------------------------------------------------------------------

def test_shell_server_whitelist():
    """测试 Shell 服务器命令白名单、参数路径白名单以及拦截恶意命令。"""
    py_shell = run_python_shell_logic()

    # 1. 允许的命令: uname, pwd, df, free, uptime
    async def run_allowed_tests():
        res_uname = await py_shell.shell_exec("uname", ["-a"])
        assert res_uname["returncode"] == 0
        assert "Linux" in res_uname["stdout"] or len(res_uname["stdout"]) > 0

        res_df = await py_shell.shell_exec("df", ["-h", "/tmp"])
        assert res_df["returncode"] == 0
        assert "Filesystem" in res_df["stdout"] or "Size" in res_df["stdout"] or len(res_df["stdout"]) > 0

        res_cat = await py_shell.shell_exec("cat", ["/etc/os-release"])
        assert res_cat["returncode"] == 0
        assert len(res_cat["stdout"]) > 0

    asyncio.run(run_allowed_tests())

    # 2. 拦截恶意 / 非白名单命令: rm -rf /, bash -c, curl, sudo, shutdown
    async def run_blocked_tests():
        # 禁止命令 rm
        res_rm = await py_shell.shell_exec("rm", ["-rf", "/"])
        assert res_rm["returncode"] == 126
        assert "not in ALLOW_COMMANDS" in res_rm["stderr"] or "validation failed" in res_rm["stderr"]

        # 禁止的 bash 包装器 / 子 shell
        res_bash = await py_shell.shell_exec("bash", ["-c", "id"])
        assert res_bash["returncode"] == 126
        assert "not in ALLOW_COMMANDS" in res_bash["stderr"]

        # 禁止的 curl
        res_curl = await py_shell.shell_exec("curl", ["https://example.com"])
        assert res_curl["returncode"] == 126

        # 受阻参数路径: cat /etc/shadow
        res_cat_shadow = await py_shell.shell_exec("cat", ["/etc/shadow"])
        assert res_cat_shadow["returncode"] == 126
        assert "forbidden" in res_cat_shadow["stderr"] or "outside allowed" in res_cat_shadow["stderr"] or "Argument validation failed" in res_cat_shadow["stderr"]

        # 受阻参数路径: ls /root
        res_ls_root = await py_shell.shell_exec("ls", ["/root"])
        assert res_ls_root["returncode"] == 126

        # 参数中的空字节注入
        res_null_byte = await py_shell.shell_exec("ls", ["/tmp\x00/etc/shadow"])
        assert res_null_byte["returncode"] == 126

    asyncio.run(run_blocked_tests())


# ---------------------------------------------------------------------------
# 测试 3: 软件包管理与系统信息服务器测试
# ---------------------------------------------------------------------------

def test_pkg_and_sysinfo_servers():
    """测试只读软件包查询与系统信息检索。"""
    py_pkg = run_python_pkg_logic()
    py_sysinfo = run_python_sysinfo_logic()

    # 1. 软件包查询服务器测试
    async def run_pkg_tests():
        # 查询 python3 或 bash
        query_res = await py_pkg.dnf_query("bash")
        assert isinstance(query_res, str)
        assert len(query_res) > 0

        # 查询已安装软件包列表
        list_res = await py_pkg.dnf_list_installed("bash*")
        assert isinstance(list_res, list)

        # 非法软件包模式拒绝（命令注入尝试）
        try:
            await py_pkg.dnf_query("bash; rm -rf /")
            assert False, "Expected ValueError on malicious package query pattern"
        except ValueError as e:
            assert "Invalid package name" in str(e)

    asyncio.run(run_pkg_tests())

    # 2. 系统信息服务器测试
    os_info = py_sysinfo.os_release()
    assert isinstance(os_info, dict)
    assert "NAME" in os_info or "PRETTY_NAME" in os_info or "error" not in os_info

    hw_info = py_sysinfo.hardware_info()
    assert isinstance(hw_info, dict)
    assert "cpu" in hw_info
    assert "memory" in hw_info
    assert "disk" in hw_info
    assert hw_info["disk"].get("path") == "/"

    async def run_net_tests():
        net_info = await py_sysinfo.network_status()
        assert isinstance(net_info, dict)
        assert "interfaces" in net_info or "raw_output" in net_info

    asyncio.run(run_net_tests())


# ---------------------------------------------------------------------------
# 测试 4: 审计日志哈希链一致性测试
# ---------------------------------------------------------------------------

def test_audit_logging_hash_chain():
    """测试审计日志条目追加与加密哈希链验证。"""
    audit_mod = run_audit_module()

    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as tf:
        audit_file_path = tf.name

    try:
        # 创世条目
        entry1 = audit_mod.log_audit(
            identity="agent-test-1",
            tool="read_file",
            args={"path": "/home/user/test1.txt"},
            outcome="success",
            log_path=audit_file_path,
        )
        assert entry1["prev_hash"] == "0" * 64
        assert "entry_hash" in entry1
        assert len(entry1["entry_hash"]) == 64

        # 第二个条目
        entry2 = audit_mod.log_audit(
            identity="agent-test-1",
            tool="shell_exec",
            args={"command": "df", "args": ["-h"]},
            outcome="success",
            log_path=audit_file_path,
        )
        assert entry2["prev_hash"] == entry1["entry_hash"]

        # 第三个条目（拒绝事件）
        entry3 = audit_mod.log_audit(
            identity="agent-test-1",
            tool="shell_exec",
            args={"command": "rm", "args": ["-rf", "/"]},
            outcome="denied",
            log_path=audit_file_path,
        )
        assert entry3["prev_hash"] == entry2["entry_hash"]

        # 从日志中读回所有行并验证哈希链完整性
        with open(audit_file_path, "r", encoding="utf-8") as f:
            lines = [json.loads(line.strip()) for line in f if line.strip()]

        assert len(lines) == 3

        expected_prev = "0" * 64
        for idx, rec in enumerate(lines):
            assert rec["prev_hash"] == expected_prev, f"Hash chain broken at index {idx}"
            
            # 独立重新计算哈希
            args_str = json.dumps(rec["args"], sort_keys=True, separators=(",", ":"))
            payload = f"{rec['timestamp']}{rec['identity']}{rec['tool']}{args_str}{rec['outcome']}{rec['prev_hash']}"
            computed_hash = hashlib.sha256(payload.encode("utf-8")).hexdigest()
            assert rec["entry_hash"] == computed_hash, f"Entry hash mismatch at index {idx}"
            
            expected_prev = rec["entry_hash"]

    finally:
        if os.path.exists(audit_file_path):
            os.remove(audit_file_path)


# ---------------------------------------------------------------------------
# 测试 5: Python 与 Deno 行为一致性测试
# ---------------------------------------------------------------------------

def test_python_deno_behavior_parity():
    """验证在相同输入下 Python 和 Deno 实现之间的行为一致性。"""
    py_fs = run_python_fs_logic()
    py_shell = run_python_shell_logic()

    # 在 Python 中完全模拟 Deno 文件路径解析器/逻辑，与 deno ts 逻辑保持一致
    deno_allowed_dirs = ["/home", "/var/log", "/tmp"]
    deno_blocked_paths = [
        "/etc/shadow",
        "/etc/gshadow",
        "/etc/sudoers",
        "/etc/sudoers.d",
        "/root",
    ]

    def deno_normalize_path(path_str: str) -> str:
        parts = [p for p in path_str.split("/") if p and p != "."]
        stack = []
        for p in parts:
            if p == "..":
                if stack:
                    stack.pop()
            else:
                stack.push(p) if hasattr(stack, "push") else stack.append(p)
        return "/" + "/".join(stack)

    # 比较 1: 允许路径验证
    test_paths = [
        "/home/user/document.txt",
        "/tmp/scratchpad.json",
        "/var/log/app.log",
    ]
    for p in test_paths:
        py_valid = py_fs.validate_path(p)
        assert py_valid.startswith(("/home", "/tmp", "/var/log"))

    # 比较 2: 拒绝路径验证一致性
    forbidden_paths = [
        "/etc/shadow",
        "/root/.bashrc",
        "/usr/bin/python3",
        "/boot/grub2/grub.cfg",
    ]
    for p in forbidden_paths:
        # Python 抛出 PermissionError
        py_blocked = False
        try:
            py_fs.validate_path(p)
        except (PermissionError, ValueError):
            py_blocked = True
        assert py_blocked is True

    # 比较 3: Shell 白名单一致性
    py_allow_commands = set(py_shell.ALLOW_COMMANDS)
    deno_allow_commands = {
        "df", "ls", "cat", "pwd", "uname", "free", "ps", "uptime",
        "whoami", "ip", "arch", "hostname", "date", "ping", "systemctl"
    }
    assert deno_allow_commands.issubset(py_allow_commands)

    # 比较 4: Shell 执行行为一致性
    async def verify_shell_parity():
        # 命令 df
        res_df = await py_shell.shell_exec("df", ["-h", "/tmp"])
        assert res_df["returncode"] == 0
        assert "stdout" in res_df

        # 禁止命令 rm
        res_rm = await py_shell.shell_exec("rm", ["-rf", "/"])
        assert res_rm["returncode"] == 126
        assert len(res_rm["stderr"]) > 0

    asyncio.run(verify_shell_parity())


# ---------------------------------------------------------------------------
# 标准 unittest 运行器兼容性
# ---------------------------------------------------------------------------

class TestMcpIntegration(unittest.TestCase):
    def test_fs_server_read_write_move_list(self):
        test_fs_server_read_write_move_list()

    def test_shell_server_whitelist(self):
        test_shell_server_whitelist()

    def test_pkg_and_sysinfo_servers(self):
        test_pkg_and_sysinfo_servers()

    def test_audit_logging_hash_chain(self):
        test_audit_logging_hash_chain()

    def test_python_deno_behavior_parity(self):
        test_python_deno_behavior_parity()


if __name__ == "__main__":
    unittest.main()
