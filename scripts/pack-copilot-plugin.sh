#!/usr/bin/env bash
# Daedalus Copilot 插件打包脚本。
#
# 职责:把源码态插件目录 daedalus-core/plugin/copilot/ 中的 Deno copilot 源码
# (5 个 .ts;测试已迁至仓库根 tests/deno/,源码目录不再含 .test.ts)与其清单 daedalus.plugin.json 组装成插件源目录,
# 经 daedalus-plugin-pack 注入 sha256 checksums 后产出:
#   1) daedalus-core/bin/daedalus.copilot.plugin.zip          —— 可分发插件包(bin/ 已 gitignore,
#      与能力插件的 <id>.plugin.zip 同一约定)
#   2) daedalus-core/files/system/opt/daedalus/plugins/daedalus.copilot/  —— 解压安装态(入库):
#      sync-daedalus.sh(已迁入 scripts/) → base_image → Containerfile COPY → 镜像 /opt/daedalus/plugins/
#      宿主 daedalus-host 与 wrapper 都以该绝对路径消费它。
#   暂存目录用 mktemp 临时目录并在退出时清理:仓库内不留构建中间产物。
#
# ★ 铁律:未打包的清单没有 checksums 字段,安装根下的 host verify/
#   run-plugin 会判 degraded 并拒绝产出启动命令 → 必须先 Pack 再安装,绝不手抄清单。
#
# 时序说明:copilot 源码已迁住 daedalus-core/plugin/copilot/
# (源码态与清单同目录;镜像内权威安装态是 plugins/daedalus.copilot/,由本脚本产出)。
#
# 用法:./scripts/pack-copilot-plugin.sh
#       ZIP_OUT=/tmp/x.zip ./scripts/pack-copilot-plugin.sh    # 只改包输出位置,安装态目标不变
set -euo pipefail

# 仓库根:脚本位于 scripts/ 子目录,需向上一级。
# 用 $(cd ... && pwd) 解析真实路径(兼容 symlink 与 ./ 相对调用)。
ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
# ROOT 即 daedalus-core 仓根;拆仓前脚本假设的 `$ROOT/daedalus-core` 嵌套布局
# 已不存在,沿用会让本脚本在任何形态下都 cd 失败。
CORE_DIR="$ROOT"
# 源码态 = 插件定义层:清单与 5 个 .ts 同目录;测试位于仓库根 tests/deno/
COPILOT_SRC="$CORE_DIR/plugin/copilot"
MANIFEST_SRC="$CORE_DIR/plugin/copilot/daedalus.plugin.json"
PLUGIN_ID="daedalus.copilot"
# 安装态落镜像树(与能力插件同一约定)
INSTALL_DIR="$CORE_DIR/files/system/opt/daedalus/plugins"
PLUGIN_DEST="$INSTALL_DIR/$PLUGIN_ID"
ZIP_OUT=${ZIP_OUT:-"$CORE_DIR/bin/$PLUGIN_ID.plugin.zip"}

# 打包输入暂存目录(清单 + 源码副本 + i18n/ locale 翻译目录),退出即清理。
# STAGE_DEST(安装态暂存目录)在步骤 4 才赋值,故 EXIT trap 写成函数、两个变量
# 一并收尾——后设的 trap 会顶掉前一个,漏掉任一目录就是仓库/TMPDIR 里的残留。
STAGE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/daedalus-plugin-stage.XXXXXX")
STAGE_DEST=""
cleanup() {
    find "$STAGE_DIR" -depth -delete
    if [ -n "$STAGE_DEST" ]; then
        find "$STAGE_DEST" -depth -delete 2>/dev/null || true
    fi
}
trap cleanup EXIT

# 静态链接 + 本机工具链约束与 core/Makefile 一致(GOTOOLCHAIN=local 禁偷偷下载)
export CGO_ENABLED=0 GOTOOLCHAIN=local

# go PATH 多源兜底(与 justfile 的 go_path_fallback 同逻辑):本脚本既可被
# `just copilot-plugin` 调用,也被 CI 漂移门与本机直接执行调用,后者拿不到
# just 的插值,非交互 shell(asdf/mise 装的 go 常不在 PATH)会在这一步 127。
if ! command -v go >/dev/null 2>&1; then
    for c in "$HOME/.asdf/shims/go" "$HOME/.local/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
        if [ -x "$c" ]; then
            export PATH="$(dirname "$c"):$PATH"
            break
        fi
    done
fi
command -v go >/dev/null || { echo "ERROR: go not found in PATH or any fallback" >&2; exit 1; }

echo "==> 1/5 编译宿主侧工具(daedalus-plugin-pack / daedalus-host)"
# 不用 make(本机无 make),直接跑等价 go build
(cd "$CORE_DIR" && go build -trimpath -o bin/ ./cmd/daedalus-plugin-pack ./cmd/daedalus-host)
PACK_BIN="$CORE_DIR/bin/daedalus-plugin-pack"
HOST_BIN="$CORE_DIR/bin/daedalus-host"

echo "==> 2/5 组装插件源目录 $STAGE_DIR"
# 源码复制:.ts 全收(源码态已无测试);.test.ts 排除分支保留为防御(测试不进镜像)
for src in "$COPILOT_SRC"/*.ts; do
    case $(basename -- "$src") in
        *.test.ts) continue ;;
    esac
    install -m 0644 "$src" "$STAGE_DIR/"
done
install -m 0644 "$MANIFEST_SRC" "$STAGE_DIR/daedalus.plugin.json"
# i18n/ 目录进 zip:locale 翻译文件由 deno 运行时按 $LANG 加载。
# 注意 en_US 必含是宿主 Validate 强约束,pack 此处只复制,Validate 在
# 加载时再校验;此处若 i18n/ 缺失,允许(老插件可能没 i18n)。
if [ -d "$COPILOT_SRC/i18n" ]; then
    cp -r "$COPILOT_SRC/i18n" "$STAGE_DIR/i18n"
fi
# Pack 要求 executable 带可执行位(校验 mode&0o111),入口脚本因此置 0755
chmod 0755 "$STAGE_DIR/main.ts"

echo "==> 3/5 打包并注入 checksums → $ZIP_OUT"
mkdir -p "$(dirname -- "$ZIP_OUT")"
find "$ZIP_OUT" -maxdepth 0 -delete 2>/dev/null || true
"$PACK_BIN" -in "$STAGE_DIR" -out "$ZIP_OUT"

echo "==> 4/5 校验 zip 并解压到安装态 $PLUGIN_DEST"
# 解压器以 O_EXCL 独占新建落盘:目标必须是空目录(本机权限面禁 rm,清理用 find -delete)。
# 先解到同一文件系统内的暂存空目录,verify + 解压全绿之后才改名换址:若先清空
# 安装态再解压,校验失败就把上一次的好产物一并丢掉,仓库里留下一个空壳安装目录。
mkdir -p "$INSTALL_DIR"
STAGE_DEST=$(mktemp -d "$INSTALL_DIR/.${PLUGIN_ID}.install-stage.XXXXXX")
OLD_DEST="${STAGE_DEST}.old"
"$PACK_BIN" -verify "$ZIP_OUT" --keep "$STAGE_DEST"
if [ -d "$PLUGIN_DEST" ]; then
    mv "$PLUGIN_DEST" "$OLD_DEST"
    if ! mv "$STAGE_DEST" "$PLUGIN_DEST"; then
        mv "$OLD_DEST" "$PLUGIN_DEST" # 换址失败 → 旧产物原样退回,绝不留空壳
        echo "ERROR: 安装态换址失败: $STAGE_DEST → $PLUGIN_DEST" >&2
        exit 1
    fi
    find "$OLD_DEST" -depth -delete
else
    mv "$STAGE_DEST" "$PLUGIN_DEST"
fi

echo "==> 5/5 宿主视角复检(list / verify / run-plugin)"
"$HOST_BIN" list -dir "$INSTALL_DIR"
"$HOST_BIN" verify "$PLUGIN_ID" -dir "$INSTALL_DIR"
# run-plugin 打印的即 wrapper 将要 exec 的静态命令(末尾是插件脚本路径)
"$HOST_BIN" run-plugin "$PLUGIN_ID" -dir "$INSTALL_DIR"

echo "完成:插件安装态 $PLUGIN_DEST(镜像内对应 /opt/daedalus/plugins/$PLUGIN_ID)"
