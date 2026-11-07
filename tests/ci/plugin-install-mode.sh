#!/usr/bin/env bash
# 插件安装态目录位门禁:安装态目录树必须 0755,且与调用方 umask 无关。
#
# 为什么这条门禁必须存在
#   安装态目录随 rootfs 进镜像,而 ostree 从容器镜像导入时,**目录 mode 取它首次出现
#   那一层的值**,后续层对同一目录的 chmod 不被采纳。曾出现的缺陷形态:打包脚本用
#   `mktemp -d`(0700)做解压暂存目录、再 `mv` 成安装态目录(mv 保留源 mode),于是一
#   个 0700 落进镜像首层,构建期脚本里的 chmod 只能落在更晚的层 —— 部署出来仍是 0700,
#   普通用户读不到 manifest,宿主把 copilot 判 degraded,copilot CLI 整条不可用。
#   因此判据必须是「打包产物本身」的 mode,不能靠镜像构建期事后 chmod。
#
# 两种跑法
#   plugin-install-mode.sh --selftest    只验判定逻辑(假安装态目录,不跑打包、不依赖 go)
#   plugin-install-mode.sh               真跑 ./scripts/pack-copilot-plugin.sh 后判产物
#
# 真跑模式下会连带验证 umask 免疫性:umask 077 下重跑一次仍须 0755。umask 077 是
# 最严的常见取值(解压器建目录走 os.Mkdir(0o755),其位受 umask 约束)。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PACK_SH="$ROOT/scripts/pack-copilot-plugin.sh"
INSTALL_DIR="$ROOT/files/system/opt/daedalus/plugins/daedalus.copilot"
# 层内首现 mode 的判据来自镜像/归档,而非本脚本:由 tests/build/plugin-install-mode-image.sh
# 承担(需 podman)。本脚本只钉到「构建产物」。真跑打包需 go 工具链;缺失时降级为
# 静态断言并打印 NOTE(CI test job 有 go,故恒为硬核对)。

# check_mode <目录> —— 目录自身与其下**所有子目录**必须恰为 0755。
# 只判目录不判文件:文件位由 zip 条目决定(pack 保证可执行位),与本次缺陷无关。
check_mode() {
    local dir="$1" bad=0 rel mode
    if [ ! -d "$dir" ]; then
        echo "FAIL: 安装态目录不存在: $dir" >&2
        return 1
    fi
    # -printf 比 stat 循环快且不依赖额外进程;相对路径仅用于报告可读性。
    while IFS= read -r line; do
        mode="${line%% *}"
        rel="${line#* }"
        if [ "$mode" != "755" ]; then
            echo "FAIL: 目录 mode 不是 0755: $mode ${rel:-<根>}" >&2
            bad=1
        fi
    done < <(find "$dir" -type d -printf '%m %P\n')
    return "$bad"
}

# assert_normalizes_before_mv —— 静态断言:解压暂存目录(0700 的来源)必须**在换址之前**
# 被归一化。只 chmod 目标目录是无效修法(mv 保留源 mode),故锚点必须落在 mv 之前的那一段。
assert_normalizes_before_mv() {
    local body chmod_line mv_line
    body="$(cat "$PACK_SH")"
    # 取最后一条把暂存目录换成安装态目录的 mv(成功路径那条),与它之前的 chmod 归一化行。
    chmod_line="$(printf '%s\n' "$body" | grep -n 'find "$STAGE_DEST" -type d -exec chmod 0755' | head -n 1 | cut -d: -f1)"
    mv_line="$(printf '%s\n' "$body" | grep -n 'mv "$STAGE_DEST" "$PLUGIN_DEST"' | head -n 1 | cut -d: -f1)"
    if [ -z "$chmod_line" ]; then
        echo "FAIL: $PACK_SH 缺少对解压暂存目录的目录位归一化(0700 会经 mv 漏进安装态)" >&2
        return 1
    fi
    if [ -z "$mv_line" ]; then
        echo "FAIL: $PACK_SH 里找不到 mv \"\$STAGE_DEST\" \"\$PLUGIN_DEST\"(锚点漂移?)" >&2
        return 1
    fi
    if [ "$chmod_line" -ge "$mv_line" ]; then
        echo "FAIL: 目录位归一化在第 ${chmod_line} 行,mv 在第 ${mv_line} 行 —— 必须归一化在换址之前(mv 保留源 mode)" >&2
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------- 自检 ---------
if [ "${1:-}" = "--selftest" ]; then
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    printf '== plugin-install-mode.sh --selftest ==\n'

    # 正例:根与子目录都是 0755。
    mkdir -p "$tmp/good/i18n" && chmod 0755 "$tmp/good" "$tmp/good/i18n"
    if check_mode "$tmp/good" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 0 "$rc" "目录树全 0755 → 通过"

    # 反例:根 0700(本缺陷的形态)→ 必须红。
    mkdir -p "$tmp/badroot/i18n" && chmod 0700 "$tmp/badroot" "$tmp/badroot/i18n"
    if check_mode "$tmp/badroot" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 1 "$rc" "根目录 0700 → 失败"

    # 反例:仅子目录 0700(根 0755)→ 必须红(子目录同样挡住普通用户)。
    mkdir -p "$tmp/badsub/i18n" && chmod 0755 "$tmp/badsub" && chmod 0700 "$tmp/badsub/i18n"
    if check_mode "$tmp/badsub" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 1 "$rc" "子目录 0700 → 失败"

    # 反例:目录不存在 → 必须红(不许静默跳过)。
    if check_mode "$tmp/nope" >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 1 "$rc" "目录缺失 → 失败"

    # 静态断言对真实脚本必须成立。
    if assert_normalizes_before_mv >/dev/null 2>&1; then rc=0; else rc=$?; fi
    st 0 "$rc" "打包脚本在 mv 之前归一化解压暂存目录"

    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

# ---------------------------------------------------------------- 正式流程 -----
printf '== 插件安装态目录位门禁 ==\n'
assert_normalizes_before_mv || exit 1
echo "  OK   打包脚本静态断言(归一化在换址之前)"

# 真跑打包:判据必须是**脚本产出的**安装态,不是仓里任何残留(该目录不入库)。
if command -v go >/dev/null 2>&1; then
    # umask 免疫:最严常见 umask 下产物仍须 0755,否则安装态的可读性取决于调用方环境。
    # 这一跑放在默认 umask 那一跑**之前**:它会把产物按 077 落盘,必须由随后那次
    # 默认 umask 的重跑把产物恢复成常规状态,免得给后续步骤留下更紧的权限。
    (cd "$ROOT" && umask 077 && ./scripts/pack-copilot-plugin.sh >/dev/null)
    check_mode "$INSTALL_DIR" || exit 1
    echo "  OK   umask 077 下产物目录树全 0755"

    (cd "$ROOT" && ./scripts/pack-copilot-plugin.sh >/dev/null)
    check_mode "$INSTALL_DIR" || exit 1
    echo "  OK   安装态目录树(默认 umask)全 0755"
else
    # 不静默跳过:静态断言已过,mode 判据留待 CI 的 go 环境补跑。
    echo "  NOTE 无 go,跳过打包产物 mode 断言(静态断言已通过;CI test job 有 go,恒为硬核对)"
fi

echo "OK: 插件安装态目录位门禁通过"
