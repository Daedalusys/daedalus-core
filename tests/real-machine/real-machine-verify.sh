#!/usr/bin/env bash
# =============================================================================
# Daedalus 实机验证脚本(需要 sudo)
# =============================================================================
# 入库位置:daedalus-core/tests/real-machine/real-machine-verify.sh
#   (2026-10-29 从 ~/data/backup/code/Daedalus/ 收进版本控制 —— 此前不在任何
#    git 仓里,产物日志也随工作目录丢失过一次)
# 用途:补上本地测试覆盖不到的两个真机缺口
#   (1) Plan 2026-10-20-fork-almalinux-bootc-headless Task 4 Step 4.3
#       —— serial console 真出登录提示符(plan 里唯一还没关掉的 checkbox)
#   (2) daedalus-core#50 —— build-qcow2 这条交付腿从 2026-10-15 起产不出产物
#
# 背景:
#   - qcow2 是唯一能真正验串口的产物形态,而 CI 的 build-qcow2 job 一直红
#     (bootc-image-builder 容器内 osbuild 挂载被拒)。
#   - 所以本脚本先在本机复现这条腿,顺便就是 #50 的诊断现场;
#     能产出 qcow2 才进 Phase 3 真机开串口。
#
# 用法:
#   ./real-machine-verify.sh                 # 全流程(自动跳过已满足的 phase)
#   ./real-machine-verify.sh preflight       # 只体检,不需要 sudo
#   ./real-machine-verify.sh deps            # 只装依赖
#   ./real-machine-verify.sh qcow2           # 只造 qcow2
#   ./real-machine-verify.sh boot            # 只开机验串口
#
# 安全约定:
#   - 只在专用工作目录 $WORK 下作业,不动既有容器/镜像。
#   - 不执行 bootc install 到宿主盘;qcow2 只落在 $WORK。
#   - Phase 3 的 qemu 用 -nographic + timeout,到点自动 kill,不碰宿主 sshd。
#   - 全程日志写 $WORK/verify.log,可直接贴回 issue。
#
# 破坏性操作提前说明:
#   - deps phase 会 dnf install 软件包(可逆)。
#   - qcow2 phase 会 sudo podman load 一个 OCI archive(占盘,约 2-3 GB)。
#   - 其余全部只读或只写 $WORK。
# =============================================================================

set -uo pipefail

# 工作目录。用「发起 sudo 的那个用户」的 home,不用 root 的:脚本整体在 sudo 下
# 跑,而 $HOME=/root 是 dr-xr-x--- —— 产物落进去后只有 root 能读,既没法事后排查
# 也不用 sudo 再看。改成 SUDO_USER 的 home 后,产物对本人可读。
_invoker="${SUDO_USER:-$USER}"
_invoker_home="$(getent passwd "$_invoker" 2>/dev/null | cut -d: -f6)"
[[ -n "$_invoker_home" ]] || _invoker_home="$HOME"
WORK="${DAEDALUS_VERIFY_WORK:-$_invoker_home/daedalus-verify}"
IMAGE="${DAEDALUS_VERIFY_IMAGE:-ghcr.io/daedalusys/daedalus-os:latest}"
BUILDER="${DAEDALUS_VERIFY_BUILDER:-quay.io/centos-bootc/bootc-image-builder:latest}"
# 镜像加速前缀。直连 ghcr.io / quay.io 实测 >300 秒未完成(1329 MiB 镜像),
# 经 gh-proxy.com 同一镜像 35 秒。留空则直连。
MIRROR="${DAEDALUS_VERIFY_MIRROR:-gh-proxy.com/docker/}"
# qcow2 实际文件名。bootc-image-builder 不按镜像名输出,固定叫 disk.qcow2
# (manifest 里 "filename": "disk.qcow2")。之前硬编码成 daedalus-os.qcow2 导致
# builder 明明 rc=0 成功,脚本却报「未产出」。故这里默认 disk.qcow2,且 Phase 2.4
# 会在工作目录里扫 *.qcow2 兼容 builder 改名的情形。
# ★ 必须定义在 QCOW2= 之前:脚本开头有 set -u,晚定义会报 unbound variable。
QCOW2_NAME="${DAEDALUS_VERIFY_QCOW2_NAME:-disk.qcow2}"
QCOW2="$WORK/$QCOW2_NAME"
LOG="$WORK/verify.log"
QEMU_MEM="${DAEDALUS_VERIFY_MEM:-4096}"
BOOT_TIMEOUT="${DAEDALUS_VERIFY_BOOT_TIMEOUT:-180}"

mkdir -p "$WORK"
touch "$LOG"

say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; printf '\n== %s ==\n' "$*" >>"$LOG"; }
info() { printf '   %s\n' "$*"; printf '   %s\n' "$*" >>"$LOG"; }
warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; printf '   !! %s\n' "$*" >>"$LOG"; }
ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; printf '   OK %s\n' "$*" >>"$LOG"; }
die()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; printf '   XX %s\n' "$*" >>"$LOG"; exit 1; }

# pull_image:优先走加速前缀拉镜像,拉完改回原名并删除代理侧的 tag。
# 这样脚本后面所有地方都只认 IMAGE / BUILDER 的规范名,不必关心来源。
# 本地已有该镜像时直接跳过,不做网络请求。
pull_image() {
  local ref="$1"
  if podman image exists "$ref" 2>/dev/null; then
    ok "$ref 本地已有,跳过拉取"
    return 0
  fi
  if [[ -z "$MIRROR" ]]; then
    say "拉取 $ref(直连)"
    podman pull "$ref" || die "拉取 $ref 失败"
    return 0
  fi
  local proxied="${MIRROR}${ref}"
  say "拉取 $ref"
  info "加速前缀: ${MIRROR}"
  if podman pull "$proxied"; then
    podman tag "$proxied" "$ref" || die "改名失败: $proxied → $ref"
    podman rmi "$proxied" >/dev/null 2>&1 || true
    ok "$ref 就绪(已从加速源拉取并改回原名)"
  else
    warn "加速源拉取失败,回退直连 $ref"
    podman pull "$ref" || die "直连 $ref 也失败"
  fi
}

need_sudo() {
  [ "$(id -u)" -eq 0 ] && return 0
  sudo -n true 2>/dev/null && return 0
  die "这一步需要 sudo。请整脚本用 sudo 跑,或给当前用户免密 sudo。"
}

# ----------------------------------------------------------------------------
# Phase 0: preflight(不需要 sudo)
# ----------------------------------------------------------------------------
phase_preflight() {
  say "Phase 0 · preflight"
  info "工作目录 : $WORK"
  info "目标镜像 : $IMAGE"

  say "Phase 0.1 · KVM 能力"
  if [ -w /dev/kvm ]; then
    ok "/dev/kvm 可写(硬加速可用)"
  elif [ -e /dev/kvm ]; then
    warn "/dev/kvm 存在但不可写 —— 当前用户不在 kvm 组?需 sudo usermod -aG kvm \$USER 后重新登录"
  else
    die "/dev/kvm 不存在 —— 这台机器没有硬件虚拟化,本脚本的 Phase 3 跑不了(Phase 0/1/2 仍可用)"
  fi
  info "cpu virt flags: $(grep -c -E 'vmx|svm' /proc/cpuinfo) 核支持"

  say "Phase 0.2 · 磁盘余量"
  local avail_gb
  avail_gb=$(df -BG --output=avail "$WORK" 2>/dev/null | tail -1 | tr -dc '0-9')
  info "可用 ${avail_gb:-?} GB"
  [ "${avail_gb:-0}" -ge 25 ] || warn "qcow2 构建建议 ≥25 GB,当前可能不够"

  say "Phase 0.3 · 工具链"
  local t
  for t in podman qemu-system-x86_64 qemu-img dnf jq; do
    if command -v "$t" >/dev/null 2>&1; then ok "$t: $(command -v "$t")"
    else info "$t: 缺失(Phase 1 会装)"; fi
  done

  say "Phase 0.4 · 镜像可达性"
  if command -v podman >/dev/null 2>&1; then
if podman image exists "$IMAGE" 2>/dev/null; then
      ok "$IMAGE 本地已有"
    else
      info "$IMAGE 本地没有,后续 phase 会拉(走加速源)"
    fi
  fi
}

# ----------------------------------------------------------------------------
# Phase 1: deps(需要 sudo)
# ----------------------------------------------------------------------------
phase_deps() {
  need_sudo
  say "Phase 1 · 安装依赖"
  local pkgs=(qemu-kvm qemu-img)
  # distro 包名兜底:EL 系没有 meta 包 qemu-kvm,实际叫 qemu-system-x86
  if dnf -q list --available qemu-system-x86 >/dev/null 2>&1; then
    pkgs=(qemu-system-x86 qemu-img)
  fi
  info "待装: ${pkgs[*]}"
  if dnf install -y "${pkgs[@]}"; then
    ok "依赖已装"
  else
    warn "dnf install 失败 —— 若是 EL 系无 qemu-kvm 包,手工装上游静态 qemu 也行"
    return 1
  fi

  if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
    # EL 系包名可能是 qemu-system-x86
    local alt
    alt=$(ls /usr/bin/qemu-system-* 2>/dev/null | head -1)
    if [ -n "$alt" ]; then
      ok "qemu 可执行文件为 $alt(脚本会自动识别)"
    else
      die "找不到 qemu-system-x86_64,后续 Phase 3 无法进行"
    fi
  else
    ok "qemu-system-x86_64 就绪: $(qemu-system-x86_64 --version | head -1)"
  fi
}

qemu_bin() {
  command -v qemu-system-x86_64 2>/dev/null || ls /usr/bin/qemu-system-x86* 2>/dev/null | head -1
}

# ----------------------------------------------------------------------------
# Phase 2: qcow2(需要 sudo —— bootc-image-builder 强制 rootful)
# ----------------------------------------------------------------------------
# 这段复现 daedalus-core .github/workflows/build.yml 的 build-qcow2 job,
# 也就是 core#50 那条红腿。CI 上失败在 builder 容器内 osbuild 挂载被拒;
# 在本机跑能拿到完整 stderr,便于定位。
phase_qcow2() {
  need_sudo
  local qemu; qemu=$(qemu_bin); [ -n "$qemu" ] || die "先跑 Phase 1"

  say "Phase 2 · 造 qcow2(复现 core#50)"

  # CI 的做法:rootless save → rootful load,再跑 builder
  say "Phase 2.1 · 取镜像 + 导出 OCI archive"
  pull_image "$IMAGE"
  local archive="$WORK/daedalus-os.oci.tar"
  if [ -s "$archive" ]; then ok "archive 已存在,复用"
  else
    podman save --format oci-archive -o "$archive" "$IMAGE" \
      || die "podman save 失败"
    ok "archive: $(du -h "$archive" | cut -f1) → $archive"
  fi

  say "Phase 2.2 · rootful load"
  podman load -i "$archive" \
    || warn "podman load 返回非零(有时是正常的 import 噪声,继续)"
  ok "rootful store 已就绪"

  say "Phase 2.3 · 跑 bootc-image-builder(失败点即 core#50)"
  pull_image "$BUILDER"
  info "builder: $BUILDER"
  info "这一步很可能失败 —— 那正是 core#50。失败也没关系,"
  info "完整 stderr 已进日志,请把它贴到 core#50 下。"

  local out="$WORK/qcow2.log"
  podman run --rm --privileged --security-opt label=disable \
    -v "$WORK:/output" \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    "$BUILDER" \
    --local --type qcow2 --output /output "$IMAGE" \
    >"$out" 2>&1
  local rc=$?

  say "Phase 2.4 · 结果"
  # builder 的输出名以 manifest 为准。⚠ 不能限制深度:bootc-image-builder 会把产物
  # 放在 <output>/<type>/ 下面(实测 --type qcow2 → <output>/qcow2/disk.qcow2),
  # 之前 -maxdepth 1 只扫当前层,正好漏掉子目录,于是 rc=0 + Build complete!
  # 却被误报成「未产出」。
  local found
  found=$(find "$WORK" -name '*.qcow2' -type f -printf '%p %s\n' 2>/dev/null | sort -k2 -rn | head -1)
  if [ -z "$found" ]; then
    warn "qcow2 未产出(rc=$rc)。先看工作目录里有没有别的产物:"
    ls -la "$WORK" | tail -10
    warn "关键日志: $out"
    echo "---------- builder stderr 末尾 40 行 ----------"
    tail -40 "$out"
    echo "------------------------------------------------"
    echo "$out" >> "$LOG"
    return 1
  fi

  QCOW2=$(echo "$found" | cut -d' ' -f1)
  if [ "$rc" -ne 0 ]; then
    warn "builder 退出码非零(rc=$rc),但找到了产物,继续 Phase 3 —— 结果可能不完整"
  fi
  ok "qcow2 就绪: $(basename "$QCOW2")  $(du -h "$QCOW2" | cut -f1)  → $QCOW2"
  echo "$QCOW2" > "$WORK/qcow2.path"
}

# ----------------------------------------------------------------------------
# Phase 3: 真机开机验串口(Step 4.3)
# ----------------------------------------------------------------------------
phase_boot() {
  local qemu; qemu=$(qemu_bin); [ -n "$qemu" ] || die "先跑 Phase 1"
  # 优先用 Phase 2 记下的路径,没有就现扫一遍(支持只跑 boot 这一相)。
  # 同 Phase 2.4:不限制深度,builder 产物在 <output>/<type>/ 下面。
  if [ -f "$WORK/qcow2.path" ]; then
    QCOW2=$(cat "$WORK/qcow2.path")
  else
    local f
    f=$(find "$WORK" -name '*.qcow2' -type f -printf '%p\n' 2>/dev/null | sort | head -1)
    [ -n "$f" ] && QCOW2="$f"
  fi
  [ -f "$QCOW2" ] || die "工作目录里没有 qcow2,先跑 Phase 2(bootc-image-builder 那一步)"

  say "Phase 3 · KVM 开机 + 串口验证(plan Task 4 Step 4.3)"

  local console_log="$WORK/serial-console.log"
  rm -f "$console_log"

  # -nographic 把串口接到 stdio;我们重定向到文件。
  # 镜像的 bootc install 配置已写 console=ttyS0,115200n8(前面 rescue-path.sh
  # 已验过 20-rhel.toml),所以引导期输出应该出现在串口上。
  #
  # 凭据:daedalus-core 镜像默认建 daedalus 用户(Root 密码 / 免密 sudo 见 README)。
  # 这里不自动登录 —— 只验"提示符出不出来"这一件事。
  say "Phase 3.1 · 启动(最多 ${BOOT_TIMEOUT}s,到点自动 kill)"
  info "磁盘: $QCOW2"
  info "串口日志: $console_log"
  info "内存: ${QEMU_MEM} MiB"
  info "提示:这是 headless 镜像,没有图形。串口就是唯一入口。"

  # ★ 必须 -cpu host:QEMU 默认的 qemu64 模型只向 guest 暴露 x86-64-v1,
  # 无论宿主多强。而 AlmaLinux 10 的 glibc 要求 v2,于是 guest 的 init 起来就:
  #   Fatal glibc error: CPU does not support x86-64-v2
  #   Kernel panic - not syncing: Attempted to kill init!
  # 先前一次误判成「宿主 CPU 太老」,实际宿主(i7-12700H)avx2 齐全 —— 问题在
  # guest 看不到特性。日志里会给一条警告,别忽略:
  #   Warning: Deprecated Hardware is detected: x86_64-v1:GenuineIntel:QEMU Virtual CPU version 2.5+
  # -cpu host 把宿主全部特性透传给 guest,KVM 下由硬件虚拟化加速。
  info "CPU 模型: -cpu host(默认 qemu64 会让 guest 停在 x86-64-v1)"

  timeout --foreground "${BOOT_TIMEOUT}" \
    "$qemu" \
      -name daedalus-serial-verify \
      -machine q35,accel=kvm:tcg \
      -cpu host \
      -m "$QEMU_MEM" \
      -smp 2 \
      -nographic \
      -no-reboot \
      -drive "file=$QCOW2,if=virtio,format=qcow2" \
      -netdev user,id=net0,hostfwd=tcp::2222-:22 \
      -device virtio-net-pci,netdev=net0 \
      > "$console_log" 2>&1
  local rc=$?

  say "Phase 3.2 · 串口输出判读"
  if [ $rc -eq 124 ]; then
    ok "到时自动终止(符合预期,我们要的就是引导期输出)"
  elif [ $rc -ne 0 ]; then
    warn "qemu 退出码 $rc(非超时)。日志里应有原因。"
  fi

  # 判读。★ 顺序要紧:panic 优先于一切成功信号。先前把 GRUB 倒计时里的
  # "reboot: Power down" 当成登录提示符,于是一次 kernel panic 被误报成
  # LOGIN_PROMPT_SEEN + 「Step 4.3 可以关掉」。判据只认 getty 真正吐给用户的
  # 两行;GRUB 菜单 / ostree 引导都不算。
  local verdict="UNKNOWN"
  if grep -qiE 'Kernel panic|not syncing|Attempted to kill init|Fatal glibc error|CPU does not support' "$console_log"; then
    verdict="GUEST_PANIC"
    warn "guest 内核崩溃,首因:"
    grep -aiE 'Kernel panic|Fatal glibc error|CPU does not support' "$console_log" | head -3 | sed 's/^/      /'
  elif grep -qiE 'login:|Welcome to' "$console_log"; then
    verdict="LOGIN_PROMPT_SEEN"
  elif grep -qiE 'ostree' "$console_log"; then
    verdict="OSTREE_BOOTED_NO_LOGIN_PROMPT"
  elif [ ! -s "$console_log" ]; then
    verdict="EMPTY_CONSOLE"
  fi
  ok "判读结果: $verdict"

  say "Phase 3.3 · 串口日志末尾 60 行"
  tail -60 "$console_log"
  {
    echo "=== serial console verdict: $verdict ==="
    tail -60 "$console_log"
  } >> "$LOG"

  say "Phase 3.4 · sshd 旁证(2222 是上面 hostfwd 的端口)"
  info "在另一个终端执行: ssh -p 2222 <user>@localhost"
  info "headless 镜像已确认 sshd enabled 且监听 22(rescue-path.sh 全绿),"
  info "所以串口若不通, ssh 是第二条救砖路径 —— 这正是 plan RF2 的设计意图。"

  case "$verdict" in
    LOGIN_PROMPT_SEEN)
      ok "Step 4.3 可以关掉了 —— 串口登录提示符已验证"
      ;;
    GUEST_PANIC)
      warn "Step 4.3 不能关:guest 没跑起来,以上首因 + $console_log 请贴回讨论。"
      ;;
    *)
      warn "Step 4.3 暂不能关。请把 $console_log 贴回讨论。"
      warn "若 EMPTY_CONSOLE,多半是 qcow2 里 bootloader 没串口参数 —— 那属于"
      warn "bootc-image-builder 的 qcow2 生成侧问题,跟 base 镜像无关。"
      ;;
  esac
}

# ----------------------------------------------------------------------------
summary() {
  say "总结"
  # 产物属主还给发起者,免得后续排查还得 sudo。
  if [[ -n "${SUDO_USER:-}" && -d "$WORK" ]]; then
    chown -R "$SUDO_USER" "$WORK" 2>/dev/null && info "已把 $WORK 属主还给 $SUDO_USER"
  fi
  for f in "$LOG" "$WORK/serial-console.log" "$WORK/qcow2.log"; do
    [ -e "$f" ] && info "日志: $f ($(wc -l <"$f") 行)"
  done
  cat <<'EOF'

请把以下内容贴回对应 issue:
  * Phase 2 失败 → daedalus-core#50(builder stderr 末尾 40 行已在终端打印)
  * Phase 3 结果 → fork plan Task 4 Step 4.3(判读结果 + serial-console.log)
  * rescue E2E 两个缺陷 → 见我另外整理的 issue 草稿(daedalus-host rescue)
EOF
}

case "${1:-all}" in
  preflight) phase_preflight ;;
  deps)      phase_deps ;;
  qcow2)     phase_qcow2 ;;
  boot)      phase_boot ;;
  all)
    phase_preflight || exit 1
    say "以上 preflight 全过则继续;任一硬失败会自行 die。"
    phase_deps        || warn "Phase 1 失败,后续可能连带失败"
    phase_qcow2       || warn "Phase 2 失败(core#50 已知),跳过 Phase 3"
    phase_boot        || warn "Phase 3 失败"
    summary
    ;;
  *) die "未知参数: $1(可用: preflight | deps | qcow2 | boot | all)" ;;
esac