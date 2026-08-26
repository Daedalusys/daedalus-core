#!/usr/bin/env bash

set -xeuo pipefail

# 确保安装先决条件
dnf install -y unzip curl

# 将 Deno 二进制文件安装到 /usr/local/bin/deno
mkdir -p /usr/local/bin
curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh -s -- -y --no-modify-path

# 确保可执行权限
chmod +x /usr/local/bin/deno

# 验证 Deno 安装
/usr/local/bin/deno --version

# 创建 Deno 工作目录并确保持久化路径存在
mkdir -p /var/deno
mkdir -p /var/usrlocal/bin
