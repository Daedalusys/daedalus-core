#!/usr/bin/env bash
# 凭据留存面取证:core#60 里"静态读 workflow 到此为止、判定需要跑起来看"的那三项。
#   A 发布产物里有没有 .git/ 与 token 字样
#   B Actions cache 条目形状 —— 有没有 refs/pull/* 写入的 setup-go 条目
#     (cache 按分支作用域共享:基础分支能读 PR 分支写入的条目 = cache 投毒的读侧)
#   C provenance / SBOM 资产里有没有 token 字样
#
# 只读:仅用 gh 读 API / 下载 release 资产,不写任何仓库状态、不改任何文件。
# 判定逻辑做成纯函数(吃文本、吐结论),`--selftest` 用合成输入钉住它们 ——
# 否则"取证脚本"本身也会变成另一种静默失明。
#
# 用法:
#   credential-surface-check.sh [--repo <owner/name>]…   # 默认查四个仓
#   credential-surface-check.sh --selftest
set -euo pipefail

# ---------------------------------------------------------------- 纯判定函数 ---
# git_entry_count <文本> —— 命中 `.git/` 路径段的条数(0 才是期望)。
git_entry_count() {
    printf '%s\n' "$1" | grep -cE '(^|/)\.git/' || true
}

# token_hits <文本> —— 命中 token 字样的去重列表(空才是期望)。
token_hits() {
    printf '%s\n' "$1" | grep -aoE 'x-access-token|GITHUB_TOKEN' | sort -u | tr '\n' ' ' | sed 's/ $//' || true
}

# pull_ref_count <cache TSV:key<TAB>ref<TAB>size> —— `refs/pull/*` 写入的条目数。
pull_ref_count() {
    printf '%s\n' "$1" | awk -F'\t' '$2 ~ /^refs\/pull\// {n++} END {print n+0}'
}

# cache_verdict <cache TSV> —— 存在 PR 可达 job 写入的条目即 POISONABLE。
cache_verdict() {
    if [ "$(pull_ref_count "$1")" -gt 0 ]; then echo POISONABLE; else echo NO_PULL_WRITE; fi
}

# ---------------------------------------------------------------- 自检 ---------
if [ "${1:-}" = "--selftest" ]; then
    fail_n=0
    st() { # st <期望> <实际> <说明>
        if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
        else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; fail_n=$((fail_n + 1)); fi
    }
    printf '== credential-surface-check.sh --selftest ==\n'
    clean_listing='daedalus.fs/daedalus.plugin.json
daedalus.fs/bin/daedalus-fs'
    dirty_listing='pkg/.git/HEAD
pkg/.git/config'
    st 0 "$(git_entry_count "$clean_listing")" ".git/ 条目数:干净的 zip 清单 → 0"
    st 2 "$(git_entry_count "$dirty_listing")" ".git/ 条目数:含 .git/HEAD 与 .git/config → 2"
    st "" "$(token_hits 'plain manifest without secrets')" "token 命中:干净文本 → 空"
    st "GITHUB_TOKEN x-access-token" "$(token_hits 'a GITHUB_TOKEN b x-access-token c GITHUB_TOKEN' | tr '\n' ' ' | sed 's/ $//')" "token 命中:去重 + 排序"
    clean_cache='setup-go-Linux-x64-ubuntu24-go-1.26.10-abc	refs/heads/main	103577698'
    poison_cache='setup-go-Linux-x64-ubuntu24-go-1.25.14-abc	refs/pull/36/merge	67443011'
    st 0 "$(pull_ref_count "$clean_cache")" "cache: 只有 refs/heads → 0 个 PR 写入条目"
    st 1 "$(pull_ref_count "$poison_cache")" "cache: refs/pull/36/merge → 1 个 PR 写入条目"
    st NO_PULL_WRITE "$(cache_verdict "$clean_cache")" "cache 判定:无 PR 写入 → NO_PULL_WRITE"
    st POISONABLE "$(cache_verdict "$poison_cache")" "cache 判定:有 PR 写入 → POISONABLE"
    if [ "$fail_n" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
    echo "RESULT: SELFTEST FAIL ($fail_n)"; exit 1
fi

REPOS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --repo) REPOS+=("${2:-}"); shift 2 ;;
        -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1(用 --help)" >&2; exit 2 ;;
    esac
done
[ "${#REPOS[@]}" -gt 0 ] || REPOS=(
    Daedalusys/daedalus-core Daedalusys/daedalus-sdk
    Daedalusys/daedalus-plugins Daedalusys/daedalus-agent
)

command -v gh >/dev/null || { echo "credential-surface-check: 缺 gh(须已登录)" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# list_entries <归档路径> —— 归档内条目清单(zip / tar.gz);非归档输出空。
list_entries() {
    case "$1" in
        *.zip)          unzip -Z1 "$1" 2>/dev/null || true ;;
        *.tar.gz|*.tgz) tar -tzf "$1" 2>/dev/null || true ;;
    esac
}

# extract_to <归档路径> <目标目录> —— 解包到目标目录(供内容级 token 扫描)。
extract_to() {
    case "$1" in
        *.zip)          unzip -qq -o "$1" -d "$2" 2>/dev/null || true ;;
        *.tar.gz|*.tgz) tar -xzf "$1" -C "$2" 2>/dev/null || true ;;
    esac
}

# scan_dir_tokens <目录> —— 打印「相对路径:命中模式」行;无命中则无输出。
# -a:Go 二进制里若含该字样也要看见(二进制按文本处理)。
scan_dir_tokens() {
    local dir="$1" f rel
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        rel="${f#"$dir"/}"
        printf '   %s: %s\n' "$rel" \
            "$(token_hits "$(grep -aEo 'x-access-token|GITHUB_TOKEN' "$f" 2>/dev/null || true)")"
    done < <(grep -ralE 'x-access-token|GITHUB_TOKEN' "$dir" 2>/dev/null || true)
}

echo "################ A + C: 发布产物认证 ################"
for repo in "${REPOS[@]}"; do
    name="${repo##*/}"
    tag="$(gh release list --repo "$repo" --limit 1 --json tagName --jq '.[].tagName' 2>/dev/null || true)"
    if [ -z "$tag" ]; then
        echo "-- $repo: 无 release → 无可检发布资产(记为 GAP,不是 PASS)"
        continue
    fi
    dir="$tmp/$name"; mkdir -p "$dir"
    gh release download "$tag" --repo "$repo" --dir "$dir" --clobber >/dev/null 2>&1 || true
    gitn=0; archives=0; jsons=0; hits=0
    for f in "$dir"/*; do
        [ -f "$f" ] || continue
        case "$f" in
            *.zip|*.tar.gz|*.tgz)
                archives=$((archives + 1))
                gitn=$((gitn + $(git_entry_count "$(list_entries "$f")")))
                xdir="$dir/x_$(basename "$f")"; mkdir -p "$xdir"
                extract_to "$f" "$xdir"
                scan_dir_tokens "$xdir"
                hits=$((hits + $(scan_dir_tokens "$xdir" | grep -c . || true)))
                ;;
            *.json)
                jsons=$((jsons + 1))
                h="$(token_hits "$(cat "$f" 2>/dev/null || true)")"
                if [ -n "$h" ]; then
                    echo "   $(basename "$f"): $h"
                    hits=$((hits + 1))
                fi
                ;;
        esac
    done
    echo "-- $repo @ $tag: 归档 $archives 个 / JSON 资产 $jsons 个"
    echo "   A) .git/ 条目数 = $gitn (期望 0)"
    echo "   C) token 命中文件数 = $hits (期望 0;JSON 资产 0 个时本项不可判)"
done

echo
echo "################ B: Actions cache 条目形状 ################"
for repo in "${REPOS[@]}"; do
    tsv="$(gh api "repos/$repo/actions/caches" \
        --jq '.actions_caches[] | [.key,.ref,(.size_in_bytes|tostring)] | @tsv' 2>/dev/null || true)"
    printf -- '-- %s: %s 条缓存\n' "$repo" "$(printf '%s\n' "$tsv" | grep -c . || true)"
    printf '%s\n' "$tsv" | awk -F'\t' '$2 ~ /^refs\/pull\// {printf "   refs/pull 写入: %s @ %s\n", $1, $2}'
    echo "   判定: $(cache_verdict "$tsv")"
done
