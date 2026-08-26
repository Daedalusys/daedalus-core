#!/usr/bin/env python3
"""Daedalus OS 软件包管理 MCP 服务器（只读）。

使用 RPM/DNF 提供安全的只读软件包查询功能。
不暴露任何安装、更新或删除操作。
"""

import asyncio
import re
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("daedalus-pkg")

# 允许的软件包名称模式，以防止命令行参数注入
PACKAGE_PATTERN = re.compile(r"^[a-zA-Z0-9_\-\.\*\+\:]+$")


def _sanitize_query(pkg: str) -> str:
    pkg = pkg.strip()
    if not pkg:
        raise ValueError("Package name/pattern cannot be empty.")
    if not PACKAGE_PATTERN.match(pkg):
        raise ValueError(f"Invalid package name or pattern: {pkg}")
    return pkg


@mcp.tool()
async def dnf_query(name: str) -> str:
    """查询给定软件包名称的软件包信息（只读）。

    使用 `rpm -q --info <name>` 查询软件包详细信息，
    如果未在本地安装，则回退到 `dnf repoquery --info <name>`。

    参数：
        name: 要查询的软件包名称（例如 'python3', 'bash'）。

    返回：
        软件包信息字符串或错误消息。
    """
    safe_name = _sanitize_query(name)

    # 1. 优先尝试查询本地已安装的 rpm
    try:
        proc = await asyncio.create_subprocess_exec(
            "rpm", "-q", "--info", safe_name,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await proc.communicate()
        if proc.returncode == 0 and stdout:
            return stdout.decode("utf-8", errors="replace").strip()
    except Exception as e:
        return f"Error executing rpm query: {e}"

    # 2. 如果未安装或 rpm 查询失败，尝试通过 dnf repoquery 获取软件源仓库信息
    try:
        proc = await asyncio.create_subprocess_exec(
            "dnf", "repoquery", "--info", safe_name,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await proc.communicate()
        if proc.returncode == 0 and stdout:
            return stdout.decode("utf-8", errors="replace").strip()
        err_msg = stderr.decode("utf-8", errors="replace").strip()
        return f"Package '{safe_name}' not found locally or in repositories. {err_msg}".strip()
    except Exception as e:
        return f"Error executing dnf repoquery: {e}"


@mcp.tool()
async def dnf_list_installed(pattern: str = "*") -> list[str]:
    """列出匹配模式的已安装软件包（只读）。

    使用 `rpm -qa <pattern>` 查询已安装的软件包。

    参数：
        pattern: 用于匹配已安装软件包的模式（默认：'*'）。

    返回：
        匹配的已安装软件包名称/版本列表。
    """
    safe_pattern = _sanitize_query(pattern)

    try:
        proc = await asyncio.create_subprocess_exec(
            "rpm", "-qa", safe_pattern,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await proc.communicate()
        if proc.returncode != 0:
            err = stderr.decode("utf-8", errors="replace").strip()
            return [f"Error listing packages: {err}"]

        output = stdout.decode("utf-8", errors="replace").strip()
        if not output:
            return []
        lines = [line.strip() for line in output.splitlines() if line.strip()]
        return sorted(lines)
    except Exception as e:
        return [f"Error executing rpm -qa: {e}"]


if __name__ == "__main__":
    mcp.run()
