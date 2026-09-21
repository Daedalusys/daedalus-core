#!/usr/bin/env bash

set -xeuo pipefail

# Daedalus 开发镜像:确保图形登录链就绪(显示管理器 + graphical default target)。
#
# 为什么需要这个脚本:bootc KDE 变体的 default target 常为 multi-user(无图形),
# 不切 graphical.target 的话 KVM VNC 只能看到文本 TTY。本机开发路线锁定 KVM 内置
# VNC(经 SSH 隧道访问),需要 VNC 连入即见图形登录框;按 owner 决定保留手动登录
# (不设 autologin),daedalus 账号(见 78-dev-user.sh)输密码进 KDE。
#
# 显示管理器策略(踩坑记录):本脚本最初无条件 `systemctl enable sddm`,但
# almalinux-bootc KDE 变体的 display-manager.service 已链到 plasmalogin.service
# (变体自带的登录管理器),systemd 拒绝覆盖已存在的 display-manager 链接
# (enable 直接 exit 1,build 挂掉)。实际上 plasmalogin 与 sddm 功能等同
# (出登录框、拉起 Plasma 会话),本脚本的真实意图是"VNC 连入即见图形登录框",
# 不是"必须用 sddm"。因此:display-manager.service 已存在 → 用现成的
# (plasmalogin);不存在 → 链到 sddm(包已装)。
#
# Slot 79:在 78-dev-user.sh(建 daedalus 账号)之后、90-signing 之前;
# 由 Containerfile 对应 stage `build.sh 78 79` 白名单执行。
# 构建容器内无运行 systemd,enable/set-default 仅操作 /etc 下的符号链接,
# chroot 环境安全。

# 显示管理器:已有 display-manager.service(无论指向谁)就用现成的;没有才链 sddm
if [ -e /etc/systemd/system/display-manager.service ] \
    || [ -e /usr/lib/systemd/system/display-manager.service ]; then
    echo "79-sddm-autostart.sh: display-manager.service 已存在,沿用现成登录管理器(不覆盖)"
else
    ln -sf /usr/lib/systemd/system/sddm.service /etc/systemd/system/display-manager.service
    echo "79-sddm-autostart.sh: display-manager.service → sddm"
fi

# 切图形 default target(VNC 连入见登录框的前提;幂等)
systemctl set-default graphical.target

# 虚拟显卡无 3D 透传(virtio-gpu -virgl)→ kwin_wayland 走 llvmpipe 软渲染,直跑
# DRM 后端会连环崩(VM 内逐轮复现 + /proc/<pid>/environ 实证,修复演化四版):
#   1) 默认 GL 合成      → EGLNativeFence 首帧 SEGV(core-dump 循环,greeter 黑屏)
#   2) KWIN_COMPOSE=Q    → 不再 SEGV,但 QPainter buffer 过不了 KMS 原子提交
#                          ("Atomic modeset commit failed! Invalid argument")
#   3) +KWIN_DRM_NO_AMS=1 → legacy 模式起,但 llvmpipe dumb buffer 连 gamma 都
#                          不被 virtio-gpu KMS 支持("Setting gamma failed")
#   4) 最终组合(存活 72s+,greeter 正常渲染)→ 本脚本固化如下。
# drop-in 落 /etc(bootc merge 持久),同时覆盖登录 greeter
# (plasma-login-kwin_wayland)与用户会话(plasma-kwin_wayland)两处单元。
# ⚠ 改 drop-in 后必须让 plasmalogin 用户的 systemd user manager 完全重建才生效
#   (restart display-manager 不够;terminate-user 或重启 VM)。
KWIN_SOFTGL_CONF='[Service]
Environment=KWIN_COMPOSE=Q
Environment=KWIN_DRM_DEVICES=/dev/dri/card0
Environment=KWIN_DRM_NO_AMS=1
Environment=LIBGL_ALWAYS_SOFTWARE=1'
mkdir -p /etc/systemd/user/plasma-login-kwin_wayland.service.d \
         /etc/systemd/user/plasma-kwin_wayland.service.d
printf '%s\n' "$KWIN_SOFTGL_CONF" \
    > /etc/systemd/user/plasma-login-kwin_wayland.service.d/10-softgl.conf
printf '%s\n' "$KWIN_SOFTGL_CONF" \
    > /etc/systemd/user/plasma-kwin_wayland.service.d/10-softgl.conf

echo "79-sddm-autostart.sh: 图形登录链就绪(default target=graphical)+ kwin 软渲染四开关已钉"
