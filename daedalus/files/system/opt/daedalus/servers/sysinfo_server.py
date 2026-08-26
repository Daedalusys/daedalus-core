#!/usr/bin/env python3
"""Daedalus OS 系统信息 MCP 服务器（只读）。

提供安全的只读系统信息工具。
读取 os-release、硬件信息（/proc/cpuinfo、/proc/meminfo、disk_usage）
以及网络状态（/proc/net/dev 或 ip addr show）。
不暴露任何网络或系统配置的写入权限。
"""

import asyncio
import json
import os
import re
import shutil
from typing import Any, Dict
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("daedalus-sysinfo")


@mcp.tool()
def os_release() -> Dict[str, str]:
    """解析并返回操作系统发行版信息（只读）。

    读取 /etc/os-release 或 /usr/lib/os-release。

    返回：
        将发行版属性键映射到字符串值的字典。
    """
    candidates = ["/etc/os-release", "/usr/lib/os-release"]
    target_file = None
    for path in candidates:
        if os.path.isfile(path):
            target_file = path
            break

    if not target_file:
        return {"error": "Neither /etc/os-release nor /usr/lib/os-release found"}

    data = {}
    try:
        with open(target_file, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                k = k.strip()
                v = v.strip()
                if (v.startswith('"') and v.endswith('"')) or (v.startswith("'") and v.endswith("'")):
                    v = v[1:-1]
                data[k] = v
        return data
    except Exception as e:
        return {"error": f"Failed to read os-release: {e}"}


@mcp.tool()
def hardware_info() -> Dict[str, Any]:
    """返回 CPU、内存和磁盘使用信息（只读）。

    从 /proc/cpuinfo 读取 CPU 信息，从 /proc/meminfo 读取内存信息，
    并通过 shutil.disk_usage 读取根文件系统磁盘使用情况。

    返回：
        包含 cpu、memory 和 disk 统计信息的字典。
    """
    result: Dict[str, Any] = {
        "cpu": {},
        "memory": {},
        "disk": {}
    }

    # 1. 从 /proc/cpuinfo 获取 CPU 信息
    if os.path.isfile("/proc/cpuinfo"):
        try:
            model_name = None
            cpu_count = 0
            with open("/proc/cpuinfo", "r", encoding="utf-8", errors="replace") as f:
                for line in f:
                    if line.startswith("processor"):
                        cpu_count += 1
                    elif line.startswith("model name") and model_name is None:
                        parts = line.split(":", 1)
                        if len(parts) > 1:
                            model_name = parts[1].strip()
            result["cpu"] = {
                "model": model_name or "Unknown",
                "cores": cpu_count
            }
        except Exception as e:
            result["cpu"] = {"error": str(e)}
    else:
        result["cpu"] = {"error": "/proc/cpuinfo not available"}

    # 2. 从 /proc/meminfo 获取内存信息
    if os.path.isfile("/proc/meminfo"):
        try:
            mem = {}
            with open("/proc/meminfo", "r", encoding="utf-8", errors="replace") as f:
                for line in f:
                    parts = line.split(":", 1)
                    if len(parts) == 2:
                        key = parts[0].strip()
                        val = parts[1].strip()
                        if key in ("MemTotal", "MemFree", "MemAvailable", "SwapTotal", "SwapFree"):
                            mem[key] = val
            result["memory"] = mem
        except Exception as e:
            result["memory"] = {"error": str(e)}
    else:
        result["memory"] = {"error": "/proc/meminfo not available"}

    # 3. "/" 的磁盘使用情况
    try:
        usage = shutil.disk_usage("/")
        result["disk"] = {
            "path": "/",
            "total_bytes": usage.total,
            "used_bytes": usage.used,
            "free_bytes": usage.free,
            "total_gb": round(usage.total / (1024 ** 3), 2),
            "used_gb": round(usage.used / (1024 ** 3), 2),
            "free_gb": round(usage.free / (1024 ** 3), 2),
        }
    except Exception as e:
        result["disk"] = {"error": str(e)}

    return result


@mcp.tool()
async def network_status() -> Dict[str, Any]:
    """返回网络接口和地址状态（只读）。

    如果可用，通过 `ip -j addr show` 查询网络信息，
    回退到解析 `/proc/net/dev`。

    返回：
        包含网络接口统计信息和详细信息的字典。
    """
    # 1. 尝试通过 asyncio 子进程安全使用 `ip -j addr show`
    try:
        proc = await asyncio.create_subprocess_exec(
            "ip", "-j", "addr", "show",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await proc.communicate()
        if proc.returncode == 0 and stdout:
            try:
                parsed = json.loads(stdout.decode("utf-8"))
                return {"interfaces": parsed}
            except json.JSONDecodeError:
                pass
    except Exception:
        # 回退到 ip addr show 纯文本或 /proc/net/dev
        pass

    # 2. 尝试标准 `ip addr show` 纯文本
    try:
        proc = await asyncio.create_subprocess_exec(
            "ip", "addr", "show",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await proc.communicate()
        if proc.returncode == 0 and stdout:
            return {"raw_output": stdout.decode("utf-8", errors="replace").strip()}
    except Exception:
        pass

    # 3. 回退到 /proc/net/dev
    if os.path.isfile("/proc/net/dev"):
        try:
            interfaces = {}
            with open("/proc/net/dev", "r", encoding="utf-8", errors="replace") as f:
                lines = f.readlines()
                # 跳过前两行标题
                for line in lines[2:]:
                    line = line.strip()
                    if not line:
                        continue
                    parts = line.split(":", 1)
                    if len(parts) == 2:
                        iface = parts[0].strip()
                        stats = parts[1].split()
                        interfaces[iface] = {
                            "rx_bytes": int(stats[0]) if len(stats) > 0 else 0,
                            "tx_bytes": int(stats[8]) if len(stats) > 8 else 0,
                        }
            return {"interfaces": interfaces}
        except Exception as e:
            return {"error": f"Failed reading /proc/net/dev: {e}"}

    return {"error": "Unable to determine network status"}


if __name__ == "__main__":
    mcp.run()
