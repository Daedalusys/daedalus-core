#!/usr/bin/env bash
# 装机重试 / 失败分类的**行为**门禁。iso-autoinstall-test.sh 是静态断言(脚本里有没有那几行),
# 这里把 iso-autoinstall.sh 按库模式加载进来,用假 bootc 真跑 install_all_sources,钉住三件
# 只有真跑才看得见的事:
#
#   重试 同一个源的**瞬时**网络失败退避重试,重试次数由 DAEDALUS_INSTALL_RETRIES 定,
#        且重试用尽后换下一个源而不是死在一个源上
#   分类 rc → transient / timeout / fatal 是显式映射,绝不兜底成 transient ——
#        把 "Device is mounted" 这种装机逻辑失败当瞬时错误重试,等于用重试掩盖真缺陷
#   预算 总预算先于外层 INSTALL_TIMEOUT 耗尽,结局是「预算耗尽」(与普通失败、与外层
#        TIMEOUT 都可区分):否则外层会把"外因慢"报成 TIMEOUT,那正是要消灭的假因
#
# 用法: iso-install-retry-test.sh    # 全绿打印 RESULT: SELFTEST PASS
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INST="$REPO/tests/real-machine/iso-autoinstall.sh"
VERIFY="$REPO/tests/real-machine/iso-verify.sh"
WORKFLOW="$REPO/.github/workflows/build.yml"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail_n=0
pass_n=0
st() { # st <条件结果 0/1> <说明>
    if [ "$1" -eq 0 ]; then
        pass_n=$((pass_n + 1)); printf 'PASS %s\n' "$2"
    else
        fail_n=$((fail_n + 1)); printf 'FAIL %s\n' "$2"
    fi
}
has() { grep -qF -- "$1" "$2" 2>/dev/null; }
contains() { case "$1" in *"$2"*) return 0 ;; esac; return 1; }
nlines() { wc -l <"$1" 2>/dev/null | tr -d ' '; }

# 假 bootc:按 $BOOTC_PLAN 逐行消费 "<rc>|<日志正文>",正文写到 stderr。
# 真实调用里 bootc 的 stdout+stderr 都被重定向进 ATT_LOG,分类正是读那份日志 ——
# 所以"日志里有什么"必须由被测脚本自己捕获,而不是由测试直接喂给 classify_failure。
mkdir -p "$WORK/bin"
cat >"$WORK/bin/bootc" <<'FAKE'
#!/usr/bin/env bash
plan="${BOOTC_PLAN:?假 bootc 需要 BOOTC_PLAN}"
line="$(head -1 "$plan" 2>/dev/null)"
[ -n "$line" ] || { printf 'fake bootc: plan 已耗尽\n' >&2; exit 98; }
tail -n +2 "$plan" >"$plan.next" && mv "$plan.next" "$plan"
msg="${line#*|}"
case "$msg" in ''|-) : ;; *) printf '%s\n' "$msg" >&2 ;; esac
exit "${line%%|*}"
FAKE
chmod +x "$WORK/bin/bootc"

# 在子 shell 里以**库模式**加载被测脚本:主流程不执行(否则它读 /proc/cmdline 就 die),
# 块设备与退避等待全部压掉。入参是要在那个环境里跑的 bash 代码。
lib_eval() {
    (
        set +u
        export PATH="$WORK/bin:$PATH"
        export DAEDALUS_ISO_AUTOINSTALL_LIB=1
        # shellcheck disable=SC1090
        source "$INST" || exit 90
        set +u
        : "${LIB_TTY:=$WORK/tty-default}"
        TTY="$LIB_TTY"
        # say 在生产里写串口(每行独立写);测试里若落进同一个普通文件,后面的写会把前面的
        # 覆盖掉,「哪一次尝试拿了多少时限/预算」这类过程事实就看不见了。所以把 say 改写成
        # 追加到独立文件;die() 往 $TTY 倒日志的行为保持原样。
        say() { printf 'daedalus-iso-autoinstall: %s\n' "$*" >>"${LIB_SAY_LOG:-$WORK/say-default}"; }
        # 测试不碰块设备;但调用**次数**要留痕 —— "每次尝试前清理"只查静态 grep 不够。
        umount_target_disk() { printf 'umount\n' >>"${LIB_UMOUNT_LOG:-$WORK/umount-default}"; }
        sleep() { :; }
        eval "$1"
    )
}

# 跑一次 install_all_sources。
#   $1 标签(plan/summary/umount/tty/log 的文件名后缀)  $2 RETRIES  $3 BUDGET  $4 是否两源(0/1)
# 退出码原样透传(die → 1)。OUTCOME 行走 EXIT trap:die 会 exit,普通 printf 到不了。
run_install() {
    local tag="$1" retries="$2" budget="$3" two="${4:-0}"
    local plan="$WORK/plan.$tag" sum="$WORK/summary.$tag" ulog="$WORK/umount.$tag"
    local log="$WORK/install.$tag" tty="$WORK/tty.$tag" saylog="$WORK/say.$tag"
    : >"$sum"; : >"$ulog"; : >"$saylog"
    BOOTC_PLAN="$plan" LIB_TTY="$tty" LIB_SAY_LOG="$saylog" LIB_UMOUNT_LOG="$ulog" \
    lib_eval "
        DEV=/dev/vda
        IMGREF='ghcr.io/daedalusys/daedalus-os:test'
        SRC_REF=''
        [ '$two' = 1 ] && SRC_REF='ghcr.1ms.run/daedalusys/daedalus-os:test'
        HTTP_PROXY_URL=''
        SRC_TIMEOUT=900
        RETRIES=$retries
        BACKOFF=1
        BACKOFF_MAX=60
        INSTALL_BUDGET=$budget
        INSTALL_LOG='$log'
        INSTALL_SUMMARY='$sum'
        trap 'printf \"OUTCOME installed=%s attempts=%s\n\" \"\${installed:-?}\" \"\${attempts:-?}\"' EXIT
        install_all_sources
    "
}

# 后置断言:摘要行数 == attempts、每行带 class=、每次尝试前都清理过。
summary_ok() { # summary_ok <标签> <期望 attempts>
    local sum="$WORK/summary.$1" want="$2"
    [ "$(nlines "$sum")" = "$want" ]; st $? "摘要 $1:行数 == attempts($want;实得 $(nlines "$sum"))"
    [ "$(grep -c 'class=' "$sum")" = "$want" ]; st $? "摘要 $1:每行带 class=($want/$want;实得 $(grep -c 'class=' "$sum"))"
    [ "$(nlines "$WORK/umount.$1")" = "$want" ]; st $? "清理 $1:每次尝试前都 umount_target_disk($want 次;实得 $(nlines "$WORK/umount.$1"))"
}

# ------------------------------------------------------------ 分类(表驱动,显式) --
printf '%s\n' 'error: Installing to disk: Creating ostree deployment: Preparing import: Fetching manifest: failed to invoke method GetFullConfig: Get "https://ghcr-cf.mrs.1ms.run/ghcrblobs07/blobs/sha256:d7601b6e...": net/http: TLS handshake timeout' >"$WORK/log-tls"
printf '%s\n' 'error: Installing to disk: Creating rootfs: Device /dev/vda is mounted' >"$WORK/log-mounted"
printf '%s\n' 'error: Installing to disk: Creating ostree deployment: Preparing import: writing deployment: Invalid argument' >"$WORK/log-ostree"
printf '%s\n' 'error: pulling image: read tcp 10.0.2.15:37666->1.2.3.4:443: i/o timeout' >"$WORK/log-iotimeout"

classify_case() { # classify_case <rc> <日志路径> <期望分类> <说明>
    local out
    out="$(LIB_TTY="$WORK/tty.classify" lib_eval "classify_failure $1 '$2'")"
    [ "$out" = "$3" ]; st $? "$4(期望 [${3}] 实得 [${out}])"
}
classify_case 1 "$WORK/log-tls" transient "分类 rc=1 + TLS handshake timeout → transient"
classify_case 1 "$WORK/log-mounted" fatal "分类 rc=1 + Device is mounted → fatal(装机逻辑失败不重试)"
classify_case 1 "$WORK/log-ostree" fatal "分类 rc=1 + Creating ostree deployment 失败 → fatal"
classify_case 124 "$WORK/log-tls" timeout "分类 rc=124 → timeout(同源不重试)"
classify_case 137 "$WORK/log-tls" timeout "分类 rc=137(SIGKILL) → timeout"
classify_case 0 "$WORK/log-tls" "" "分类 rc=0 → 空输出(成功不分类,哪怕日志里有超时字样)"

out="$(LIB_TTY="$WORK/tty.tr" lib_eval "is_transient_log '$WORK/log-tls' && echo hit || echo miss")"
[ "$out" = hit ]; st $? "分类 is_transient_log 命中 TLS 超时日志"
out="$(LIB_TTY="$WORK/tty.tr" lib_eval "is_transient_log '$WORK/log-mounted' && echo hit || echo miss")"
[ "$out" = miss ]; st $? "分类 is_transient_log 不命中装机逻辑失败日志(不兜底成 transient)"
out="$(LIB_TTY="$WORK/tty.tr" lib_eval "is_transient_log '$WORK/log-iotimeout' && echo hit || echo miss")"
[ "$out" = hit ]; st $? "分类 is_transient_log 命中 i/o timeout"

# ------------------------------------------------------------------ 循环:重试 --
# 第 1 次 TLS 超时、第 2 次成功:同源重试一次就够,不该去开第二个源。
printf '%s\n' '1|Get "https://ghcr-cf.mrs.1ms.run/x": net/http: TLS handshake timeout' '0|-' >"$WORK/plan.retry"
out="$(run_install retry 1 2100 0)"; rc=$?
contains "$out" 'installed=1 attempts=2'; st $? "循环 同源瞬时失败 → 退避重试一次后成功(期望 installed=1 attempts=2;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -eq 0 ]; st $? "循环 重试成功后正常返回(rc=$rc)"
summary_ok retry 2

# rc=124 是**外层时限到**,不是网络瞬时失败:同源重试只会再烧一个 SRC_TIMEOUT。
printf '%s\n' '124|-' '0|-' >"$WORK/plan.tmo"
out="$(run_install tmo 1 2100 0)"; rc=$?
contains "$out" 'installed=0 attempts=1'; st $? "循环 rc=124 同源不重试(期望 attempts=1;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -ne 0 ]; st $? "循环 全部失败时 die(rc=$rc)"
summary_ok tmo 1
if has '预算耗尽' "$WORK/say.tmo"; then
    st 1 "循环 普通失败的 die 消息不得是「预算耗尽」(否则两种结局无法区分)"
else
    st 0 "循环 普通失败的 die 消息与「预算耗尽」可区分"
fi

# 两个源都永远瞬时失败:每源 1+RETRIES 次,然后 die(不是死在第一个源上)。
printf '%s\n' '1|read tcp: i/o timeout' '1|read tcp: i/o timeout' \
                '1|read tcp: i/o timeout' '1|read tcp: i/o timeout' >"$WORK/plan.exhaust"
out="$(run_install exhaust 1 2100 1)"; rc=$?
contains "$out" 'installed=0 attempts=4'; st $? "循环 每源 1+RETRIES 次后换下一源(期望 attempts=4;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -ne 0 ]; st $? "循环 两源用尽后 die(rc=$rc)"
summary_ok exhaust 4

# 第一个源用尽重试、第二个源成功:换源语义正确,且成功即停。
printf '%s\n' '1|read tcp: i/o timeout' '1|read tcp: i/o timeout' '0|-' >"$WORK/plan.next"
out="$(run_install next 1 2100 1)"; rc=$?
contains "$out" 'installed=1 attempts=3'; st $? "循环 第一源用尽 → 第二源成功(期望 installed=1 attempts=3;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -eq 0 ]; st $? "循环 换源后成功正常返回(rc=$rc)"
summary_ok next 3

# fatal 不许重试:fatal 重试等于用重试掩盖真缺陷,而且白烧预算。
printf '%s\n' '1|error: Installing to disk: Creating rootfs: Device /dev/vda is mounted' >"$WORK/plan.fatal"
out="$(run_install fatal 1 2100 0)"; rc=$?
contains "$out" 'installed=0 attempts=1'; st $? "循环 fatal 不重试同源(期望 attempts=1;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -ne 0 ]; st $? "循环 fatal 直接 die(rc=$rc)"

# ---------------------------------------------------------------- 循环:预算 --
# 预算 0:一次尝试都不发起,结局是「预算耗尽」—— 与普通失败、与外层 TIMEOUT(124)都不同。
printf '%s\n' '0|-' >"$WORK/plan.budget"
out="$(run_install budget 1 0 0)"; rc=$?
contains "$out" 'installed=0 attempts=0'; st $? "预算 预算耗尽前不发起尝试(期望 attempts=0;实得 $(printf '%s' "$out" | tr '\n' ' '))"
if has '预算耗尽' "$WORK/say.budget"; then
    st 0 "预算 结局报「预算耗尽」(不是笼统的失败)"
else
    st 1 "预算 结局必须报「预算耗尽」(串口末尾:$(tail -1 "$WORK/say.budget" 2>/dev/null))"
fi
[ "$rc" -ne 124 ]; st $? "预算 预算耗尽的退出码不是 124(外层的 TIMEOUT 才有权用 124)"
[ "$(nlines "$WORK/summary.budget")" = 0 ]; st $? "预算 没发起尝试就不写摘要行"

# ------------------------------------------------ 预算:单次时限按「有无后备源」分形状 --
# CI 只剩一个源(加速镜像已退役,core#138):没有后备可换时,SRC_TIMEOUT 的截断只会白扔预算,
# 最后一个源必须拿剩余预算的全部。两源时截断仍要保留:装不完就换下一个源。
printf '%s\n' '124|-' >"$WORK/plan.lastsrc"
out="$(run_install lastsrc 1 2100 0)"; rc=$?
pl="$(grep -o '单次时限 [0-9]*s' "$WORK/say.lastsrc" | head -1)"
[ "$pl" = '单次时限 2100s' ]; st $? "时限 单源(最后一个源)拿剩余预算全部(期望 [单次时限 2100s] 实得 [$pl])"
contains "$out" 'installed=0 attempts=1'; st $? "时限 单源仍是 rc=124 即 die、同源不重试(期望 attempts=1;实得 $(printf '%s' "$out" | tr '\n' ' '))"

printf '%s\n' '124|-' '0|-' >"$WORK/plan.firstsrc"
out="$(run_install firstsrc 1 2100 1)"; rc=$?
pls="$(grep -o '单次时限 [0-9]*s' "$WORK/say.firstsrc" | tr '\n' ' ')"
contains "$pls" '单次时限 900s'; st $? "时限 还有后备源时仍按 SRC_TIMEOUT 截断(期望含 [单次时限 900s] 实得 [$pls])"
contains "$pls" '单次时限 2100s'; st $? "时限 换到最后一个源后拿剩余预算全部(期望含 [单次时限 2100s] 实得 [$pls])"
contains "$out" 'installed=1 attempts=2'; st $? "时限 多源换源后成功(期望 installed=1 attempts=2;实得 $(printf '%s' "$out" | tr '\n' ' '))"

# 剩余预算比 SRC_TIMEOUT 还小时,(0, 剩余预算] 的夹取仍然生效 —— 不许越过总预算。
printf '%s\n' '124|-' >"$WORK/plan.smallbudget"
out="$(run_install smallbudget 1 600 0)"; rc=$?
pl="$(grep -o '单次时限 [0-9]*s' "$WORK/say.smallbudget" | head -1)"
[ "$pl" = '单次时限 600s' ]; st $? "时限 剩余预算小于 SRC_TIMEOUT 时取剩余预算(期望 [单次时限 600s] 实得 [$pl])"

# ------------------------------------------------------------ 预算与外层时限关系 --
# 总预算必须**严格小于** iso-verify.sh 等 guest 重启的上限,否则外层把"预算耗尽"
# 报成 TIMEOUT —— 那正是本次要消灭的假因。
lib_budget="$(LIB_TTY="$WORK/tty.cfg" lib_eval 'printf "%s" "$INSTALL_BUDGET"')"
verify_timeout="$(sed -n 's/.*VERIFY_INSTALL_TIMEOUT:-\([0-9][0-9]*\)}.*/\1/p' "$VERIFY" | head -1)"
if [ -n "$lib_budget" ] && [ -n "$verify_timeout" ] && [ "$lib_budget" -lt "$verify_timeout" ]; then
    st 0 "预算 默认总预算 ${lib_budget}s 严格小于外层 INSTALL_TIMEOUT ${verify_timeout}s"
else
    st 1 "预算 必须满足 总预算 < 外层 INSTALL_TIMEOUT(budget=${lib_budget:-?} timeout=${verify_timeout:-?})"
fi

# --------------------------------------------------------------------- 接线 --
if [ -f "$WORKFLOW" ]; then
    has 'bash tests/ci/iso-install-retry-test.sh' "$WORKFLOW"
    st $? "接线 build.yml 的 CI gate selftests 真的跑了本测试(静态门禁不接线等于不存在)"
else
    st 1 "接线 出货档 workflow 存在:$WORKFLOW"
fi
has '/run/daedalus-iso-install-summary.txt' "$VERIFY"
st $? "接线 iso-verify.sh 装机失败时收集 summary(否则证据只活在 live 根里)"

# ------------------------------------------------ 清盘判据:设备子目录挂载(识别) --
# 线上形态:bootc 拉镜像时把 /dev/vda3 的子目录 /ostree/bootc/storage 挂到
# /run/bootc/storage。findmnt -S /dev/vda 匹配不到(源是 vda3 不是整盘),lsblk 也不列
# 这种"挂载根不是 /"的项 —— 两个老来源同时漏,清盘就判"干净"。这里把两个老来源桩成
# "看不见"(正是线上行为),让断言只压在 mountinfo 这条新来源上,顺带钉住前缀歧义红线。
mkdir -p "$WORK/blind"
printf '#!/usr/bin/env bash\nexit 1\n' >"$WORK/blind/findmnt"
printf '#!/usr/bin/env bash\nexit 1\n' >"$WORK/blind/lsblk"
chmod +x "$WORK/blind/findmnt" "$WORK/blind/lsblk"

# 桩 mountinfo:第 5 列是挂载点,`-` 之后第二列是源设备。
mi="$WORK/mountinfo.subvol"
cat >"$mi" <<'EOF'
36 35 98:0 / / rw - xfs /dev/vda rw
37 36 98:1 /ostree/bootc/storage /run/bootc/storage rw - xfs /dev/vda3 rw
38 36 98:2 / /mnt/other rw - xfs /dev/vdb3 rw
39 36 98:3 / /mnt/ten rw - xfs /dev/vda10 rw
EOF
out="$(PATH="$WORK/blind:$PATH" DAEDALUS_MOUNTINFO="$mi" bash -c "
  DAEDALUS_ISO_AUTOINSTALL_LIB=1 source '$INST'
  DEV=/dev/vda
  target_mountpoints")"
[ "$(printf '%s\n' "$out" | grep -cx '/run/bootc/storage')" = 1 ]
st $? "清盘判据 设备子目录挂载被看见(/run/bootc/storage;实得 [$(printf '%s' "$out" | tr '\n' ' ')])"
[ "$(printf '%s\n' "$out" | grep -cx '/')" = 1 ]
st $? "清盘判据 整盘挂载仍被看见(/)"
[ "$(printf '%s\n' "$out" | grep -cx '/mnt/other')" = 0 ]
st $? "清盘判据 前缀歧义:/dev/vdb3 不得命中"
# 目标盘恒为整盘(主流程强制 lsblk TYPE=disk),所以 vda10 就是它的第 10 个分区:
# 漏掉它 = 残留挂载没清 = 又回到本次要修的那个故障,必须命中。
[ "$(printf '%s\n' "$out" | grep -cx '/mnt/ten')" = 1 ]
st $? "清盘判据 两位数字分区 /dev/vda10 必须命中(它是 vda 的真分区)"

# 设备命名规则不同:nvme0n1 的分区带 p(nvme0n1p3)。只认一位数字后缀的话这类盘全漏。
mi_nvme="$WORK/mountinfo.nvme"
cat >"$mi_nvme" <<'EOF'
40 36 259:0 / / rw - xfs /dev/nvme0n1 rw
41 36 259:3 /ostree/bootc/storage /run/bootc/storage rw - xfs /dev/nvme0n1p3 rw
42 36 259:5 /srv/data /mnt/nvme-bracket rw - xfs /dev/nvme0n1p4[/srv/data] rw
43 36 259:4 / /mnt/nvme-other rw - xfs /dev/nvme0n2p3 rw
EOF
out="$(PATH="$WORK/blind:$PATH" DAEDALUS_MOUNTINFO="$mi_nvme" bash -c "
  DAEDALUS_ISO_AUTOINSTALL_LIB=1 source '$INST'
  DEV=/dev/nvme0n1
  target_mountpoints")"
[ "$(printf '%s\n' "$out" | grep -cx '/run/bootc/storage')" = 1 ]
st $? "清盘判据 NVMe 分区子目录挂载被看见(/dev/nvme0n1p3;实得 [$(printf '%s' "$out" | tr '\n' ' ')])"
[ "$(printf '%s\n' "$out" | grep -cx '/')" = 1 ]
st $? "清盘判据 NVMe 整盘挂载仍被看见(/)"
[ "$(printf '%s\n' "$out" | grep -cx '/mnt/nvme-bracket')" = 1 ]
st $? "清盘判据 NVMe 分区 findmnt 风格「设备[子目录]」被看见(/dev/nvme0n1p4[/srv/data])"
[ "$(printf '%s\n' "$out" | grep -cx '/mnt/nvme-other')" = 0 ]
st $? "清盘判据 前缀歧义:/dev/nvme0n2p3 不得命中"

# 读不到 mountinfo 时不得"没有挂载点"——那会把清盘退化成"看不见就当干净"(假绿)。
out="$(PATH="$WORK/blind:$PATH" DAEDALUS_MOUNTINFO="$WORK/mountinfo.不存在" bash -c "
  DAEDALUS_ISO_AUTOINSTALL_LIB=1 source '$INST'
  DEV=/dev/vda
  target_mountpoints")"
[ -n "$(printf '%s' "$out" | tr -d '[:space:]')" ]
st $? "清盘判据 mountinfo 读不到时不返回空(否则清盘失明还算干净)"

# ------------------------------------- 清盘判据:端到端复现线上那次失败(行为) --
# 假 bootc 除按 $BOOTC_PLAN 消费 rc 外,还模拟真实 bootc 的前置判断:目标盘上还有残留
# 挂载就拒绝落盘(真实报错 "Device /dev/vda is mounted")。清盘没把子目录挂载挂掉时,
# 第二个源就根本轮不到成功 —— 这正是线上那次红(第 1 个源超时后卡死在清盘)。
mkdir -p "$WORK/e2ebin"
cat >"$WORK/e2ebin/bootc" <<'FAKE'
#!/usr/bin/env bash
mi="${DAEDALUS_MOUNTINFO:?端到端假 bootc 需要桩 mountinfo}"
if awk '$5 == "/run/bootc/storage" { found = 1 } END { exit !found }' "$mi"; then
    printf 'error: Installing to disk: Creating rootfs: Device /dev/vda is mounted\n' >&2
    exit 1
fi
plan="${BOOTC_PLAN:?假 bootc 需要 BOOTC_PLAN}"
line="$(head -1 "$plan" 2>/dev/null)"
[ -n "$line" ] || { printf 'fake bootc: plan 已耗尽\n' >&2; exit 98; }
tail -n +2 "$plan" >"$plan.next" && mv "$plan.next" "$plan"
msg="${line#*|}"
case "$msg" in ''|-) : ;; *) printf '%s\n' "$msg" >&2 ;; esac
exit "${line%%|*}"
FAKE
chmod +x "$WORK/e2ebin/bootc"

# 与 lib_eval 相同,但**保留真实的 umount_target_disk** —— 清盘判据本身就是要验的东西,
# 桩掉它等于把被测代码换成桩。umount/swapoff 桩成"真的把挂载从桩 mountinfo 里删掉"
# (内核在卸载后就是这么做的),让"清理前 / 清理后"两次复查看到同一份会变化的状态。
lib_eval_real_umount() {
    (
        set +u
        export PATH="$WORK/blind:$WORK/e2ebin:$PATH"
        export DAEDALUS_ISO_AUTOINSTALL_LIB=1
        # shellcheck disable=SC1090
        source "$INST" || exit 90
        set +u
        TTY="${LIB_TTY:?}"
        umount() {
            local a mp=""
            for a in "$@"; do case "$a" in -*) ;; *) mp="$a" ;; esac; done
            printf 'umount %s\n' "$mp" >>"${LIB_UMOUNT_LOG:?}"
            [ -n "$mp" ] || return 0
            awk -v mp="$mp" '$5 != mp' "$MOUNTINFO" >"$MOUNTINFO.next" &&
                mv "$MOUNTINFO.next" "$MOUNTINFO"
        }
        swapoff() { return 0; }
        sleep() { :; }
        eval "$1"
    )
}

run_install_e2e() { # run_install_e2e <标签> <桩 mountinfo> <plan>
    local tag="$1" mi="$2" plan="$3"
    local sum="$WORK/summary.$tag" ulog="$WORK/umount.$tag"
    local log="$WORK/install.$tag" tty="$WORK/tty.$tag"
    : >"$sum"; : >"$ulog"
    DAEDALUS_MOUNTINFO="$mi" BOOTC_PLAN="$plan" LIB_TTY="$tty" LIB_UMOUNT_LOG="$ulog" \
    lib_eval_real_umount "
        DEV=/dev/vda
        IMGREF='ghcr.io/daedalusys/daedalus-os:test'
        SRC_REF='ghcr.1ms.run/daedalusys/daedalus-os:test'
        HTTP_PROXY_URL=''
        SRC_TIMEOUT=900
        RETRIES=1
        BACKOFF=1
        BACKOFF_MAX=60
        INSTALL_BUDGET=2100
        INSTALL_LOG='$log'
        INSTALL_SUMMARY='$sum'
        trap 'printf \"OUTCOME installed=%s attempts=%s\n\" \"\${installed:-?}\" \"\${attempts:-?}\"' EXIT
        install_all_sources
    "
}

# 第 1 个源超时(rc=124)且目标盘上留着 vda3 的子目录挂载 → 清盘必须真的把它挂掉,
# 让第 2 个源跑起来并成功。修之前清盘看不见它,第二个源一次都跑不到(线上原样)。
e2e_mi="$WORK/mountinfo.e2e"
cat >"$e2e_mi" <<'EOF'
36 35 98:0 / / rw - xfs /dev/vda rw
37 36 98:1 /ostree/bootc/storage /run/bootc/storage rw - xfs /dev/vda3 rw
38 36 98:2 / /mnt/other rw - xfs /dev/vdb3 rw
EOF
printf '%s\n' '124|-' '0|-' >"$WORK/plan.e2e"
out="$(run_install_e2e e2e "$e2e_mi" "$WORK/plan.e2e")"; rc=$?
contains "$out" 'installed=1 attempts=2'
st $? "端到端 第一个源超时后清盘 → 第二个源跑起来并成功(期望 installed=1 attempts=2;实得 $(printf '%s' "$out" | tr '\n' ' '))"
[ "$rc" -eq 0 ]; st $? "端到端 换源后成功正常返回(rc=$rc)"
[ "$(grep -cx 'umount /run/bootc/storage' "$WORK/umount.e2e" 2>/dev/null)" = 1 ]
st $? "端到端 子目录挂载被真正 umount(umount 记录:$(printf '%s' "$(tr '\n' ' ' <"$WORK/umount.e2e" 2>/dev/null)"))"
[ "$(grep -cx 'umount /mnt/other' "$WORK/umount.e2e" 2>/dev/null)" = 0 ]
st $? "端到端 前缀歧义盘 /dev/vdb3 的挂载不得被 umount"
[ "$(nlines "$WORK/summary.e2e")" = 2 ]
st $? "端到端 摘要行数 == attempts(2;实得 $(nlines "$WORK/summary.e2e"))"

echo
printf -- '---------------- %d 条通过 / %d 条失败 ----------------\n' "$pass_n" "$fail_n"
if [ "$fail_n" -eq 0 ]; then
    echo "RESULT: SELFTEST PASS"
    exit 0
fi
echo "RESULT: SELFTEST FAIL ($fail_n)"
exit 1
