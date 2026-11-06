#!/usr/bin/env bash
# =============================================================================
# bootc-switch-verify.sh —— 真机验证 bootc 部署链: 引导 → switch → rollback
# =============================================================================
# 解决什么问题
#   「原子更新 + 一键回滚」是产品的核心主张,但 `bootc switch` / `bootc rollback`
#   此前**全仓零覆盖**、任何机器都没跑过一次。bootc-verify.sh 只验到「能引导到
#   login 提示符」——那只是第一跳;换部署与回滚是第二、三跳。
#
# 为什么需要一个专门的 verify 镜像
#   CI 构建的发布镜像**没有登录凭据**(78-dev-user.sh 是凭据门控的,CI 不传),
#   而 `bootc switch/rollback` 必须在 guest 内执行 → 必须能登进 guest。
#   本脚本因此在本地派生一个**一次性**镜像(复刻 78-dev-user.sh 的用户/组/sudoers/
#   tmpfiles),只用于验证:不推任何 registry、不进发布产物、密码随机且用完即弃。
#
# 为什么不需要 registry
#   `bootc switch --transport oci-archive <tar>` 可直接从 OCI 归档切换部署
#   (guest 内实际验证过该 transport 被支持)。切换目标镜像经 `podman save` 打包后
#   scp 进 guest,全程零凭据、零外部 registry。
#
# 用法(在 x86-64-v3 + KVM 宿主上,需 sudo)
#   sudo ./bootc-switch-verify.sh                 # 全流程
#   ./bootc-switch-verify.sh --only-preflight     # 只体检
#   sudo VERIFY_WORK=/tmp/x ./bootc-switch-verify.sh
#
# 派生镜像/qcow2/QEMU/SSH 的机制由共享驱动 tests/real-machine/lib/guest-driver.sh
# 提供(与 tests/real-machine/probe-*.sh 同一套);本脚本只保留 switch/rollback 断言。
#
# 环境变量
#   VERIFY_WORK          工作目录(默认:$SUDO_USER 家目录下 daedalus-switch-verify)
#   VERIFY_IMAGE         基准镜像(默认 ghcr.io/daedalusys/daedalus-os:latest)
#   VERIFY_MEM           引导内存 MiB(默认 4096)
#   VERIFY_BOOT_TIMEOUT  等待 SSH/重启的单步上限秒数(默认 240)
#
# 退出码
#   0  全链通过(引导 → switch 生效 → rollback 复原)
#   1  验证失败(某一步的期望不成立)
#   2  前置不满足
# =============================================================================
set -uo pipefail

# 端口与工作目录的历史默认值由本脚本钉住(探针套件默认 2223/daedalus-guest-driver,
# 两者互不干扰);派生镜像/qcow2/引导/SSH 全部委托共享驱动。
VERIFY_SSH_PORT="${VERIFY_SSH_PORT:-2222}"
export VERIFY_SSH_PORT
_inv="${SUDO_USER:-$USER}"
_inv_home="$(getent passwd "$_inv" 2>/dev/null | cut -d: -f6)"; [ -n "${_inv_home:-}" ] || _inv_home="$HOME"
VERIFY_WORK="${VERIFY_WORK:-$_inv_home/daedalus-switch-verify}"
export VERIFY_WORK
# 证据文件名是复核者的契约,保持不变(驱动库默认 guest-driver.log)。
export DRIVER_LOG="$VERIFY_WORK/bootc-switch-verify.log"
# shellcheck source=lib/guest-driver.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/guest-driver.sh"

ONLY_PREFLIGHT=0

while [ $# -gt 0 ]; do
    case "$1" in
        --only-preflight) ONLY_PREFLIGHT=1; shift ;;
        -h|--help) sed -n '2,34p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

WORK="$DRIVER_WORK"
LOG="$DRIVER_LOG"
trap driver_cleanup EXIT

# ------------------------------------------------------------------ 前置体检 --
say "Phase 0 · preflight"
[ "$(uname -m)" = x86_64 ] || { bad "只覆盖 x86_64"; exit 2; }
miss=""; for f in avx2 bmi1 bmi2 fma movbe xsave; do grep -qw "$f" /proc/cpuinfo || miss="$miss $f"; done
[ -z "$miss" ] || { bad "CPU 非 x86-64-v3,缺:$miss"; exit 2; }
ok "CPU 支持 x86-64-v3"
[ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] || { bad "/dev/kvm 不可读写"; exit 2; }
ok "/dev/kvm 可读写"
for b in qemu-system-x86_64 podman qemu-img ssh scp; do
    command -v "$b" >/dev/null 2>&1 || { bad "缺工具 $b"; exit 2; }
done
ok "qemu / podman / qemu-img / ssh / scp 就位"
info "工作目录: $WORK"
if [ "$ONLY_PREFLIGHT" = 1 ]; then say "只体检模式,停止"; exit 0; fi
[ "$(id -u)" -eq 0 ] || { bad "需要 root(派生镜像/出 qcow2 都要 rootful podman):请 sudo"; exit 2; }

# ------------------------------------------------------- Phase 1 · 派生镜像 ----
# 一次性 verify 镜像(复刻 78-dev-user.sh 的组/用户/sudoers/tmpfiles)与 qcow2
# 全部委托共享驱动:见 tests/real-machine/lib/guest-driver.sh。
driver_init
D1="$DRIVER_IMAGE_BASE"
VUSER="$DRIVER_USER"

say "Phase 1.1 · 派生切换目标镜像(带 marker)"
D2="$DRIVER_IMAGE_TARGET"
driver_derive "$D1" "$D2" "echo \"switch-target $D2\" > /usr/share/daedalus-verify-marker"
ok "切换目标镜像就绪: $D2(verify 用户: $VUSER,密码随机)"

# 切换目标的 OCI 归档(scp 进 guest 用)
say "Phase 2.1 · 打包切换目标为 OCI 归档"
ARCHIVE="$WORK/switch-target.tar"
driver_oci_archive "$D2" "$ARCHIVE"

# ---------------------------------------------------- Phase 3 · 引导 + SSH ----
SERIAL="$DRIVER_SERIAL"
ADV="$WORK/advice.log"
advise() { printf '%s\n' "$*" | tee -a "$ADV"; }

say "Phase 3 · 引导 qcow2(可写,允许重启)+ 等 SSH"
driver_boot
QCOW2="$DRIVER_QCOW2"

# --------------------------------------------------- Phase 4 · deployment A ---
say "Phase 4 · 基线 deployment(A)"
BOOT_ID_A="$(gssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null | tr -d '\r')"
STATUS_A="$(gssh 'sudo bootc status' 2>/dev/null)"
DIGEST_A="$(printf '%s' "$STATUS_A" | sed -n 's/.*imageDigest: *\(sha256:[0-9a-f]*\).*/\1/p' | head -1)"
MARKER_A="$(gssh 'test -f /usr/share/daedalus-verify-marker && echo PRESENT || echo ABSENT' 2>/dev/null | tr -d '\r')"
info "boot_id(A): $BOOT_ID_A"
info "digest(A) : $DIGEST_A"
info "guest 根分区: $(gssh 'df -h / | tail -1' 2>/dev/null | tr -s ' ')"
info "marker(A) : $MARKER_A   期望 ABSENT(基线镜像没有 marker)"
[ -n "$DIGEST_A" ] || { bad "拿不到 A 的 imageDigest"; printf '%s\n' "$STATUS_A" | head -20 | sed 's/^/      /'; exit 1; }
[ "$MARKER_A" = ABSENT ] || { bad "基线不该有 marker"; exit 1; }
ok "A 基线符合预期"

# ------------------------------------------------- Phase 5 · switch 到 B ------
say "Phase 5 · scp 切换目标 + bootc switch --apply"
gssh_scp "$ARCHIVE" "/home/${VUSER}/switch-target.tar" || { bad "scp 归档进 guest 失败"; exit 1; }
ok "归档已进 guest:/home/${VUSER}/switch-target.tar"

SW="$(gssh "sudo bootc switch --transport oci-archive /home/${VUSER}/switch-target.tar --apply" 2>&1)"
info "switch 输出: $(printf '%s' "$SW" | tr '\n' ' ' | head -c 200)"
ok "已发起 switch(--apply 会重启)"
BOOT_ID_B="$(driver_reboot_and_wait "$BOOT_ID_A" "$DRIVER_BOOT_TIMEOUT")" \
    || { bad "switch 后未观察到重启+SSH 回来;串口尾部:"; tail -20 "$SERIAL" | sed 's/^/      /'; exit 1; }
STATUS_B="$(gssh 'sudo bootc status' 2>/dev/null)"
DIGEST_B="$(printf '%s' "$STATUS_B" | sed -n 's/.*imageDigest: *\(sha256:[0-9a-f]*\).*/\1/p' | head -1)"
MARKER_B="$(gssh 'test -f /usr/share/daedalus-verify-marker && echo PRESENT || echo ABSENT' 2>/dev/null | tr -d '\r')"
info "boot_id(B): $BOOT_ID_B"
info "digest(B) : $DIGEST_B"
info "marker(B) : $MARKER_B   期望 PRESENT(切换目标带 marker)"
[ "$MARKER_B" = PRESENT ] || { bad "switch 后 marker 不在 —— 没有真的切到目标镜像"; exit 1; }
[ "$DIGEST_B" != "$DIGEST_A" ] || { bad "switch 前后 digest 相同 —— 没换成"; exit 1; }
ok "switch 生效: 部署已切到目标镜像,marker 出现"

# ---------------------------------------------- Phase 6 · rollback 回 A -------
say "Phase 6 · bootc rollback --apply"
gssh 'sudo bootc rollback --apply' >>"$LOG" 2>&1
ok "已发起 rollback(--apply 会重启)"
BOOT_ID_C="$(driver_reboot_and_wait "$BOOT_ID_B" "$DRIVER_BOOT_TIMEOUT")" \
    || { bad "rollback 后未观察到重启+SSH 回来;串口尾部:"; tail -20 "$SERIAL" | sed 's/^/      /'; exit 1; }
STATUS_C="$(gssh 'sudo bootc status' 2>/dev/null)"
DIGEST_C="$(printf '%s' "$STATUS_C" | sed -n 's/.*imageDigest: *\(sha256:[0-9a-f]*\).*/\1/p' | head -1)"
MARKER_C="$(gssh 'test -f /usr/share/daedalus-verify-marker && echo PRESENT || echo ABSENT' 2>/dev/null | tr -d '\r')"
info "boot_id(C): $BOOT_ID_C"
info "digest(C) : $DIGEST_C"
info "marker(C) : $MARKER_C   期望 ABSENT(已回滚到基线)"
[ "$MARKER_C" = ABSENT ] || { bad "rollback 后 marker 仍在 —— 没真的回滚"; exit 1; }
[ "$DIGEST_C" = "$DIGEST_A" ] || { bad "rollback 后的 digest 不等于基线(A=$DIGEST_A, C=$DIGEST_C)"; exit 1; }
ok "rollback 生效: 部署已回到基线"

# ------------------------------------------------------------------ Summary ---
say "Summary"
cat <<EOF
   判定          : SWITCH_ROLLBACK_OK
   基线 digest(A): $DIGEST_A
   切换 digest(B): $DIGEST_B   marker=$( [ "$MARKER_B" = PRESENT ] && echo 出现 )
   回滚 digest(C): $DIGEST_C   marker=$MARKER_C
   qcow2         : $QCOW2
   串口日志      : $SERIAL
   完整日志      : $LOG
   工作目录      : $WORK
EOF
advise "证据:serial-console.log(三次引导)、bootc-switch-verify.log;digest 三段见上。"
# 收尾:把工作目录(含 600 的 .verify-pass 与 root 建的日志)归还给调用者,
# 否则 CI 的 upload-artifact 以 runner 用户读会 Permission denied。
chown -R "$_owner" "$WORK" 2>/dev/null || true
exit 0
