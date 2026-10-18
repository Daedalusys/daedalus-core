#!/usr/bin/env bash
# T1 — 验证 headless 变体镜像:无 KDE / 有救砖包 / xrdp unit masked / VARIANT 标记
#
# 调用方式:
#   tests/build/headless-variant.sh [IMAGE_REF]
#   默认 IMAGE_REF=localhost/daedalus-os:headless
#
# 退出码:
#   0 — 所有硬约束通过(FAIL 行不出现);WARN 行允许
#   1 — 任一硬约束失败
#
# 设计要点:
# - 必须 FAIL 的:任何 KDE/sddm 残留包、xrdp 缺失、xrdp 在 multi-user.target.wants
#   仍 enabled、/etc/os-release 缺 VARIANT=headless(下游 daedalus-host rescue 依赖)
# - 仅 WARN 的:xorgxrdp(EPEL/COPR 偶有)、xorg-x11-drv-vmware(显式驱动未必装),
#   这两项 plan 明确允许降级;其余硬约束必须命中。
# - chroot 内 systemctl is-enabled 不可用 → 用 ls 检查 /etc/systemd/system/multi-user.target.wants/
#   和 /etc/systemd/system/xrdp.service 链接目标。
set -euo pipefail

IMAGE="${1:-localhost/daedalus-os:headless}"

echo "==> checking ${IMAGE} headless invariants"

# --- KDE / sddm 残留检测(必须不存在) ---
podman run --rm "${IMAGE}" rpm -q sddm              >/dev/null 2>&1 \
    && { echo "FAIL: sddm installed"; exit 1; } || true
podman run --rm "${IMAGE}" rpm -q plasma-workspace  >/dev/null 2>&1 \
    && { echo "FAIL: plasma installed"; exit 1; } || true
podman run --rm "${IMAGE}" rpm -q kwin-wayland      >/dev/null 2>&1 \
    && { echo "FAIL: kwin installed"; exit 1; } || true

# --- 救砖包必须存在(xrdp);其余 WARN ---
podman run --rm "${IMAGE}" rpm -q xrdp              >/dev/null 2>&1 \
    || { echo "FAIL: xrdp not installed (救砖缺失)"; exit 1; }
podman run --rm "${IMAGE}" rpm -q xorgxrdp          >/dev/null 2>&1 \
    || echo "WARN: xorgxrdp not installed (enable-vnc may degrade)"
podman run --rm "${IMAGE}" rpm -q xorg-x11-drv-vmware >/dev/null 2>&1 \
    || echo "WARN: xorg-x11-drv-vmware not installed (救砖无显式 vmware 驱动)"

# --- xrdp unit 默认必须 masked(不能在 multi-user.target.wants 里 enabled) ---
ENABLED="$(podman run --rm "${IMAGE}" ls /etc/systemd/system/multi-user.target.wants/ 2>/dev/null | grep -E '^xrdp' || true)"
if [ -n "${ENABLED}" ]; then
    echo "FAIL: xrdp enabled in multi-user.target.wants: ${ENABLED}"
    exit 1
fi
# mask 后 /etc/systemd/system/xrdp.service 是符号链接,目标应是 /dev/null
if podman run --rm "${IMAGE}" readlink -f /etc/systemd/system/xrdp.service 2>/dev/null | grep -q '^/dev/null$'; then
    echo "INFO: xrdp.service mask 到 /dev/null (预期)"
fi

# --- /etc/os-release 必须含 VARIANT=headless(下游 T2 依赖) ---
OSREL="$(podman run --rm "${IMAGE}" cat /etc/os-release 2>/dev/null || true)"
if ! printf '%s\n' "${OSREL}" | grep -qx 'VARIANT=headless'; then
    echo "FAIL: /etc/os-release 缺 VARIANT=headless (daedalus-host rescue 无法识别)"
    exit 1
fi

echo "==> headless invariants OK"