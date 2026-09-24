# Daedalus copilot 共享准备逻辑 —— 被 scripts/justfile.demo 的
# dev-copilot / cli-copilot recipe source 后调用 prep_copilot。
# 本文件不是独立可执行脚本,只定义函数;任一步失败都以非零返回交由
# 调用方的 set -e 接住,绝不吞错。
prep_copilot() {
    # demo 二进制(-tags demo 路径重写)与 prod go-build 产物同名覆盖。
    just go-build-demo

    # deno --allow-run 旗标(经 demo 重写后)只放行此布局位下的路径,audit/shell
    # 必须真实存在于此,deno 沙箱才允许 spawn。该布局位已被 .gitignore 保护。
    local bin_layout="daedalus-core/files/system/usr/local/bin"
    install -Dm0755 daedalus-core/bin/daedalus-audit "${bin_layout}/daedalus-audit"
    install -Dm0755 daedalus-core/bin/daedalus-shell "${bin_layout}/daedalus-shell"

    # 解包即校验:任一摘要不符,daedalus-plugin-pack 直接非零退出。
    local plugdir
    plugdir=$(mktemp -d)
    local p
    for p in fs shell pkg sysinfo copilot; do
        daedalus-core/bin/daedalus-plugin-pack \
            -verify "daedalus-core/bin/daedalus.${p}.plugin.zip" --keep "${plugdir}/daedalus.${p}"
    done

    # copilot 内 audit.ts/exec.ts 按 env 解析辅助二进制,须与上面 install 的布局位一致。
    export DAEDALUS_AUDIT_BIN="$PWD/${bin_layout}/daedalus-audit"
    export DAEDALUS_SHELL_BIN="$PWD/${bin_layout}/daedalus-shell"

    # 供调用方拼 daedalus-host -dir run-plugin argv,不经文件中转。
    export DAEDALUS_PLUGDIR="${plugdir}"
}
