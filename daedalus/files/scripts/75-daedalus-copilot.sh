#!/usr/bin/env bash
set -xeuo pipefail

# Daedalus Copilot 权限收尾(计划 todo 11 三层迁移后)。
#
# copilot 源码态已迁至 daedalus/plugin/copilot/(代码逻辑与插件定义层,镜像外);
# 镜像内权威安装态是 /opt/daedalus/plugins/daedalus.copilot/(todo 8 打包产物,
# 随 rootfs 树 sync/COPY 落位,文件权限由 plugin-pack 解压器固定)。
# 旧镜像内 copilot 源目录(/opt/daedalus 下的 deno 子树)已废弃,不再在镜像内创建。
echo "=== Configuring Daedalus Copilot CLI permissions ==="
# 安装态目录存在时确保目录位(缺失属构建接线问题,由插件打包流水线负责,不在此失败)。
if [ -d /opt/daedalus/plugins/daedalus.copilot ]; then
    chmod 0755 /opt/daedalus/plugins/daedalus.copilot
fi
if [ -f /usr/local/bin/daedalus ]; then
    chmod +x /usr/local/bin/daedalus
fi
# 任务 21: copilot 运行期依赖的审计 CLI 与 shell 服务器由 `just plugin-pack` 以 0755
# 落进 rootfs 树 /usr/local/bin/(audit.ts/exec.ts 生产默认路径),overlay COPY 保留权限位;
# 与上方 wrapper 同款做幂等权限兜底,不新增安装职责。
for bin in daedalus-audit daedalus-shell; do
    if [ -f "/usr/local/bin/${bin}" ]; then
        chmod +x "/usr/local/bin/${bin}"
    fi
done
echo "Daedalus Copilot configuration completed."
