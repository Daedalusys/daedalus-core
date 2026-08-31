#!/usr/bin/env bash
# 插件 i18n "manifest 声明 ↔ 实物" 双向同步工具(被 just i18n-sync / i18n-sync-autofix 包装)。
#
# 职责:比对插件目录下 daedalus.plugin.json 的 "i18n" 数组声明与 i18n/ 目录里
# 实际存在的 locale 文件(i18n/en_US.json → locale "en_US"):
#   - 声明有但实物无 → 列清单 exit 1
#   - 实物有但声明无 → 列清单 exit 1
#   - en_US 不在其中 → 报 "en_US 必定位兜底" exit 1(硬约束,与 --autofix 无关)
#   - 完全一致       → 打印 OK
#
# 模式:
#   默认(无 --autofix):严格校验,不改任何文件,漂移即 exit 1 —— CI 守门用
#   --autofix:        以 i18n/ 目录实物为准改写 manifest 的 i18n 字段(其余字段
#                     原样保留,2 空格缩进写回),i18n/ 目录绝不改动 —— 开发者加
#                     新 locale 后一键同步 manifest
#
# 依赖:jq(本仓库处理 JSON 的约定工具);不依赖 deno/node,与运行时解耦。
#
# 用法: ./scripts/plugin-i18n-sync.sh [--autofix] [--no-cross-check] [plugin-dir]
#   默认(无 --autofix):严格校验,有不一致 exit 1
#   --autofix:        以 i18n/ 目录为准,自动重写 manifest 的 i18n 字段
#   --no-cross-check: 关闭 Go 侧 i18n key 校验(默认开启:扫描 daedalus/core/
#                     下 i18n.T("...") 字面量 key,校验 locale json 有对应条目)
#   plugin-dir:      插件根目录(默认当前目录;含 daedalus.plugin.json 与 i18n/)
set -euo pipefail

# 仓库根:脚本位于 scripts/ 子目录,需向上一级。
ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd -P)

autofix=0
cross_check=1
plugin_dir="."
# 参数解析:--autofix / --no-cross-check 开关 + 可选位置参数(插件目录,默认当前目录)
for arg in "$@"; do
    case $arg in
        -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        --autofix) autofix=1 ;;
        --no-cross-check) cross_check=0 ;;
        -*) echo "错误:未知选项 $arg(用法见 --help)" >&2; exit 1 ;;
        *) plugin_dir=$arg ;;
    esac
done

MANIFEST="$plugin_dir/daedalus.plugin.json"
I18N_DIR="$plugin_dir/i18n"
[ -f "$MANIFEST" ] || { echo "错误:清单不存在: $MANIFEST" >&2; exit 1; }
command -v jq >/dev/null || { echo "错误:需要 jq(仓库处理 JSON 的约定工具)" >&2; exit 1; }

# ── Go 侧 i18n key 校验(--check-cross,默认开启;决策 9:融入本脚本,不做独立工具)──
# 扫描 daedalus/core/ 下 i18n.T("...") 字符串字面量首参 key(grep 提取,不用
# go/ast:工具链调用太重,误报时才升级),逐个校验 internal/i18n/locales/ 下
# en_US.json 与 zh_CN.json 均有条目;任一缺即差集报错 exit 1。排除 *_test.go。
# locales/ 目录不存在(任务 1 的包未建)时跳过且不失败 —— 向后兼容窗口。
check_go_i18n_keys() {
    local locales_dir="$ROOT/daedalus/core/internal/i18n/locales"
    [ -d "$locales_dir" ] || { echo "⊘ Go i18n 包未就位,跳过 cross-check"; return 0; }

    # 提取全部 Go 侧 key:只匹配 i18n.T("key" 形式(字符串字面量首参,
    # 不匹配变量传递);--include 排除 *_test.go(测试 T() 调用不参与生产校验)。
    local keys
    keys=$(grep -rhoE 'i18n\.T\("([a-z0-9_.]+)"' "$ROOT/daedalus/core/" \
        --include='*.go' --exclude='*_test.go' \
        | sed 's/.*"\(.*\)"/\1/' | sort -u || true)
    [ -n "$keys" ] || return 0   # 任务 4 迁移未跑,尚无任何 Go 侧 key

    # 逐 key 校验两个 locale json 均有条目,收集缺失明细
    local missing=0 detail=""
    while IFS= read -r key; do
        local lacks=""
        for f in en_US zh_CN; do
            if jq -e --arg k "$key" 'has($k)' "$locales_dir/$f.json" >/dev/null; then
                continue
            fi
            if [ -z "$lacks" ]; then lacks="$f.json"; else lacks+=", $f.json"; fi
        done
        if [ -n "$lacks" ]; then
            missing=$((missing + 1))
            detail+="  - $key (缺于$lacks)"$'\n'
        fi
    done <<< "$keys"

    if [ "$missing" -gt 0 ]; then
        echo "✗ Go i18n key 漂移(Go 源码有但 locale 文件缺):" >&2
        printf '%s' "$detail" >&2
        exit 1
    fi
    echo "✓ 跨语言 Go i18n key 校验通过($(wc -l <<< "$keys") 个 key)"
}

# ── 实物扫描:ls i18n/*.json → basename 去后缀;POSIX locale 命名校验 ──
# locale 文法:^[a-z]{2,3}(_[A-Z][A-Za-z0-9]+)?$ (en_US / zh_CN / zh_TW / ja 均过);
# 野名字(如 foo.json)直接报错 exit 1,防止漂进 manifest。
physical=()
if [ -d "$I18N_DIR" ]; then
    for f in "$I18N_DIR"/*.json; do
        [ -e "$f" ] || continue   # 无匹配时 glob 保持字面量,跳过
        loc=$(basename -- "$f" .json)
        if ! [[ $loc =~ ^[a-z]{2,3}(_[A-Z][A-Za-z0-9]+)?$ ]]; then
            echo "错误:locale 文件名不合法: $f(需 POSIX locale 形式,如 en_US.json / zh_CN.json / ja.json)" >&2
            exit 1
        fi
        physical+=("$loc")
    done
fi

# ── 声明读取:manifest 的 i18n 数组(jq -r '.i18n[]';字段缺省视为空数组)──
declared=()
while IFS= read -r loc; do
    declared+=("$loc")
done < <(jq -r '.i18n[]? // empty' "$MANIFEST")

# ── 差集计算(awk 关联数组实现集合差,避免子进程 O(n²))──
# 语义:第一个文件进字典,第二个文件不在字典者输出。
# declared_only = 声明−实物;physical_only = 实物−声明
declared_only=$(printf '%s\n' "${physical[@]:-}" | awk 'NR==FNR{d[$1];next}!($1 in d)&&NF{print $1}' - <(printf '%s\n' "${declared[@]:-}"))
physical_only=$(printf '%s\n' "${declared[@]:-}" | awk 'NR==FNR{d[$1];next}!($1 in d)&&NF{print $1}' - <(printf '%s\n' "${physical[@]:-}"))

# ── en_US 必定位兜底(硬约束:实物与声明两侧任一存在即算有,均无必报错)──
has_en_us=0
[ ${#physical[@]} -gt 0 ] && printf '%s\n' "${physical[@]}" | grep -qx en_US && has_en_us=1
[ ${#declared[@]} -gt 0 ] && printf '%s\n' "${declared[@]}" | grep -qx en_US && has_en_us=1
if [ "$has_en_us" -eq 0 ]; then
    echo "错误:en_US 必定位兜底:任何插件都不能省(en_US.json 或 manifest i18n 数组至少含其一)" >&2
    exit 1
fi

# ── 无漂移 → OK;有漂移 → 严格模式列清单 exit 1 / --autofix 模式改写 manifest ──
if [ -z "$declared_only" ] && [ -z "$physical_only" ]; then
    echo "OK:manifest 声明与 i18n/ 实物一致(${physical[*]:-无})"
    [ "$cross_check" -eq 1 ] && check_go_i18n_keys
    exit 0
fi

if [ "$autofix" -eq 0 ]; then
    echo "错误:i18n 声明与实物漂移(严格模式,不修改任何文件):" >&2
    if [ -n "$declared_only" ]; then
        echo "  声明里但实物没有:" >&2
        printf '%s\n' "$declared_only" | sed 's/^/    - /' >&2
    fi
    if [ -n "$physical_only" ]; then
        echo "  实物有但声明里没有:" >&2
        printf '%s\n' "$physical_only" | sed 's/^/    - /' >&2
    fi
    echo "提示:开发者加新 locale 后用 --autofix 一键改写 manifest" >&2
    exit 1
fi

# --autofix:以实物为权威(即并集:实物 ∪ 声明),去重排序后改写 manifest 的
# i18n 字段;jq 读出 → 只改 i18n → 2 空格缩进写回,其余字段原样保留。
merged=$({ printf '%s\n' "${physical[@]:-}"; printf '%s\n' "$declared_only"; } \
    | awk '!seen[$0]++ && NF' | sort)
merged_json=$(printf '%s\n' "$merged" | jq -R . | jq -s 'map(select(length>0))')
tmp=$(mktemp "${TMPDIR:-/tmp}/daedalus-manifest.XXXXXX")
trap 'rm -f "$tmp"' EXIT
jq --argjson i18n "$merged_json" '.i18n = $i18n' "$MANIFEST" > "$tmp"
mv "$tmp" "$MANIFEST"
echo "已改写 $MANIFEST 的 i18n 字段为: $(echo "$merged_json" | jq -c .)"
[ "$cross_check" -eq 1 ] && check_go_i18n_keys
