#!/usr/bin/env bash
# fetch-plugins.sh: 跨仓 release 流水线的插件拉取腿。
#
# 从 daedalus-plugins 仓的 GitHub release 拉 6 个能力插件 zip
# (daedalus.{fs,shell,pkg,sysinfo,service,blueprint}.plugin.zip),经
# daedalus-plugin-pack -verify 解压(解压即校验,任一 checksum 不符拒绝安装,
# fail-closed)到镜像树安装态 daedalus-core/files/system/opt/daedalus/plugins/,
# 供 just build 的 sync 腿同步进镜像 /opt/daedalus/plugins。
#
# 本机无 gh / 无网络时用 --local-zip-dir 指向本地已有 6 个 zip 的目录兜底
# (本地等价全链见 scripts/local-cross-repo-test.sh)。
#
# 用法:
#   fetch-plugins.sh [--local-zip-dir <dir>] [--dest <dir>] [--help]
#
# 选项:
#   --local-zip-dir <dir>  目录内已有 6 个 *.plugin.zip 时直接解压,跳过
#                          gh release download(本地兜底;V3 构建机/CI 默认走 gh)
#   --dest <dir>           解压目标目录(默认 = 镜像树安装态
#                          daedalus-core/files/system/opt/daedalus/plugins/)
#   --help                 打印本帮助并 exit 0
set -euo pipefail

# 解析脚本真实路径(防 /home/lofibass/code → /var/lofibass_ssd/code symlink 陷阱)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# 定位 daedalus-core 仓根:兼容两种落位——
#   mono-repo 布局:脚本在 <root>/daedalus-core/scripts/,仓根 = SCRIPT_DIR/../..
#   post-split 布局:脚本在 <core 仓根>/scripts/,仓根 = SCRIPT_DIR/..
if [ -f "${SCRIPT_DIR}/../../daedalus-core/go.mod" ]; then
    CORE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd -P)/daedalus-core"
elif [ -f "${SCRIPT_DIR}/../go.mod" ]; then
    CORE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
else
    echo "错误: 无法定位 daedalus-core 仓根(go.mod 未找到)" >&2
    exit 1
fi

usage() {
    cat <<'EOF'
用法: fetch-plugins.sh [--local-zip-dir <dir>] [--dest <dir>] [--help]

从 daedalus-plugins 仓 GitHub release 拉 6 个能力插件 zip 并解压(校验)到
镜像树安装态;本机无 gh/无网络时用 --local-zip-dir 指向本地 zip 目录兜底。

选项:
  --local-zip-dir <dir>  目录内已有 6 个 *.plugin.zip 时直接解压,跳过
                         gh release download(本地兜底)
  --dest <dir>           解压目标目录(默认 = 镜像树安装态
                         daedalus-core/files/system/opt/daedalus/plugins/)
  --help                 打印本帮助并 exit 0
EOF
    exit 0
}

ZIP_DIR=""
DEST=""
while [ $# -gt 0 ]; do
    case "$1" in
        --local-zip-dir)
            [ $# -ge 2 ] || { echo "错误: --local-zip-dir 需要参数" >&2; exit 2; }
            ZIP_DIR="$2"
            shift 2
            ;;
        --dest)
            [ $# -ge 2 ] || { echo "错误: --dest 需要参数" >&2; exit 2; }
            DEST="$2"
            shift 2
            ;;
        --help|-h)
            usage
            ;;
        *)
            echo "错误: 未知选项 $1(用 --help 查看用法)" >&2
            exit 2
            ;;
    esac
done

# 打包器二进制:与脚本同仓的 daedalus-core/bin/(先 just go-build / plugin-pack 构建)
PACK_BIN="${CORE_ROOT}/bin/daedalus-plugin-pack"
if [ ! -x "${PACK_BIN}" ]; then
    echo "错误: 找不到打包器 ${PACK_BIN};请先构建(cd daedalus-core && go build -trimpath -o bin/daedalus-plugin-pack ./cmd/daedalus-plugin-pack 或 just go-build)" >&2
    exit 1
fi

# 解压目标:默认镜像树安装态(just build 的 sync 腿同步进镜像 /opt/daedalus/plugins)
if [ -z "${DEST}" ]; then
    DEST="${CORE_ROOT}/files/system/opt/daedalus/plugins"
fi

# zip 来源:--local-zip-dir 显式指定(本地兜底)或 gh release download(V3 构建机/CI)
if [ -z "${ZIP_DIR}" ]; then
    ZIP_DIR="/tmp/plugins-release"
    if ! command -v gh >/dev/null 2>&1; then
        echo "错误: 本机无 gh,无法 gh release download;请用 --local-zip-dir <dir> 指向本地已有 6 个 *.plugin.zip 的目录(本地等价全链见 scripts/local-cross-repo-test.sh)" >&2
        exit 1
    fi
    mkdir -p "${ZIP_DIR}"
    echo "== 从 daedalus-plugins 仓 GitHub release 拉取插件 zip → ${ZIP_DIR}"
    gh release download --repo Daedalusys/daedalus-plugins --pattern '*.plugin.zip' --dir "${ZIP_DIR}/"
fi

# 6 个能力插件逐一解压(解压即校验:任一 checksum 不符拒绝安装,fail-closed)
for cap in fs shell pkg sysinfo service blueprint; do
    id="daedalus.${cap}"
    zip="${ZIP_DIR}/${id}.plugin.zip"
    if [ ! -f "${zip}" ]; then
        echo "错误: 缺少 ${zip}(期望 6 个能力插件 zip 齐全)" >&2
        exit 1
    fi
    dest="${DEST}/${id}"
    mkdir -p "${dest}"
    # 解压器要求空目录(O_EXCL 不覆盖既有文件);本机权限面禁 rm,用 find -delete 清空
    find "${dest}" -mindepth 1 -delete
    "${PACK_BIN}" -verify "${zip}" --keep "${dest}"
    echo "已安装: ${id} <- ${zip}"
done
echo "fetch-plugins: 6 个能力插件已解压(校验通过) -> ${DEST}"