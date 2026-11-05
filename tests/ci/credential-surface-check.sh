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

# cred_hits <文本> —— 命中**凭据载体**形态的字样(真正的泄漏信号)。
#   x-access-token : checkout 把 token 写进 .git/config 的 http.extraheader,
#                    解码后的值即此形;
#   extraheader    : .git/config 里那个键名本身;
#   .git/config    : 凭据落盘的文件路径。
# 注意:`GITHUB_TOKEN` 只是**变量名** —— 源码 tarball 天然含本仓 .github/workflows/,
# 那里出现 `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}` 是变量引用,不是泄漏;
# 故它单列一栏(name_hits),不进凭据计数。
cred_hits() {
    printf '%s\n' "$1" | grep -aoE 'x-access-token|extraheader|\.git/config' | sort -u | tr '\n' ' ' | sed 's/ $//' || true
}

# name_hits <文本> —— 命中变量名 `GITHUB_TOKEN`(信息性,不构成泄漏判定)。
name_hits() {
    printf '%s\n' "$1" | grep -aoE 'GITHUB_TOKEN' | sort -u | tr '\n' ' ' | sed 's/ $//' || true
}

# pull_ref_count <cache TSV:key<TAB>ref<TAB>size> —— `refs/pull/*` 写入的条目数。
pull_ref_count() {
    printf '%s\n' "$1" | awk -F'\t' '$2 ~ /^refs\/pull\// {n++} END {print n+0}'
}

# cache_verdict <cache TSV> —— 存在 PR 可达 job 写入的条目即 POISONABLE。
cache_verdict() {
    if [ "$(pull_ref_count "$1")" -gt 0 ]; then echo POISONABLE; else echo NO_PULL_WRITE; fi
}

# asset_kind <文件名> —— 资产归类。归档按 zip/tar 列举条目;`json` 覆盖 SBOM
# (`*.sbom.json`)与 goreleaser 的 SLSA provenance(`*.intoto.jsonl`)—— 只认
# `*.json` 会把 provenance 整类漏扫,而 banner 仍声称扫过它。
asset_kind() {
    case "$1" in
        *.zip|*.tar.gz|*.tgz)                  echo archive ;;
        *.json|*.jsonl|*.intoto.jsonl)         echo json ;;
        *)                                     echo other ;;
    esac
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
    st "" "$(cred_hits 'plain manifest without secrets')" "凭据载体:干净文本 → 空"
    st "GITHUB_TOKEN" "$(name_hits 'a GITHUB_TOKEN b x-access-token c GITHUB_TOKEN')" "变量名:去重后的变量名列表"
    clean_cache='setup-go-Linux-x64-ubuntu24-go-1.26.10-abc	refs/heads/main	103577698'
    poison_cache='setup-go-Linux-x64-ubuntu24-go-1.25.14-abc	refs/pull/36/merge	67443011'
    st 0 "$(pull_ref_count "$clean_cache")" "cache: 只有 refs/heads → 0 个 PR 写入条目"
    st 1 "$(pull_ref_count "$poison_cache")" "cache: refs/pull/36/merge → 1 个 PR 写入条目"
    st NO_PULL_WRITE "$(cache_verdict "$clean_cache")" "cache 判定:无 PR 写入 → NO_PULL_WRITE"
    st POISONABLE "$(cache_verdict "$poison_cache")" "cache 判定:有 PR 写入 → POISONABLE"

    # 资产分类:I2 —— goreleaser 的 provenance 是 `*.intoto.jsonl`、SBOM 是
    # `*.sbom.json`;只认 `*.json` 会把 provenance 整类漏掉(banner 却声称扫了它)。
    st archive "$(asset_kind x.plugin.zip)" "资产分类:plugin.zip → 归档"
    st json "$(asset_kind x.intoto.jsonl)" "资产分类:intoto.jsonl(provenance)→ JSON"
    st json "$(asset_kind x.sbom.json)" "资产分类:sbom.json → JSON"
    st other "$(asset_kind checksums.txt)" "资产分类:其它 → other"

    # 凭据载体 vs 变量名:`GITHUB_TOKEN` 出现在源码 tarball 的 workflow 文件里是
    # **正常**的(那是变量引用),不是泄漏;真正的信号是 checkout 写进 .git/config
    # 的凭据载体形态(x-access-token / extraheader / .git/config 本身)。
    st "x-access-token" "$(cred_hits 'a x-access-token b x-access-token')" "凭据载体:x-access-token 去重命中"
    st "" "$(cred_hits 'GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}')" "凭据载体:变量名引用不算命中"
    st "GITHUB_TOKEN" "$(name_hits 'GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}')" "变量名:单列一栏(信息性)"

    # I1:release 资产下载失败 → 必须报 GAP,不得渲染成「0 / 期望 0」的干净结论。
    fakebin="$(mktemp -d)"
    cat >"$fakebin/gh" <<'EOG'
#!/usr/bin/env bash
case "$*" in
    *"release list"*)     echo "v9.9.9" ;;
    *"release download"*) exit 1 ;;
    *)                    exit 1 ;;
esac
EOG
    chmod +x "$fakebin/gh"
    out="$(PATH="$fakebin:$PATH" bash "$0" --repo Daedalusys/daedalus-plugins 2>&1 || true)"
    st 1 "$(printf '%s' "$out" | grep -c 'GAP')" "资产下载失败 → 报 GAP(不得当作干净结论)"
    st 0 "$(printf '%s' "$out" | grep -c '条目数')" "资产下载失败 → 不输出 A) 计数行(避免读成 PASS)"
    rm -rf "$fakebin"

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

# scan_dir_tokens <目录> —— 打印「相对路径: 凭据载体=[…] 变量名=[…]」;两者皆无则无输出。
# -a:Go 二进制里若含该字样也要看见(二进制按文本处理)。
scan_dir_tokens() {
    local dir="$1" f rel c n
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        rel="${f#"$dir"/}"
        c="$(cred_hits "$(grep -aEo 'x-access-token|extraheader|\.git/config' "$f" 2>/dev/null || true)")"
        n="$(name_hits "$(grep -aEo 'GITHUB_TOKEN' "$f" 2>/dev/null || true)")"
        [ -n "$c" ] || [ -n "$n" ] || continue
        printf '   %s: 凭据载体=[%s] 变量名=[%s]\n' "$rel" "$c" "$n"
    done < <(grep -ralE 'x-access-token|extraheader|GITHUB_TOKEN' "$dir" 2>/dev/null || true)
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
    # 下载失败必须显式记 GAP:否则零资产会被渲染成「A) .git/ 条目数 = 0(期望 0)」,
    # 读起来像 PASS —— 那是把网络故障伪装成"干净"。
    if ! gh release download "$tag" --repo "$repo" --dir "$dir" --clobber >/dev/null 2>&1; then
        echo "-- $repo @ $tag: 资产下载失败 → GAP(凭据面不可判定,不得读成干净)"
        continue
    fi
    gitn=0; archives=0; jsons=0; creds=0; names=0
    echo "-- $repo @ $tag:"
    for f in "$dir"/*; do
        [ -f "$f" ] || continue
        case "$(asset_kind "$f")" in
            archive)
                archives=$((archives + 1))
                gitn=$((gitn + $(git_entry_count "$(list_entries "$f")")))
                xdir="$dir/x_$(basename "$f")"; mkdir -p "$xdir"
                extract_to "$f" "$xdir"
                sout="$(scan_dir_tokens "$xdir")"
                [ -z "$sout" ] || printf '%s\n' "$sout"
                creds=$((creds + $(printf '%s' "$sout" | grep -c '凭据载体=\[[^]]' || true)))
                names=$((names + $(printf '%s' "$sout" | grep -c 'GITHUB_TOKEN' || true)))
                ;;
            json)
                jsons=$((jsons + 1))
                c="$(cred_hits "$(grep -aEo 'x-access-token|extraheader|\.git/config' "$f" 2>/dev/null || true)")"
                n="$(name_hits "$(grep -aEo 'GITHUB_TOKEN' "$f" 2>/dev/null || true)")"
                if [ -n "$c" ] || [ -n "$n" ]; then
                    printf '   %s: 凭据载体=[%s] 变量名=[%s]\n' "$(basename "$f")" "$c" "$n"
                fi
                [ -z "$c" ] || creds=$((creds + 1))
                [ -z "$n" ] || names=$((names + 1))
                ;;
        esac
    done
    echo "   A) .git/ 条目数 = $gitn (期望 0);归档 $archives 个"
    echo "   C) 凭据载体命中文件数 = $creds (期望 0);JSON(含 provenance / SBOM)资产 $jsons 个;变量名 GITHUB_TOKEN 出现文件数 = $names (信息性,源码引用/变量的正常出现)"
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
