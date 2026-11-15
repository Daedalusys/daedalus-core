#!/usr/bin/env bash
# =============================================================================
# probe-copilot-sandbox.sh —— copilot 进 OS 层沙箱的技术探针(core#117,S1–S5)
# =============================================================================
# 只回答「能不能」,不改产品代码:不写 /etc、不改单元、不装包;临时写入只落 $WORK、
# /var/tmp 与 $HOME/.local/share/daedalus(探针写完即删)。判据只认实测证据(Seccomp:
# 字段 / fd 的 tty 属性 / 系统调用返回 / 进程存活),不认 systemd-run 的返回码 —— 属性被
# 静默忽略时返回码照样是 0,那正是要抓的假绿。
#
# S1 JIT vs MemoryDenyWriteExecute + seccomp | S2 --scope 能不能带 Protect*/SystemCallFilter
# S3 TTY 与 Ctrl-C 在候选形态下是否保住     | S4 IPAddressAllow/Deny 是否真拦得住
# S5 Deno 拒绝写入时抛的 e.name(旧探针 D3 判据可信度)
#
# 非 root 时 system manager 形态(DynamicUser / BPF 过滤)记 NOTE BLOCKED,并以 user
# manager 等效形态取证;BLOCKED 绝不折算成 PASS。
# 用法: bash tests/real-machine/probe-copilot-sandbox.sh   # 输出即证据,重定向留档
# 退出码: 0 = 无 FAIL(BLOCKED 不算失败),1 = 有 FAIL。
# =============================================================================
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/probe-copilot-sandbox.XXXXXX")}"
# 反证路径必须「当前用户可写但不在允许集里」:选 /var/tmp(不在 /tmp 之下,也不在 $HOME 之下)。
OUTSIDE="/var/tmp/probe-copilot-sandbox-outside"
mkdir -p "$WORK"
IS_ROOT=0; [ "$(id -u)" -eq 0 ] && IS_ROOT=1
pass_n=0 fail_n=0 blocked_n=0

say()     { printf '\n== %s ==\n' "$*"; }
ev()      { sed 's/^/      /'; }
pass()    { pass_n=$((pass_n+1));       printf 'PASS %s\n' "$*"; }
fail()    { fail_n=$((fail_n+1));       printf 'FAIL %s\n' "$*"; }
blocked() { blocked_n=$((blocked_n+1)); printf 'NOTE BLOCKED %s\n' "$*"; }

cleanup() {
  systemctl --user reset-failed 'probe117-*' >/dev/null 2>&1 || true
  for f in "$WORK"/sig-*.innerpid; do [ -r "$f" ] && kill "$(cat "$f")" 2>/dev/null; done
  rm -rf "$OUTSIDE"
  return 0
}
trap cleanup EXIT

# ------------------------------------------------------------ 环境三元组 ------
say "环境三元组(结论绑定版本,否则不可复现)"
ENV_TRIPLE="systemd=$(systemctl --version | head -1 | awk '{print $2}') | deno=$(deno --version 2>/dev/null | head -1) | kernel=$(uname -r)"
printf '%s\n' "$ENV_TRIPLE" | ev
printf 'euid=%s root=%s work=%s\n' "$(id -u)" "$IS_ROOT" "$WORK" | ev

DENO_BIN="$(command -v deno 2>/dev/null || true)"
if [ -z "$DENO_BIN" ]; then
  blocked "S0 无 deno —— S1/S3/S5 全部无法取证"
  printf -- '---------------- %d PASS / %d FAIL / %d BLOCKED ----------------\n' "$pass_n" "$fail_n" "$blocked_n"
  printf 'RESULT: SELFTEST FAIL (1)\n'
  exit 1
fi
# deno 在本机是 asdf shim:版本随工作目录解析,故所有瞬时单元都钉 WorkingDirectory=$REPO。
urun() { timeout 40 systemd-run --user --quiet --wait --collect --pipe -p WorkingDirectory="$REPO" "$@"; }
INUNIT_VER="$(urun "$DENO_BIN" --version 2>&1 | head -1)"
printf 'unit 内 deno: %s\n' "${INUNIT_VER:-<空>}" | ev
[ -n "${INUNIT_VER:-}" ] || blocked "S0 user manager 不可用(systemd-run --user 起不来单元)—— 全部用例无法取证"

JIT='const f = new Function("a","b","return a+b"); let s=0; for (let i=0;i<2e5;i++) s=f(s,i); console.log("JIT_OK", s>0)'

# ======================================================== S1: JIT vs W^X ======
say "S1 · V8 JIT 与 MemoryDenyWriteExecute / seccomp 的兼容性(最可能否掉提议的一条)"

S1_PLAN="$(timeout 40 systemd-run --quiet --wait --collect --pipe \
  -p DynamicUser=yes -p ProtectSystem=strict -p NoNewPrivileges=yes \
  -p SystemCallFilter=@system-service -p 'SystemCallFilter=~@privileged @resources @obsolete' \
  -p MemoryDenyWriteExecute=yes "$DENO_BIN" eval "$JIT" 2>&1)"
printf 'S1-plan 计划原文(系统 manager + DynamicUser): %s\n' "$(printf '%s' "$S1_PLAN" | tr '\n' ' ' | cut -c1-140)" | ev
if printf '%s' "$S1_PLAN" | grep -q 'JIT_OK true'; then
  pass "S1-plan 计划原文形态跑通(系统 manager 下 V8 存活)"
elif [ "$IS_ROOT" = 1 ]; then
  fail "S1-plan 以 root 跑计划原文形态仍失败:$(printf '%s' "$S1_PLAN" | grep -iE 'fail|error|denied' | head -1)"
else
  blocked "S1-plan 计划原文形态需 root(实测: $(printf '%s' "$S1_PLAN" | head -1))—— 下面的 user manager 等效形态才是本机真证据"
fi

# 只开 W^X(不带 seccomp):把「V8 需要可写可执行页」单独证出来。
S1_WX="$(urun -p MemoryDenyWriteExecute=yes "$DENO_BIN" eval "$JIT" 2>&1)"
printf 'S1-W^X(只 MemoryDenyWriteExecute=yes)关键行:\n' | ev
printf '%s\n' "${S1_WX:-<无输出>}" | grep -E 'Fatal error|Check failed|JIT_OK|panicked|Version:' | ev
if printf '%s' "$S1_WX" | grep -q 'JIT_OK true'; then
  pass "S1 W^X 下 V8 JIT 存活"
else
  fail "S1 MemoryDenyWriteExecute=yes 直接杀死 V8:$(printf '%s' "$S1_WX" | grep -E 'Fatal error|Check failed' | head -1)"
fi

# 只开 seccomp(不带 W^X):默认动作是 SIGSYS,故失败不体现在字符串上,要看单元退出态。
S1_SEC_OUT="$(timeout 40 systemd-run --user --wait --pipe -p WorkingDirectory="$REPO" \
  -p SystemCallFilter=@system-service "$DENO_BIN" eval "$JIT" 2>&1)"
printf 'S1-seccomp(只 SystemCallFilter=@system-service,不 --quiet 以取退出态):\n' | ev
printf '%s\n' "$S1_SEC_OUT" | grep -E 'Main processes terminated|Finished with result|JIT_OK' | ev
printf 'unprivileged 形参提示:改 SystemCallErrorNumber=EPERM 后同一 filter → %s\n' \
  "$(urun -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM "$DENO_BIN" eval "$JIT" 2>&1 | head -1)" | ev
printf 'W^X + seccomp + SystemCallErrorNumber=EPERM(把 seccomp 致死因素摘掉后只剩 W^X):\n' | ev
urun -p MemoryDenyWriteExecute=yes -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM \
  "$DENO_BIN" eval "$JIT" 2>&1 | grep -E 'Fatal error|Check failed|JIT_OK' | ev
if printf '%s' "$S1_SEC_OUT" | grep -q 'code=dumped/status=SYS'; then
  fail "S1 纯 seccomp(@system-service)也会杀 deno:默认动作 SIGSYS(code=dumped/status=SYS);换 SystemCallErrorNumber=EPERM 即恢复 → 死因是 syscall 白名单差集,不是 W^X 单独一条"
else
  pass "S1 纯 seccomp 未额外致死(退出态见上)"
fi
# 把「差集在哪」钉到具体 syscall:deno 启动会调 pkey_alloc(V8 的 pkey 预留)。@system-service
# 的原子集合里没有它(递归展开由人工用 systemd-analyze syscall-filter 复核,见 issue 评论),
# 这解释了为什么 EPERM 化之后就活、默认 SIGSYS 就死。
if command -v strace >/dev/null 2>&1; then
  strace -f -qq -e trace=all -o "$WORK/deno.strace" "$DENO_BIN" eval 'console.log(1)' >/dev/null 2>&1
  PKEY_SEEN="$(grep -c pkey_alloc "$WORK/deno.strace" 2>/dev/null || true)"; PKEY_SEEN="${PKEY_SEEN:-0}"
  printf 'deno 启动期 pkey_alloc 调用次数: %s(@system-service 不含该 syscall)\n' "$PKEY_SEEN" | ev
fi

S1_JL="$(urun -p MemoryDenyWriteExecute=yes -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM \
  "$DENO_BIN" eval --v8-flags=--jitless "$JIT" 2>&1)"
printf 'S1-逃生门(W^X + seccomp + --v8-flags=--jitless): %s\n' "$(printf '%s' "${S1_JL:-<无输出>}" | tr '\n' ' ' | cut -c1-120)" | ev
if printf '%s' "$S1_JL" | grep -q 'JIT_OK true'; then
  pass "S1 逃生门成立:--v8-flags=--jitless 下 W^X + seccomp 全开仍存活(代价 = 放弃 V8 优化 JIT)"
else
  blocked "S1 逃生门未成立(--jitless 亦失败),S1 只剩「沙箱内跑不了 copilot」这一结论"
fi

# ==================================================== S2: scope 能不能带属性 ==
say "S2 · systemd-run --scope 是否真能施加 Protect*/SystemCallFilter(看证据不看返回码)"
SCOPE_REJ=0
for prop in "ProtectSystem=strict" "SystemCallFilter=@system-service" "MemoryDenyWriteExecute=yes" \
            "NoNewPrivileges=yes" "RestrictAddressFamilies=AF_UNIX" "RestrictNetworkInterfaces=lo"; do
  out="$(timeout 20 systemd-run --user --quiet --scope -p "$prop" true 2>&1)"
  printf 'scope -p %-38s → %s\n' "$prop" "${out:-<接受>}" | ev
  printf '%s' "$out" | grep -qi 'Unknown assignment' && SCOPE_REJ=$((SCOPE_REJ+1))
done
if [ "$SCOPE_REJ" -gt 0 ]; then
  fail "S2 --scope 拒收沙箱属性:$SCOPE_REJ/6 条被 systemd-run 直接判「Unknown assignment」→ scope 形态不可用,只能走 service 形态"
else
  pass "S2 --scope 接受了全部六条属性(是否生效另看证据)"
fi

SVC_OUT="$(urun -p ProtectSystem=strict -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM \
  bash -c 'grep -E "^Seccomp:" /proc/self/status; findmnt -no OPTIONS / 2>/dev/null || awk "\$2==\"/\"{print \$4}" /proc/self/mounts' 2>&1)"
printf 'S2-service(ProtectSystem=strict + seccomp)逐字:\n%s\n' "$SVC_OUT" | ev
SECCOMP_MODE="$(printf '%s' "$SVC_OUT" | awk '/^Seccomp:/{print $2; exit}')"
ROOT_OPTS="$(printf '%s' "$SVC_OUT" | tail -1)"
case "${SECCOMP_MODE:-}" in
  2) pass "S2 service 形态 seccomp 真生效(Seccomp: 2)" ;;
  *) fail "S2 service 形态 seccomp 未生效(Seccomp: ${SECCOMP_MODE:-缺失})" ;;
esac
if printf '%s' "$ROOT_OPTS" | grep -q '^ro'; then
  pass "S2 service 形态 ProtectSystem=strict 真生效(/ 为 ro)"
elif [ "$IS_ROOT" = 1 ]; then
  fail "S2 系统 manager 下 ProtectSystem=strict 也没把 / 置 ro → 该属性对本单元无效"
else
  blocked "S2 ProtectSystem=strict 在 user manager 下被接受但不生效(/ 仍 rw,journal 无警告),系统 manager 形态需 root → 生产单元由 pid1 执行,本机无法取证"
fi

# ========================================================= S3: TTY 与信号 ====
say "S3 · TTY(isTerminal)与 Ctrl-C 在候选形态下是否保住"
TTYPROBE='console.log("ISATTY", Deno.stdin.isTerminal(), Deno.stdout.isTerminal(), Deno.stderr.isTerminal())'
if command -v script >/dev/null 2>&1; then
  tty_row() { local label="$1"; shift
    local o; o="$(script -qec "$*" /dev/null 2>&1 | tr -d '\r' | grep -o 'ISATTY.*' | head -1)"
    printf '%-16s %s\n' "$label" "${o:-<无输出>}" | ev; printf '%s' "${o:-}"; }
  T_BARE="$(tty_row  "裸执行"     "$DENO_BIN eval '$TTYPROBE'")"
  T_SCOPE="$(tty_row "--scope"    "timeout 20 systemd-run --user --quiet --scope $DENO_BIN eval '$TTYPROBE'")"
  T_PIPE="$(tty_row  "unit --pipe" "timeout 20 systemd-run --user --quiet --wait --collect --pipe $DENO_BIN eval '$TTYPROBE'")"
  T_PTY="$(tty_row   "unit --pty"  "timeout 20 systemd-run --user --quiet --wait --collect --pty $DENO_BIN eval '$TTYPROBE'")"
  T_NOPTY="$("$DENO_BIN" eval "$TTYPROBE" 2>&1 | grep -o 'ISATTY.*' | head -1)"
  printf '%-16s %s\n' "无 pty(自检)" "${T_NOPTY:-<无输出>}" | ev
  if printf '%s' "$T_NOPTY" | grep -q 'ISATTY false false false'; then
    pass "S3 测法自检:无 pty 时 isTerminal=false(读数可信)"
  else
    fail "S3 测法不可信:无 pty 时仍报 tty(${T_NOPTY:-<无输出>})"
  fi
  if printf '%s' "$T_PIPE" | grep -q 'ISATTY.*true.*true'; then
    pass "S3 候选形态(unit --pipe)在调用方为 TTY 时 stdin/stdout isTerminal=true"
  else
    fail "S3 候选形态丢了 TTY(${T_PIPE:-<无输出>})→ #117 的「wrapper 经 unit 进沙箱」不成立"
  fi
else
  blocked "S3 无 script(util-linux),无法造 pty"
fi

cat > "$WORK/sig-inner.sh" <<'INNER'
#!/usr/bin/env bash
# 假 daedalus-shell:报 pid/pgid/sid,起长命令子进程,等 SIGINT。
echo "SIGPROBE pid=$$ pgid=$(ps -o pgid= -p $$ | tr -d ' ') sid=$(ps -o sid= -p $$ | tr -d ' ')"
echo "$$" > "$1"
sleep 120 & CH=$!
trap 'echo "RECEIVED_INT"; kill "$CH" 2>/dev/null; exit 130' INT
wait "$CH"
INNER
chmod +x "$WORK/sig-inner.sh"

# Ctrl-C 的落点是「前端(systemd-run)所在进程组」:用 setsid 给前端独立会话,再对整组发
# SIGINT —— 这才是终端里按下 Ctrl-C 的语义(而不是直接戳单元内进程)。
sig_case() { # sig_case <标签> <pipe|scope>
  local label="$1" mode="$2"
  local logf="$WORK/sig-$label.log" pidf="$WORK/sig-$label.innerpid"
  local resf="$WORK/sig-$label.result"
  : > "$logf"; rm -f "$pidf"
  case "$mode" in
    pipe)  setsid systemd-run --user --quiet --wait --collect --pipe bash "$WORK/sig-inner.sh" "$pidf" >"$logf" 2>&1 & ;;
    scope) setsid systemd-run --user --quiet --scope             bash "$WORK/sig-inner.sh" "$pidf" >"$logf" 2>&1 & ;;
  esac
  local front=$!
  local i=0
  while [ "$i" -lt 40 ] && [ ! -s "$pidf" ]; do sleep 0.1; i=$((i+1)); done
  local pgid self_pgid
  pgid="$(ps -o pgid= -p "$front" 2>/dev/null | tr -d ' ')"
  self_pgid="$(ps -o pgid= -p $$ | tr -d ' ')"
  printf '%-8s 前端 pid=%s pgid=%s(self=%s) 单元内 pid=%s\n' "$mode" "$front" "${pgid:-?}" "$self_pgid" \
    "$(cat "$pidf" 2>/dev/null || echo '?')" | ev
  if [ -n "${pgid:-}" ] && [ "$pgid" != "$self_pgid" ]; then
    kill -INT -"$pgid" 2>/dev/null
  else
    kill -INT "$front" 2>/dev/null     # 同组时只发单 pid,避免把探针自己打死
  fi
  # 看门狗兜底:真 Ctrl-C 应在秒级结束;8s 还没退出就说明 SIGINT 没起到终止作用。
  ( sleep 8; kill -KILL -"${pgid:-$front}" 2>/dev/null ) &
  local wdog=$!
  wait "$front" 2>/dev/null; local rc=$?      # 130 = SIGINT 致死;137 = 被看门狗强杀
  kill "$wdog" 2>/dev/null
  sleep 1
  local alive=no
  [ -s "$pidf" ] && kill -0 "$(cat "$pidf")" 2>/dev/null && alive=yes
  local got; got="$(awk '/RECEIVED_INT/{n++} END{print n+0}' "$logf" 2>/dev/null)"
  printf '%-8s 前端 rc=%s 单元内进程存活=%s SIGINT 送达单元=%s\n' "$mode" "$rc" "$alive" "${got:-0}" | ev
  printf '%s %s %s\n' "$rc" "$alive" "${got:-0}" > "$resf"
  [ -s "$pidf" ] && kill "$(cat "$pidf")" 2>/dev/null
  return 0
}

sig_case pipe pipe
read -r SIG_PIPE_RC SIG_PIPE_ALIVE SIG_PIPE_GOT < "$WORK/sig-pipe.result"
if [ "$SIG_PIPE_ALIVE" = yes ]; then
  fail "S3 Ctrl-C 停不住沙箱内进程:--pipe 形态前端 rc=$SIG_PIPE_RC(130=SIGINT 致死 / 137=看门狗强杀),单元内进程存活且从未收到 SIGINT(送达=$SIG_PIPE_GOT)"
else
  pass "S3 --pipe 形态 Ctrl-C 能停住单元内进程(rc=$SIG_PIPE_RC 存活=$SIG_PIPE_ALIVE 送达=$SIG_PIPE_GOT)"
fi

sig_case scope scope
read -r SIG_SCOPE_RC SIG_SCOPE_ALIVE SIG_SCOPE_GOT < "$WORK/sig-scope.result"
blocked "S3 --scope 形态的 Ctrl-C 传播无法与本探针的 setsid/pgrp 装置区分开(实测 SIGINT 未送达,进程仅在看门狗强杀后消失;rc=$SIG_SCOPE_RC 存活=$SIG_SCOPE_ALIVE 送达=$SIG_SCOPE_GOT),且该形态本身带不了沙箱属性(S2)—— 候选形态只剩 service 形态"

# ========================================================= S4: 网络白名单 ====
say "S4 · IPAddressAllow/Deny 网络白名单是否真拦得住"
if command -v curl >/dev/null 2>&1; then
  AH=example.com; DH=github.com
  a_plain="$(curl -sS -m 6 -o /dev/null -w '%{http_code}' "https://$AH" 2>/dev/null)"
  d_plain="$(curl -sS -m 6 -o /dev/null -w '%{http_code}' "https://$DH" 2>/dev/null)"
  printf '无沙箱: %s=%s  %s=%s\n' "$AH" "${a_plain:-ERR}" "$DH" "${d_plain:-ERR}" | ev
  if [ "${d_plain:-000}" = "000" ]; then
    printf '(对照: %s 本来就不可达 → 不能拿它「连不上」当拒绝证据,那是假绿;故只用可达的 %s 判)\n' "$DH" "$AH" | ev
  else
    printf '(对照: %s 无沙箱可通 → 若白名单下它不通才可能算拒绝证据)\n' "$DH" | ev
  fi
  AH4="$(getent ahostsv4 "$AH" | awk '{print $1}' | head -1)"
  OUT_DENY="$(urun -p IPAddressDeny=any bash -c "curl -sS -m 6 -o /dev/null -w 'code=%{http_code}' https://$AH" 2>&1)"
  printf 'IPAddressDeny=any 下访问可达主机 %s → %s\n' "$AH" "$(printf '%s' "${OUT_DENY:-<无输出>}" | tr '\n' ' ')" | ev
  OUT_ALLOW="$(urun -p IPAddressDeny=any -p "IPAddressAllow=${AH4:-127.0.0.1}" \
    bash -c "curl -sS -m 6 -o /dev/null -w 'allowed=%{http_code} ' https://$AH; curl -sS -m 6 -o /dev/null -w 'other=%{http_code}' https://$DH" 2>&1)"
  printf 'IPAddressDeny=any + IPAddressAllow=%s → %s\n' "${AH4:-?}" "$(printf '%s' "${OUT_ALLOW:-<无输出>}" | tr '\n' ' ')" | ev
  printf 'unprivileged_bpf_disabled=%s(copilot 以普通用户跑时,BPF 过滤器装载不了)\n' \
    "$(cat /proc/sys/kernel/unprivileged_bpf_disabled 2>/dev/null || echo '?')" | ev
  printf 'manifest 现状(--allow-net 无白名单):\n' | ev
  grep -n 'allow-net' "$REPO/plugin/copilot/daedalus.plugin.json" | ev
  if printf '%s' "$OUT_DENY" | grep -q 'code=200'; then
    if [ "$IS_ROOT" = 1 ]; then
      fail "S4 系统 manager 下 IPAddressDeny=any 亦未拦住(仍 200)→ 该内核+systemd 组合上 BPF 白名单思路不成立"
    else
      fail "S4 user manager 下 IPAddressDeny=any 静默无效(仍 200,journal 无警告)→ copilot 以用户身份跑时 BPF 网络白名单用不上"
    fi
  else
    pass "S4 IPAddressDeny=any 真拦住了(证据见上)"
  fi
  if [ "$IS_ROOT" = 0 ]; then
    blocked "S4 系统 manager 形态(BPF 由 pid1 装载,需 root)本机无法取证 —— 只有把 copilot 改成系统单元才有这条路"
  fi
else
  blocked "S4 无 curl,无法验网络"
fi

# ========================================================= S5: Deno 拒绝语义 =
# 关键区别:copilot 生产跑的是 `deno run main.ts`,而旧探针用的是 `deno eval`。两者的
# 权限语义不同 —— 先各自实测,再判旧判据可信不可信。
say 'S5 · Deno 拒绝写入时抛的 e.name(deno run vs deno eval,旧探针 D3 判据可信度)'
cat > "$WORK/s5-write.ts" <<'TS'
const p = Deno.args[0];
try {
  Deno.writeTextFileSync(p, "x");
  console.log("WROTE " + p);
} catch (e) {
  console.log("DENIED " + p + " → " + e.name);
}
const q = await Deno.permissions.query({ name: "write", path: p });
console.log("query " + p + " = " + q.state);
TS
rm -rf "$OUTSIDE"; mkdir -p "$OUTSIDE"

# (a) 旧判据的形态:deno eval + --allow-write=/tmp 写 /etc
S5_A="$("$DENO_BIN" eval --allow-write=/tmp \
  'try{Deno.writeTextFileSync("/etc/probe-copilot-denied","x");console.log("WROTE")}catch(e){console.log("DENIED:"+e.name)}' 2>&1)"
printf -- '(a) deno eval --allow-write=/tmp 写 /etc: %s\n' "$(printf '%s' "$S5_A" | tr '\n' ' ')" | ev
# eval 到底执不执行权限检查:用 --deny-write 做负对照 + 直接问权限系统
S5_EVALDENY="$("$DENO_BIN" eval --deny-write \
  'try{Deno.writeTextFileSync("/tmp/probe117-evaldeny","x");Deno.removeSync("/tmp/probe117-evaldeny");console.log("WROTE")}catch(e){console.log("DENIED:"+e.name)}' 2>&1)"
S5_EVALQ="$("$DENO_BIN" eval --allow-write=/tmp \
  'const q=await Deno.permissions.query({name:"write",path:"/usr/x"});console.log(q.state)' 2>&1)"
printf -- '(a) deno eval --deny-write 写 /tmp: %s\n' "$(printf '%s' "$S5_EVALDENY" | tr '\n' ' ')" | ev
printf '(a) deno eval --allow-write=/tmp 问 /usr 的写权限: %s\n' "$(printf '%s' "$S5_EVALQ" | tr '\n' ' ')" | ev

# (b) 生产形态:deno run + --allow-write=/tmp 写「用户可写但不在允许集」的 /var/tmp
S5_B="$("$DENO_BIN" run --allow-write=/tmp "$WORK/s5-write.ts" "$OUTSIDE/x" 2>&1)"
printf '(b) deno run --allow-write=/tmp 写 %s(当前用户可写): %s\n' "$OUTSIDE" "$(printf '%s' "$S5_B" | tr '\n' ' ')" | ev
S5_C="$("$DENO_BIN" run --allow-write=/ "$WORK/s5-write.ts" "$OUTSIDE/y" 2>&1)"
printf '(b) deno run --allow-write=/ 写同一路径: %s\n' "$(printf '%s' "$S5_C" | tr '\n' ' ')" | ev
# (c) 旧探针 D3b 的 '$HOME' 字面量:单引号原样传给 Deno,run 下是否生效?
mkdir -p "$HOME/.local/share/daedalus"
S5_D="$("$DENO_BIN" run --allow-write='/tmp,$HOME/.local/share/daedalus' "$WORK/s5-write.ts" \
  "$HOME/.local/share/daedalus/.probe117" 2>&1)"
printf -- "(c) deno run --allow-write='/tmp,\$HOME/.local/share/daedalus'(字面量)写 \$HOME 下: %s\n" \
  "$(printf '%s' "$S5_D" | tr '\n' ' ')" | ev
S5_E="$("$DENO_BIN" run --allow-write="/tmp,$HOME/.local/share/daedalus" "$WORK/s5-write.ts" \
  "$HOME/.local/share/daedalus/.probe117" 2>&1)"
printf '(c) 同一条但路径写死展开: %s\n' "$(printf '%s' "$S5_E" | tr '\n' ' ')" | ev
rm -f "$HOME/.local/share/daedalus/.probe117"

if printf '%s' "$S5_A" | grep -q 'DENIED:PermissionDenied' \
   && printf '%s' "$S5_EVALDENY" | grep -q 'WROTE' \
   && printf '%s' "$S5_EVALQ" | grep -q 'granted'; then
  fail "S5 旧 D3 判据失效:deno eval 不做权限检查(--deny-write 仍 WROTE、query /usr = granted),所以 eval 下的 PermissionDenied 是 OS 的 EACCES,不是 Deno 拦的 —— 它测不到 Deno 边界"
else
  pass "S5 deno eval 确在执行业务权限检查(负对照未放行),旧 D3 形态可用"
fi
if printf '%s' "$S5_B" | grep -q 'NotCapable' && printf '%s' "$S5_B" | grep -q '= denied' \
   && printf '%s' "$S5_C" | grep -q 'WROTE'; then
  pass "S5 生产形态(deno run)实测 e.name=NotCapable:用户可写路径被拒、--allow-write=/ 即成功 → 正确的判据是 run + NotCapable"
else
  fail "S5 deno run 下未复现 NotCapable 拒绝:$(printf '%s' "$S5_B" | head -1) / $(printf '%s' "$S5_C" | head -1)"
fi
if printf '%s' "$S5_D" | grep -q 'NotCapable' && printf '%s' "$S5_E" | grep -q 'WROTE'; then
  pass "S5 旧探针 D3b 的 '\$HOME' 字面量在 deno run 下不展开(NotCapable),写死绝对路径才有效 → 旧写法是假绿"
elif printf '%s' "$S5_D" | grep -q 'WROTE'; then
  pass "S5 '\$HOME' 字面量在 deno run 下被展开,旧写法无害(仍建议写死绝对路径)"
else
  fail "S5 '\$HOME' 字面量判据不明:$(printf '%s' "$S5_D" | head -1)"
fi

# ---------------------------------------------------------------- 汇总 ------
say "汇总"
printf -- '---------------- %d PASS / %d FAIL / %d BLOCKED ----------------\n' "$pass_n" "$fail_n" "$blocked_n"
printf '提示:本探针的 FAIL = 被检验的提议不成立(不是探针故障);探针自身不可信会表现为 BLOCKED 或缺失输出。\n'
if [ "$fail_n" -eq 0 ]; then
  printf 'RESULT: SELFTEST PASS\n'
  exit 0
fi
printf 'RESULT: SELFTEST FAIL (%d)\n' "$fail_n"
exit 1
