# Daedalus copilot 共享准备逻辑 —— 被 scripts/justfile.demo 的
# dev-copilot / cli-copilot recipe source 后调用 prep_copilot。
# 本文件不是独立可执行脚本,只定义函数;任一步失败都以非零返回交由
# 调用方的 set -e 接住,绝不吞错。
#
# 路径基准 = **本文件所在仓根**(daedalus-core/),由 BASH_SOURCE 推出,与调用者
# 的 cwd 无关。拆仓之后 daedalus-core 不再是工作区根下的子目录,mono-repo 时期
# 留下的 `daedalus-core/...` 前缀一律失效,故全部路径都锚定仓根。
prep_copilot() {
    local repo_root
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
    export DAEDALUS_REPO_ROOT="$repo_root"

    # demo 二进制(-tags demo 路径重写)与 prod go-build 产物同名覆盖。
    just go-build-demo

    # deno --allow-run 旗标(经 demo 重写后)只放行此布局位下的路径,audit/shell
    # 必须真实存在于此,deno 沙箱才允许 spawn。该布局位已被 .gitignore 保护。
    local bin_layout="${repo_root}/files/system/usr/local/bin"
    install -Dm0755 "${repo_root}/bin/daedalus-audit" "${bin_layout}/daedalus-audit"
    # shell 能力二进制不由本仓构建:能力迁出后由 `just plugin-pack` 从插件仓现构,
    # 落到**同一个**布局位。这里只校验它在位(fail-closed),不再从早已不存在的
    # daedalus-core/bin/daedalus-shell 取(那是拆仓前的形态)。
    [ -x "${bin_layout}/daedalus-shell" ] || {
        echo "prep_copilot: 缺少 ${bin_layout}/daedalus-shell —— 先跑 just plugin-pack(shell 能力二进制由插件仓现构后落位到该布局位)" >&2
        return 1
    }

    # 解包即校验:任一摘要不符,daedalus-plugin-pack 直接非零退出。
    local plugdir
    plugdir=$(mktemp -d)
    local p
    for p in fs shell pkg sysinfo copilot; do
        "${repo_root}/bin/daedalus-plugin-pack" \
            -verify "${repo_root}/bin/daedalus.${p}.plugin.zip" --keep "${plugdir}/daedalus.${p}"
    done

    # copilot 内 audit.ts/exec.ts 按 env 解析辅助二进制,须与上面 install 的布局位一致。
    export DAEDALUS_AUDIT_BIN="${bin_layout}/daedalus-audit"
    export DAEDALUS_SHELL_BIN="${bin_layout}/daedalus-shell"

    # demo 宿主的 /opt/daedalus 重写根必须能跨 cwd 成立:它把 entrypoint 里的
    # /opt/daedalus 换成 {prefix}/opt/daedalus,而该结果是**按调用者 cwd 解析**的,
    # 相对 prefix 会让"从不同目录起 just"得到不同结果。故这里生成一份绝对 prefix
    # 的临时 dev 配置,经优先级最高的 DAEDALUS_DEV_PATHS 指过去;用户自备的 deno
    # 路径若存在于仓根 daedalus-dev.toml,则沿用(它是机器相关的唯一一项)。
    local devcfg deno
    devcfg="$(mktemp)"
    {
        echo "# 由 scripts/copilot-prep.sh 生成(绝对 prefix,与 cwd 无关),每次 prep 重建;勿手改。"
        echo "prefix = \"${repo_root}/files/system\""
        if [ -f "${repo_root}/daedalus-dev.toml" ]; then
            deno="$(sed -n 's/^[[:space:]]*deno[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "${repo_root}/daedalus-dev.toml" | head -n 1)"
            [ -n "${deno}" ] && echo "deno = \"${deno}\""
        fi
    } > "${devcfg}"
    export DAEDALUS_DEV_PATHS="${devcfg}"

    # 供调用方拼 daedalus-host -dir run-plugin argv,不经文件中转。
    export DAEDALUS_PLUGDIR="${plugdir}"
}
