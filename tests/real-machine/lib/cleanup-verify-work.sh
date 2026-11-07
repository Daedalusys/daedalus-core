#!/usr/bin/env bash
# 清理自托管 runner 上按 run 隔离留下的旧工作目录。
#
# 为什么需要:实机腿的 VERIFY_WORK 带 run_id(跨 run 不串味),代价是每次 run 都会
# 留下一个含 qcow2 的目录。runner 是持久化的,盘被历史产物吃满之后,qemu 建不出
# 磁盘、podman 存不下镜像 —— 门禁会以"镜像坏了"的样子红,而真正的原因是磁盘。
# 所以保留数必须在 runner 侧有界,而不是指望人记得清。
#
# 用法:
#   cleanup-verify-work.sh [--keep N] [--dry-run] <根目录> [<前缀> ...]
#   默认 keep=2,dry-run 默认关;不给前缀时清 `<前缀>*` 里除最新 N 个之外的目录。
set -uo pipefail

KEEP=2
DRY_RUN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --keep)      KEEP="${2:-}"; shift 2 ;;
        --keep=*)    KEEP="${1#*=}"; shift ;;
        --dry-run)   DRY_RUN=1; shift ;;
        -h|--help)   sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)           break ;;
    esac
done

case "$KEEP" in ''|*[!0-9]*) echo "--keep 必须是整数" >&2; exit 2 ;; esac
ROOT="${1:-}"; shift || true
[ -n "$ROOT" ] || { echo "用法: cleanup-verify-work.sh [--keep N] [--dry-run] <根目录> [<前缀> ...]" >&2; exit 2; }
[ -d "$ROOT" ] || { echo "根目录不存在: $ROOT" >&2; exit 2; }
[ $# -gt 0 ] || { echo "至少要给一个前缀(否则会删到不该删的目录)" >&2; exit 2; }

removed=0
kept=0
for prefix in "$@"; do
    # 按 mtime 新→旧;目录名含 run_id,但 run_id 不保证单调(重跑/回填),用 mtime。
    mapfile -t dirs < <(
        find "$ROOT" -mindepth 1 -maxdepth 1 -type d -name "${prefix}*" \
             -printf '%T@\t%p\n' 2>/dev/null | sort -rn | cut -f2-
    )
    i=0
    for d in "${dirs[@]}"; do
        i=$((i + 1))
        if [ "$i" -le "$KEEP" ]; then
            kept=$((kept + 1))
            continue
        fi
        if [ "$DRY_RUN" = 1 ]; then
            printf 'would remove %s\n' "$d"
        else
            rm -rf -- "$d" && printf 'removed %s\n' "$d"
        fi
        removed=$((removed + 1))
    done
done

printf 'cleanup: removed=%d kept=%d keep=%d root=%s\n' "$removed" "$kept" "$KEEP" "$ROOT"
