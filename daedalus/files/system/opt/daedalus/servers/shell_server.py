#!/usr/bin/env python3
"""Daedalus OS Shell MCP 服务器（白名单与路径验证）。

通过显式的 argv 白名单和路径验证参数提供安全的命令执行。
通过 asyncio.create_subprocess_exec 直接执行子进程（绝不使用 shell=True），
具有净化后的环境变量和 30 秒执行超时。
"""

import asyncio
import json
import logging
import os
import time
from typing import Any, Dict, List, Optional
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("daedalus-shell")

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("daedalus-shell")

# 默认允许的诊断/只读命令
DEFAULT_ALLOW_COMMANDS = {
    "df",
    "ls",
    "cat",
    "pwd",
    "uname",
    "free",
    "ps",
    "uptime",
    "whoami",
    "ip",
    "arch",
    "hostname",
    "date",
    "ping",
    "systemctl",
}

# 允许通过环境变量覆盖或扩展允许的命令
_env_commands = os.environ.get("ALLOW_COMMANDS")
if _env_commands:
    ALLOW_COMMANDS = set(c.strip() for c in _env_commands.split(",") if c.strip())
else:
    ALLOW_COMMANDS = DEFAULT_ALLOW_COMMANDS

# 路径类参数允许的路径前缀
ALLOWED_PATH_PREFIXES = (
    "/home",
    "/var/log",
    "/tmp",
    "/proc",
    "/sys",
    "/etc/os-release",
    "/usr/lib/os-release",
    "/etc/fedora-release",
    "/etc/almalinux-release",
)

# 显式禁止的敏感路径
BLOCKED_PATHS = (
    "/etc/shadow",
    "/etc/gshadow",
    "/etc/sudoers",
    "/etc/sudoers.d",
    "/root",
)

# 净化后的执行环境（剥离敏感令牌和自定义变量）
CLEAN_ENV = {
    "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
    "LANG": "C.UTF-8",
}

TIMEOUT_SECONDS = 30.0


def is_path_like(arg: str) -> bool:
    """检查参数字符串是否类似于文件系统路径。"""
    if "\x00" in arg:
        return True
    if arg.startswith("/") or "/" in arg or arg in (".", "..") or arg.startswith(".."):
        return True
    return False


def validate_path(path_str: str) -> str:
    """验证路径参数是否在允许的目录内。

    参数：
        path_str: 要验证的路径字符串。

    返回：
        解析后的规范化路径字符串。

    异常：
        ValueError: 如果检测到空字节。
        PermissionError: 如果路径被阻止或超出允许的目录。
    """
    if "\x00" in path_str:
        raise ValueError("Null bytes are not allowed in path arguments.")

    resolved = os.path.realpath(os.path.abspath(path_str))

    # 检查显式阻止的路径
    for blocked in BLOCKED_PATHS:
        clean_blocked = blocked.rstrip("/")
        if resolved == clean_blocked or resolved.startswith(clean_blocked + "/"):
            raise PermissionError(f"Access to blocked path '{path_str}' ({resolved}) is forbidden.")

    # 检查允许的前缀
    allowed = False
    for prefix in ALLOWED_PATH_PREFIXES:
        clean_prefix = prefix.rstrip("/")
        if resolved == clean_prefix or resolved.startswith(clean_prefix + "/"):
            allowed = True
            break

    if not allowed:
        raise PermissionError(
            f"Path '{path_str}' (resolved: {resolved}) is outside allowed directories: "
            f"{', '.join(ALLOWED_PATH_PREFIXES)}"
        )

    return resolved


def validate_arg(arg: str) -> None:
    """检查参数中的空字节并验证嵌入的路径。"""
    if not isinstance(arg, str):
        raise ValueError(f"Argument must be a string, got {type(arg).__name__}")

    if "\x00" in arg:
        raise ValueError("Null bytes are not allowed in arguments.")

    if "=" in arg and (arg.startswith("-") or arg.startswith("--")):
        _, val = arg.split("=", 1)
        if is_path_like(val):
            validate_path(val)
    elif is_path_like(arg):
        validate_path(arg)


def validate_command(command: str) -> str:
    """根据 ALLOW_COMMANDS 白名单验证命令。"""
    if not command or not isinstance(command, str):
        raise ValueError("Command must be a non-empty string.")

    if "\x00" in command:
        raise ValueError("Null bytes are not allowed in command.")

    cmd_base = os.path.basename(command.strip())
    if cmd_base not in ALLOW_COMMANDS:
        raise PermissionError(f"Command '{command}' is not in ALLOW_COMMANDS allowlist.")

    if "/" in command:
        cmd_dir = os.path.dirname(os.path.realpath(command))
        allowed_bin_dirs = {"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
        if cmd_dir not in allowed_bin_dirs:
            raise PermissionError(f"Command path '{command}' is not in a valid system bin directory.")

    return cmd_base


def _record_audit(
    command: str,
    args: List[str],
    allowed: bool,
    returncode: Optional[int],
    error: Optional[str] = None,
) -> None:
    """记录结构化审计日志条目。"""
    entry = {
        "timestamp": time.time(),
        "tool": "shell_exec",
        "command": command,
        "args": args,
        "allowed": allowed,
        "returncode": returncode,
        "error": error,
    }
    logger.info("AUDIT: %s", json.dumps(entry))
    audit_path = "/var/log/daedalus/audit.jsonl"
    try:
        if os.path.isdir(os.path.dirname(audit_path)):
            with open(audit_path, "a", encoding="utf-8") as f:
                f.write(json.dumps(entry) + "\n")
    except Exception:
        pass


@mcp.tool()
async def shell_exec(command: str, args: List[str] = []) -> Dict[str, Any]:
    """安全地执行白名单中的命令及参数（只读 / 诊断）。

    命令被限制在 argv 白名单中，并且参数路径将针对允许的系统目录
    （/home、/var/log、/tmp、/proc、/sys、/etc/os-release）进行严格验证。
    执行过程在净化后的环境中隔离运行，并具有 30 秒超时限制。

    参数：
        command: 要执行的命令名称（必须在 ALLOW_COMMANDS 中）。
        args: 要传递的命令行参数列表。

    返回：
        包含 stdout、stderr、returncode 以及可选错误信息的字典。
    """
    if args is None:
        args = []

    # 1. 针对白名单验证命令
    try:
        cmd_to_run = validate_command(command)
    except Exception as e:
        _record_audit(command, args, allowed=False, returncode=126, error=str(e))
        return {
            "stdout": "",
            "stderr": f"Command validation failed: {e}",
            "returncode": 126,
            "error": str(e),
        }

    # 2. 验证所有参数
    try:
        for arg in args:
            validate_arg(arg)
    except Exception as e:
        _record_audit(command, args, allowed=False, returncode=126, error=str(e))
        return {
            "stdout": "",
            "stderr": f"Argument validation failed: {e}",
            "returncode": 126,
            "error": str(e),
        }

    # 3. 异步执行子进程（不使用 shell=True）
    try:
        proc = await asyncio.create_subprocess_exec(
            cmd_to_run,
            *args,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=CLEAN_ENV,
        )
        try:
            stdout_bytes, stderr_bytes = await asyncio.wait_for(
                proc.communicate(),
                timeout=TIMEOUT_SECONDS,
            )
            stdout_str = stdout_bytes.decode("utf-8", errors="replace")
            stderr_str = stderr_bytes.decode("utf-8", errors="replace")
            _record_audit(command, args, allowed=True, returncode=proc.returncode)
            return {
                "stdout": stdout_str,
                "stderr": stderr_str,
                "returncode": proc.returncode,
            }
        except asyncio.TimeoutError:
            try:
                proc.kill()
                await proc.wait()
            except Exception:
                pass
            err_msg = f"Command timed out after {TIMEOUT_SECONDS} seconds"
            _record_audit(command, args, allowed=True, returncode=124, error=err_msg)
            return {
                "stdout": "",
                "stderr": err_msg,
                "returncode": 124,
                "error": err_msg,
            }
    except Exception as e:
        _record_audit(command, args, allowed=True, returncode=1, error=str(e))
        return {
            "stdout": "",
            "stderr": f"Execution failed: {e}",
            "returncode": 1,
            "error": str(e),
        }


if __name__ == "__main__":
    mcp.run()
