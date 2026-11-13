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
# /etc/daedalus/iso-install.conf 的键
#   DAEDALUS_INSTALL_IMGREF           规范 ref:既是兜底安装源,也是装好系统的 target
#                                    (target 决定后续更新通道,不能指向加速镜像)
#   DAEDALUS_INSTALL_SOURCE_IMGREF    可选:优先安装源(加速镜像),按 manifest digest 钉死
#   DAEDALUS_INSTALL_SOURCE_TIMEOUT   可选:单个源的装机时限(默认 900 秒)
set -uo pipefail

TTY=/dev/ttyS0
IMGREF_FILE=/etc/daedalus/iso-install.conf
INSTALL_LOG=/run/daedalus-iso-autoinstall.log

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

DEV=""
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
# 单个源的装机时限:加速源不可用时要在有限时间内放弃并回落到规范源,而不是把整条
# 门禁的时间预算耗光(外网慢的时候一个源能拖满几小时)。
SRC_TIMEOUT="$(sed -n 's/^DAEDALUS_INSTALL_SOURCE_TIMEOUT=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
case "$SRC_TIMEOUT" in ''|*[!0-9]*) SRC_TIMEOUT=900 ;; esac

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
build_sources

installed=0
attempt=0
for src in "${SOURCES[@]}"; do
    SRC_IMGREF="$(with_transport "$src")"
    attempt=$((attempt + 1))
    ATT_LOG="$INSTALL_LOG.$attempt"
    # 每次尝试都从**干净盘**开始。
    umount_target_disk
    say "目标盘 $DEV,源镜像 $SRC_IMGREF,开始无人值守安装(第 $attempt 次)"
    # --karg console=ttyS0:装好的系统要能在串口上判「装完了」,判据落在结果上而不是过程上。
    # --skip-fetch-check:fetch check 会以装好系统的凭据再拉一次目标 ref,那不是这条门禁要
    # 验的东西,失败反而会误杀(源与目标是同一个公开 ref 的不同写法时尤其如此)。
    # --kill-after:timeout 默认只发 SIGTERM。补 SIGKILL 是为了让**进程**一定死掉,不再
    # 继续拉镜像/写盘;它不负责清挂载点(挂载是内核状态,SIGKILL 后照样留着)——
    # 残留一律由上面的 umount_target_disk 处理。
    rc=0
    timeout --kill-after=30 "$SRC_TIMEOUT" bootc install to-disk --wipe "$DEV" \
            --source-imgref "$SRC_IMGREF" --target-imgref "$IMGREF" \
            --skip-fetch-check --karg console=ttyS0,115200n8 >"$ATT_LOG" 2>&1 || rc=$?
    case "$rc" in
        0) installed=1; break ;;
        124) say "源 $SRC_IMGREF 装机未成功(rc=124:该源 ${SRC_TIMEOUT}s 内没装完,日志 $ATT_LOG),换下一个源" ;;
        137) say "源 $SRC_IMGREF 装机未成功(rc=137:SIGTERM 被忽略、已 SIGKILL,日志 $ATT_LOG),换下一个源" ;;
        *) say "源 $SRC_IMGREF 装机未成功(rc=$rc,日志 $ATT_LOG),换下一个源" ;;
    esac
done
[ "$installed" -eq 1 ] || die "所有安装源都失败(共 $attempt 次尝试,日志 $INSTALL_LOG.*)"

# 独立于串口的完成标记(留在 live 根里;装机成功的主判据仍是「装好的盘能引导到 login:」)
: >/etc/daedalus-iso-installed
say "安装完成,重启进装好的系统"
sync
systemctl reboot --force
