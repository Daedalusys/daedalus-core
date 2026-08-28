#!/usr/bin/env bash

set -xeuo pipefail

# MCP 能力服务器收尾步骤（Phase A 重构后）。
#
# 原职责（创建 Python 虚拟环境、pip 安装 mcp/fastmcp、chmod 参考实现脚本）
# 已全部退役：能力服务器与审计/宿主/打包器由 Go 静态二进制提供。
# 安装布局（`just plugin-pack` 接线，见 task 21）：
#   - /usr/local/bin/daedalus-{host,audit,shell}：宿主 + copilot 运行时依赖
#   - /opt/daedalus/plugins/daedalus.<cap>/bin/：4 个能力服务器（systemd ExecStart 指向）
# 不再需要任何 Python 依赖链；此处仅做非致命的存在性提示，不阻断构建。

# 确保 /opt/daedalus 目录结构和权限（插件安装态 plugins/ 位于其下）
if [ -d /opt/daedalus ]; then
    chmod -R 0755 /opt/daedalus
fi

# Go 二进制存在性提示（信息性检查，缺失不失败）
for bin in daedalus-host daedalus-audit daedalus-shell; do
    if [ ! -x "/usr/local/bin/${bin}" ]; then
        echo "NOTE: /usr/local/bin/${bin} 尚未安装（由 just plugin-pack 提供）"
    fi
done
for cap in fs shell pkg sysinfo; do
    if [ ! -x "/opt/daedalus/plugins/daedalus.${cap}/bin/daedalus-${cap}" ]; then
        echo "NOTE: /opt/daedalus/plugins/daedalus.${cap}/bin/daedalus-${cap} 尚未安装（由 just plugin-pack 提供）"
    fi
done

# 启用 daedalus 基础服务（单次初始化服务）
systemctl enable daedalus-audit.service || true
systemctl enable daedalus-env.service || true
