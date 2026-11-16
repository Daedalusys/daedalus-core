#!/usr/bin/env bash
# net-retry.sh 的自检:有界(超时真被砍)、重试(次数可数)、参数守门(非法即拒跑)。
# 全用本地脚本当「被执行的命令」,不联网、不碰 podman、不需要 KVM。
set -uo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/net-retry.sh"

fail=0
st() { if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"
       else printf '  FAIL  %s (期望 %s,实际 %s)\n' "$3" "$1" "$2"; fail=$((fail + 1)); fi }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# 计数脚本:每被调用一次就把计数 +1 写进 $1。第 2 次起返回 0(用于「先失败后成功」)。
cat > "$tmp/flaky.sh" <<'EOS'
#!/usr/bin/env bash
f="$1"; n=0
[ -f "$f" ] && n="$(cat "$f")"
n=$((n + 1)); printf '%s\n' "$n" > "$f"
[ "$n" -ge 2 ] || exit 7
exit 0
EOS
# 永远失败的脚本:每次调用都计数,然后以 9 退出。
cat > "$tmp/always-fail.sh" <<'EOS'
#!/usr/bin/env bash
f="$1"; n=0
[ -f "$f" ] && n="$(cat "$f")"
n=$((n + 1)); printf '%s\n' "$n" > "$f"
exit 9
EOS
chmod +x "$tmp/flaky.sh" "$tmp/always-fail.sh"

calls() { [ -f "$1" ] && cat "$1" || printf '0\n'; }

# 1) 第一次失败、第二次成功 → 返回 0,且恰好调用 2 次(用计数文件证明,不靠输出猜)
c1="$tmp/c1"
rc=0; net_retry 5 3 "$tmp/flaky.sh" "$c1" >/dev/null 2>&1 || rc=$?
st 0 "$rc" "先失败后成功 → 返回 0"
st 2 "$(calls "$c1")" "先失败后成功 → 恰好调用 2 次"

# 2) 全部失败 → 返回非 0(最后一次的退出码),调用次数 == 尝试次数
c2="$tmp/c2"
rc=0; net_retry 5 2 "$tmp/always-fail.sh" "$c2" >/dev/null 2>&1 || rc=$?
st 9 "$rc" "全部失败 → 返回最后一次的退出码 9"
st 2 "$(calls "$c2")" "全部失败 → 调用次数等于尝试次数"

# 3) 命令挂住(sleep 30)配 1 秒预算 → 124(证明真被砍,而不是等满 30 秒)
t0="$(date +%s)"
rc=0; net_retry 1 1 sleep 30 >/dev/null 2>&1 || rc=$?
t1="$(date +%s)"
st 124 "$rc" "挂住的命令 → 124(timeout 退出码)"
st 1 "$([ $((t1 - t0)) -lt 10 ] && echo 1 || echo 0)" "挂住的命令 → 被砍掉而不是跑满 30s(用时 $((t1 - t0))s)"

# 4) 参数错 → 返回 2,且一条命令都不许执行(计数文件证明为 0)
c4="$tmp/c4"
rc=0; net_retry 5 2 >/dev/null 2>&1 || rc=$?
st 2 "$rc" "只给 2 个参数 → 2"
rc=0; net_retry 5 >/dev/null 2>&1 || rc=$?
st 2 "$rc" "只给 1 个参数 → 2"
rc=0; net_retry abc 3 "$tmp/always-fail.sh" "$c4" >/dev/null 2>&1 || rc=$?
st 2 "$rc" "预算非整数 → 2"
st 0 "$(calls "$c4")" "参数非法 → 一条命令都没执行(计数为 0)"
rc=0; net_retry 5 xyz "$tmp/always-fail.sh" "$c4" >/dev/null 2>&1 || rc=$?
st 2 "$rc" "尝试次数非整数 → 2"
st 0 "$(calls "$c4")" "次数非法 → 一条命令都没执行(计数仍为 0)"

# 5) 诊断走 stderr(不污染 stdout):调用方只取 stdout 时不能混进重试日志
c5="$tmp/c5"
out="$tmp/out5"; err="$tmp/err5"
net_retry 5 2 "$tmp/always-fail.sh" "$c5" >"$out" 2>"$err"
st 0 "$(wc -c <"$out" | tr -d ' ')" "重试诊断不进 stdout"
st 1 "$([ -s "$err" ] && echo 1 || echo 0)" "重试诊断写在 stderr"

printf '\n'
if [ "$fail" -eq 0 ]; then echo "RESULT: SELFTEST PASS"; exit 0; fi
echo "RESULT: SELFTEST FAIL ($fail)"; exit 1
