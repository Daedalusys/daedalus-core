#!/usr/bin/env bash
# =============================================================================
# probe-copilot-sandbox.sh —— copilot 进 OS 层沙箱的技术探针(core#117,S1–S5)
# =============================================================================
# 只回答「能不能」,不改产品代码:不写 /etc、不改单元、不装包;写入只落 $WORK、
# /var/tmp 与 $HOME/.local/share/daedalus(写完即删),root 形态另在 /usr/local/bin 放一份
# deno 中转副本(该目录才是 SELinux bin_t,退出时同样删除)。判据只认实测证据(Seccomp:
# 字段 / fd 的 tty 属性 / 系统调用返回 / 进程存活),不认 systemd-run 的返回码 —— 属性被
# 静默忽略时返回码照样是 0,那正是要抓的假绿。
#
# S1 JIT vs MemoryDenyWriteExecute + seccomp | S2 --scope 能不能带 Protect*/SystemCallFilter
# S3 TTY 与 Ctrl-C 在候选形态下是否保住     | S4 IPAddressAllow/Deny 是否真拦得住
# S5 Deno 拒绝写入时抛的 e.name(旧探针 D3 判据可信度)
#
# 两种管理器形态都要跑,结论分开记:root 用 system manager,DynamicUser / Protect* /
# IPAddressDeny 由 pid1 施加;普通用户用 user manager,同一批属性可能被静默忽略。
# 非 root 时 system manager 形态记 NOTE BLOCKED,并以 user manager 等效形态取证;
# BLOCKED 绝不折算成 PASS。
# 用法: bash tests/real-machine/probe-copilot-sandbox.sh   # 输出即证据,重定向留档
#       sudo bash tests/real-machine/probe-copilot-sandbox.sh  # system manager 形态
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
STAGED_DENO=""   # root 形态下为 bin_t 中转副本路径,cleanup 负责删
pass_n=0 fail_n=0 blocked_n=0

say()     { printf '\n== %s ==\n' "$*"; }
ev()      { sed 's/^/      /'; }
pass()    { pass_n=$((pass_n+1));       printf 'PASS %s\n' "$*"; }
fail()    { fail_n=$((fail_n+1));       printf 'FAIL %s\n' "$*"; }
blocked() { blocked_n=$((blocked_n+1)); printf 'NOTE BLOCKED %s\n' "$*"; }

cleanup() {
  systemctl --user reset-failed 'probe117-*' >/dev/null 2>&1 || true
  # root 形态的瞬时单元在 system manager 里,user manager 的清不掉。
  [ "$IS_ROOT" = 1 ] && systemctl reset-failed 'probe117-*' 'probe-*' >/dev/null 2>&1 || true
  for f in "$WORK"/sig-*.innerpid; do [ -r "$f" ] && kill "$(cat "$f")" 2>/dev/null; done
  # 中转副本是探针自己装的,必须带走 —— 否则 /usr/local/bin 会留一个孤儿 deno。
  [ -n "${STAGED_DENO:-}" ] && rm -f "$STAGED_DENO"
  rm -rf "$OUTSIDE"
  return 0
}
trap cleanup EXIT

# ------------------------------------------------------------ 环境三元组 ------
say "环境三元组(结论绑定版本,否则不可复现)"
printf 'euid=%s root=%s work=%s\n' "$(id -u)" "$IS_ROOT" "$WORK" | ev

# DENO_BIN 可由环境指名:靶场 root 的 PATH 里没有 asdf shim,探针必须能指名道姓地用
# 同一个 deno,否则「root 形态」会因为找不到解释器而整条 BLOCKED。
DENO_BIN="${DENO_BIN:-$(command -v deno 2>/dev/null || true)}"
if [ -z "$DENO_BIN" ]; then
  DENO_BIN="$(ls -d /home/*/.asdf/installs/deno/*/bin/deno /usr/local/bin/deno /usr/bin/deno 2>/dev/null | sort -V | tail -1)"
fi
if [ -z "$DENO_BIN" ]; then
  blocked "S0 无 deno —— S1/S3/S5 全部无法取证"
  printf -- '---------------- %d PASS / %d FAIL / %d BLOCKED ----------------\n' "$pass_n" "$fail_n" "$blocked_n"
  printf 'RESULT: SELFTEST FAIL (1)\n'
  exit 1
fi
# asdf shim 靠 $HOME/.asdf 才解得出真版本:root 下 $HOME=/root,同一个 shim 会解不出
# deno,探测口径就随执行者身份漂了。一律钉真二进制,谁跑都是同一个 deno。
resolve_deno() {
  local p="${1:-}" cand
  case "$p" in
    */shims/*)
      cand="$(asdf where deno 2>/dev/null || true)"; cand="${cand%/}/bin/deno"
      if [ ! -x "$cand" ]; then
        cand="$(ls -d /home/*/.asdf/installs/deno/*/bin/deno 2>/dev/null | sort -V | tail -1)"
      fi
      [ -x "${cand:-}" ] && { printf '%s' "$cand"; return; }
      ;;
  esac
  printf '%s' "$p"
}
DENO_BIN="$(resolve_deno "$DENO_BIN")"
printf 'deno 真二进制: %s\n' "$DENO_BIN" | ev
# 结论必须绑定版本,否则不可复现 —— 三元组放在这里,才能拿到真实的 deno 版本。
printf '环境三元组: systemd=%s | deno=%s | kernel=%s\n' \
  "$(systemctl --version | head -1 | awk '{print $2}')" \
  "$("$DENO_BIN" --version 2>/dev/null | head -1)" "$(uname -r)" | ev

# DynamicUser 单元里的进程读不到普通用户家目录(0700),deno 也在那儿 → 照原样跑,
# 失败原因会变成「读不到解释器」而不是被测属性。复制一份到 0755 临时目录再用。
WORK_EXEC="$WORK/exec"; mkdir -p "$WORK_EXEC/deno-cache"; chmod 0755 "$WORK" "$WORK_EXEC"; chmod 0777 "$WORK_EXEC/deno-cache"
DENO_EXEC="$DENO_BIN"
if cp -f "$DENO_BIN" "$WORK_EXEC/deno" 2>/dev/null; then
  chmod 0755 "$WORK_EXEC/deno"
  DENO_EXEC="$WORK_EXEC/deno"
  printf 'DynamicUser 用副本: %s\n' "$DENO_EXEC" | ev
fi
# DynamicUser 单元没有 HOME,Deno 的编译缓存会没地方放 → 显式给一个可写的 DENO_DIR。
# 注意:必须用 --setenv,不能写 -p "Environment=A=1 B=2" —— systemd-run 会按空白把
# "B=2" 切成另一个属性,最后当成待执行的命令(实测 "Failed to find executable B=2")。
DENO_SETENV=(--setenv="HOME=$WORK_EXEC" --setenv="DENO_DIR=$WORK_EXEC/deno-cache")

# 管理器形态:euid 0 走 system manager(pid1 施加 DynamicUser / Protect* / BPF),
# 否则走 user manager。两者能力不同,所以同一条用例要按形态分别判。
if [ "$IS_ROOT" = 1 ]; then
  MGR=(systemd-run);                 MGR_LABEL="system manager(root)"
else
  MGR=(systemd-run --user);          MGR_LABEL="user manager(uid $(id -u))"
fi
printf 'manager: %s\n' "$MGR_LABEL" | ev

# system manager 的单元跑在 SELinux unconfined_service_t 域里:该域能**读** user_home_t /
# user_tmp_t,但不能**执行**它们(实测 203/EXEC,日志 "Unable to locate executable:
# Permission denied";SELinux 策略不拦 unconfined_t 执行家目录文件)。只有 bin_t 的目录
# 里的文件能被单元执行 —— 故 root 形态把 deno 复制一份到 /usr/local/bin 再用,退出即删。
DENO_UNIT="$DENO_EXEC"
if [ "$IS_ROOT" = 1 ]; then
  for cand in /usr/local/bin/deno-probe117 /usr/libexec/deno-probe117; do
    [ -d "$(dirname "$cand")" ] || continue
    if install -m 0755 "$DENO_BIN" "$cand" 2>/dev/null; then
      STAGED_DENO="$cand"; DENO_UNIT="$cand"; break
    fi
  done
  if [ -n "${STAGED_DENO:-}" ]; then
    printf 'SELinux bin_t 中转副本(单元可执行): %s\n' "$STAGED_DENO" | ev
  else
    printf '!! 没有 bin_t 目录可放中转副本 → 单元内很可能 203/EXEC,结论以 S0 为准\n' | ev
  fi
fi

# WorkingDirectory 钉仓库根:asdf shim 的版本解析随 cwd 漂,钉住才能保证单元内是同一个 deno。
urun()  { timeout 40 "${MGR[@]}" --quiet --wait --collect --pipe -p WorkingDirectory="$REPO" "$@"; }
# 不带 --quiet 的变体:要读单元退出态(code=dumped/status=SYS 只在 non-quiet 输出里)。
urunv() { timeout 40 "${MGR[@]}" --wait --collect --pipe -p WorkingDirectory="$REPO" "$@"; }
INUNIT_VER="$(urun "$DENO_UNIT" --version 2>&1 | head -1)"
printf 'unit 内 deno: %s\n' "${INUNIT_VER:-<空>}" | ev
# 只有真的报出版本号才算前提成立。单元里跑不起来时,head -1 拿到的是 systemd 的报错
# ("Failed to find executable ..." / "Failed to start transient service unit ..."),
# 非空但不是版本号 —— 若只看「非空」,后面每条用例都会产出一堆 FAIL,而正确的行为是
# 一条 NOTE BLOCKED(缺前提不许折算成 PASS,也不该伪装成「提议不成立」)。
if ! printf '%s' "${INUNIT_VER:-}" | grep -qE '^deno [0-9]'; then
  blocked "S0 $MGR_LABEL 起不来单元,或单元里跑不起 deno($DENO_UNIT):${INUNIT_VER:-<无输出>}"
  printf -- '---------------- %d PASS / %d FAIL / %d BLOCKED ----------------\n' "$pass_n" "$fail_n" "$blocked_n"
  printf 'RESULT: SELFTEST FAIL (1)\n'
  exit 1
fi

JIT='const f = new Function("a","b","return a+b"); let s=0; for (let i=0;i<2e5;i++) s=f(s,i); console.log("JIT_OK", s>0)'

# ======================================================== S1: JIT vs W^X ======
say "S1 · V8 JIT 与 MemoryDenyWriteExecute / seccomp 的兼容性(最可能否掉提议的一条)"

# 计划原文形态(系统 manager + DynamicUser + seccomp + W^X)。这里故意去掉 --quiet:
# 被 seccomp 打死时 stdout 是空的,只看 stdout 会把「SIGSYS 致死」误报成「无输出」。
S1_PLAN="$(timeout 40 systemd-run --wait --collect --pipe \
  -p DynamicUser=yes -p ProtectSystem=strict -p NoNewPrivileges=yes \
  -p SystemCallFilter=@system-service -p 'SystemCallFilter=~@privileged @resources @obsolete' \
  -p MemoryDenyWriteExecute=yes "${DENO_SETENV[@]}" \
  "$DENO_UNIT" eval "$JIT" 2>&1)"
printf 'S1-plan 计划原文(系统 manager + DynamicUser + seccomp + W^X)关键行:\n' | ev
printf '%s\n' "${S1_PLAN:-<无输出>}" \
  | grep -E 'JIT_OK|Fatal error|Check failed|Main processes terminated|Finished with result|Failed to' | ev
# 只摘掉 seccomp(保留 DynamicUser/ProtectSystem/W^X):把「谁杀死的」拆开归因。
S1_PLAN_WX="$(timeout 40 systemd-run --quiet --wait --collect --pipe \
  -p DynamicUser=yes -p ProtectSystem=strict -p NoNewPrivileges=yes \
  -p MemoryDenyWriteExecute=yes "${DENO_SETENV[@]}" "$DENO_UNIT" eval "$JIT" 2>&1)"
printf 'S1-plan 去掉 seccomp(只剩 DynamicUser+ProtectSystem+W^X): %s\n' \
  "$(printf '%s' "${S1_PLAN_WX:-<无输出>}" | grep -E '^JIT_OK true|Check failed|Fatal error' | grep -v 'Args:' | head -1)" | ev
if printf '%s' "$S1_PLAN" | grep -q 'JIT_OK true'; then
  pass "S1-plan 计划原文形态跑通(系统 manager + DynamicUser 下 V8 存活)"
elif printf '%s' "$S1_PLAN" | grep -Eq 'status=[0-9]+/SYS|Bad system call'; then
  fail "S1-plan 计划原文形态被 seccomp 打死(SIGSYS $(printf '%s' "$S1_PLAN" | grep -Eo 'status=[0-9]+/SYS' | head -1));同一形态摘掉 seccomp 后 → $(printf '%s' "${S1_PLAN_WX:-<无输出>}" | grep -E '^JIT_OK true|Check failed|Fatal error' | grep -v 'Args:' | head -1)"
elif [ "$IS_ROOT" = 1 ]; then
  fail "S1-plan 以 root 跑计划原文形态失败:$(printf '%s' "$S1_PLAN" | grep -E 'Check failed|Fatal error|Failed to' | head -1)"
else
  blocked "S1-plan 计划原文形态需 root(实测: $(printf '%s' "$S1_PLAN" | head -1))—— 下面的 user manager 等效形态才是本机真证据"
fi

# 只开 W^X(不带 seccomp):把「V8 需要可写可执行页」单独证出来。
S1_WX="$(urun -p MemoryDenyWriteExecute=yes "$DENO_UNIT" eval "$JIT" 2>&1)"
printf 'S1-W^X(只 MemoryDenyWriteExecute=yes)关键行:\n' | ev
printf '%s\n' "${S1_WX:-<无输出>}" | grep -E 'Fatal error|Check failed|JIT_OK|panicked|Version:' | ev
if printf '%s' "$S1_WX" | grep -q 'JIT_OK true'; then
  pass "S1 W^X 下 V8 JIT 存活"
else
  fail "S1 MemoryDenyWriteExecute=yes 直接杀死 V8:$(printf '%s' "$S1_WX" | grep -E 'Fatal error|Check failed' | head -1)"
fi

# 只开 seccomp(不带 W^X):默认动作是 SIGSYS,故失败不体现在字符串上,要看单元退出态。
S1_SEC_OUT="$(urunv -p SystemCallFilter=@system-service "$DENO_UNIT" eval "$JIT" 2>&1)"
printf 'S1-seccomp(只 SystemCallFilter=@system-service,不 --quiet 以取退出态):\n' | ev
printf '%s\n' "$S1_SEC_OUT" | grep -E 'Main processes terminated|Finished with result|JIT_OK' | ev
printf 'unprivileged 形参提示:改 SystemCallErrorNumber=EPERM 后同一 filter → %s\n' \
  "$(urun -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM "$DENO_UNIT" eval "$JIT" 2>&1 | head -1)" | ev
printf 'W^X + seccomp + SystemCallErrorNumber=EPERM(把 seccomp 致死因素摘掉后只剩 W^X):\n' | ev
urun -p MemoryDenyWriteExecute=yes -p SystemCallFilter=@system-service -p SystemCallErrorNumber=EPERM \
  "$DENO_UNIT" eval "$JIT" 2>&1 | grep -E 'Fatal error|Check failed|JIT_OK' | ev
# 判据:「被 SIGSYS 打死」在 systemd 输出里的形态是 status=31/SYS(旧写法 code=dumped/status=SYS
# 只匹配到一种排版)。正向证据必须是 JIT_OK,否则一律不作 PASS —— 上一版这里按字符串
# 匹配失败就落到 else 分支报「未额外致死」,实测是把 SIGSYS 崩了报成 PASS(假绿)。
if printf '%s' "$S1_SEC_OUT" | grep -Eq 'status=[0-9]+/SYS|code=dumped[^ ]*.*SYS|Bad system call'; then
  fail "S1 纯 seccomp(@system-service)也会杀 deno:单元退出态 $(printf '%s' "$S1_SEC_OUT" | grep -Eo 'code=dumped, status=[0-9]+/SYS|Main processes terminated[^$]*' | head -1);换 SystemCallErrorNumber=EPERM 即恢复 → 死因是 syscall 白名单差集,不是 W^X 单独一条"
elif printf '%s' "$S1_SEC_OUT" | grep -q 'JIT_OK true'; then
  pass "S1 纯 seccomp(@system-service)下 deno 存活且 JIT 跑通(退出态见上)"
else
  fail "S1 纯 seccomp 结果不可判定(既未见 SIGSYS 退出态,也无 JIT_OK 正向证据):$(printf '%s' "$S1_SEC_OUT" | tail -1)"
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
  "$DENO_UNIT" eval --v8-flags=--jitless "$JIT" 2>&1)"
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
  out="$(timeout 20 "${MGR[@]}" --quiet --scope -p "$prop" true 2>&1)"
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
# 造真 pty:util-linux 的 `script` 优先;靶场可能没装 script(实测 Fedora 缺 util-linux
# 的 script 二进制),退回 python3 的 pty.fork()。两者都给真 tty —— 不能用管道冒充,
# 管道下 isTerminal 恒 false,会把候选形态误判成丢了 TTY。
PTY_HOW=""
if command -v script >/dev/null 2>&1; then
  PTY_HOW="script(util-linux)"
  pty_sh() { script -qec "$1" /dev/null 2>&1; }
elif command -v python3 >/dev/null 2>&1; then
  PTY_HOW="python3 pty.fork"
  pty_sh() {
    python3 -c '
import os, pty, select, sys
pid, fd = pty.fork()
if pid == 0:
    os.execv("/bin/sh", ["/bin/sh", "-c", sys.argv[1]])
buf = b""
while True:
    try:
        r, _, _ = select.select([fd], [], [], 30)
        if not r:
            break
        chunk = os.read(fd, 4096)
    except OSError:
        break
    if not chunk:
        break
    buf += chunk
os.waitpid(pid, 0)
sys.stdout.write(buf.decode("utf-8", "replace"))
' "$1"
  }
fi

if [ -n "$PTY_HOW" ]; then
  printf 'pty 装置: %s\n' "$PTY_HOW" | ev
  MGR_STR="${MGR[*]}"
  tty_row() { local label="$1"; shift
    local o; o="$(pty_sh "$*" | tr -d '\r' | grep -o 'ISATTY.*' | head -1)"
    printf '%-16s %s\n' "$label" "${o:-<无输出>}" | ev; printf '%s' "${o:-}"; }
  T_BARE="$(tty_row  "裸执行"     "$DENO_BIN eval '$TTYPROBE'")"
  T_SCOPE="$(tty_row "--scope"    "timeout 20 $MGR_STR --quiet --scope $DENO_UNIT eval '$TTYPROBE'")"
  T_PIPE="$(tty_row  "unit --pipe" "timeout 20 $MGR_STR --quiet --wait --collect --pipe $DENO_UNIT eval '$TTYPROBE'")"
  T_PTY="$(tty_row   "unit --pty"  "timeout 20 $MGR_STR --quiet --wait --collect --pty $DENO_UNIT eval '$TTYPROBE'")"
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
  blocked "S3 既无 script(util-linux) 也无 python3,无法造 pty"
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

# Ctrl-C 的忠实模拟:必须让前端拿到「真控制终端 + 前台进程组」。
#   · 非交互 shell 里用 `&` 起的前端,SIGINT 会被继承成 SIG_IGN(实测 SigIgn=0x6),
#     单元内进程于是永远收不到 SIGINT —— 那会把「shell 后台化」错判成 systemd 不转发
#     信号(本探针上一版就踩了这个坑,把假绿写成了 FAIL);
#   · 正解是 pty:fork 出会话首进程并让它持有控制终端,再对它所在进程组发 SIGINT,
#     这正是终端行规程在按下 Ctrl-C 时做的事。
cat > "$WORK/sig-pty.py" <<'PYEOF'
import os, pty, select, signal, sys, time
delay = float(os.environ.get("SIG_DELAY", "1.5"))
cmd = sys.argv[1:]
pid, fd = pty.fork()
if pid == 0:                      # 子进程:新会话 + 该 pty 作控制终端
    os.execvp(cmd[0], cmd)
buf, start, sent, status = b"", time.time(), False, None
while True:
    if not sent and time.time() - start > delay:
        try:
            os.killpg(pid, signal.SIGINT)   # 等价于终端对前台进程组发 Ctrl-C
        except ProcessLookupError:
            pass
        sent = True
    try:
        r, _, _ = select.select([fd], [], [], 0.2)
    except OSError:
        break
    if r:
        try:
            chunk = os.read(fd, 4096)
        except OSError:
            chunk = b""
        if not chunk:
            break
        buf += chunk
    wpid, st = os.waitpid(pid, os.WNOHANG)
    if wpid:
        status = st
        break
if status is None:
    try:
        os.killpg(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    _, status = os.waitpid(pid, 0)
sys.stdout.write(buf.decode("utf-8", "replace"))
if os.WIFSIGNALED(status):
    sys.stdout.write("\nPTY_EXIT=signal:%d\n" % os.WTERMSIG(status))
else:
    sys.stdout.write("\nPTY_EXIT=code:%d\n" % os.WEXITSTATUS(status))
PYEOF

sig_case() { # sig_case <标签> <pipe|scope>
  local label="$1" mode="$2"
  local logf="$WORK/sig-$label.log" pidf="$WORK/sig-$label.innerpid" resf="$WORK/sig-$label.result"
  : > "$logf"; rm -f "$pidf"
  local -a front_cmd
  case "$mode" in
    pipe)  front_cmd=("${MGR[@]}" --quiet --wait --collect --pipe bash "$WORK/sig-inner.sh" "$pidf") ;;
    scope) front_cmd=("${MGR[@]}" --quiet --scope             bash "$WORK/sig-inner.sh" "$pidf") ;;
  esac
  # SIG_DELAY 给单元 1.5s 起来并写好 pid 文件,再发 Ctrl-C。
  SIG_DELAY=1.5 python3 "$WORK/sig-pty.py" "${front_cmd[@]}" >"$logf" 2>&1
  local front_rc; front_rc="$(sed -n 's/^PTY_EXIT=//p' "$logf" | tail -1)"
  sleep 1
  local alive=no
  [ -s "$pidf" ] && kill -0 "$(cat "$pidf")" 2>/dev/null && alive=yes
  local got; got="$(awk '/RECEIVED_INT/{n++} END{print n+0}' "$logf" 2>/dev/null)"
  printf '%-8s 前端退出=%s 单元内进程存活=%s SIGINT 送达单元=%s 单元内 pid=%s\n' \
    "$mode" "${front_rc:-?}" "$alive" "${got:-0}" "$(cat "$pidf" 2>/dev/null || echo '?')" | ev
  printf '%s %s %s\n' "${front_rc:-?}" "$alive" "${got:-0}" > "$resf"
  [ -s "$pidf" ] && kill "$(cat "$pidf")" 2>/dev/null
  return 0
}

sig_case pipe pipe
read -r SIG_PIPE_RC SIG_PIPE_ALIVE SIG_PIPE_GOT < "$WORK/sig-pipe.result"
case "${SIG_PIPE_RC:-}" in
  signal:2) FRONT_KILL="前端被 SIGINT 杀死" ;;
  code:130) FRONT_KILL="前端以 130 退出" ;;
  *)        FRONT_KILL="前端退出=${SIG_PIPE_RC:-?}" ;;
esac
if [ "$SIG_PIPE_ALIVE" = yes ]; then
  fail "S3 Ctrl-C 停不住沙箱内进程:--pipe 形态 $FRONT_KILL,但单元内进程仍存活且从未收到 SIGINT(送达=${SIG_PIPE_GOT:-0})→ 前端与单元脱钩,退出会留孤儿"
elif [ "${SIG_PIPE_GOT:-0}" -ge 1 ]; then
  pass "S3 --pipe 形态 Ctrl-C 能停住单元内进程($FRONT_KILL 送达=${SIG_PIPE_GOT} 存活=$SIG_PIPE_ALIVE)"
else
  fail "S3 --pipe 形态 SIGINT 未送达单元($FRONT_KILL 送达=${SIG_PIPE_GOT:-0} 存活=$SIG_PIPE_ALIVE)→ 前端退了,单元内进程没收到信号"
fi

sig_case scope scope
read -r SIG_SCOPE_RC SIG_SCOPE_ALIVE SIG_SCOPE_GOT < "$WORK/sig-scope.result"
blocked "S3 --scope 形态的 Ctrl-C 传播不单独判定(rc=${SIG_SCOPE_RC:-?} 存活=${SIG_SCOPE_ALIVE:-?} 送达=${SIG_SCOPE_GOT:-0}):该形态本身带不了沙箱属性(S2 已证),候选形态只剩 service 形态"

# ========================================================= S4: 网络白名单 ====
say "S4 · IPAddressAllow/Deny 网络白名单是否真拦得住"
if command -v curl >/dev/null 2>&1; then
  AH=example.com; DH=github.com
  AH4="$(getent ahostsv4 "$AH" | awk '{print $1}' | head -1)"
  DH4="$(getent ahostsv4 "$DH" | awk '{print $1}' | head -1)"
  # curl --resolve 把名字解析钉在本地:单元内不发 DNS 查询,连接直接打到那个 IP。
  # 这是把「DNS 被拦(53 端口)」与「目标 IP 被拦」分开的唯一办法 —— 用裸域名测,
  # IPAddressDeny=any 会先把 DNS 拦掉,失败退化成「解析不了」,原因分不清。
  RES_AH=(--resolve "$AH:443:${AH4:-127.0.0.1}")
  RES_DH=(--resolve "$DH:443:${DH4:-127.0.0.1}")
  a_plain="$(curl -sS -m 6 "${RES_AH[@]}" -o /dev/null -w '%{http_code}' "https://$AH" 2>/dev/null)"
  d_plain="$(curl -sS -m 6 "${RES_DH[@]}" -o /dev/null -w '%{http_code}' "https://$DH" 2>/dev/null)"
  printf '无沙箱(名字解析钉到确定 IP): %s(%s)=%s  %s(%s)=%s\n' \
    "$AH" "${AH4:-?}" "${a_plain:-ERR}" "$DH" "${DH4:-?}" "${d_plain:-ERR}" | ev
  if [ "${d_plain:-000}" = "000" ]; then
    printf '(对照: %s 本来就不可达 → 不能拿它「连不上」当拒绝证据,那是假绿;故只用可达的 %s 判)\n' "$DH" "$AH" | ev
  else
    printf '(对照: %s 无沙箱可通 → 若白名单下它不通才可能算拒绝证据)\n' "$DH" | ev
  fi
  # 旁证:裸域名(含 DNS)在 IPAddressDeny=any 下的表现。
  OUT_DENY="$(urun -p IPAddressDeny=any bash -c "curl -sS -m 6 -o /dev/null -w 'code=%{http_code}' https://$AH" 2>&1)"
  printf 'IPAddressDeny=any 下访问裸域名 %s(含 DNS 环节)→ %s\n' "$AH" \
    "$(printf '%s' "${OUT_DENY:-<无输出>}" | tr '\n' ' ' | cut -c1-120)" | ev
  # 正证:钉 IP 后 IPAddressDeny=any 是否真拦。
  DENY_PINNED="$(urun -p IPAddressDeny=any bash -c "curl -sS -m 6 ${RES_AH[*]} -o /dev/null -w 'deny_any=%{http_code}' https://$AH" 2>&1)"
  printf 'IPAddressDeny=any 下访问钉住的 %s(%s)→ %s\n' "$AH" "${AH4:-?}" \
    "$(printf '%s' "${DENY_PINNED:-<无输出>}" | tr '\n' ' ' | cut -c1-140)" | ev
  # 正反对照:白名单 IP 必须可达,非白名单 IP 必须不可达。
  PAIR="$(urun -p IPAddressDeny=any -p "IPAddressAllow=${AH4:-127.0.0.1}" \
    bash -c "curl -sS -m 6 ${RES_AH[*]} -o /dev/null -w 'allow=%{http_code} ' https://$AH; curl -sS -m 6 ${RES_DH[*]} -o /dev/null -w 'deny=%{http_code}' https://$DH" 2>&1)"
  printf 'IPAddressDeny=any + IPAddressAllow=%s(正:%s / 反:%s)→ %s\n' "${AH4:-?}" "$AH" "$DH" \
    "$(printf '%s' "${PAIR:-<无输出>}" | tr '\n' ' ' | cut -c1-160)" | ev
  printf 'unprivileged_bpf_disabled=%s(copilot 以普通用户跑时,BPF 过滤器装载不了)\n' \
    "$(cat /proc/sys/kernel/unprivileged_bpf_disabled 2>/dev/null || echo '?')" | ev
  printf 'manifest 现状(--allow-net 无白名单):\n' | ev
  grep -n 'allow-net' "$REPO/plugin/copilot/daedalus.plugin.json" | ev
  # 判据三段式,顺序不能换:
  #   ① 前提:无沙箱下该 IP 必须可达(否则「拒绝」可能是环境本来不通);
  #   ② 同一个 IP 在只开 deny 时是否仍 200 —— 仍 200 就是过滤器没生效,此时**任何**
  #      「deny=000」都只能算瞬时噪声(实测踩过:user manager 下 deny_any=200,而对照
  #      主机恰好瞬时不通,deny=000 差点被判成「白名单生效」);
  #   ③ 正反对照:允许后可达 + 非白名单不可达。
  BPF_STATE="$(cat /proc/sys/kernel/unprivileged_bpf_disabled 2>/dev/null || echo '?')"
  if [ "${a_plain:-000}" != "200" ]; then
    blocked "S4 前提不成立:无沙箱下 $AH(${AH4:-?}) 都不可达(code=${a_plain:-ERR}),本机无法区分「被拦」与「本来不通」→ 网络白名单用例不予判定"
  elif printf '%s' "$DENY_PINNED" | grep -q 'deny_any=200'; then
    if [ "$IS_ROOT" = 1 ]; then
      fail "S4 system manager 下 IPAddressDeny=any 未生效:同一个可达 IP 仍 200(deny_any=200)→ 该内核+systemd 组合上 BPF 白名单思路不成立"
    else
      fail "S4 user manager 下 IPAddressDeny=any 未生效:同一个可达 IP 仍 200(deny_any=200),unprivileged_bpf_disabled=$BPF_STATE → copilot 以用户身份跑时部署不了 BPF 网络白名单"
    fi
  elif printf '%s' "$PAIR" | grep -q 'allow=200' && ! printf '%s' "$PAIR" | grep -q 'deny=200'; then
    # 判据最强的一环是「同一个 IP 的开/关翻转」:无沙箱 200 → 只开 deny 时 000
    # → 加 IPAddressAllow 后 200。翻转只可能由该属性造成。
    pass "S4 IP 层白名单真生效:同一 IP($AH=${AH4:-?})无沙箱 200 → IPAddressDeny=any 下不可达(deny_any=000)→ 加入 IPAddressAllow 后 200(allowed=200),非白名单 $DH 仍不可达($MGR_LABEL)"
  else
    fail "S4 正反对照不成立:白名单 IP 在沙箱内也不可达(allow≠200),无法证明「非白名单不可达」"
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

# deno eval 的权限语义有两个版本分叉:2.9.5 上根本不接受权限旗标(usage 错),2.9.8 上
# 接受但不施加边界。两种都意味着旧 D3(deno eval + 期望 PermissionDenied)测不到 Deno
# 边界 —— 必须分开记。绝不能因为「没看到预期字符串」就落到 PASS:
# 上一版的 else 分支把「命令根本没跑起来」报成了「旧 D3 形态可用」(假绿)。
S5_A_CLEAN="$(printf '%s' "$S5_A" | sed 's/\x1b\[[0-9;]*m//g')"
if printf '%s' "$S5_A_CLEAN" | grep -qE 'unexpected argument|Usage: deno eval'; then
  fail "S5 旧 D3 判据不可用:本机 deno eval 不接受权限旗标(逐字:$(printf '%s' "$S5_A_CLEAN" | grep -oE "unexpected argument '[^']*' found" | head -1)),旧 D3 那条命令从未真正跑起来"
elif printf '%s' "$S5_A" | grep -q 'DENIED:PermissionDenied' \
     && printf '%s' "$S5_EVALDENY" | grep -q 'WROTE' \
     && printf '%s' "$S5_EVALQ" | grep -q 'granted'; then
  fail "S5 旧 D3 判据失效:deno eval 不做权限检查(--deny-write 仍 WROTE、query /usr = granted),所以 eval 下的 PermissionDenied 是 OS 的 EACCES,不是 Deno 拦的 —— 它测不到 Deno 边界"
elif printf '%s' "$S5_EVALDENY" | grep -q 'DENIED'; then
  pass "S5 deno eval 施加了权限检查(--deny-write 真被拒),但 e.name 需与断言一致才算可复用"
else
  fail "S5 deno eval 语义不可判定:$(printf '%s' "$S5_A_CLEAN" | head -1) / $(printf '%s' "$S5_EVALDENY" | head -1)"
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
