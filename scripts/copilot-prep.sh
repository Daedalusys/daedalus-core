# Daedalus copilot 共享准备逻辑 —— 被 scripts/justfile.demo 的
# dev-copilot / cli-copilot recipe source 后调用 prep_copilot。
# 本文件不是独立可执行脚本(无执行位,0644 即可),只定义函数,
# 被 source 时除函数定义外不执行任何动作。

# 准备 copilot 运行所需的全部 dev 产物。调用方 recipe 脚本需自带
# set -euo pipefail(本函数不设置);函数内任何一步失败(install 失败 /
# daedalus-plugin-pack 校验失败)都以非零返回,由调用方的 set -e 接住,
# 绝不吞错误。步骤:
#   (a) just go-build-demo 重建 demo 二进制(-tags demo 路径重写);
#   (b) 安装 daedalus-audit / daedalus-shell 到镜像布局位
#       daedalus/files/system/usr/local/bin/(deno --allow-run 旗标放行的
#       路径,已被 .gitignore 保护,不入库);
#   (c) mktemp -d 建临时目录,用 daedalus-plugin-pack -verify --keep 解包
#       5 个官方插件 zip(fs/shell/pkg/sysinfo/copilot),解压即 sha256 校验;
#   (d) export DAEDALUS_AUDIT_BIN / DAEDALUS_SHELL_BIN 指向布局位二进制
#       (copilot 内 audit.ts / exec.ts 按 env 解析辅助二进制);
#   (e) export DAEDALUS_PLUGDIR=<plugdir> 供调用方直接读取,传给
#       daedalus-host -dir 拼 run-plugin 启动命令,不经文件中转。
prep_copilot() {
    # (a) 重建 demo 二进制:与 prod go-build 产物同名覆盖;失败直接非零返回
    just go-build-demo

    # (b) 镜像布局位:deno --allow-run 旗标(经 demo 重写后)只放行
    # ./daedalus/files/system/usr/local/bin/... 下的路径;audit/shell 必须
    # 真实存在于此,deno 沙箱才允许 spawn。
    local bin_layout="daedalus/files/system/usr/local/bin"
    install -Dm0755 daedalus/core/bin/daedalus-audit "${bin_layout}/daedalus-audit"
    install -Dm0755 daedalus/core/bin/daedalus-shell "${bin_layout}/daedalus-shell"

    # (c) 解包 5 个官方插件安装态到临时目录;解压即校验,任一摘要不符
    # daedalus-plugin-pack 直接非零退出。
    local plugdir
    plugdir=$(mktemp -d)
    local p
    for p in fs shell pkg sysinfo copilot; do
        daedalus/core/bin/daedalus-plugin-pack \
            -verify "daedalus/core/bin/daedalus.${p}.plugin.zip" --keep "${plugdir}/daedalus.${p}"
    done

    # (d) copilot 内 audit.ts/exec.ts 用 env 解析辅助二进制;必须与上面
    # install 的布局位一致,deno 沙箱和 copilot 解析才对得上。
    export DAEDALUS_AUDIT_BIN="$PWD/${bin_layout}/daedalus-audit"
    export DAEDALUS_SHELL_BIN="$PWD/${bin_layout}/daedalus-shell"

    # (e) 导出插件临时目录给调用方,直接拼 run-plugin argv。
    export DAEDALUS_PLUGDIR="${plugdir}"
}
