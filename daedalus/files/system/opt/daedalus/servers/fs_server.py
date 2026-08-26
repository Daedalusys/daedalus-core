#!/usr/bin/env python3
"""
daedalus-fs: 模型上下文协议 (MCP) 文件系统服务器
具有严格路径验证的允许目录模型。
仅限于 /home、/var/log、/tmp。
"""

from __future__ import annotations

import os
import shutil
from pathlib import Path
from typing import List

from mcp.server.fastmcp import FastMCP
from mcp.types import ToolAnnotations

# 定义允许的目录白名单
ALLOWED_DIRS: List[str] = ["/home", "/var/log", "/tmp"]

# 初始化 FastMCP 服务器
mcp = FastMCP("daedalus-fs")


def validate_path(path_str: str, write: bool = False) -> str:
    """
    验证给定路径是否安全并包含在 ALLOWED_DIRS 中。

    规则：
    1. 拒绝空字节。
    2. 拒绝相对路径（必须以 '/' 开头）。
    3. 解析符号链接并使用 realpath 规范化路径。
    4. 确保规范化后的路径以允许的目录之一开头。

    参数：
        path_str: 要验证的目标路径。
        write: 是否为写入/修改操作。

    返回：
        规范化后的绝对路径字符串。

    异常：
        ValueError: 如果路径包含空字节或者是相对路径。
        PermissionError: 如果路径解析到允许的目录白名单之外。
    """
    if not path_str or not isinstance(path_str, str):
        raise ValueError("Path must be a non-empty string.")

    if "\0" in path_str:
        raise ValueError("Invalid path: null bytes are forbidden.")

    if not path_str.startswith("/"):
        raise ValueError(f"Invalid path '{path_str}': only absolute paths are permitted.")

    # 规范化 realpath，解析所有符号链接、/./、/../
    canonical_path = os.path.realpath(path_str)

    # 检查允许的前缀
    # 确保精确匹配或带有 '/' 边界的子路径，以避免如 /home2 的情况
    is_allowed = False
    for allowed in ALLOWED_DIRS:
        allowed_canonical = os.path.realpath(allowed)
        if canonical_path == allowed_canonical or canonical_path.startswith(allowed_canonical.rstrip("/") + "/"):
            is_allowed = True
            break

    if not is_allowed:
        raise PermissionError(
            f"Access denied: path '{path_str}' (resolved: '{canonical_path}') is outside allowed directories ({', '.join(ALLOWED_DIRS)})."
        )

    return canonical_path


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=True,
        destructiveHint=False,
        idempotentHint=True,
        openWorldHint=False,
    )
)
def read_file(path: str) -> str:
    """
    从允许的目录（/home、/var/log、/tmp）内的文件中读取文本内容。

    参数：
        path: 文件的绝对路径。

    返回：
        文本形式的文件内容。
    """
    safe_path = validate_path(path, write=False)

    if not os.path.exists(safe_path):
        raise FileNotFoundError(f"File not found: {path}")

    if not os.path.isfile(safe_path):
        raise IsADirectoryError(f"Target is a directory, not a file: {path}")

    with open(safe_path, "r", encoding="utf-8", errors="replace") as f:
        return f.read()


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=False,
        destructiveHint=True,
        idempotentHint=True,
        openWorldHint=False,
    )
)
def write_file(path: str, content: str) -> str:
    """
    将文本内容写入或覆盖到允许目录（/home、/var/log、/tmp）内的文件中。
    如果父目录不存在，则自动创建父目录。

    参数：
        path: 目标文件的绝对路径。
        content: 要写入文件的文本内容。

    返回：
        确认消息。
    """
    safe_path = validate_path(path, write=True)

    if os.path.isdir(safe_path):
        raise IsADirectoryError(f"Target is a directory: {path}")

    parent_dir = os.path.dirname(safe_path)
    if parent_dir:
        os.makedirs(parent_dir, exist_ok=True)

    with open(safe_path, "w", encoding="utf-8") as f:
        f.write(content)

    return f"Successfully wrote {len(content)} characters to {path}"


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=True,
        destructiveHint=False,
        idempotentHint=True,
        openWorldHint=False,
    )
)
def list_dir(path: str) -> List[str]:
    """
    列出允许目录（/home、/var/log、/tmp）内目录的内容。

    参数：
        path: 目录的绝对路径。

    返回：
        目录项名称列表。
    """
    safe_path = validate_path(path, write=False)

    if not os.path.exists(safe_path):
        raise FileNotFoundError(f"Directory not found: {path}")

    if not os.path.isdir(safe_path):
        raise NotADirectoryError(f"Target is not a directory: {path}")

    return sorted(os.listdir(safe_path))


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=False,
        destructiveHint=True,
        idempotentHint=False,
        openWorldHint=False,
    )
)
def move_file(src: str, dst: str) -> str:
    """
    在允许的目录（/home、/var/log、/tmp）内移动或重命名文件或目录。
    源路径和目标路径都必须严格在允许的白名单内。

    参数：
        src: 源文件或目录的绝对路径。
        dst: 目标文件或目录的绝对路径。

    返回：
        确认消息。
    """
    safe_src = validate_path(src, write=True)
    safe_dst = validate_path(dst, write=True)

    if not os.path.exists(safe_src):
        raise FileNotFoundError(f"Source path not found: {src}")

    dst_parent = os.path.dirname(safe_dst)
    if dst_parent:
        os.makedirs(dst_parent, exist_ok=True)

    shutil.move(safe_src, safe_dst)
    return f"Successfully moved {src} to {dst}"


if __name__ == "__main__":
    mcp.run()
