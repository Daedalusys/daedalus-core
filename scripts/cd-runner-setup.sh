#!/usr/bin/env bash
# =============================================================================
# cd-runner-setup.sh —— 在 CD 靶机上安装 GitHub Actions 自托管 runner
# =============================================================================
# 装什么
#   一个带 `daedalus-kvm` 标签的自托管 runner,供 daedalus-core 的
#   `deploy-verify-kvm` job 跑真 KVM 引导验证(qcow2 → qemu → 串口等 login:)。
#
# 为什么必须真机
#   AlmaLinux 10 的 glibc 硬性要求 x86-64-v3(avx2/bmi1/bmi2/fma/movbe/xsave),
#   托管 runner 与多数开发机都跑不了目标镜像;qemu 的 -cpu host 也必须是 v3。
#
# 本脚本由 scripts/cd-verify.sh runner 派发(经 scp + ssh -t 送到 /tmp 后执行),
# 也可单独在靶机上跑。sudo 需要交互输密码 —— 故请用 `ssh -t` 方式执行。
#
# 用法(在靶机上)
#   sudo -v                                  # 先缓存 sudo 凭据,后续步骤不必反复输
#   DAEDALUS_RUNNER_TOKEN=<token> bash cd-runner-setup.sh
#   # 或从文件读(推荐,避免 token 出现在 ps 里)
#   bash cd-runner-setup.sh --token-file /tmp/.daedalus-runner-token
#
# 环境变量
#   DAEDALUS_REPO      目标仓(默认 Daedalusys/daedalus-core)
#   RUNNER_VER         runner 版本(默认 2.338.2)
#   RUNNER_NAME        runner 名字(默认取主机名)
#   RUNNER_LABELS      附加标签(默认 daedalus-kvm)
#   RUNNER_DIR         安装目录(默认 ~/actions-runner)
#   GRANT_NOPASSWD     设为 0 则不给当前用户配免密 sudo(默认 1 = 配)
#
# 退出码
#   0 装好并已启动
#   2 前置不满足(非 x86-64-v3 / 无 /dev/kvm / 磁盘不足 / 缺 token)
# =============================================================================
set -euo pipefail

REPO="${DAEDALUS_REPO:-Daedalusys/daedalus-core}"
RUNNER_VER="${RUNNER_VER:-2.338.2}"
RUNNER_NAME="${RUNNER_NAME:-$(hostname -s)}"
RUNNER_LABELS="${RUNNER_LABELS:-daedalus-kvm}"
RUNNER_DIR="${RUNNER_DIR:-$HOME/actions-runner}"
GRANT_NOPASSWD="${GRANT_NOPASSWD:-1}"
TOKEN_FILE="${TOKEN_FILE:-/tmp/.daedalus-runner-token}"
# 需要装的包:podman/qemu 已可能就位;bootc/ostree 是 P2 的 bootc 换部署验证用的,
# 一次装齐省得后面再来一趟。
PACKAGES=(podman qemu-kvm qemu-img bootc ostree)

say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; }
bad()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; }

TOKEN=""
while [ $# -gt 0 ]; do
    case "$1" in
        --token-file) TOKEN_FILE="${2:-}"; shift 2 ;;
        --token)      TOKEN="${2:-}"; shift 2 ;;
        -h|--help)    sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done
if [ -z "$TOKEN" ] && [ -r "$TOKEN_FILE" ]; then
    TOKEN="$(cat "$TOKEN_FILE")"
    # token 是一次性的,读完即焚
    shred -u "$TOKEN_FILE" 2>/dev/null || rm -f "$TOKEN_FILE"
fi
[ -n "$TOKEN" ] || { bad "缺注册 token(用 --token-file 或 DAEDALUS_RUNNER_TOKEN 提供)"; exit 2; }

# --------------------------------------------------------------- 前置体检 ---
say "Phase 0 · 靶机体检"
[ "$(uname -m)" = x86_64 ] || { bad "架构 $(uname -m) 不是 x86_64"; exit 2; }
missing=""
for f in avx2 bmi1 bmi2 fma movbe xsave; do
    grep -qw "$f" /proc/cpuinfo || missing="$missing $f"
done
[ -z "$missing" ] || { bad "CPU 不支持 x86-64-v3,缺:$missing —— 跑不了 AlmaLinux 10 目标镜像"; exit 2; }
ok "CPU 支持 x86-64-v3($(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //'))"

[ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] \
    && ok "/dev/kvm 可读写" \
    || { bad "/dev/kvm 不可读写 —— KVM 引导跑不了(检查是否在 kvm 组)"; exit 2; }

avail_gb="$(df -BG --output=avail "$HOME" 2>/dev/null | tail -1 | tr -dc '0-9')"
[ "${avail_gb:-0}" -ge 40 ] || { bad "家目录可用空间 ${avail_gb:-?} GB < 40 GB(qcow2 + podman 解包不够)"; exit 2; }
ok "家目录可用 ${avail_gb} GB"

# --------------------------------------------------------------- sudo 凭据 ---
say "Phase 1 · sudo 与免密"
sudo -v || { bad "sudo 不可用(本脚本需要 sudo 装包与装 systemd 服务)"; exit 2; }
if sudo -n true 2>/dev/null; then
    ok "sudo 已免密"
else
    if [ "$GRANT_NOPASSWD" = "1" ]; then
        SUDOERS_FILE="/etc/sudoers.d/daedalus-runner"
        warn "当前用户 $(id -un) 的 sudo 需要密码;CD job 里的 sudo 是非交互的,必须免密。"
        warn "将写入 $SUDOERS_FILE:$(id -un) ALL=(ALL) NOPASSWD: ALL"
        warn "撤销:rm -f $SUDOERS_FILE"
        printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$(id -un)" | sudo tee "$SUDOERS_FILE" >/dev/null
        sudo chmod 440 "$SUDOERS_FILE"
        sudo -n true || { bad "免密 sudo 写入后仍不可用"; exit 2; }
        ok "已为 $(id -un) 配置免密 sudo"
    else
        warn "GRANT_NOPASSWD=0,未配置免密 sudo —— deploy-verify-kvm job 里的 sudo 会失败"
    fi
fi

# ------------------------------------------------------------------- 装包 ---
say "Phase 2 · 工具链"
if command -v dnf >/dev/null 2>&1; then
    sudo dnf install -y "${PACKAGES[@]}" || { bad "dnf install 失败"; exit 2; }
else
    bad "没有 dnf —— 本脚本按 EL 系(Fedora/RHEL/Alma)写;其他发行版请手工装:${PACKAGES[*]}"
    exit 2
fi
for b in podman qemu-system-x86_64 qemu-img; do
    command -v "$b" >/dev/null 2>&1 || { bad "装完仍缺 $b"; exit 2; }
done
ok "podman / qemu / qemu-img 就位"
command -v bootc >/dev/null 2>&1 && ok "bootc 就位(P2 的 bootc 换部署验证用)" \
    || warn "bootc 未就位(P2 才需要,不影响 P1)"

# ------------------------------------------------------------------ runner ---
say "Phase 3 · 安装 runner"
if [ -x "$RUNNER_DIR/config.sh" ]; then
    ok "复用已存在的 runner 安装目录 $RUNNER_DIR"
else
    mkdir -p "$RUNNER_DIR"
    tarball="$RUNNER_DIR/runner.tar.gz"
    url="https://github.com/actions/runner/releases/download/v${RUNNER_VER}/actions-runner-linux-x64-${RUNNER_VER}.tar.gz"
    info "下载 $url"
    curl -fsSL -o "$tarball" "$url" || { bad "下载 runner 失败"; exit 2; }
    tar -xzf "$tarball" -C "$RUNNER_DIR" || { bad "解压 runner 失败"; exit 2; }
    rm -f "$tarball"
    ok "runner v${RUNNER_VER} 已解压到 $RUNNER_DIR"
fi

cd "$RUNNER_DIR"
./config.sh \
    --url "https://github.com/$REPO" \
    --token "$TOKEN" \
    --name "$RUNNER_NAME" \
    --labels "$RUNNER_LABELS" \
    --unattended \
    --work _work \
    || { bad "runner 注册失败(标签重复?token 过期?)"; exit 2; }
ok "runner 已注册: $RUNNER_NAME(标签 $RUNNER_LABELS)"

say "Phase 4 · 装成 systemd 服务"
sudo ./svc.sh install "$(id -un)" || { bad "svc.sh install 失败"; exit 2; }
sudo ./svc.sh start   || { bad "svc.sh start 失败"; exit 2; }
sleep 3
sudo ./svc.sh status || warn "svc.sh status 非零(服务可能仍在启动中)"
ok "runner 服务已启动"

# ------------------------------------------------------------------- 收尾 ---
say "Phase 5 · 验证"
info "靶机侧自检(不依赖 GitHub):"
info "  sudo bash <core仓>/tests/real-machine/bootc-verify.sh --only-preflight"
info "  期望:CPU 支持 x86-64-v3 / /dev/kvm 可读写 / 退出码 0"
info "仓库侧确认 runner 在线:"
info "  gh api repos/$REPO/actions/runners --jq '.runners[] | \"\\(.name) \\(.status) \\(.labels|map(.name)|join(\",\"))\"'"

cat <<EOF

   靶机:   $(hostname -s)  ($(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //'))
   runner: $RUNNER_NAME  标签 $RUNNER_LABELS
   目录:   $RUNNER_DIR
   仓库:   $REPO

   下一步(在开发机上):bash scripts/cd-verify.sh p1
EOF
