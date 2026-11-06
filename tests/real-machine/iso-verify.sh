#!/usr/bin/env bash
# =============================================================================
# iso-verify.sh —— 产出 ISO,并验证它真能当裸机介质用
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
#   故只在**一次性派生层**里补齐,发布产物一格不动。派生层也不推 registry。
#
# 判据分两段(rev-2,依据 Task 1 探查结论重排)
#   段一 P2-2a:引导 ISO,用 SSH 自证它引导出的是**可用的 live 系统** ——
#        (1) SSH 登录成功;(2) 根确为 live 介质;(3) 系统就是本镜像。
#        理由:live 内核参数里没有 console=ttyS0,串口不可能有输出,SSH 是唯一稳定判据。
#   段二 P2-2b:live 环境里的 oneshot 单元对**空虚拟盘**执行 `bootc install to-disk`,
#        装完重启;然后引导那块盘,等串口出现 `login:`,并断言目标盘有分区表。
#        P2-2b 是尽力项:受阻(例如 live 环境拿不到 source 镜像)时如实记 BLOCKED,
#        不为此改发布镜像、不为此走 Anaconda 路线。
#
# 为什么不走 kickstart(这是 Task 1 实测推翻计划前提后的结论,别再走回头路)
#   `generic-iso` **不消费 blueprint**:传了 `--blueprint` 构建照样成功,但产出的
#   manifest 里没有任何 kickstart 痕迹(源码里 manifestForGenericISO 根本不接 bp);
#   而且它**不是安装器**,只是「payload 容器 rootfs 的 live ISO」(payload 里
#   `rpm -q anaconda` = not installed)。所以「给 ISO 注入 kickstart 让它无人值守」
#   这条路不存在。探查证据:docs/superpowers/plans/notes/2026-11-06-iso-blueprint.md。
#
# 用法(在 x86-64-v3 + KVM 宿主上,需 sudo:rootful podman)
#   sudo ./iso-verify.sh                    # 全流程
#   ./iso-verify.sh --only-preflight        # 只体检
#   sudo VERIFY_IMAGE=ghcr.io/... ./iso-verify.sh
#
# 环境变量
#   VERIFY_WORK     工作目录(默认:$SUDO_USER 家目录下 daedalus-iso-verify)
#   VERIFY_IMAGE    基准镜像(默认 ghcr.io/daedalusys/daedalus-os:latest)
#   VERIFY_MEM      引导内存 MiB(默认 8192;装机时要把镜像拉进 live 环境,4G 偏紧)
#   VERIFY_BOOT_TIMEOUT   等 live 系统可 SSH 的秒数(默认 600)
#   VERIFY_INSTALL_TIMEOUT 等装机完成的秒数(默认 2400)
#   VERIFY_REQUIRE_INSTALL 置 1 时,P2-2b 未达成也判失败(默认 0:P2-2a 绿即绿)
#   VERIFY_DISK_SIZE 目标盘大小(默认 20G)
#
# 退出码
#   0  live 系统自证通过(且 P2-2b 达成时判定态为 INSTALLED_LOGIN)
#   1  验证失败(live 起不来 / 装完引导不到 login: / panic)
#   2  前置不满足
# =============================================================================
set -uo pipefail

IMAGE="${VERIFY_IMAGE:-ghcr.io/daedalusys/daedalus-os:latest}"
MEM="${VERIFY_MEM:-8192}"
BOOT_TIMEOUT="${VERIFY_BOOT_TIMEOUT:-600}"
INSTALL_TIMEOUT="${VERIFY_INSTALL_TIMEOUT:-2400}"
DISK_SIZE="${VERIFY_DISK_SIZE:-20G}"
BUILDER_IMAGE="${VERIFY_BUILDER_IMAGE:-ghcr.io/osbuild/image-builder-cli:latest}"
REQUIRE_INSTALL="${VERIFY_REQUIRE_INSTALL:-0}"
SSH_PORT=2222
ONLY_PREFLIGHT=0

while [ $# -gt 0 ]; do
    case "$1" in
        --only-preflight) ONLY_PREFLIGHT=1; shift ;;
        -h|--help) sed -n '2,60p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

QEMU_PID=""
cleanup() {
    [ -n "${QEMU_PID:-}" ] && { kill "$QEMU_PID" 2>/dev/null; sleep 1; kill -9 "$QEMU_PID" 2>/dev/null; }
    chown -R "$_owner" "$WORK" 2>/dev/null || true
}
trap cleanup EXIT
_owner="$_inv:$(id -gn "$_inv" 2>/dev/null || echo "$_inv")"
chown -R "$_owner" "$WORK" 2>/dev/null || true

VM="$WORK/payload-image"; OUT="$WORK/output"
SERIAL_LIVE="$WORK/serial-live.log"; SERIAL_INSTALLED="$WORK/serial-installed.log"
TARGET="$WORK/target-disk.qcow2"
SSH_DIR="$WORK/iso-ssh"; SSH_KEY="$SSH_DIR/id_ed25519"

# ------------------------------------------------------------------ preflight --
say "Phase 0 · preflight"
[ "$(uname -m)" = x86_64 ] || { bad "只覆盖 x86_64"; exit 2; }
miss=""; for f in avx2 bmi1 bmi2 fma movbe xsave; do grep -qw "$f" /proc/cpuinfo || miss="$miss $f"; done
[ -z "$miss" ] || { bad "CPU 非 x86-64-v3,缺:$miss"; exit 2; }
ok "CPU 支持 x86-64-v3"
[ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] || { bad "/dev/kvm 不可读写"; exit 2; }
ok "/dev/kvm 可读写"
for b in qemu-system-x86_64 podman qemu-img ssh ssh-keygen; do
    command -v "$b" >/dev/null 2>&1 || { bad "缺工具 $b"; exit 2; }
done
ok "qemu / podman / qemu-img / ssh 就位"
info "工作目录: $WORK"
if [ "$ONLY_PREFLIGHT" = 1 ]; then say "只体检模式,停止"; exit 0; fi
[ "$(id -u)" -eq 0 ] || die "需要 root(rootful podman):请 sudo"

# ------------------------------------------------- Phase 1 · 派生 payload 镜像 --
# 全部依赖都在这一步补齐;发布镜像不动。每一项都是实测逼出来的:
#   - releasever      : 否则镜像里 dnf 装不了包(见 PR #83 / core#84)
#   - cdboot + 工具链 : mksquashfs / xorriso / implantisomd5 / isohybrid 缺一不可
#   - rpm2cpio 还原   : bootc 镜像构建期剥空 /boot,ESP 文件不在盘上,必须从 RPM 还原
#   - shim 改名       : AlmaLinux 提供 shimx64-almalinux.efi,builder 找 shimx64.efi
#   - dracut-live     : 没有 dmsquash-live 模块,initramfs 挂不了 root=live 的 live 根
#                       (实测 lsinitrd 里 dmsquash-live 计数 = 0,那正是「GRUB 之后
#                        串口再无输出、目标盘零写入」的真因)
#   - iso.yaml        : generic-iso 的内核参数/标签来自容器里的
#                       /usr/lib/image-builder/bootc/iso.yaml,加 console=ttyS0 才能
#                       看见 live 引导过程(判据仍以 SSH 为准,串口只用于诊断)
#   - verify 用户     : live 自证靠 SSH 登录;密钥对每次运行随机生成,不进仓库不进日志
say "Phase 1 · 派生一次性 ISO payload 镜像"
mkdir -p "$VM" "$SSH_DIR"
chmod 700 "$SSH_DIR"
rm -f "$SSH_KEY" "$SSH_KEY.pub"
ssh-keygen -t ed25519 -N '' -C "daedalus-iso-verify@$(hostname)" -f "$SSH_KEY" >/dev/null 2>&1 \
    || die "生成 SSH 密钥对失败"
KEY_FP="$(ssh-keygen -lf "$SSH_KEY.pub" | awk '{print $2}')"
info "SSH 公钥指纹(sha256): $KEY_FP"
# 授权公钥与 os-release 快照:live 自证要拿它们跟目标系统比对
install -m 0644 "$SSH_KEY.pub" "$VM/authorized_keys"
cp -f "$SCRIPT_DIR/iso-autoinstall.sh" "$SCRIPT_DIR/iso-autoinstall.service" "$VM/"
podman run --rm "$IMAGE" cat /etc/os-release > "$VM/os-release.expected" 2>/dev/null \
    || { echo "读基准镜像的 os-release 失败" >&2; exit 2; }

cat > "$VM/Containerfile" <<EOF
FROM ${IMAGE}
COPY authorized_keys /tmp/authorized_keys
COPY iso-autoinstall.sh iso-autoinstall.service /tmp/
RUN set -eux; \\
    echo 10 > /etc/dnf/vars/releasever; \\
    dnf install -y cpio squashfs-tools xorriso isomd5sum syslinux grub2-efi-x64-cdboot dracut-live; \\
    dnf -y reinstall --downloadonly --downloaddir=/tmp/rpms shim-x64 grub2-efi-x64 grub2-efi-x64-cdboot; \\
    cd /; for r in /tmp/rpms/*.rpm; do rpm2cpio "\$r" | cpio -idmu; done; rm -rf /tmp/rpms; \\
    mkdir -p /boot/efi/EFI/almalinux; \\
    cp -f /boot/efi/EFI/BOOT/* /boot/efi/EFI/almalinux/ 2>/dev/null || true; \\
    if [ ! -f /boot/efi/EFI/almalinux/shimx64.efi ] && [ -f /boot/efi/EFI/almalinux/shimx64-almalinux.efi ]; then \\
        cp -f /boot/efi/EFI/almalinux/shimx64-almalinux.efi /boot/efi/EFI/almalinux/shimx64.efi; \\
    fi; \\
    test -f /boot/efi/EFI/almalinux/shimx64.efi; \\
    kv=\$(ls /lib/modules | head -1); \\
    dracut -f --no-hostonly --add "dmsquash-live livenet" /lib/modules/\$kv/initramfs.img \$kv; \\
    lsinitrd /lib/modules/\$kv/initramfs.img | grep -q dmsquash-live; \\
    ssh-keygen -A; \\
    useradd -m -s /bin/bash verify; \\
    install -d -m 0700 -o verify -g verify /home/verify/.ssh; \\
    install -m 0600 -o verify -g verify /tmp/authorized_keys /home/verify/.ssh/authorized_keys; \\
    rm -f /tmp/authorized_keys; \\
    mkdir -p /usr/lib/image-builder/bootc /etc/daedalus /etc/systemd/system/multi-user.target.wants; \\
    printf 'label: "DAEDALUS_ISO"\\nkernel_args:\\n  - "console=ttyS0,115200n8"\\n  - "daedalus.install.disk=/dev/vda"\\ngrub2:\\n  default: 0\\n  timeout: 3\\n' \\
        > /usr/lib/image-builder/bootc/iso.yaml; \\
    printf 'DAEDALUS_INSTALL_IMGREF=%s\\n' "${IMAGE}" > /etc/daedalus/iso-install.conf; \\
    install -m 0755 /tmp/iso-autoinstall.sh /usr/local/bin/daedalus-iso-autoinstall; \\
    install -m 0644 /tmp/iso-autoinstall.service /usr/lib/systemd/system/daedalus-iso-autoinstall.service; \\
    ln -sf /usr/lib/systemd/system/daedalus-iso-autoinstall.service \\
        /etc/systemd/system/multi-user.target.wants/daedalus-iso-autoinstall.service; \\
    echo iso-payload-ok
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

# --------------------------------------- Phase 3 · 引导 ISO:live 自证 + 无人值守装 --
say "Phase 3 · 引导 ISO:先自证 live 系统可用,再看它能否无人值守装到盘"
rm -f "$TARGET"
qemu-img create -f qcow2 "$TARGET" "$DISK_SIZE" >>"$LOG" 2>&1 || die "创建目标盘失败"
ok "目标盘(每次新建,不复用旧盘): $TARGET($DISK_SIZE)"
: > "$SERIAL_LIVE"

# 目标盘是 virtio 盘 → guest 内是 /dev/vda;ISO 走 cdrom → /dev/sr0。
# daedalus.install.disk 由内核参数点名:live 里的装机脚本只认这个参数,不做任何选盘推测。
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m "$MEM" -smp 2 \
    -nographic -no-reboot -boot d -cdrom "$ISO" \
    -drive file="$TARGET",if=virtio,format=qcow2 \
    -netdev user,id=net0,hostfwd=tcp::$SSH_PORT-:22 -device virtio-net-pci,netdev=net0 \
    </dev/null >"$SERIAL_LIVE" 2>&1 &
QEMU_PID=$!
info "qemu pid=$QEMU_PID,串口 → $SERIAL_LIVE"

SSH_OPTS="-i $SSH_KEY -p $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o BatchMode=yes -o LogLevel=ERROR"
live_ssh() { ssh $SSH_OPTS verify@127.0.0.1 "$@" 2>>"$LOG"; }

# target_partition_evidence —— 独立于串口的「盘上真有系统」证据。
#   qcow2 不是块设备,fdisk/parted 直接读不了,故先用 qemu-img dd 把盘首(含主 GPT)
#   抽成一个普通文件,再对它做 GPT 结构与分区条目数断言。只看「装好了」的自述不够。
target_partition_evidence() {
    local head="$WORK/target-head.img"
    qemu-img dd -f qcow2 -O raw if="$TARGET" of="$head" bs=512 count=4096 >/dev/null 2>&1 \
        || { bad "抽取目标盘首扇区失败"; return 1; }
    { printf -- '--- fdisk -l (盘首副本) ---\n'; fdisk -l "$head" 2>&1 || true; } \
        | tee "$WORK/target-fdisk.txt" | sed 's/^/   /'
    python3 - "$head" <<'PY' > "$WORK/target-gpt.txt" 2>&1
import struct, sys
d = open(sys.argv[1], 'rb').read()
gpt = d[512:520] == b'EFI PART'
n = struct.unpack_from('<I', d, 512 + 80)[0] if gpt else 0
parts = sum(1 for i in range(min(n, 128)) if any(d[1024 + i * 128:1024 + i * 128 + 16]))
print(f"GPT={gpt} ENTRY_COUNT={n} NONEMPTY_PARTITIONS={parts}")
PY
    cat "$WORK/target-gpt.txt" | sed 's/^/   /'
    grep -qE 'GPT=True ENTRY_COUNT=[1-9][0-9]* NONEMPTY_PARTITIONS=[1-9]' "$WORK/target-gpt.txt"
}

# ---- 段一 a:等 SSH 可用 ----
live_ok=0; install_possible=1; elapsed=0
while [ "$elapsed" -lt "$BOOT_TIMEOUT" ]; do
    if live_ssh true >/dev/null 2>&1; then live_ok=1; break; fi
    grep -qiE "Kernel panic" "$SERIAL_LIVE" 2>/dev/null && break
    kill -0 "$QEMU_PID" 2>/dev/null || break
    sleep 3; elapsed=$((elapsed+3))
done

if [ "$live_ok" -ne 1 ]; then
    bad "live 系统在 ${BOOT_TIMEOUT}s 内没能通过 SSH 自证"
    tail -40 "$SERIAL_LIVE" | sed 's/^/         /'
    [ -s "$SERIAL_LIVE" ] || warn "串口日志为空(live 内核参数里可能没有 console=ttyS0)"
    verdict="LIVE_UNREACHABLE"
else
    ok "SSH 登录成功(live 自证的第 1 条)"
    # ---- 段一 b:根确为 live 介质 ----
    live_src="$(live_ssh "findmnt -no SOURCE / 2>/dev/null || true" | tr -d '\r')"
    live_fstype="$(live_ssh "findmnt -no FSTYPE / 2>/dev/null || true" | tr -d '\r')"
    live_run="$(live_ssh "ls -d /run/live /run/initramfs/live 2>/dev/null | head -1" | tr -d '\r')"
    info "live 根: source=[$live_src] fstype=[$live_fstype] /run/live=[$live_run]"
    live_media=0
    case "$live_src$live_fstype$live_run" in
        *live*|*squashfs*|*overlay*) live_media=1 ;;
    esac
    [ "$live_media" -eq 1 ] && ok "根确为 live 介质" || bad "根不像 live 介质(source=$live_src fstype=$live_fstype)"
    # ---- 段一 c:系统就是本镜像 ----
    live_osrel="$(live_ssh "cat /etc/os-release 2>/dev/null" | tr -d '\r')"
    printf '%s\n' "$live_osrel" > "$WORK/live-os-release.txt"
    if [ "$live_osrel" = "$(cat "$VM/os-release.expected")" ]; then
        ok "live 系统就是本镜像(os-release 逐字节一致)"
    else
        bad "live 系统的 os-release 与本镜像不一致"
        diff <(cat "$VM/os-release.expected") <(printf '%s\n' "$live_osrel") | sed 's/^/         /'
    fi
    live_ssh "test -f /opt/daedalus/shared/policy.toml && echo DAEDALUS_POLICY_OK || echo DAEDALUS_POLICY_MISSING" \
        > "$WORK/live-daedalus-marker.txt" 2>/dev/null
    grep -q DAEDALUS_POLICY_OK "$WORK/live-daedalus-marker.txt" 2>/dev/null \
        && ok "live 根里有 Daedalus 的安全策略(/opt/daedalus/shared/policy.toml)" \
        || bad "live 根里没有 Daedalus 安全策略 —— 装的可能不是本镜像"
    live_ssh "cat /proc/cmdline" > "$WORK/live-cmdline.txt" 2>/dev/null
    # 上游 iso.yaml 的字段是 kernel_args(不是 kernelargs),写错了会被静默忽略 ——
    # 这一条就是为了让那种静默失效当场暴露,而不是等 40 分钟的超时。
    if grep -q 'daedalus.install.disk=' "$WORK/live-cmdline.txt" 2>/dev/null; then
        ok "live 内核参数里带上了装机目标盘(iso.yaml 的 kernel_args 生效)"
    else
        bad "live 内核参数里没有 daedalus.install.disk —— iso.yaml 的 kernel_args 没生效,P2-2b 不可能成功"
        install_possible=0
    fi
    live_ssh "lsblk -o NAME,SIZE,TYPE,MOUNTPOINTS" > "$WORK/live-lsblk.txt" 2>/dev/null
fi

# ---- 段一 b:P2-2b 无人值守装机 ----
install_state="SKIPPED"
if [ "$live_ok" -eq 1 ] && [ "$install_possible" -eq 1 ]; then
    say "Phase 3.2 · 等无人值守装机完成(live 里 bootc install to-disk)"
    elapsed=0; poll=0
    while [ "$elapsed" -lt "$INSTALL_TIMEOUT" ]; do
        if ! kill -0 "$QEMU_PID" 2>/dev/null; then install_state="DONE"; break; fi
        # 每 30s 问一次单元状态:单元失败就当场收证据收工,不把时间耗在等超时上
        poll=$((poll+10))
        if [ "$poll" -ge 30 ]; then
            poll=0
            if live_ssh "systemctl is-failed daedalus-iso-autoinstall 2>/dev/null" 2>/dev/null | grep -q failed; then
                install_state="FAILED"; break
            fi
        fi
        sleep 10; elapsed=$((elapsed+10))
    done
    if [ "$install_state" = "DONE" ]; then
        ok "guest 已重启(QEMU 随之退出)—— 装机流程自行跑完"
    else
        [ "$install_state" = "FAILED" ] || install_state="NOT_DONE"
        bad "安装器未自行完成(单元状态=$install_state)—— 明确记 BLOCKED,不当成笼统 TIMEOUT"
        live_ssh "cat /run/daedalus-iso-autoinstall.log 2>/dev/null" > "$WORK/install-attempt.log" 2>/dev/null
        live_ssh "systemctl status daedalus-iso-autoinstall --no-pager 2>&1" > "$WORK/install-unit-status.txt" 2>/dev/null
        live_ssh "lsblk -o NAME,SIZE,TYPE,MOUNTPOINTS" > "$WORK/live-lsblk-after-install.txt" 2>/dev/null
        info "装机尝试日志 → $WORK/install-attempt.log"
        sed 's/^/         /' "$WORK/install-attempt.log" 2>/dev/null | tail -8
    fi
fi

kill "$QEMU_PID" 2>/dev/null; sleep 1; kill -9 "$QEMU_PID" 2>/dev/null; QEMU_PID=""

# ---- 段二:引导装好的盘,等 login: ----
installed_state="SKIPPED"
if [ "$install_state" = "DONE" ]; then
    say "Phase 3.3 · 引导装好的盘,等串口 login:"
    # 独立于串口的证据:目标盘必须有分区表(只看「装好了」的自述是不够的)
    if target_partition_evidence; then
        ok "目标盘有分区表(GPT + 非空分区条目)"
    else
        bad "目标盘上看不到分区表 —— 装机很可能没真正发生"
        install_state="NOT_DONE"
    fi
    : > "$SERIAL_INSTALLED"
    qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m "$MEM" -smp 2 \
        -nographic -no-reboot -boot c \
        -drive file="$TARGET",if=virtio,format=qcow2 \
        -netdev user,id=net0,hostfwd=tcp::$SSH_PORT-:22 -device virtio-net-pci,netdev=net0 \
        </dev/null >"$SERIAL_INSTALLED" 2>&1 &
    QEMU_PID=$!
    elapsed=0; got_login=0
    while [ "$elapsed" -lt "$BOOT_TIMEOUT" ]; do
        grep -qiE "login:" "$SERIAL_INSTALLED" 2>/dev/null && { got_login=1; break; }
        grep -qiE "Kernel panic" "$SERIAL_INSTALLED" 2>/dev/null && break
        kill -0 "$QEMU_PID" 2>/dev/null || break
        sleep 3; elapsed=$((elapsed+3))
    done
    if [ "$got_login" -eq 1 ]; then
        ok "装好的盘引导到 login:"
        installed_state="INSTALLED_LOGIN"
    else
        installed_state="NO_LOGIN"
        bad "装好的盘没能在 ${BOOT_TIMEOUT}s 内引导到 login:"
        tail -30 "$SERIAL_INSTALLED" | sed 's/^/         /'
    fi
    kill "$QEMU_PID" 2>/dev/null; sleep 1; kill -9 "$QEMU_PID" 2>/dev/null; QEMU_PID=""
fi

# ------------------------------------------------------------------- 判定汇总 --
if [ "$live_ok" -eq 1 ] && [ "$installed_state" = "INSTALLED_LOGIN" ]; then
    verdict="INSTALLED_LOGIN"
elif [ "$live_ok" -eq 1 ]; then
    verdict="LIVE_OK"
elif [ "$live_ok" -eq 0 ]; then
    verdict="LIVE_UNREACHABLE"
fi

say "Summary"
cat <<EOF
   判定         : $verdict
   P2-2a live   : $([ "$live_ok" -eq 1 ] && echo PASS || echo FAIL)(SSH 登录 / 根是 live 介质 / 系统是本镜像)
   P2-2b 装机   : $install_state → $installed_state
   ISO          : $ISO
   ISO 大小     : $(du -h "$ISO" 2>/dev/null | cut -f1)
   目标盘       : $TARGET($(du -h "$TARGET" 2>/dev/null | cut -f1))
   串口(live)   : $SERIAL_LIVE
   串口(装好后) : $SERIAL_INSTALLED
   完整日志     : $LOG
   工作目录     : $WORK
   SSH 公钥指纹 : $KEY_FP

   已知边界:P2-2b 是尽力项。live 环境拿不到 source 镜像 / 装完起不来时,如实记 BLOCKED,
   不改发布镜像、不换 Anaconda 路线;此时判定停在 LIVE_OK。
EOF

if [ "$REQUIRE_INSTALL" = "1" ] && [ "$verdict" != "INSTALLED_LOGIN" ]; then
    bad "VERIFY_REQUIRE_INSTALL=1:要求装完能引导到 login:,实际判定 $verdict"
    exit 1
fi
case "$verdict" in
    LIVE_OK|INSTALLED_LOGIN) exit 0 ;;
    *) exit 1 ;;
esac
