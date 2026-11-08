#!/usr/bin/env bash
# cleanup-verify-work.sh 的自检:保留数必须有界,且绝不误删非目标目录。
set -euo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SUT="$HERE/cleanup-verify-work.sh"

fail=0
st() { if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
       else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail=$((fail + 1)); fi }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/verify-out-1" "$tmp/verify-out-2" "$tmp/verify-out-3" \
         "$tmp/verify-out-4" "$tmp/unrelated-dir"

# 1) 保留最新 2 个,其余删掉
bash "$SUT" --keep 2 "$tmp" verify-out- >/dev/null
st 2 "$(find "$tmp" -maxdepth 1 -type d -name 'verify-out-*' | wc -l | tr -d ' ')" "只保留最新 2 个"

# 2) 非目标目录必须原样在
st 1 "$([ -d "$tmp/unrelated-dir" ] && echo 1 || echo 0)" "不碰非目标目录"

# 3) dry-run 不删任何东西
mkdir -p "$tmp/verify-out-5" "$tmp/verify-out-6"
before="$(find "$tmp" -maxdepth 1 -type d -name 'verify-out-*' | wc -l | tr -d ' ')"
bash "$SUT" --keep 1 --dry-run "$tmp" verify-out- >/dev/null
st "$before" "$(find "$tmp" -maxdepth 1 -type d -name 'verify-out-*' | wc -l | tr -d ' ')" "dry-run 不删"

# 4) 目录少于保留数 → 一个都不删
mkdir -p "$tmp/few-1"
bash "$SUT" --keep 5 "$tmp" few- >/dev/null
st 1 "$(find "$tmp" -maxdepth 1 -type d -name 'few-*' | wc -l | tr -d ' ')" "不足保留数 → 不删"

# 5) 不给前缀 → 拒跑(防止把根目录下所有东西当目标)
rc=0; bash "$SUT" --keep 1 "$tmp" >/dev/null 2>&1 || rc=$?
st 2 "$rc" "缺前缀 → 拒跑"

# 6) --keep 非整数 → 拒跑
rc=0; bash "$SUT" --keep abc "$tmp" verify-out- >/dev/null 2>&1 || rc=$?
st 2 "$rc" "--keep 非整数 → 拒跑"

# 7) --keep 不带值 → 拒跑(不能死循环:shift 2 在只剩一个参数时会失败)
rc=0
timeout 5 bash "$SUT" --keep "$tmp" verify-out- >/dev/null 2>&1 || rc=$?
st 2 "$rc" "--keep 不带值 → 拒跑(且不死循环)"

# 8) 空前缀 → 拒跑(空串会让 -name \"*\" 匹配到根目录下每一个目录)
rc=0; timeout 5 bash "$SUT" --keep 1 "$tmp" "" >/dev/null 2>&1 || rc=$?
st 2 "$rc" "空前缀 → 拒跑"

# 9) 前缀含通配符 → 拒跑(会越过前缀边界去匹配别的东西)
rc=0; timeout 5 bash "$SUT" --keep 1 "$tmp" "verify-*" >/dev/null 2>&1 || rc=$?
st 2 "$rc" "前缀含通配符 → 拒跑"

# 10) 根目录不存在 → 拒跑
rc=0; bash "$SUT" --keep 1 "$tmp/nonexistent" verify-out- >/dev/null 2>&1 || rc=$?
st 2 "$rc" "根目录不存在 → 拒跑"

# 11) --keep 0 → 拒跑(0 会把当前 run 自己的目录也删掉)
rc=0; timeout 5 bash "$SUT" --keep 0 "$tmp" verify-out- >/dev/null 2>&1 || rc=$?
st 2 "$rc" "--keep 0 → 拒跑(不能删当前 run 自己的目录)"

# 12) 删不掉时不得报成功:盘满是最需要这条的时候
#     用 PATH 上的假 rm 制造失败(真机上 chmod 拦不住 rm,父目录可写就能删)。
mkdir -p "$tmp/ro-1" "$tmp/ro-2" "$tmp/ro-3" "$tmp/fakebin"
printf '#!/bin/sh\nexit 1\n' > "$tmp/fakebin/rm"; chmod +x "$tmp/fakebin/rm"
rc=0
PATH="$tmp/fakebin:$PATH" timeout 10 bash "$SUT" --keep 1 "$tmp" ro- >/dev/null 2>&1 || rc=$?
st 1 "$rc" "有目录删不掉 → 非零退出"
st 1 "$([ -d "$tmp/ro-3" ] && echo 1 || echo 0)" "删不掉时目录确实还在(不是假装删了)"

# 13) 清理正常时退出 0
mkdir -p "$tmp/ok-1" "$tmp/ok-2"
rc=0; bash "$SUT" --keep 1 "$tmp" ok- >/dev/null 2>&1 || rc=$?
st 0 "$rc" "清理成功 → 退出 0"

printf '\n'
if [ "$fail" -eq 0 ]; then echo "RESULT: CLEANUP SELFTEST PASS"; exit 0; fi
echo "RESULT: CLEANUP SELFTEST FAIL ($fail)"; exit 1
