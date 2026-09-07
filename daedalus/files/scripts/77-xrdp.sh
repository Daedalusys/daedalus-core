#!/usr/bin/env bash

set -xeuo pipefail

# Daedalus 扩展:为 KVM guest 装 xrdp,便于本地 RDP 客户端(Remmina / mstsc /
# xfreerdp)直连 3389,不需要桥接 VNC。
#
# 实现细节:
# - xrdp 默认以 nobody 读 /etc/ssl/private 证书;无权限会反复 cert 错误。
#   把 nobody 加到 ssl-cert 组解决。
# - firewall-offline-cmd 在容器构建期(无 firewalld daemon)直接写规则,下次
#   daemon 启动时自动加载。等价于运行时 `firewall-cmd --permanent`。
# - systemctl enable xrdp 让首次启动就开 RDP 端口。
#
# Slot 77:在 76-daedalus-plugin-gen.sh 之后、90-signing 之前。20-desktop.sh
# 已把 DE 装好(sddm / gdm / cosmic-greeter),xrdp 仅在此之上提供 RDP 接入。
dnf install -y xrdp
firewall-offline-cmd --add-port=3389/tcp
usermod -aG ssl-cert nobody
systemctl enable xrdp
