#!/usr/bin/env bash
# =============================================================================
# bootc-verify.sh —— 在 x86-64-v3 宿主上验证 bootc 镜像「真能引导」
# =============================================================================
# 解决什么问题
#   CI 只负责产 qcow2(daedalus-core build-qcow2 job),从不引导它;
#   2026-10-22 有过一次手工引导成功,但日志没留下、脚本也不在版本控制里。
#   本脚本把「下/拿到 qcow2 → KVM 引导 → 串口抓到 login 提示符 → 留证」固化下来。
#
# 为什么必须 x86-64-v3
#   AlmaLinux 10 的 glibc 硬性要求 x86-64-v3(avx2/bmi1/bmi2/fma/movbe/xsave)。
#   在没有 avx2 的机器上容器会直接 `Fatal glibc error: CPU does not support
#   x86-64-v3`,qemu 用默认 qemu64(仅 v1)也会有同样问题 —— 所以必须 -cpu host。
#
# 用法(在目标宿主上,需要 root 以便装 qemu + 起 KVM)
#   sudo ./bootc-verify.sh                     # 全流程
#   sudo ./bootc-verify.sh --qcow2 /path/disk.qcow2    # 用已有的 qcow2,跳过下载/构建
#   sudo ./bootc-verify.sh --only-preflight    # 只体检,不装包不动磁盘
#   ./bootc-verify.sh --help
#
# 环境变量
#   VERIFY_WORK      工作目录(默认:发起 sudo 那个用户的 ~/daedalus-verify)
#   VERIFY_IMAGE     用于构建 qcow2 的镜像(默认 ghcr.io/daedalusys/daedalus-os:latest)
#   VERIFY_RUN_ID    直接指定 CI run id 去下 artifact(默认:自动找最近一次成功的)
#   VERIFY_MEM       引导内存 MiB(默认 4096)
#   VERIFY_BOOT_TIMEOUT  等 login 提示符的秒数(默认 180)
#
# 安全边界
#   - 只写 $VERIFY_WORK;不碰宿主任何块设备(不执行 bootc install,不 dd 任何盘)。
#   - 装包仅 dnf install podman qemu-kvm qemu-img(可逆)。
#   - qemu 以 timeout + 主动 kill 收尾,不残留进程、不占宿主 22 端口(hostfwd 用 2222)。
#   - 前后各记一次 lsblk,末段对比,证明宿主盘未被写入。
#
# 退出码
#   0  引导成功(串口出现 login 提示符,且无 kernel panic)
#   1  引导失败(panic / 无输出 / 超时未见 login)
#   2  前置不满足(非 x86-64-v3、无 /dev/kvm、磁盘不足、缺工具且装不上)
# =============================================================================

set -uo pipefail

WORK="${VERIFY_WORK:-}"
IMAGE="${VERIFY_IMAGE:-ghcr.io/daedalusys/daedalus-os:latest}"
RUN_ID="${VERIFY_RUN_ID:-}"
MEM="${VERIFY_MEM:-4096}"
BOOT_TIMEOUT="${VERIFY_BOOT_TIMEOUT:-180}"
QCOW2=""
ONLY_PREFLIGHT=0

c_say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
c_info() { printf '   %s\n' "$*"; }
c_ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; }
c_warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; }
c_bad()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; }

# EL9 的 QEMU 系统模拟器不叫 qemu-system-x86_64,而是 /usr/libexec/qemu-kvm
# (qemu-kvm-core 提供,无 /usr/bin 软链)。Debian/Ubuntu 才是 qemu-system-x86_64。
# 按可用性解析,不要按发行版猜。
QEMU_BIN=""
resolve_qemu() {
    local c
    for c in qemu-system-x86_64 qemu-kvm /usr/libexec/qemu-kvm; do
        if command -v "$c" >/dev/null 2>&1; then QEMU_BIN="$(command -v "$c")"; return 0; fi
    done
    QEMU_BIN=""
    return 1
}


usage() { sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0; }

while [ $# -gt 0 ]; do
    case "$1" in
        --qcow2)          QCOW2="${2:-}"; shift 2 ;;
        --qcow2=*)        QCOW2="${1#*=}"; shift ;;
        --only-preflight) ONLY_PREFLIGHT=1; shift ;;
        -h|--help)        usage ;;
        *) echo "未知参数: $1(用 --help)" >&2; exit 2 ;;
    esac
done

# 工作目录落在「发起 sudo 的用户」home 下:root 的 ~ 是 dr-xr-x---,
# 产物落进去后本人读不到,也没法事后贴日志。
_invoker="${SUDO_USER:-$USER}"
_invoker_home="$(getent passwd "$_invoker" 2>/dev/null | cut -d: -f6)"
[ -n "${_invoker_home:-}" ] || _invoker_home="$HOME"
WORK="${WORK:-$_invoker_home/daedalus-verify}"
LOG="$WORK/bootc-verify.log"
SERIAL="$WORK/serial-console.log"
mkdir -p "$WORK" || { echo "无法创建工作目录 $WORK" >&2; exit 2; }
: > "$LOG"
_orig_owner="$_invoker:$(id -gn "$_invoker" 2>/dev/null || echo "$_invoker")"
chown -R "$_orig_owner" "$WORK" 2>/dev/null || true

log() { printf '%s\n' "$*" >>"$LOG"; }
say() { c_say "$*"; log ""; log "== $* =="; }
info(){ c_info "$*"; log "   $*"; }
ok()  { c_ok "$*"; log "   OK $*"; }
warn(){ c_warn "$*"; log "   !! $*"; }
bad() { c_bad "$*"; log "   XX $*"; }

need_root() {
    [ "$(id -u)" -eq 0 ] && return 0
    c_bad "需要 root:请用 sudo 运行本脚本(装包与 /dev/kvm 都需要)"
    exit 2
}

# ---------------------------------------------------------------------------
say "Phase 0 · preflight"
info "工作目录 : $WORK"
info "目标镜像 : $IMAGE"
info "日志     : $LOG"

arch="$(uname -m)"
if [ "$arch" != "x86_64" ]; then
    bad "架构 $arch 不是 x86_64,本脚本只覆盖 amd64"
    exit 2
fi

missing_flags=""
for f in avx2 bmi1 bmi2 fma movbe xsave; do
    grep -qw "$f" /proc/cpuinfo || missing_flags="$missing_flags $f"
done
if [ -n "$missing_flags" ]; then
    bad "CPU 不支持 x86-64-v3,缺:$missing_flags"
    bad "AlmaLinux 10 起 glibc 硬性要求 v3;换一台 avx2 机器再跑"
    exit 2
fi
ok "CPU 支持 x86-64-v3($(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //'))"

if [ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    ok "/dev/kvm 可读写(硬加速可用)"
else
    bad "/dev/kvm 不可用;无 KVM 时 -cpu host 不可用,引导会失败"
    exit 2
fi

avail_gb="$(df -BG --output=avail "$WORK" 2>/dev/null | tail -1 | tr -dc '0-9')"
info "工作目录可用空间: ${avail_gb:-?} GB"
if [ "${avail_gb:-0}" -lt 15 ]; then
    warn "可用空间 < 15 GB;qcow2 约 1.3 GB,但 podman 存储/解包会额外占用"
fi

say "Phase 0.1 · 宿主盘基线(用于事后证明未被写入)"
lsblk -o NAME,SIZE,TYPE,MOUNTPOINT >"$WORK/lsblk-before.txt" 2>&1
cat /proc/cmdline >"$WORK/cmdline-before.txt" 2>&1
[ -r /etc/fstab ] && cp /etc/fstab "$WORK/fstab-before.txt" 2>/dev/null || true
ok "已记录 lsblk / cmdline / fstab 基线"

if [ "$ONLY_PREFLIGHT" = 1 ]; then
    say "只体检模式,停止"
    info "日志: $LOG"
    exit 0
fi

need_root

# ---------------------------------------------------------------------------
say "Phase 1 · 工具链"
need_pkgs=""
command -v podman >/dev/null 2>&1 || need_pkgs="$need_pkgs podman"
resolve_qemu || need_pkgs="$need_pkgs qemu-kvm"
command -v qemu-img >/dev/null 2>&1 || need_pkgs="$need_pkgs qemu-img"
# 注意:EL9 里没有名为 qemu-system-x86 的包(实测 dnf download 报「没有可用的软件包」),
# 提供 QEMU 系统模拟器的是 qemu-kvm,二进制落在 /usr/libexec/qemu-kvm。
if [ -n "$need_pkgs" ]; then
    info "需要安装:$need_pkgs"
    if command -v dnf >/dev/null 2>&1; then
        # shellcheck disable=SC2086
        if ! dnf install -y $need_pkgs >>"$LOG" 2>&1; then
            bad "dnf install 失败,详见 $LOG"
            exit 2
        fi
    elif command -v apt-get >/dev/null 2>&1; then
        if ! apt-get update -y >>"$LOG" 2>&1 || ! apt-get install -y podman qemu-system-x86 qemu-utils >>"$LOG" 2>&1; then
            bad "apt-get install 失败,详见 $LOG"
            exit 2
        fi
    else
        bad "既没有 dnf 也没有 apt-get,无法自动安装"
        exit 2
    fi
fi
command -v podman >/dev/null 2>&1 && ok "podman → $(command -v podman)" || { bad "缺 podman"; exit 2; }
command -v qemu-img >/dev/null 2>&1 && ok "qemu-img → $(command -v qemu-img)" || { bad "缺 qemu-img"; exit 2; }
if resolve_qemu; then
    ok "qemu → $QEMU_BIN"
    "$QEMU_BIN" --version | head -1 | sed 's/^/   /' | tee -a "$LOG"
else
    bad "装了 qemu-kvm 仍找不到系统模拟器(试过 qemu-system-x86_64 / qemu-kvm / /usr/libexec/qemu-kvm)"
    bad "排查:rpm -ql qemu-kvm-core | grep -E 'qemu-(system|kvm)'"
    exit 2
fi

# ---------------------------------------------------------------------------
say "Phase 2 · 取得 qcow2"
if [ -n "$QCOW2" ]; then
    [ -f "$QCOW2" ] || { bad "--qcow2 指向的文件不存在: $QCOW2"; exit 2; }
    info "使用外部指定的 qcow2: $QCOW2"
else
    QCOW2="$WORK/disk.qcow2"
    if [ -f "$QCOW2" ]; then
        ok "复用已有 $QCOW2($(du -h "$QCOW2" | cut -f1))"
    elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
        rid="$RUN_ID"
        if [ -z "$rid" ]; then
            info "查询最近一次成功产出 qcow2 的 CI run …"
            rid="$(gh run list --repo Daedalusys/daedalus-core --workflow build.yml \
                    --status success --limit 20 --json databaseId \
                    --jq '.[].databaseId' 2>/dev/null | head -1)"
        fi
        if [ -n "$rid" ]; then
            info "下载 artifact daedalus-os-qcow2-headless(run $rid)"
            rm -rf "$WORK/dl" 2>/dev/null || true
            if gh run download "$rid" --repo Daedalusys/daedalus-core \
                   -n daedalus-os-qcow2-headless -D "$WORK/dl" >>"$LOG" 2>&1; then
                found="$(find "$WORK/dl" -name '*.qcow2' -type f | head -1)"
                if [ -n "$found" ]; then
                    mv "$found" "$QCOW2"
                    ok "已取得 $QCOW2($(du -h "$QCOW2" | cut -f1))"
                else
                    bad "artifact 里没有 .qcow2(目录:$(ls -A "$WORK/dl" 2>/dev/null | head -5 | tr '\n' ' '))"
                fi
            else
                bad "gh run download 失败,详见 $LOG"
            fi
        else
            warn "找不到可用的成功 run id"
        fi
    else
        warn "gh 不可用或未登录"
    fi

    if [ ! -f "$QCOW2" ]; then
        say "Phase 2.1 · 回退:本地用 bootc-image-builder 造 qcow2"
        info "会 podman pull $IMAGE(~7 GB)并 rootful 构建,耗时较长"
        podman pull "$IMAGE" >>"$LOG" 2>&1 || { bad "podman pull 失败,详见 $LOG"; exit 2; }
        podman save --format oci-archive -o "$WORK/daedalus-os.oci.tar" "$IMAGE" >>"$LOG" 2>&1 \
            || { bad "podman save 失败,详见 $LOG"; exit 2; }
        podman load -i "$WORK/daedalus-os.oci.tar" >>"$LOG" 2>&1 \
            || { bad "podman load 失败,详见 $LOG"; exit 2; }
        podman run --rm --privileged \
            -v "$WORK":/output \
            -v /var/lib/containers/storage:/var/lib/containers/storage \
            --security-opt label=disable \
            quay.io/centos-bootc/bootc-image-builder:latest \
            --type qcow2 --local "$IMAGE" >>"$LOG" 2>&1 \
            || { bad "bootc-image-builder 失败,详见 $LOG"; exit 2; }
        found="$(find "$WORK" -name '*.qcow2' -type f | head -1)"
        [ -n "$found" ] || { bad "builder 报成功但没有产出 .qcow2"; exit 2; }
        [ "$found" = "$QCOW2" ] || mv "$found" "$QCOW2"
        ok "已构建 $QCOW2($(du -h "$QCOW2" | cut -f1))"
    fi
fi
info "qcow2 sha256: $(sha256sum "$QCOW2" | cut -d' ' -f1)"

# ---------------------------------------------------------------------------
say "Phase 3 · KVM 引导 + 串口抓取"
info "机器: q35 + accel=kvm + -cpu host(必须 host,v1 CPU 会让 guest glibc panic)"
info "内存 ${MEM} MiB,等待 login 提示符最长 ${BOOT_TIMEOUT}s"
: > "$SERIAL"

"$QEMU_BIN" \
    -machine q35,accel=kvm \
    -cpu host \
    -m "$MEM" -smp 2 \
    -nographic -no-reboot \
    -drive file="$QCOW2",if=virtio,format=qcow2 \
    -netdev user,id=net0,hostfwd=tcp::2222-:22 \
    -device virtio-net,netdev=net0 \
    </dev/null >>"$SERIAL" 2>&1 &
qpid=$!
info "qemu pid=$qpid,二进制=$QEMU_BIN"
info "串口日志 → $SERIAL"

verdict=""
elapsed=0
while [ "$elapsed" -lt "$BOOT_TIMEOUT" ]; do
    if grep -qiE "Kernel panic|glibc.*x86-64-v(2|3)|CPU does not support" "$SERIAL" 2>/dev/null; then
        verdict="PANIC"; break
    fi
    if grep -qE "login: " "$SERIAL" 2>/dev/null; then
        verdict="LOGIN"; break
    fi
    kill -0 "$qpid" 2>/dev/null || { verdict="QEMU_EXITED"; break; }
    sleep 2; elapsed=$((elapsed+2))
done

[ -n "$verdict" ] || verdict="TIMEOUT_EMPTY"
if grep -qE "login: " "$SERIAL" 2>/dev/null; then verdict="LOGIN"; fi
if grep -qiE "Kernel panic|CPU does not support" "$SERIAL" 2>/dev/null; then verdict="PANIC"; fi

kill "$qpid" 2>/dev/null || true
sleep 1
kill -9 "$qpid" 2>/dev/null || true
wait "$qpid" 2>/dev/null || true

case "$verdict" in
    LOGIN)
        ok "串口出现 login 提示符 —— 镜像可引导"
        info "末尾 12 行:"
        tail -12 "$SERIAL" | sed 's/^/         /'
        ;;
    PANIC)
        bad "串口出现 kernel panic / CPU 不支持 —— 镜像不可引导"
        info "关键行:"
        grep -iE "Kernel panic|CPU does not support|glibc" "$SERIAL" | head -8 | sed 's/^/         /'
        ;;
    QEMU_EXITED)
        bad "qemu 提前退出(退出码见日志),未出现 login"
        tail -20 "$SERIAL" | sed 's/^/         /'
        ;;
    *)
        bad "等待 ${BOOT_TIMEOUT}s 未见 login 提示符"
        info "串口末尾 20 行(若为空说明连 bootloader 都没输出):"
        tail -20 "$SERIAL" | sed 's/^/         /'
        ;;
esac

# ---------------------------------------------------------------------------
say "Phase 4 · 宿主未被写入(对照基线)"
lsblk -o NAME,SIZE,TYPE,MOUNTPOINT >"$WORK/lsblk-after.txt" 2>&1
if diff -u "$WORK/lsblk-before.txt" "$WORK/lsblk-after.txt" >"$WORK/lsblk.diff" 2>&1; then
    ok "lsblk 与引导前一致(宿主盘未被写入)"
else
    warn "lsblk 有变化,请人工确认(差异见 $WORK/lsblk.diff);预期仅 loop/zram 之类临时设备"
    sed 's/^/         /' "$WORK/lsblk.diff" | head -20
fi

say "Summary"
cat <<EOF
   判定     : $verdict
   qcow2    : $QCOW2
   sha256   : $(sha256sum "$QCOW2" | cut -d' ' -f1)
   串口日志 : $SERIAL
   完整日志 : $LOG
   工作目录 : $WORK

   把这个目录整个带回来(或只带 serial-console.log + bootc-verify.log + lsblk-*.txt)
   即可作为 Todo 9 的证据入库。
EOF

case "$verdict" in
    LOGIN) exit 0 ;;
    *)     exit 1 ;;
esac
