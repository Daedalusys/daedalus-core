#!/usr/bin/env bash
# artifact-fingerprint.sh 的判定逻辑自检:不碰 qemu、不需要 KVM、不需要真镜像。
set -euo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/artifact-fingerprint.sh"

fail=0
st() { if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
       else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail=$((fail + 1)); fi }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fp="$tmp/fp"

# 1) 无指纹文件 → 不匹配(必须触发重建,不能当一致)
if artifact_fp_match "$fp" "sha256:aaa" 2>/dev/null; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹文件不存在 → 不匹配"

# 2) 写入后可读回、且匹配
artifact_fp_write "$fp" "sha256:aaa"
st "sha256:aaa" "$(cat "$fp")" "写入后可读回"
if artifact_fp_match "$fp" "sha256:aaa"; then rc=0; else rc=$?; fi
st 0 "$rc" "指纹一致 → 匹配"

# 3) 镜像变了 → 不匹配
if artifact_fp_match "$fp" "sha256:bbb"; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹不同 → 不匹配"

# 4) 指纹文件为空 → 不匹配(损坏不能 fail-open)
: > "$fp"
if artifact_fp_match "$fp" "sha256:aaa"; then rc=0; else rc=$?; fi
st 1 "$rc" "指纹文件为空 → 不匹配"

# 5) 写入是原子的:tmp 文件不残留
artifact_fp_write "$fp" "sha256:ccc"
st 0 "$(ls "$tmp" | grep -c '\.tmp$' || true)" "写入后无 .tmp 残留"

# 6) describe 对不可读文件给出可读占位
st "<无>" "$(artifact_fp_describe "$tmp/nonexistent")" "describe 对缺失文件 → <无>"

if [ "$fail" -eq 0 ]; then echo "RESULT: ARTIFACT-FP SELFTEST PASS"; exit 0; fi
echo "RESULT: ARTIFACT-FP SELFTEST FAIL ($fail)"; exit 1
