#!/usr/bin/env bash

set -xeuo pipefail

# 安装 Python 3.12 运行时和开发软件包
dnf install -y \
    python3 \
    python3-pip \
    python3-devel

# 创建持久化的 AI 中间件虚拟环境
mkdir -p /opt/daedalus
python3 -m venv /opt/daedalus/venv

# 升级 pip 并安装标准模型上下文协议 (MCP) SDK
/opt/daedalus/venv/bin/pip install --upgrade pip
/opt/daedalus/venv/bin/pip install mcp
