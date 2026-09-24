#!/usr/bin/env bash
# 3 仓平级布局守门:检查 daedalus-core 的兄弟仓
# daedalus-sdk / daedalus-plugins 是否以平级目录形态就位,供 go.work 本地 dev 桥使用。
# 任一缺失/不匹配即 exit 1,并逐项报告缺哪个。
#
# 布局约定(与 daedalus-core/go.work 的 use 路径对应):
#   ~/work/daedalusys/
#   ├── daedalus-core/      # 本仓(go.work 在此)
#   ├── daedalus-sdk/       # 兄弟仓(module github.com/Daedalusys/daedalus-sdk)
#   └── daedalus-plugins/   # 兄弟仓(6 个 sub-module,各含独立 go.mod)
set -u

# 解析脚本真实路径(防 symlink 陷阱:本机 /home/lofibass/code → /var/lofibass_ssd/code)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CORE_ROOT="$(dirname "$SCRIPT_DIR")"   # daedalus-core 仓根
PARENT="$(dirname "$CORE_ROOT")"       # 3 仓平级父目录

fail=0

# 1) 兄弟仓目录存在
for repo in daedalus-sdk daedalus-plugins; do
    if [ ! -d "$PARENT/$repo" ]; then
        echo "MISSING: 兄弟仓目录 $PARENT/$repo 不存在"
        echo "  3 仓须平级 clone: mkdir -p ~/work/daedalusys && cd ~/work/daedalusys &&"
        echo "  git clone <core> && git clone <sdk> && git clone <plugins>"
        echo "  仓名必须为 daedalus-core / daedalus-sdk / daedalus-plugins(与 go.work 路径对应)"
        fail=1
    fi
done

# 2) 各仓 go.mod 存在 + module 路径匹配 github.com/Daedalusys/...
check_module() {
    # $1 = 模块目录, $2 = 期望 module 前缀
    local dir="$1" prefix="$2"
    if [ ! -f "$dir/go.mod" ]; then
        echo "MISSING: $dir/go.mod 不存在"
        fail=1
        return
    fi
    local mod
    mod="$(head -1 "$dir/go.mod")"
    case "$mod" in
        "module $prefix"*) ;;
        *)
            echo "MISMATCH: $dir/go.mod 首行 = '$mod',期望 'module $prefix...'"
            fail=1
            ;;
    esac
}

check_module "$CORE_ROOT" "github.com/Daedalusys/daedalus-core"
check_module "$PARENT/daedalus-sdk" "github.com/Daedalusys/daedalus-sdk"
for cap in fs shell pkg sysinfo service blueprint; do
    check_module "$PARENT/daedalus-plugins/$cap" "github.com/Daedalusys/daedalus-plugins/$cap"
done

if [ "$fail" -ne 0 ]; then
    echo "verify-dev-layout: FAIL(见上方缺失项;3 仓须平级 clone 且仓名固定为 daedalus-core/daedalus-sdk/daedalus-plugins)"
    exit 1
fi
echo "verify-dev-layout: OK(3 仓平级布局就位: daedalus-core + daedalus-sdk + daedalus-plugins)"