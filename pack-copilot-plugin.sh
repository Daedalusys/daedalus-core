#!/usr/bin/env bash
# Daedalus Copilot 插件打包脚本(计划 todo 8)。
#
# 职责:把源码态插件目录 daedalus/plugin/copilot/ 中的 Deno copilot 源码
# (5 个 .ts;测试已随 todo 14 迁至仓库根 tests/deno/,源码目录不再含 .test.ts)与其清单 daedalus.plugin.json 组装成插件源目录,
# 经 daedalus-plugin-pack 注入 sha256 checksums 后产出:
#   1) daedalus/core/bin/daedalus.copilot.plugin.zip          —— 可分发插件包(bin/ 已 gitignore,
#      与 todo 9 能力插件的 <id>.plugin.zip 同一约定)
#   2) daedalus/files/system/opt/daedalus/plugins/daedalus.copilot/  —— 解压安装态(入库):
#      sync-daedalus.sh → base_image → Containerfile COPY → 镜像 /opt/daedalus/plugins/
#      宿主 daedalus-host 与 wrapper 都以该绝对路径消费它。
#   暂存目录用 mktemp 临时目录并在退出时清理:仓库内不留构建中间产物。
#
# ★ task 7 交接铁律:未打包的清单没有 checksums 字段,安装根下的 host verify/
#   run-plugin 会判 degraded 并拒绝产出启动命令 → 必须先 Pack 再安装,绝不手抄清单。
#
# 时序说明:copilot 源码已随 todo 11 三层迁移住进 daedalus/plugin/copilot/
# (源码态与清单同目录;镜像内权威安装态是 plugins/daedalus.copilot/,由本脚本产出)。
#
# 用法:./pack-copilot-plugin.sh
#       ZIP_OUT=/tmp/x.zip ./pack-copilot-plugin.sh    # 只改包输出位置,安装态目标不变
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "$0")" && pwd -P)
CORE_DIR="$ROOT/daedalus/core"
# 源码态 = 插件定义层(todo 11 起):清单与 5 个 .ts 同目录;测试位于仓库根 tests/deno/(todo 14 迁出)
COPILOT_SRC="$ROOT/daedalus/plugin/copilot"
MANIFEST_SRC="$ROOT/daedalus/plugin/copilot/daedalus.plugin.json"
PLUGIN_ID="daedalus.copilot"
# 安装态落镜像树(与 todo 9 的能力插件同一约定)
INSTALL_DIR="$ROOT/daedalus/files/system/opt/daedalus/plugins"
PLUGIN_DEST="$INSTALL_DIR/$PLUGIN_ID"
ZIP_OUT=${ZIP_OUT:-"$CORE_DIR/bin/$PLUGIN_ID.plugin.zip"}

# 打包输入暂存目录(清单 + 源码副本),退出即清理
STAGE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/daedalus-plugin-stage.XXXXXX")
trap 'find "$STAGE_DIR" -depth -delete' EXIT

# 静态链接 + 本机工具链约束与 core/Makefile 一致(GOTOOLCHAIN=local 禁偷偷下载)
export CGO_ENABLED=0 GOTOOLCHAIN=local

echo "==> 1/5 编译宿主侧工具(daedalus-plugin-pack / daedalus-host)"
# 不用 make(本机无 make,见 notepad issues todo 1),直接跑等价 go build
(cd "$CORE_DIR" && go build -trimpath -o bin/ ./cmd/daedalus-plugin-pack ./cmd/daedalus-host)
PACK_BIN="$CORE_DIR/bin/daedalus-plugin-pack"
HOST_BIN="$CORE_DIR/bin/daedalus-host"

echo "==> 2/5 组装插件源目录 $STAGE_DIR"
# 源码复制:.ts 全收(源码态已无测试);.test.ts 排除分支保留为防御(测试不进镜像,见计划门禁 #4)
for src in "$COPILOT_SRC"/*.ts; do
    case $(basename -- "$src") in
        *.test.ts) continue ;;
    esac
    install -m 0644 "$src" "$STAGE_DIR/"
done
install -m 0644 "$MANIFEST_SRC" "$STAGE_DIR/daedalus.plugin.json"
# Pack 要求 executable 带可执行位(校验 mode&0o111),入口脚本因此置 0755
chmod 0755 "$STAGE_DIR/main.ts"

echo "==> 3/5 打包并注入 checksums → $ZIP_OUT"
mkdir -p "$(dirname -- "$ZIP_OUT")"
find "$ZIP_OUT" -maxdepth 0 -delete 2>/dev/null || true
"$PACK_BIN" -in "$STAGE_DIR" -out "$ZIP_OUT"

echo "==> 4/5 校验 zip 并解压到安装态 $PLUGIN_DEST"
# 解压器以 O_EXCL 独占新建落盘:目标必须为空目录,故先清空(本机权限面禁 rm,用 find -delete)
mkdir -p "$PLUGIN_DEST"
find "$PLUGIN_DEST" -mindepth 1 -delete
"$PACK_BIN" -verify "$ZIP_OUT" --keep "$PLUGIN_DEST"

echo "==> 5/5 宿主视角复检(list / verify / run-plugin)"
"$HOST_BIN" list -dir "$INSTALL_DIR"
"$HOST_BIN" verify "$PLUGIN_ID" -dir "$INSTALL_DIR"
# run-plugin 打印的即 wrapper 将要 exec 的静态命令(末尾是插件脚本路径)
"$HOST_BIN" run-plugin "$PLUGIN_ID" -dir "$INSTALL_DIR"

echo "完成:插件安装态 $PLUGIN_DEST(镜像内对应 /opt/daedalus/plugins/$PLUGIN_ID)"
