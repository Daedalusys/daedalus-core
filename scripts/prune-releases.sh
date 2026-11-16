#!/usr/bin/env bash
# Release 保留策略:只留最新 N 个(sha 形态的 tag),其余连 git tag 一起删。
#
# 为什么需要:core 每推一次 main 产出一个 release(qcow2 1.27GB + ISO 1.46GB)。
# 保留策略靠人记得清 —— 2026-11-16 早上手工从 31 清到 2,当天又长回 6(11.7 GB)。
# 手工保留策略等于没有。
#
# 安全边界(宁可不删,也不删错):
#   1. gh 读不出列表 → 失败退出;绝不把「读失败」当成「没有可删的」;
#   2. 只删 sha 形态的 tag(^[0-9a-f]{7,40}$)—— 人手建的语义版本号永不碰;
#   3. draft / prerelease 永不删;
#   4. 按创建时间倒序,最新 N 个永不删;
#   5. 待删数量超过上限(默认 50)视为配置或解析出错,拒绝执行。
#
# 用法:
#   prune-releases.sh --keep N --repo OWNER/NAME [--dry-run] [--max-delete M]
# 退出码:
#   0  全部按计划(dry-run 也算)
#   1  gh 列表失败 / 待删超上限 / 有删除失败
#   2  参数错
set -uo pipefail

KEEP=0
REPO=""
DRY=0
MAX_DELETE=50
while [ $# -gt 0 ]; do
    case "$1" in
        --keep)       [ -n "${2:-}" ] || { echo "--keep 需要整数" >&2; exit 2; }; KEEP="$2"; shift 2 ;;
        --keep=*)     KEEP="${1#*=}"; shift ;;
        --repo)       [ -n "${2:-}" ] || { echo "--repo 需要 OWNER/NAME" >&2; exit 2; }; REPO="$2"; shift 2 ;;
        --repo=*)     REPO="${1#*=}"; shift ;;
        --max-delete) [ -n "${2:-}" ] || { echo "--max-delete 需要整数" >&2; exit 2; }; MAX_DELETE="$2"; shift 2 ;;
        --max-delete=*) MAX_DELETE="${1#*=}"; shift ;;
        --dry-run)    DRY=1; shift ;;
        -h|--help)    sed -n '2,23p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done
case "$KEEP" in ''|*[!0-9]*) echo "--keep 必须是整数" >&2; exit 2 ;; esac
case "$MAX_DELETE" in ''|*[!0-9]*) echo "--max-delete 必须是整数" >&2; exit 2 ;; esac
[ "$KEEP" -ge 1 ] || { echo "--keep 必须 ≥ 1(0 会把当前出货的 Release 也删掉)" >&2; exit 2; }
[ -n "$REPO" ] || { echo "--repo 必填(形如 OWNER/NAME)" >&2; exit 2; }

# 读不出列表 ≠ 没有可删的:读取失败必须失败退出,否则一次 API 抖动就会让
# 「保留 N」这个承诺静默失效 —— 而它失效的代价是 GB 级资产长期堆积。
if ! raw="$(gh release list --repo "$REPO" --limit 200 \
        --json tagName,createdAt,isDraft,isPrerelease \
        --jq '.[] | [.createdAt, .tagName, (.isDraft|tostring), (.isPrerelease|tostring)] | @tsv')"; then
    echo "ERROR: gh release list 失败,拒绝在未知状态下删任何东西" >&2
    exit 1
fi

total=0; kept=0; sha_kept=0
candidates=()
while IFS=$'\t' read -r created tag draft pre; do
    [ -n "${tag:-}" ] || continue
    total=$((total + 1))
    if ! printf '%s' "$tag" | grep -Eq '^[0-9a-f]{7,40}$'; then
        printf 'KEEP(非 sha tag)\t%s\t%s\n' "$tag" "$created"
        kept=$((kept + 1)); continue
    fi
    if [ "$draft" = "true" ] || [ "$pre" = "true" ]; then
        printf 'KEEP(draft/prerelease)\t%s\t%s\n' "$tag" "$created"
        kept=$((kept + 1)); continue
    fi
    if [ "$sha_kept" -lt "$KEEP" ]; then
        printf 'KEEP(最新 %s 个之内)\t%s\t%s\n' "$KEEP" "$tag" "$created"
        kept=$((kept + 1)); sha_kept=$((sha_kept + 1)); continue
    fi
    candidates+=("$tag")
done <<<"$(printf '%s\n' "$raw" | sort -r)"

del_n=${#candidates[@]}
if [ "$del_n" -gt "$MAX_DELETE" ]; then
    echo "ERROR: 待删 $del_n 个 > 上限 $MAX_DELETE,拒绝执行(先核对列表)" >&2
    exit 1
fi

fail_n=0
for tag in "${candidates[@]:-}"; do
    [ -n "$tag" ] || continue
    if [ "$DRY" = 1 ]; then printf 'WOULD DELETE\t%s\n' "$tag"; continue; fi
    if gh release delete "$tag" --yes --cleanup-tag --repo "$REPO"; then
        printf 'DELETED\t%s\n' "$tag"
    else
        printf 'ERROR: 删除 %s 失败\n' "$tag" >&2
        fail_n=$((fail_n + 1))
    fi
done
printf 'prune-releases: 共 %d 个,保留 %d 个,删除 %d 个,失败 %d 个(dry-run=%d)\n' \
    "$total" "$kept" "$del_n" "$fail_n" "$DRY"
[ "$fail_n" -eq 0 ]
