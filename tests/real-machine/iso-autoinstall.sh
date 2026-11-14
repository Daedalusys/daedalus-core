#!/usr/bin/env bash
# 无人值守装机:在 generic-iso 的 live 环境里把镜像装到目标盘,装完重启。
#
# 为什么是「live 环境自装」而不是 kickstart
#   generic-iso **不消费 blueprint**(实测:传了 --blueprint 构建成功但 manifest 里零痕迹,
#   源码里 manifestForGenericISO 根本不接 bp),它也没有 Anaconda —— 这张 ISO 的 live 根
#   就是 payload 容器的 rootfs。所以让 live 环境自己执行 bootc install 是把系统真正落到
#   盘上的唯一路径。探查证据见 docs/superpowers/plans/notes/2026-11-06-iso-blueprint.md。
#
# 安全底线(与 Review Focus 一一对应)
#   - 目标盘**必须**由内核参数 daedalus.install.disk= 点名;没给就拒绝执行,
#     这条路径上不存在任何"自动挑第一块盘"的逻辑;
#   - 只接受整盘(lsblk TYPE=disk),并显式剔除 live 介质自身所在的设备;
#   - 本脚本只在 live 介质上跑(由单元文件的 ConditionKernelCommandLine 保证),
#     装好的系统里同一份文件不会被再次触发。
#
# 失败语义(为什么不是「一次失败整腿红」)
#   拉 1.5G 镜像时第三方加速源偶发 TLS 超时,一次抖动就把整条门禁判红,而同一个 commit
#   重跑又绿 —— 那是外因,不是装机逻辑缺陷。所以:
#     - 瞬时网络失败(见 is_transient_log)同源退避重试,次数由 DAEDALUS_INSTALL_RETRIES 定;
#     - 失败分三类(transient / timeout / fatal),fatal **不重试**:用重试掩盖真缺陷更贵;
#     - 全程有总预算 DAEDALUS_INSTALL_BUDGET,且必须严格小于外层 iso-verify.sh 的
#       INSTALL_TIMEOUT —— 否则外层会把"预算耗尽"笼统报成 TIMEOUT,那正是要消灭的假因。
#
# /etc/daedalus/iso-install.conf 的键
#   DAEDALUS_INSTALL_IMGREF           规范 ref:既是兜底安装源,也是装好系统的 target
#                                    (target 决定后续更新通道,不能指向加速镜像)
#   DAEDALUS_INSTALL_SOURCE_IMGREF    可选:优先安装源(加速镜像),按 manifest digest 钉死
#   DAEDALUS_INSTALL_SOURCE_TIMEOUT   可选:单个源的装机时限(默认 900 秒)
#   DAEDALUS_INSTALL_RETRIES          可选:同一个源瞬时失败的重试次数(默认 1;每源共 1+N 次)
#   DAEDALUS_INSTALL_BUDGET           可选:全部尝试的总预算秒数(默认 2100,必须 < 外层 2400)
#   DAEDALUS_INSTALL_BACKOFF          可选:首次退避秒数(默认 10,每次 ×2,封顶 60)
#
# 可测性:DAEDALUS_ISO_AUTOINSTALL_LIB=1 时只定义常量与函数,不跑主流程
#   (tests/ci/iso-install-retry-test.sh 靠它把本脚本当库来驱动)。
set -uo pipefail

TTY=/dev/ttyS0
IMGREF_FILE=/etc/daedalus/iso-install.conf
INSTALL_LOG=/run/daedalus-iso-autoinstall.log
# 每次尝试一行摘要(class=ok/transient/timeout/fatal):失败时由 iso-verify.sh 收进
# evidence,让"外因不可得"与"装机逻辑失败"在 artifact 里就能分开,不用人肉翻日志。
INSTALL_SUMMARY="${DAEDALUS_INSTALL_SUMMARY:-/run/daedalus-iso-install-summary.txt}"

# conf 没写就吃这些默认值。BUDGET 必须严格小于 iso-verify.sh 的 INSTALL_TIMEOUT(2400):
# 让预算**先**耗尽,才能给出「预算耗尽」这个独立结局,而不是被外层报成 TIMEOUT。
SRC_TIMEOUT=900
RETRIES="${DAEDALUS_INSTALL_RETRIES:-1}"
INSTALL_BUDGET="${DAEDALUS_INSTALL_BUDGET:-2100}"
BACKOFF="${DAEDALUS_INSTALL_BACKOFF:-10}"
BACKOFF_MAX=60

DEV=""
IMGREF=""
SRC_REF=""
HTTP_PROXY_URL=""
installed=0
attempt=0
attempts=0

say() { printf 'daedalus-iso-autoinstall: %s\n' "$*" >"$TTY" 2>/dev/null || true; }
# die 把**所有**尝试的日志都倒到串口:每次尝试一份文件,否则只剩最后一次,
# 第一次为什么失败就永远看不到(那句 "Device ... is mounted" 会盖掉它)。
die() {
    say "失败:$*"
    local f
    for f in "$INSTALL_LOG"*; do
        [ -f "$f" ] || continue
        printf -- '--- %s ---\n' "$f" >"$TTY" 2>/dev/null || true
        cat "$f" >"$TTY" 2>/dev/null || true
    done
    exit 1
}

# conf_num <键> <默认值> —— 从 conf 里取一个**数字**键;缺失或非数字一律用默认值
# (不要因为一个手抖的配置把时限/预算变成 0 或空)。
conf_num() {
    local v
    v="$(sed -n "s/^$1=//p" "$IMGREF_FILE" 2>/dev/null | head -1)"
    case "$v" in
        ''|*[!0-9]*) printf '%s' "$2" ;;
        *) printf '%s' "$v" ;;
    esac
}

# bootc 的 --source-imgref 要**显式 transport 前缀**(实测裸 ref 会被判 Invalid transport),
# 配置里存的是人读得懂的裸 ref,这里补前缀;已经带前缀的原样透传。
with_transport() {
    case "$1" in
        registry:*|docker://*|containers-storage:*|oci:*|oci-archive:*|dir:*) printf '%s' "$1" ;;
        *) printf 'registry:%s' "$1" ;;
    esac
}

# target_mountpoints —— 列出 $DEV 及其分区上的挂载点。
#
# 两个来源必须取并集:`findmnt -S $DEV` 只匹配**整盘**挂载,分区挂载只有 lsblk 看得见;
# 只用其中一个,"检查通过"就可能只是"没看见"。
# 非绝对路径的条目(`[SWAP]` 这类渲染占位符)不是挂载点,跳过。
target_mountpoints() {
    { findmnt -rno TARGET -S "$DEV" 2>/dev/null
      lsblk -rno MOUNTPOINT "$DEV" 2>/dev/null
    } | while read -r mp; do
        case "$mp" in /*) printf '%s\n' "$mp" ;; esac
    done | sort -u
}

# umount_target_disk —— 把 $DEV 上的残留挂载/swap 清干净,并把清理前后的盘状态打进串口。
#
# 为什么每次装机前都要清:上一次尝试(尤其被 timeout 打断的)会在目标盘上留下分区与
# 挂载点,下一次 bootc 就会以 "Creating rootfs: Device $DEV is mounted" 收场 ——
# 那句错不含挂载点,排查时等于没有信息,还会把真正的首因盖掉。
#
# 只动 $DEV 自己:live 介质的载体在前面已按拓扑排除(那是「擦掉自己」的红线)。
# 不做 wipefs:本故障是挂载态残留,不是旧文件系统签名;`bootc install --wipe`
# 自己会重写分区表,多一次 wipefs 只增加破坏面。
#
# 不降级成 `umount -l`:lazy umount 会把挂载从 mountinfo 摘掉,但内核要等最后一个引用
# 释放才真正放开块设备 —— 后置检查因此看不见残留,而 bootc 照样 EBUSY。busy 就直接报错
# 收工,并把"谁占着"的现场留下。
umount_target_disk() {
    say "清理目标盘 $DEV(清理前)"
    lsblk -o NAME,SIZE,TYPE,MOUNTPOINT "$DEV" >"$TTY" 2>/dev/null || true
    local mp still
    # swap 不是挂载:先 swapoff,否则擦盘时分区仍被内核占着。
    while read -r mp; do
        [ -n "$mp" ] || continue
        swapoff "$mp" 2>/dev/null || die "swapoff $mp 失败(目标盘仍被占用)"
    done < <(lsblk -rpno PATH,FSTYPE "$DEV" 2>/dev/null | awk '$2=="swap"{print $1}')
    while read -r mp; do
        [ -n "$mp" ] || continue
        umount -R "$mp" 2>/dev/null || {
            findmnt -T "$mp" -o TARGET,SOURCE,FSTYPE,OPTIONS >"$TTY" 2>/dev/null || true
            die "卸载 $mp 失败(仍被占用;现场见上)"
        }
    done < <(target_mountpoints)
    # 后置检查必须与清理用**同一个**集合,否则"清干净了"可能只是"看不见了"。
    still="$(target_mountpoints | tr '\n' ' ')"
    [ -z "$still" ] || die "目标盘 $DEV 清理后仍被挂载:$still(先查是谁挂的,不要直接重试)"
    say "清理目标盘 $DEV(清理后)"
    lsblk -o NAME,SIZE,TYPE,MOUNTPOINT "$DEV" >"$TTY" 2>/dev/null || true
}

# is_transient_log <logfile> —— 日志里有没有**拉取侧**瞬时网络失败的特征。
#
# 只认这张表,表外一律不算瞬时:把 "Creating ostree deployment: ... Device ... is mounted"
# 这种装机逻辑失败当瞬时错误去重试,等于用重试掩盖真缺陷(而且白烧预算)。
is_transient_log() {
    local log="$1"
    [ -r "$log" ] || return 1
    grep -qE 'TLS handshake timeout|i/o timeout|net/http: request canceled|connection reset by peer|Client\.Timeout exceeded|Temporary failure in name resolution|no such host|502 Bad Gateway|503 Service Unavailable|429 Too Many Requests' "$log"
}

# classify_failure <rc> <logfile> —— 一次尝试的结局归成四态之一(抄到 stdout):
#   rc=0      → 空(成功不分类)
#   124|137   → timeout(外层时限到 / 被 SIGKILL;重试同源只会再烧一个时限)
#   瞬时表命中 → transient
#   其余      → fatal  ← **不兜底成 transient**:未识别的失败一律按装机逻辑失败处理
classify_failure() {
    local rc="$1" log="$2"
    [ "$rc" -eq 0 ] && return 0
    case "$rc" in
        124|137) printf 'timeout\n'; return 0 ;;
    esac
    if is_transient_log "$log"; then
        printf 'transient\n'
        return 0
    fi
    printf 'fatal\n'
}

# write_summary <attempt> <src> <rc> <class> —— 追加一行尝试摘要。
# 尽力而为:摘要只服务取证,写不进去不能反过来把装机判失败。
write_summary() {
    local cls="${4:-ok}"
    printf 'attempt=%s src=%s rc=%s class=%s at=%s\n' \
        "$1" "$2" "$3" "$cls" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        >>"$INSTALL_SUMMARY" 2>/dev/null || true
}

# 源列表:加速源在前、规范源兜底。加速源不通时回落,而不是整条腿直接红。
build_sources() {
    if [ -n "$HTTP_PROXY_URL" ]; then
        # 代理已是传输通道:规范 ref 既是安装源也是 target,少一次第三方中转。
        SOURCES=("$IMGREF")
        return
    fi
    SOURCES=()
    [ -n "$SRC_REF" ] && SOURCES+=("$SRC_REF")
    SOURCES+=("$IMGREF")
}

# install_all_sources —— 源循环。每源最多 1+RETRIES 次:
#   transient            → 退避后同源重试(backoff 每次 ×2,封顶 BACKOFF_MAX)
#   timeout / fatal / 用尽 → 换下一个源
# 每次尝试前清盘(残留挂载会让下一次以 "Device ... is mounted" 收场);
# 单次时限取 min(SRC_TIMEOUT, 剩余预算),预算在**每次尝试前**算,绝不允许越过。
# 置全局 installed(0/1)与 attempts(总尝试次数);预算耗尽与全部失败都 die,消息各不相同。
install_all_sources() {
    local src cls rc tries backoff per_limit remaining used starts fatal_seen=0 last_class=""
    build_sources
    installed=0
    attempts=0
    attempt=0
    starts=$SECONDS
    for src in "${SOURCES[@]}"; do
        SRC_IMGREF="$(with_transport "$src")"
        tries=0
        backoff="$BACKOFF"
        while :; do
            used=$(( SECONDS - starts ))
            remaining=$(( INSTALL_BUDGET - used ))
            if [ "$remaining" -le 0 ]; then
                die "预算耗尽(共 $attempts 次尝试,已用 ${used}s ≥ 预算 ${INSTALL_BUDGET}s;日志 $INSTALL_LOG.*)—— 拉取侧外因不可得(网络/加速源慢),不是装机逻辑失败"
            fi
            tries=$((tries + 1))
            attempt=$((attempt + 1))
            # 同一个计数两个名字:日志名沿用 attempt(静态门禁钉死了这个形状),
            # 对外的总尝试次数叫 attempts(摘要与消息都用它)。
            attempts=$attempt
            ATT_LOG="$INSTALL_LOG.$attempt"
            # 每次尝试都从**干净盘**开始。
            umount_target_disk
            per_limit="$SRC_TIMEOUT"
            # 单次时限落进 (0, 剩余预算]:配置写成 0 / 负增长等于关掉时限,那样一次尝试就能
            # 越过总预算,外层又会把"预算耗尽"报成 TIMEOUT —— 正好是本次要消灭的假因。
            if [ "$per_limit" -le 0 ] || [ "$per_limit" -gt "$remaining" ]; then
                per_limit="$remaining"
            fi
            say "目标盘 $DEV,源镜像 $SRC_IMGREF,开始无人值守安装(第 $attempt 次;单次时限 ${per_limit}s;剩余预算 ${remaining}s)"
            # --karg console=ttyS0:装好的系统要能在串口上判「装完了」,判据落在结果上而不是过程上。
            # --skip-fetch-check:fetch check 会以装好系统的凭据再拉一次目标 ref,那不是这条门禁要
            # 验的东西,失败反而会误杀(源与目标是同一个公开 ref 的不同写法时尤其如此)。
            # --kill-after:timeout 默认只发 SIGTERM。补 SIGKILL 是为了让**进程**一定死掉,不再
            # 继续拉镜像/写盘;它不负责清挂载点(挂载是内核状态,SIGKILL 后照样留着)——
            # 残留一律由上面的 umount_target_disk 处理。
            rc=0
            timeout --kill-after=30 "$per_limit" bootc install to-disk --wipe "$DEV" \
                    --source-imgref "$SRC_IMGREF" --target-imgref "$IMGREF" \
                    --skip-fetch-check --karg console=ttyS0,115200n8 >"$ATT_LOG" 2>&1 || rc=$?
            cls="$(classify_failure "$rc" "$ATT_LOG")"
            last_class="$cls"
            [ "$cls" = fatal ] && fatal_seen=1
            write_summary "$attempt" "$src" "$rc" "$cls"
            case "$rc" in
                0)
                    installed=1
                    say "源 $SRC_IMGREF 装机成功(第 $attempt 次)"
                    break
                    ;;
                124) say "源 $SRC_IMGREF 装机未成功(rc=124:该源 ${per_limit}s 内没装完,日志 $ATT_LOG),换下一个源" ;;
                137) say "源 $SRC_IMGREF 装机未成功(rc=137:SIGTERM 被忽略、已 SIGKILL,日志 $ATT_LOG),换下一个源" ;;
                *) say "源 $SRC_IMGREF 装机未成功(rc=$rc,分类 $cls,日志 $ATT_LOG)" ;;
            esac
            # 只有 transient(拉取侧瞬时)才值得在同源再试;timeout/fatal 换下一个源。
            if [ "$cls" = transient ] && [ "$tries" -le "$RETRIES" ]; then
                say "第 $attempt 次是瞬时网络失败,退避 ${backoff}s 后同源重试(本源第 $((tries + 1)) 次 / 上限 $((RETRIES + 1)) 次)"
                sleep "$backoff"
                backoff=$(( backoff * 2 ))
                [ "$backoff" -le "$BACKOFF_MAX" ] || backoff="$BACKOFF_MAX"
                continue
            fi
            break
        done
        [ "$installed" -eq 1 ] && break
    done
    if [ "$installed" -ne 1 ]; then
        if [ "$fatal_seen" -eq 1 ]; then
            die "所有安装源都失败(共 $attempts 次尝试,日志 $INSTALL_LOG.*)—— 其中至少一次是装机逻辑失败(fatal),这类失败重试不解决,先看日志里的真因"
        fi
        die "所有安装源都失败(共 $attempts 次尝试,日志 $INSTALL_LOG.*)—— 最后分类 ${last_class:-unknown}:拉取侧外因不可得(网络/加速源),不是装机逻辑失败"
    fi
}

main() {
    local arg _dmi LIVE_SRC LIVE_DISK
    for arg in $(cat /proc/cmdline); do
        case "$arg" in
            daedalus.install.disk=*) DEV="${arg#daedalus.install.disk=}" ;;
        esac
    done
    [ -n "$DEV" ] || die "内核参数未给出 daedalus.install.disk=<设备>,拒绝安装(本脚本不做任何选盘推测)"
    [ -b "$DEV" ] || die "$DEV 不是块设备"
    [ "$(lsblk -ndo TYPE "$DEV" 2>/dev/null)" = disk ] || die "$DEV 不是整盘(lsblk TYPE != disk),拒绝清空"

    # live 介质的 backing 设备绝不能被当作目标盘:那等于把安装介质自己擦掉。
    #
    # 比对按**拓扑**做,不按设备名字符串:live 介质是 /dev/vda1 而目标是 /dev/vda 时,
    # 前缀匹配放行,后一条 bootc --wipe 就把介质本身擦了。lsblk -s 给出的是从该设备
    # 一路到顶层磁盘的链,tail -1 才是顶层盘。
    LIVE_SRC="$(findmnt -no SOURCE /run/initramfs/live 2>/dev/null || true)"
    if [ -n "$LIVE_SRC" ]; then
        LIVE_DISK="$(lsblk -sno NAME -p "$LIVE_SRC" 2>/dev/null | tail -1)"
        if [ -n "$LIVE_DISK" ]; then
            [ "$LIVE_DISK" != "$DEV" ] || die "$DEV 是当前 live 介质($LIVE_SRC)所在的磁盘,拒绝安装"
        else
            # 解析不出拓扑:只按设备名形状兜底比对,并把事实留在串口上(不静默放行)。
            say "警告:解析不出 live 介质 $LIVE_SRC 的顶层磁盘,按设备名形状比对"
            case "$LIVE_SRC" in
                "$DEV"* ) die "$DEV 看起来是当前 live 介质的载体,拒绝安装" ;;
            esac
        fi
    else
        say "警告:findmnt 拿不到 /run/initramfs/live 的载体,无法排除介质自擦"
    fi

    IMGREF="$(sed -n 's/^DAEDALUS_INSTALL_IMGREF=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
    [ -n "$IMGREF" ] || die "$IMGREF_FILE 里没有 DAEDALUS_INSTALL_IMGREF,无法确定安装来源"

    # 源与目标允许不同:源可以指向加速镜像(内容按 manifest digest 钉死),目标必须始终是
    # 规范 ref —— 目标决定装好系统以后的更新通道,指向加速镜像等于把更新权交给第三方。
    SRC_REF="$(sed -n 's/^DAEDALUS_INSTALL_SOURCE_IMGREF=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
    # 时限与预算是两条独立的闸:单个源有时限(加速源不通要及时回落),全程有总预算
    # (再多源 × 再多重试也不能把外层的等待上限吃光)。
    SRC_TIMEOUT="$(conf_num DAEDALUS_INSTALL_SOURCE_TIMEOUT "$SRC_TIMEOUT")"
    RETRIES="$(conf_num DAEDALUS_INSTALL_RETRIES "$RETRIES")"
    INSTALL_BUDGET="$(conf_num DAEDALUS_INSTALL_BUDGET "$INSTALL_BUDGET")"
    BACKOFF="$(conf_num DAEDALUS_INSTALL_BACKOFF "$BACKOFF")"

    # 装机拉取通道(可选):配了代理就只走规范 ref —— 代理本身即传输通道,加速镜像那条
    # 路可以退休。两侧网络视角不同:runner 上的代理监听 127.0.0.1;guest 里 127.0.0.1 是
    # 它自己,QEMU user-net 把宿主暴露成 10.0.2.2,所以 guest 侧要写 http://10.0.2.2:<port>。
    # 代理地址**不得带凭据**:它会进串口日志与 artifact。
    HTTP_PROXY_URL="$(sed -n 's/^DAEDALUS_INSTALL_HTTP_PROXY=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
    if [ -z "$HTTP_PROXY_URL" ] && [ -r /sys/class/dmi/id/product_serial ]; then
        # 常规通道:地址由 QEMU SMBIOS(type=1 serial)在本次引导里递进来。
        # 它**不在 ISO 里** —— payload 会被烘成 ISO 并晋级成 Release 资产,地址不能留在那。
        _dmi="$(tr -d '\r\n' < /sys/class/dmi/id/product_serial 2>/dev/null || true)"
        case "$_dmi" in
            http://*|https://*) HTTP_PROXY_URL="$_dmi" ;;
        esac
    fi
    if [ -n "$HTTP_PROXY_URL" ]; then
        case "$HTTP_PROXY_URL" in
            *@*) die "代理地址不得携带凭据(它会进串口日志与 artifact)" ;;
        esac
        export HTTPS_PROXY="$HTTP_PROXY_URL"
        export HTTP_PROXY="$HTTP_PROXY_URL"
        export NO_PROXY="localhost,127.0.0.1"
        # 地址不打印:它会进串口日志、artifact,以及(若哪天烘回 payload)Release 资产。
        say "拉取走代理(地址不打印;加速镜像本次不用)"
    fi

    install_all_sources

    # 独立于串口的完成标记(留在 live 根里;装机成功的主判据仍是「装好的盘能引导到 login:」)
    : >/etc/daedalus-iso-installed
    say "安装完成,重启进装好的系统"
    sync
    systemctl reboot --force
}

# 库模式:只定义常量与函数,不执行主流程(行为门禁 tests/ci/iso-install-retry-test.sh 靠它)。
if [ "${DAEDALUS_ISO_AUTOINSTALL_LIB:-0}" = 1 ]; then return 0; fi
main "$@"
