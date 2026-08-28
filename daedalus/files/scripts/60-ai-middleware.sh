#!/usr/bin/env bash

set -xeuo pipefail

# AI 中间件安装步骤（Phase A 重构后）。
#
# 原职责（安装 Python3 运行时、创建 /opt/daedalus 虚拟环境并 pip 安装 MCP SDK）
# 已随 Go 能力服务器的落地而退役：四个能力服务器（fs/shell/pkg/sysinfo）与
# 审计/宿主/打包器均由 daedalus/core 的 Go 工作区构建为静态链接原生二进制
# （`just plugin-pack` 打包插件并安装到 /usr/local/bin 与 /opt/daedalus/plugins，见 task 21）。
#
# 本脚本仅保留目录初始化职责：确保 /opt/daedalus 运行时根存在
# （插件安装态 plugins/ 等布局都挂载在该前缀下；copilot 源码态见 daedalus/plugin/copilot/）。
mkdir -p /opt/daedalus
