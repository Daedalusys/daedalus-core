#!/usr/bin/env bash

set -xeuo pipefail

# 确保 /opt/daedalus 目录结构和权限
if [ -d /opt/daedalus ]; then
    chmod -R 0755 /opt/daedalus
fi

# 确保 Python 虚拟环境和依赖项
mkdir -p /opt/daedalus
if [ ! -d /opt/daedalus/venv ]; then
    python3 -m venv /opt/daedalus/venv
fi

/opt/daedalus/venv/bin/pip install --upgrade pip
/opt/daedalus/venv/bin/pip install --upgrade mcp fastmcp

# 确保服务器和脚本具有正确的权限
if [ -d /opt/daedalus/servers ]; then
    chmod +x /opt/daedalus/servers/*.py || true
fi
if [ -f /opt/daedalus/audit-log.py ]; then
    chmod +x /opt/daedalus/audit-log.py
fi

# 启用 daedalus 基础服务（单次初始化服务）
systemctl enable daedalus-audit.service || true
systemctl enable daedalus-env.service || true
