#!/usr/bin/env bash

set -xeuo pipefail

# 对象模型状态根目录预建（构建步骤 63，排在 60-ai-middleware 与 65-ai-safety 之间）。
#
# 目的：为 C4 状态记忆（state memory）与事务（tx）日志建立系统级根目录
#       /var/lib/daedalus 的镜像内预建落位。
#
# 分工说明：
#   - todo 16（dirs 链）：Go 侧统一解析状态/日志目录路径约定；
#   - todo 9（StateDirectory）：systemd 单元（如 daedalus-fs.service）声明
#     StateDirectory=daedalus，经 DynamicUser 沙箱时由 systemd 提供
#     /var/lib/daedalus 的 bind-mount 私有视图（每次启动全新目录）；
#   - 本脚本：镜像 rootfs 内预建该目录本身——用户态（非 DynamicUser）进程
#     直接写此路径，无需依赖单元视图；两种视角互不替代。
#
# 开发机限制：本脚本设计在镜像构建（root）上下文执行；宿主用户直接
#             `bash` 运行会因无权限创建 /var/lib 子目录而失败（EACCES），
#             属预期上下文差异，与兄弟脚本（65 等直接写 /usr、/var）一致。

mkdir -p /var/lib/daedalus
chmod 0755 /var/lib/daedalus
chown root:root /var/lib/daedalus
