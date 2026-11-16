#!/usr/bin/env bash
# stage-evidence.sh 的行为自检。
#
# 为什么必须有:它是「证据包不再上传磁盘」的唯一执行点。两种退化都不会有人天天看 ——
#   退化 A(fail-open):大小判断失效 → 206 GB 的 artifact 一夜之间长回来;
#   退化 B(fail-closed):把日志也当大文件跳过 → 失败现场没了,门禁红得没有线索。
# 所以这里同时钉「大文件不进」与「小文件必进」两个方向。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
STAGE="$ROOT/tests/real-machine/lib/stage-evidence.sh"

PASS_N=0; FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}
ok() { # ok <退出码> <说明>(期望 0)
    if [ "$1" -eq 0 ]; then printf '  PASS  %s\n' "$2"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (退出码 %s)\n' "$2" "$1"; FAIL_N=$((FAIL_N + 1)); fi
}

printf '== stage-evidence.selftest.sh ==\n'
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 造一个仿真 run 工作目录:9 MiB 的「磁盘」、100 B 的日志、嵌套日志、符号链接、正好 8 MiB 的边界文件
SRC="$TMP/switch-verify-1"
mkdir -p "$SRC/nested"
truncate -s 9M "$SRC/switch-target.tar"
printf 'line one\n' >"$SRC/serial-console.log"
printf 'nested log\n' >"$SRC/nested/probe-x.log"
truncate -s 8M "$SRC/edge.bin"
ln -s "$SRC/serial-console.log" "$SRC/link.log"

# 1) 正常路径:小文件进、大文件跳过、符号链接不跟
rc=0; out="$("$STAGE" "$SRC" "$TMP/out1" 2>&1)" || rc=$?
ok "$rc" "src 有证据 → 退出码 0"
[ -f "$TMP/out1/serial-console.log" ] && r=0 || r=1; st 0 "$r" "小日志被收进 dst"
[ -f "$TMP/out1/nested/probe-x.log" ] && r=0 || r=1; st 0 "$r" "嵌套小日志保留相对路径"
[ -f "$TMP/out1/edge.bin" ] && r=0 || r=1; st 0 "$r" "正好等于上限(8 MiB)的文件被收"
[ -e "$TMP/out1/switch-target.tar" ] && r=1 || r=0; st 0 "$r" "9 MiB 的磁盘载荷没进 dst"
[ -e "$TMP/out1/link.log" ] && r=1 || r=0; st 0 "$r" "符号链接不跟(-type f 排除)"
st 1 "$(grep -c '^SKIPPED' "$TMP/out1/MANIFEST.txt")" "MANIFEST 记下了被跳过的文件"
st 1 "$(grep -c 'switch-target.tar' "$TMP/out1/MANIFEST.txt")" "MANIFEST 里那条 SKIPPED 就是它"
case "$out" in *"跳过 1 个 9437184 字节"*) r=0 ;; *) r=1 ;; esac; st 0 "$r" "摘要打印跳过字节数"

# 2) dst 在 src 里面(workflow 的真实形态):不许把自己的 MANIFEST 再收一遍
rc=0; "$STAGE" "$SRC" "$SRC/evidence" >/dev/null 2>&1 || rc=$?
ok "$rc" "dst 在 src 里面也能跑"
[ -e "$SRC/evidence/evidence" ] && r=1 || r=0; st 0 "$r" "不递归拷贝 dst 自身"

# 3) 上限压到 10 字节:一个都收不到 → 退出码 1(证据缺失必须显性)
#    用独立目录:上面那次在 $SRC 里留下了 evidence/ 子目录,里面的小文件会
#    让「一个都收不到」这个前提不成立(计划原文复用 $SRC,在真实仓里必然假绿)。
BIGONLY="$TMP/bigonly"
mkdir -p "$BIGONLY"
truncate -s 9M "$BIGONLY/target-disk.qcow2"
rc=0; "$STAGE" "$BIGONLY" "$TMP/out3" --max-bytes 10 >/dev/null 2>&1 || rc=$?
st 1 "$rc" "一个文件都没收到 → 退出码 1"

# 4) src 不存在 → 退出码 1
rc=0; "$STAGE" "$TMP/nope" "$TMP/out4" >/dev/null 2>&1 || rc=$?
st 1 "$rc" "src 不存在 → 退出码 1"

# 5) 参数错 → 退出码 2
rc=0; "$STAGE" "$SRC" "$TMP/out5" --max-bytes abc >/dev/null 2>&1 || rc=$?
st 2 "$rc" "--max-bytes 非整数 → 退出码 2"

if [ "$FAIL_N" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
echo "RESULT: SELFTEST FAIL ($FAIL_N)"; exit 1
