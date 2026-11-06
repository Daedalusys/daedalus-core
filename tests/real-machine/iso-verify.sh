#!/usr/bin/env bash
# =============================================================================
# iso-verify.sh —— 产出 ISO 并验证它真能引导到安装器
# =============================================================================
# 解决什么问题
#   `just iso`(legacy `bootc-image-builder --type iso`)**构建不出任何 ISO**:
#   osbuild 的每个包下载都失败。上游已把 `iso`/`anaconda-iso` 标为 legacy 且
#   不再被新的 osbuild-based builder 支持(见 core#84 的 6 组对照实验)。
#   本脚本走**官方推荐的新工具链** `image-builder-cli` + `generic-iso`,并补齐
#   ISO 载荷,把「能产出可引导 ISO」变成可重跑、有产物、有退出码的验证。
#
# 为什么需要一个派生 payload 镜像(而不是改发布镜像)
#   ISO 载荷需要的几个东西在**发布镜像里没有**,直接改发布镜像会:
#     - 让所有下游(容器/qcow2/registry 拉取)都多带只对 ISO 有用的东西;
#     - 与 bootc 自己的 /boot 处理逻辑打架(bootc 镜像构建期把 /boot 剥空,
#       ESP 是安装时才创建的 —— 实测 `rpm -V shim-x64` 全部 missing);
#     - 连累已跑通的 qcow2 实机门禁重跑。
#   故只在**一次性派生层**里补齐,发布产物一格不动。
#
# 用法(在 x86-64-v3 + KVM 宿主上,需 sudo:rootful podman)
#   sudo ./iso-verify.sh                    # 全流程:派生 → 出 ISO → KVM 引导
#   ./iso-verify.sh --only-preflight        # 只体检
#   sudo VERIFY_IMAGE=ghcr.io/... ./iso-verify.sh
#
# 环境变量
#   VERIFY_WORK     工作目录(默认:$SUDO_USER 家目录下 daedalus-iso-verify)
#   VERIFY_IMAGE    基准镜像(默认 ghcr.io/daedalusys/daedalus-os:latest)
#   VERIFY_MEM      引导内存 MiB(默认 4096)
#   VERIFY_BOOT_TIMEOUT  等 GRUB 菜单的秒数(默认 300)
#
# 退出码
#   0  ISO 产出且成功引导到安装器菜单
#   1  验证失败(构建失败 / 引导不起来 / panic)
#   2  前置不满足
#
# 已知边界(照实写,别当成通过)
#   generic-iso 的 "Install" 流程**不是无人值守**:实测挂空盘引导 20 分钟,
#   目标盘零写入、无分区表,随后该盘 "No bootable device"。要验证「装完 → 重启
#   → login」需要给 ISO 配 kickstart/blueprint,属产品级配置,不在本脚本范围。
#   本脚本只判到「ISO 是可引导的安装器」——这一条此前从未有人验过。
# =============================================================================
set -uo pipefail

IMAGE="${VERIFY_IMAGE:-ghcr.io/daedalusys/daedalus-os:latest}"
MEM="${VERIFY_MEM:-4096}"
BOOT_TIMEOUT="${VERIFY_BOOT_TIMEOUT:-300}"
BUILDER_IMAGE="${VERIFY_BUILDER_IMAGE:-ghcr.io/osbuild/image-builder-cli:latest}"
ONLY_PREFLIGHT=0

while [ $# -gt 0 ]; do
    case "$1" in
        --only-preflight) ONLY_PREFLIGHT=1; shift ;;
        -h|--help) sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

_inv="${SUDO_USER:-$USER}"
_inv_home="$(getent passwd "$_inv" 2>/dev/null | cut -d: -f6)"; [ -n "${_inv_home:-}" ] || _inv_home="$HOME"
WORK="${VERIFY_WORK:-$_inv_home/daedalus-iso-verify}"
mkdir -p "$WORK" || { echo "无法创建工作目录 $WORK" >&2; exit 2; }
LOG="$WORK/iso-verify.log"; : > "$LOG"

log()  { printf '%s\n' "$*" >>"$LOG"; }
say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; log ""; log "== $* =="; }
info() { printf '   %s\n' "$*"; log "   $*"; }
ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; log "   OK $*"; }
warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; log "   !! $*"; }
bad()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; log "   XX $*"; }
die()  { bad "$*"; exit 1; }

QEMU_PID=""
cleanup() {
    [ -n "${QEMU_PID:-}" ] && { kill "$QEMU_PID" 2>/dev/null; sleep 1; kill -9 "$QEMU_PID" 2>/dev/null; }
    chown -R "$_owner" "$WORK" 2>/dev/null || true
}
trap cleanup EXIT
_owner="$_inv:$(id -gn "$_inv" 2>/dev/null || echo "$_inv")"
chown -R "$_owner" "$WORK" 2>/dev/null || true

VM="$WORK/payload-image"; OUT="$WORK/output"; SERIAL="$WORK/serial-console.log"

# ------------------------------------------------------------------ preflight --
say "Phase 0 · preflight"
[ "$(uname -m)" = x86_64 ] || { bad "只覆盖 x86_64"; exit 2; }
miss=""; for f in avx2 bmi1 bmi2 fma movbe xsave; do grep -qw "$f" /proc/cpuinfo || miss="$miss $f"; done
[ -z "$miss" ] || { bad "CPU 非 x86-64-v3,缺:$miss"; exit 2; }
ok "CPU 支持 x86-64-v3"
[ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] || { bad "/dev/kvm 不可读写"; exit 2; }
ok "/dev/kvm 可读写"
for b in qemu-system-x86_64 podman qemu-img; do
    command -v "$b" >/dev/null 2>&1 || { bad "缺工具 $b"; exit 2; }
done
ok "qemu / podman / qemu-img 就位"
info "工作目录: $WORK"
if [ "$ONLY_PREFLIGHT" = 1 ]; then say "只体检模式,停止"; exit 0; fi
[ "$(id -u)" -eq 0 ] || die "需要 root(rootful podman):请 sudo"

# ------------------------------------------------- Phase 1 · 派生 payload 镜像 --
# 全部依赖都在这一步补齐;发布镜像不动。每一项都是实测逼出来的:
#   - releasever      : 否则镜像里 dnf 装不了包(见 PR #83 / core#84)
#   - cdboot + 工具链 : mksquashfs / xorriso / implantisomd5 / isohybrid 缺一不可
#   - rpm2cpio 还原   : bootc 镜像构建期剥空 /boot,ESP 文件不在盘上,必须从 RPM 还原
#   - shim 改名       : AlmaLinux 提供 shimx64-almalinux.efi,builder 找 shimx64.efi
say "Phase 1 · 派生一次性 ISO payload 镜像"
mkdir -p "$VM"
cat > "$VM/Containerfile" <<EOF
FROM ${IMAGE}
RUN set -eux; \\
    echo 10 > /etc/dnf/vars/releasever; \\
    dnf install -y cpio squashfs-tools xorriso isomd5sum syslinux grub2-efi-x64-cdboot; \\
    dnf -y reinstall --downloadonly --downloaddir=/tmp/rpms shim-x64 grub2-efi-x64 grub2-efi-x64-cdboot; \\
    cd /; for r in /tmp/rpms/*.rpm; do rpm2cpio "\$r" | cpio -idmu; done; rm -rf /tmp/rpms; \\
    mkdir -p /boot/efi/EFI/almalinux; \\
    cp -f /boot/efi/EFI/BOOT/* /boot/efi/EFI/almalinux/ 2>/dev/null || true; \\
    if [ ! -f /boot/efi/EFI/almalinux/shimx64.efi ] && [ -f /boot/efi/EFI/almalinux/shimx64-almalinux.efi ]; then \\
        cp -f /boot/efi/EFI/almalinux/shimx64-almalinux.efi /boot/efi/EFI/almalinux/shimx64.efi; \\
    fi; \\
    test -f /boot/efi/EFI/almalinux/shimx64.efi; ls /boot/efi/EFI/almalinux/
EOF
podman image exists "$IMAGE" || podman pull "$IMAGE" >>"$LOG" 2>&1 || die "拉取 $IMAGE 失败"
podman build --security-opt label=disable -t localhost/daedalus-iso-payload -f "$VM/Containerfile" "$VM" >>"$LOG" 2>&1 \
    || { bad "派生 payload 镜像失败,见 $LOG"; exit 1; }
ok "payload 镜像就绪: localhost/daedalus-iso-payload"

# ------------------------------------------------------- Phase 2 · 出 ISO ------
say "Phase 2 · 用新版 image-builder CLI 出 generic-iso"
mkdir -p "$OUT"; rm -rf "$OUT"/*
podman run --rm --privileged --security-opt label=disable \
    -v "$OUT:/output" -v /var/lib/containers/storage:/var/lib/containers/storage \
    "$BUILDER_IMAGE" \
    build --bootc-ref localhost/daedalus-iso-payload --output-dir /output generic-iso >>"$LOG" 2>&1 \
    || { bad "generic-iso 构建失败,见 $LOG"; exit 1; }
ISO="$(find "$OUT" -name '*.iso' | head -1)"
[ -n "$ISO" ] || { bad "builder 报成功但没有 .iso"; exit 1; }
ok "ISO: $ISO($(du -h "$ISO" | cut -f1))"

# -------------------------------------------- Phase 3 · KVM 引导到安装器菜单 --
say "Phase 3 · KVM 引导 ISO,等 GRUB/安装器菜单"
: > "$SERIAL"
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m "$MEM" -smp 2 \
    -nographic -no-reboot -boot d -cdrom "$ISO" \
    </dev/null >"$SERIAL" 2>&1 &
QEMU_PID=$!
info "qemu pid=$QEMU_PID,串口 → $SERIAL"

verdict=""; elapsed=0
while [ "$elapsed" -lt "$BOOT_TIMEOUT" ]; do
    grep -qiE "Kernel panic" "$SERIAL" 2>/dev/null && { verdict="PANIC"; break; }
    # GRUB 从 ISO 起来了 = ISO 是可引导的(菜单项文案随发行版变,只认 GRUB 头)
    grep -qE "GRUB version|Install .*Kitten|Install AlmaLinux" "$SERIAL" 2>/dev/null && { verdict="INSTALLER"; break; }
    kill -0 "$QEMU_PID" 2>/dev/null || { verdict="QEMU_EXITED"; break; }
    sleep 2; elapsed=$((elapsed+2))
done
[ -n "$verdict" ] || verdict="TIMEOUT"
kill "$QEMU_PID" 2>/dev/null; sleep 1; kill -9 "$QEMU_PID" 2>/dev/null; QEMU_PID=""

case "$verdict" in
    INSTALLER)
        ok "ISO 可引导:串口出现安装器菜单"
        grep -aE "Install |GRUB version" "$SERIAL" | head -4 | sed 's/^/         /'
        ;;
    PANIC)   bad "串口出现 kernel panic —— ISO 不可引导"; grep -aiE "panic" "$SERIAL" | head -5 | sed 's/^/         /' ;;
    QEMU_EXITED) bad "qemu 提前退出(未出现菜单)"; tail -15 "$SERIAL" | sed 's/^/         /' ;;
    *)       bad "等待 ${BOOT_TIMEOUT}s 未见安装器菜单"; tail -15 "$SERIAL" | sed 's/^/         /' ;;
esac

say "Summary"
cat <<EOF
   判定     : $verdict
   ISO      : $ISO
   ISO 大小 : $(du -h "$ISO" 2>/dev/null | cut -f1)
   串口日志 : $SERIAL
   完整日志 : $LOG
   工作目录 : $WORK

   已知边界:generic-iso 的安装流程**不是无人值守**(实测挂空盘 20 分钟零写入),
   本脚本只判到「可引导的安装器」。完整「安装 → 重启 → login」需 kickstart。
EOF
[ "$verdict" = INSTALLER ] && exit 0 || exit 1
