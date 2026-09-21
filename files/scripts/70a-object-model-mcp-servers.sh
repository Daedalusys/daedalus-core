#!/usr/bin/env bash

set -xeuo pipefail

# 对象模型能力服务器收尾步骤（aios 计划 todo 11，镜像 70-daedalus-mcp-servers.sh 的结构）。
#
# 职责边界（与 70 完全同型，勿在此发明新机制）：
#   - 安装/打包落位的唯一实现是 `just plugin-pack`（能力循环把
#     daedalus-service 打进 plugins/daedalus.service/bin/，并按 task-21 形态
#     同步一份到 /usr/local/bin/；本脚本不做任何 cp/install——70 对
#     host/audit/shell 与 4 能力同样只 chmod+提示，复制职责从不在编号脚本里）；
#   - 本脚本只做两件事：① 修正新落位的权限位（0755），② 非致命的存在性提示。
#
# 步骤排序证明（base_image/files/scripts/build.sh 第 12 行：
# find -maxdepth 1 -iname "*-*.sh" | sort --sort=human-numeric 自动发现，
# Containerfile 零编辑即接入）：
#   - 镜像构建上下文（almalinux-bootc 基础镜像 LANG 未设 → C/POSIX 排序），
#     数字前缀同为 70 时按整行字节回退比较：'-'(0x2D) < 'a'(0x61)，
#     故 70-daedalus-mcp-servers.sh → 70a-object-model-mcp-servers.sh → 75-…，
#     70a 恰好排在 70 与 75 之间（w2-t11.log 留档 LC_ALL=C 实测）；
#   - 开发机 zh_CN.UTF-8 排序表对连字符按主权忽略，70-/70a 并列时次序翻转
#     （亦留档实测）。语义无害：70 与 70a 均为幂等 chmod+提示，彼此无数据
#     依赖，任一先后都收敛到同一终态。
#
# 不启用单元：能力型单元（fs/shell/pkg/sysinfo/service）在全部构建步骤中
# 从不 systemctl enable——70 仅 enable 两个一次性初始化服务
# （daedalus-audit/daedalus-env），本步骤无初始化服务可 enable，故无对应段。
#
# 开发机限制：本脚本按镜像构建（root）上下文设计，宿主直接 `bash` 运行会因
#             /opt、/usr/local 无写权限而失败（EACCES），与兄弟脚本（63/65/70）
#             的上下文约定一致；dev 等价断言走 `just plugin-pack` + 暂存演练。

# 确保 daedalus.service 插件安装态目录与二进制的权限（镜像 70 对 /opt/daedalus 的 chmod 语义，
# 但只圈定本步骤新增落位，不重复整树 chmod——70 已覆盖 plugins/ 其余部分）
if [ -d /opt/daedalus/plugins/daedalus.service ]; then
    chmod -R 0755 /opt/daedalus/plugins/daedalus.service
fi

# /usr/local/bin 副本（task-21 双落位形态，供镜像内字面路径启动的 QA 链路消费）
if [ -f /usr/local/bin/daedalus-service ]; then
    chmod 0755 /usr/local/bin/daedalus-service
fi

# Go 二进制存在性提示（信息性检查，缺失不失败——安装由 just plugin-pack 在构建镜像前完成）
if [ ! -x "/usr/local/bin/daedalus-service" ]; then
    echo "NOTE: /usr/local/bin/daedalus-service 尚未安装（由 just plugin-pack 提供）"
fi
if [ ! -x "/opt/daedalus/plugins/daedalus.service/bin/daedalus-service" ]; then
    echo "NOTE: /opt/daedalus/plugins/daedalus.service/bin/daedalus-service 尚未安装（由 just plugin-pack 提供）"
fi
