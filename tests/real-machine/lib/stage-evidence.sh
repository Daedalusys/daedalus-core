#!/usr/bin/env bash
# 从一次 run 的工作目录里挑出「证据」(小文本),把磁盘镜像排除在外。
#
# 为什么需要:实机腿的证据上传曾把**整个 run 工作目录**传上去 ——
# switch-target.tar 1.3GB / target-disk.qcow2 2.3GB / a2-source.tar 1.3GB,
# 每轮各一份、30 天保留 → 2026-11-16 实测 GitHub 上积了 371 GB / 187 个 artifact
# (core 全量,翻完所有分页)。而证据本身全是 1.5KB–102KB 的日志。
#
# 筛选口径是**大小上限**(默认 8 MiB),不是扩展名白名单:不同腿的日志名各不相同
# (probe-*.log / serial-*.log / bootc-switch-verify.log),按名字筛必然漏;
# 磁盘则一定大于上限。被跳过的文件逐条记进 MANIFEST.txt,所以「什么没传、
# 为什么没传」是可审计的,而不是静默丢。
#
# 用法:
#   stage-evidence.sh <src-dir> <dst-dir> [--max-bytes N]
# 退出码:
#   0  至少挑到 1 个文件
#   1  src 不存在,或一个都没挑到
#   2  参数错
set -uo pipefail

MAX_BYTES=8388608
SRC=""
DST=""
while [ $# -gt 0 ]; do
    case "$1" in
        --max-bytes)
            # 值缺失时立刻拒跑:后面的 shift 2 在 $#=1 时会原地打转
            [ -n "${2:-}" ] || { echo "--max-bytes 需要整数" >&2; exit 2; }
            MAX_BYTES="$2"; shift 2 ;;
        --max-bytes=*) MAX_BYTES="${1#*=}"; shift ;;
        -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)
            if [ -z "$SRC" ]; then SRC="$1"
            elif [ -z "$DST" ]; then DST="$1"
            else echo "多余的参数: $1" >&2; exit 2
            fi
            shift ;;
    esac
done
case "$MAX_BYTES" in ''|*[!0-9]*) echo "--max-bytes 必须是整数" >&2; exit 2 ;; esac
[ -n "$SRC" ] && [ -n "$DST" ] || {
    echo "用法: stage-evidence.sh <src-dir> <dst-dir> [--max-bytes N]" >&2; exit 2; }
[ -d "$SRC" ] || { echo "stage-evidence: src 不存在: $SRC" >&2; exit 1; }

SRC_ABS="$(cd "$SRC" && pwd)"
mkdir -p "$DST" || { echo "stage-evidence: 建不出 dst: $DST" >&2; exit 1; }
DST_ABS="$(cd "$DST" && pwd)"
MANIFEST="$DST_ABS/MANIFEST.txt"

copied=0; copied_bytes=0; skipped=0; skipped_bytes=0
{
    printf '# stage-evidence manifest\n'
    printf '# src: %s\n' "$SRC_ABS"
    printf '# max_bytes: %s\n' "$MAX_BYTES"
    printf '# 格式: <动作>\t<字节>\t<相对路径>\n'
} >"$MANIFEST"

# -type f 天然排除符号链接;不用 -L,所以不跟链接目录。
# dst 在 src 里面时(workflow 的真实形态)跳过 dst 子树,否则会把自己的 MANIFEST 再收一遍。
while IFS= read -r -d '' f; do
    case "$f" in "$DST_ABS"/*) continue ;; esac
    rel="${f#"$SRC_ABS"/}"
    size="$(stat -c %s "$f" 2>/dev/null)" || continue
    if [ "$size" -le "$MAX_BYTES" ]; then
        mkdir -p "$DST_ABS/$(dirname "$rel")" || continue
        if cp -p "$f" "$DST_ABS/$rel"; then
            printf 'COPIED\t%s\t%s\n' "$size" "$rel" >>"$MANIFEST"
            copied=$((copied + 1)); copied_bytes=$((copied_bytes + size))
        fi
    else
        printf 'SKIPPED\t%s\t%s\n' "$size" "$rel" >>"$MANIFEST"
        skipped=$((skipped + 1)); skipped_bytes=$((skipped_bytes + size))
    fi
done < <(find "$SRC_ABS" -maxdepth 4 -type f -print0 2>/dev/null)

printf 'stage-evidence: 收 %d 个文件 %d 字节,跳过 %d 个 %d 字节(上限 %d)\n' \
    "$copied" "$copied_bytes" "$skipped" "$skipped_bytes" "$MAX_BYTES"
[ "$copied" -gt 0 ] || { echo "stage-evidence: 一个文件都没收到(证据缺失)" >&2; exit 1; }
exit 0
