#!/usr/bin/env bash

set -xeuo pipefail

# 安装先决条件(unzip 解压 release 资产;sha256sum 来自已装的 coreutils)。
# curl 不硬装:部分基础镜像由 curl-minimal 提供,`dnf install curl` 会撞包冲突
# ("curl-minimal conflicts with curl"),而已有的 curl-minimal 足够本脚本使用。
command -v curl >/dev/null || dnf install -y curl
dnf install -y unzip

# Deno 版本 / 校验和的唯一事实源;与 CI 读同一份文件。
# shellcheck source=/dev/null
. "$(dirname "$0")/deno-version.env"

# 只钉 x86_64:镜像仅构 --platform=linux/amd64。其它架构 fail-closed,
# 不回退 latest —— 装错架构的二进制比构建失败更难查。
case "$(uname -m)" in
    x86_64) DENO_TARGET="x86_64-unknown-linux-gnu" ;;
    *) echo "ERROR: 未钉 $(uname -m) 架构的 Deno 资产(当前只钉 x86_64)" >&2; exit 1 ;;
esac

DENO_BASE="https://dl.deno.land/release/v${DENO_VERSION}"
DENO_ZIP="deno-${DENO_TARGET}.zip"
DENO_WORK="$(mktemp -d)"
trap 'rm -rf "$DENO_WORK"' EXIT

# 下载 + 校验:判据是「字节等于钉死的那份」,不是安装器退出码。
curl -fsSL -o "${DENO_WORK}/${DENO_ZIP}" "${DENO_BASE}/${DENO_ZIP}"
printf '%s  %s\n' "${DENO_SHA256_X86_64}" "${DENO_WORK}/${DENO_ZIP}" | sha256sum -c -

# 解包到 /usr/local/bin;unzip 保留 zip 条目权限,再显式 chmod 兜底。
unzip -o -d /usr/local/bin "${DENO_WORK}/${DENO_ZIP}" >/dev/null
chmod +x /usr/local/bin/deno

# 硬校验:装上的必须正是钉住的那个版本(校验和管字节,这里管版本号自证)
installed="$(/usr/local/bin/deno --version | head -n1 | awk '{print $2}')"
if [ "$installed" != "$DENO_VERSION" ]; then
    echo "ERROR: 安装的 Deno 版本 ${installed} != ${DENO_VERSION}" >&2
    exit 1
fi

# 创建 Deno 工作目录并确保持久化路径存在
mkdir -p /var/deno
mkdir -p /var/usrlocal/bin
