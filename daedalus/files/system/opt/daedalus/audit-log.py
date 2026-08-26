#!/usr/bin/env python3
"""Daedalus OS 带有哈希链的仅追加审计日志记录。

提供向 /var/log/daedalus/audit.jsonl 写入哈希链审计日志的功能，并通过 fcntl 文件锁提供并发保护。
"""

import argparse
import datetime
import fcntl
import hashlib
import json
import os
import sys
from typing import Any, Dict, Optional, Union

AUDIT_LOG_PATH = os.environ.get("DAEDALUS_AUDIT_LOG_PATH", "/var/log/daedalus/audit.jsonl")
GENESIS_HASH = "0" * 64
POLICY_VERSION = "1.0"


def compute_entry_hash(
    timestamp: str,
    identity: str,
    tool: str,
    args: Any,
    outcome: str,
    prev_hash: str,
) -> str:
    """计算审计条目的 sha256 哈希值。"""
    # 确保 args 被规范化/一致地序列化
    if isinstance(args, str):
        args_str = args
    else:
        args_str = json.dumps(args, sort_keys=True, separators=(",", ":"))

    payload = f"{timestamp}{identity}{tool}{args_str}{outcome}{prev_hash}"
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def get_last_entry_hash(f) -> str:
    """读取审计日志文件的最后一行并返回其 entry_hash。
    
    如果文件为空或不包含有效的 entry_hash，则返回 GENESIS_HASH。
    """
    f.seek(0, os.SEEK_END)
    file_size = f.tell()
    if file_size == 0:
        return GENESIS_HASH

    # 从文件末尾向前读取以查找最后一个非空行
    buffer_size = 4096
    offset = file_size
    lines = []
    residual = ""

    while offset > 0 and len(lines) < 2:
        read_size = min(buffer_size, offset)
        offset -= read_size
        f.seek(offset)
        chunk = f.read(read_size)
        chunk_combined = chunk + residual
        split_lines = chunk_combined.splitlines()
        if len(split_lines) > 1:
            lines = split_lines
            break
        residual = chunk_combined

    if not lines and residual:
        lines = [residual]

    # 查找最后一个非空行
    for line in reversed(lines):
        line = line.strip()
        if line:
            try:
                data = json.loads(line)
                if isinstance(data, dict) and "entry_hash" in data:
                    return str(data["entry_hash"])
            except Exception:
                continue

    return GENESIS_HASH


def log_audit(
    identity: str,
    tool: str,
    args: Any = None,
    outcome: str = "success",
    policy_version: str = POLICY_VERSION,
    log_path: str = AUDIT_LOG_PATH,
) -> Dict[str, Any]:
    """向审计日志追加一条哈希链审计条目。

    参数：
        identity: 调用者 ID（例如 'agent-1', 'mcp-client'）。
        tool: 调用的工具名称。
        args: 参数字典、列表或基本类型。
        outcome: 结果状态（'success', 'denied', 'error'）。
        policy_version: 策略版本字符串（默认：'1.0'）。
        log_path: 目标审计日志文件路径。

    返回：
        写入的审计记录字典。
    """
    if args is None:
        args = {}

    log_dir = os.path.dirname(log_path)
    if log_dir and not os.path.exists(log_dir):
        try:
            os.makedirs(log_dir, exist_ok=True)
        except OSError:
            pass

    # 以 a+ 模式打开文件（读取 + 追加，不截断文件）
    with open(log_path, "a+", encoding="utf-8") as f:
        # 获取排他锁，防止哈希链计算过程中的竞态条件
        fcntl.flock(f.fileno(), fcntl.LOCK_EX)
        try:
            prev_hash = get_last_entry_hash(f)
            timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
            
            entry_hash = compute_entry_hash(
                timestamp=timestamp,
                identity=identity,
                tool=tool,
                args=args,
                outcome=outcome,
                prev_hash=prev_hash,
            )

            record = {
                "timestamp": timestamp,
                "identity": identity,
                "tool": tool,
                "args": args,
                "policy_version": policy_version,
                "outcome": outcome,
                "prev_hash": prev_hash,
                "entry_hash": entry_hash,
            }

            # 移动到末尾并追加条目
            f.seek(0, os.SEEK_END)
            f.write(json.dumps(record, sort_keys=True) + "\n")
            f.flush()
            return record
        finally:
            fcntl.flock(f.fileno(), fcntl.LOCK_UN)


def main() -> None:
    parser = argparse.ArgumentParser(description="Daedalus OS Audit Logging CLI")
    parser.add_argument("--identity", default="cli", help="Caller identity (default: cli)")
    parser.add_argument("--tool", required=True, help="Tool name called")
    parser.add_argument("--args", default="{}", help="Arguments as JSON string or raw text")
    parser.add_argument(
        "--outcome",
        choices=["success", "denied", "error"],
        default="success",
        help="Call outcome (default: success)",
    )
    parser.add_argument("--policy-version", default=POLICY_VERSION, help="Policy version")
    parser.add_argument("--log-path", default=AUDIT_LOG_PATH, help="Path to audit log file")

    parsed = parser.parse_args()

    try:
        args_val = json.loads(parsed.args)
    except Exception:
        args_val = parsed.args

    record = log_audit(
        identity=parsed.identity,
        tool=parsed.tool,
        args=args_val,
        outcome=parsed.outcome,
        policy_version=parsed.policy_version,
        log_path=parsed.log_path,
    )

    print(json.dumps(record, indent=2))


if __name__ == "__main__":
    main()
