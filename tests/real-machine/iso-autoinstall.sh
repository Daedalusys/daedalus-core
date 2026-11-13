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
die() { say "失败:$*"; [ -s "$INSTALL_LOG" ] && cat "$INSTALL_LOG" >"$TTY" 2>/dev/null; exit 1; }

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
LIVE_SRC="$(findmnt -no SOURCE /run/initramfs/live 2>/dev/null || true)"
case "$LIVE_SRC" in
    "" ) : ;;
    "$DEV"* ) die "$DEV 是当前 live 介质的载体,拒绝安装" ;;
esac

IMGREF="$(sed -n 's/^DAEDALUS_INSTALL_IMGREF=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
[ -n "$IMGREF" ] || die "$IMGREF_FILE 里没有 DAEDALUS_INSTALL_IMGREF,无法确定安装来源"

# 源与目标允许不同:源可以指向加速镜像(内容按 manifest digest 钉死),目标必须始终是
# 规范 ref —— 目标决定装好系统以后的更新通道,指向加速镜像等于把更新权交给第三方。
SRC_REF="$(sed -n 's/^DAEDALUS_INSTALL_SOURCE_IMGREF=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
# 单个源的装机时限:加速源不可用时要在有限时间内放弃并回落到规范源,而不是把整条
# 门禁的时间预算耗光(外网慢的时候一个源能拖满几小时)。
SRC_TIMEOUT="$(sed -n 's/^DAEDALUS_INSTALL_SOURCE_TIMEOUT=//p' "$IMGREF_FILE" 2>/dev/null | head -1)"
case "$SRC_TIMEOUT" in ''|*[!0-9]*) SRC_TIMEOUT=900 ;; esac

# bootc 的 --source-imgref 要**显式 transport 前缀**(实测裸 ref 会被判 Invalid transport),
# 配置里存的是人读得懂的裸 ref,这里补前缀;已经带前缀的原样透传。
with_transport() {
    case "$1" in
        registry:*|docker://*|containers-storage:*|oci:*|oci-archive:*|dir:*) printf '%s' "$1" ;;
        *) printf 'registry:%s' "$1" ;;
    esac
}

# umount_target_disk —— 把 $DEV 上残留的挂载点卸干净,并把清理前后的盘状态打进串口。
#
# 为什么每次装机前都要清:上一次尝试(尤其是被 timeout 打断的)会在目标盘上留下
# 分区与挂载点,下一次 bootc 就会以 "Creating rootfs: Device $DEV is mounted" 收场
# —— 那句错不含挂载点,排查时等于没有信息,还会把真正的首因(第一次为什么失败)盖掉。
#
# 只动 $DEV 自己:live 介质的载体在前面已被明确排除(那是「擦掉自己」的红线)。
# 这里不做 wipefs:本故障是**挂载态**残留,不是旧文件系统签名;`bootc install
# --wipe` 自己会重写分区表,多一次 wipefs 只增加破坏面。
umount_target_disk() {
    say "清理目标盘 $DEV(清理前)"
    lsblk -o NAME,SIZE,TYPE,MOUNTPOINT "$DEV" >"$TTY" 2>/dev/null || true
    local mp still
    while read -r mp; do
        [ -n "$mp" ] || continue
        umount -R "$mp" 2>/dev/null || umount -l "$mp" 2>/dev/null || true
    done < <({ findmnt -rno TARGET -S "$DEV" 2>/dev/null; lsblk -rno MOUNTPOINT "$DEV" 2>/dev/null; } \
             | sed '/^$/d' | sort -u)
    # 清理后还在挂载就当场报出挂载点:bootc 那句 "is mounted" 不含这些信息。
    still="$(lsblk -rno MOUNTPOINT "$DEV" 2>/dev/null | sed '/^$/d' | sort -u | tr '\n' ' ')"
    [ -z "$still" ] || die "目标盘 $DEV 清理后仍被挂载:$still(先查是谁挂的,不要直接重试)"
    say "清理目标盘 $DEV(清理后)"
    lsblk -o NAME,SIZE,TYPE,MOUNTPOINT "$DEV" >"$TTY" 2>/dev/null || true
}

# 源列表:加速源在前、规范源兜底。加速源不通时回落,而不是整条腿直接红。
SOURCES=()
[ -n "$SRC_REF" ] && SOURCES+=("$SRC_REF")
SOURCES+=("$IMGREF")

installed=0
for src in "${SOURCES[@]}"; do
    SRC_IMGREF="$(with_transport "$src")"
    # 每次尝试都从**干净盘**开始。
    umount_target_disk
    say "目标盘 $DEV,源镜像 $SRC_IMGREF,开始无人值守安装"
    # --karg console=ttyS0:装好的系统要能在串口上判「装完了」,判据落在结果上而不是过程上。
    # --skip-fetch-check:fetch check 会以装好系统的凭据再拉一次目标 ref,那不是这条门禁要
    # 验的东西,失败反而会误杀(源与目标是同一个公开 ref 的不同写法时尤其如此)。
    # --kill-after:timeout 默认只发 SIGTERM;bootc 若不理会就会带着半成品挂载点活到
    # 下一次尝试,那次就会以 "Device $DEV is mounted" 收场。补一次 SIGKILL 兜底。
    rc=0
    timeout --kill-after=30 "$SRC_TIMEOUT" bootc install to-disk --wipe "$DEV" \
            --source-imgref "$SRC_IMGREF" --target-imgref "$IMGREF" \
            --skip-fetch-check --karg console=ttyS0,115200n8 >"$INSTALL_LOG" 2>&1 || rc=$?
    if [ "$rc" -eq 0 ]; then
        installed=1
        break
    fi
    if [ "$rc" -eq 124 ]; then
        say "源 $SRC_IMGREF 装机未成功(rc=124:该源 ${SRC_TIMEOUT}s 内没装完),换下一个源"
    else
        say "源 $SRC_IMGREF 装机未成功(rc=$rc,日志在 $INSTALL_LOG),换下一个源"
    fi
done
[ "$installed" -eq 1 ] || die "所有安装源都失败(最后一次日志在 $INSTALL_LOG)"

# 独立于串口的完成标记(留在 live 根里;装机成功的主判据仍是「装好的盘能引导到 login:」)
: >/etc/daedalus-iso-installed
say "安装完成,重启进装好的系统"
sync
systemctl reboot --force
