#!/usr/bin/env bash

set -xeuo pipefail

# 确保安装先决条件
dnf install -y unzip curl

# 将 Deno 二进制文件安装到 /usr/local/bin/deno
# (2026-08-29 CI 修复: 新版 deno.land 安装器在装完二进制后会额外尝试从 jsr.io
#  拉取 shell 补全组件(@deno/installer-shell-setup), 该步在 CI 网络下可能失败
#  并以非零码退出导致整个构建炸掉——但它只是锦上添花, 非必需。
#  处理: 安装器失败不立即致命(允许失败继续), 由下方 deno --version 硬校验兜底:
#  真正的验收标准是二进制可用, 而非安装器退出码。)
mkdir -p /usr/local/bin
curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh -s -- -y --no-modify-path \
    || echo "NOTE: deno 安装器非零退出(多为 jsr.io shell-setup 网络抖动), 以下方版本校验为准"

# 硬校验: 二进制真实可用才算安装成功(这是唯一重要的验收)
if ! /usr/local/bin/deno --version; then
    echo "ERROR: deno 二进制安装失败(--version 校验不通过)" >&2
    exit 1
fi

# 创建 Deno 工作目录并确保持久化路径存在
mkdir -p /var/deno
mkdir -p /var/usrlocal/bin
