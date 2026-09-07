#!/usr/bin/env bash

set -xeuo pipefail

# Daedalus 开发镜像:注入一个默认 sudo 用户 + 设 root 密码 + 开 sshd。
#
# 为什么需要 bootc 基础镜像(almalinux-bootc)默认【无任何普通用户、root 锁定】
# ——这是不可变镜像的出厂约定,导致 KVM 首启无账号可登(bootc-image-builder 直烧
# qcow2 也不走 Anaconda 建用户向导)。为方便本机 dev(KVM/RDP/SSH)登录,这里直接
# 建一个固定账号。
#
# ⚠ 安全提示:密码已经过环境变量门控,仓库本身不含任何凭据(README 也不列具体值)。
#   凭据仅在构建时经 DAEDALUS_DEV_USER/DAEDALUS_DEV_PASS 注入;未提供则跳过用户注入,
#   产出公开安全的镜像(无内置账号)。对外分发无需再手动删除本脚本。
#
# 落点:bootc 把镜像的 /etc(passwd/shadow/group/sudoers.d)merge 进部署,账号与
#   sudo 持久生效;home 目录随镜像 /var seed。SELinux 若拦,首启 autorelabel 兜底。
# Slot 78:在 77-xrdp.sh 之后(xrdp 组此时已由 xrdp 包创建,可安全加组),
#   90-signing 之前。

# 凭据门控:密码不硬编码进仓库(公开安全)。构建时经环境变量注入:
#   DAEDALUS_DEV_USER=xxx DAEDALUS_DEV_PASS=yyy podman build ...
# 任一未设置 → 跳过用户注入(不阻断 build;本地开发者在 .env 里配好后
# 经 just build 的 --env 透传,或 Containerfile ARG 传入)。
DEV_USER="${DAEDALUS_DEV_USER:-}"
DEV_PASS="${DAEDALUS_DEV_PASS:-}"
if [ -z "${DEV_USER}" ] || [ -z "${DEV_PASS}" ]; then
    echo "78-dev-user.sh: DAEDALUS_DEV_USER/DAEDALUS_DEV_PASS 未设置,跳过开发用户注入(公开构建模式)"
    exit 0
fi

# sudo 组:RHEL/AlmaLinux 用 wheel;video/render 组用于虚拟显卡 DRM 访问——headless KVM
# 已带 virtio 显卡,guest 内出现 /dev/dri/card0(root:video)与 renderD128(root:render),
# 而 SSH/RDP 登录拿不到 logind seat ACL,组成员是该用户打开 DRM 节点的唯一途径;
# xrdp 组仅在 77 真装了 xrdp 时存在
extra_groups="wheel,video,render"
getent group xrdp >/dev/null 2>&1 && extra_groups="wheel,video,render,xrdp"

# EL10 bootc 基础镜像的 video/render 组处于【病态半初始化】(三轮容器内复刻实证):
#   /etc/group:无条目;  /etc/gshadow:有条目(video:::, render:!*::);
#   getent:幻影应答(nsswitch 的 systemd 模块);  groupadd -f:rc 0 但双文件零写入
#   (shadow-utils 经 NSS UserDB 认为"已存在"而躺平)。
# 工具层(groupadd 全形态/getent 判断)在该镜像上全部不可信,唯一稳态解:
# 直接写 /etc/group(条件化 sed,幂等),GID 钉标准值 video=39 / render=105
# (与 gshadow 既有条目及 udevd 运行时属主一致)。构建期 /etc 可写,bootc merge 持久。
grep -qE '^video:'  /etc/group || echo 'video:x:39:'   >> /etc/group
grep -qE '^render:' /etc/group || echo 'render:x:105:' >> /etc/group

if ! id "${DEV_USER}" >/dev/null 2>&1; then
    useradd -m -G "${extra_groups}" -s /bin/bash "${DEV_USER}"
fi

# bootc 运行时【不从镜像 seed /var】(只带 /usr + 合并 /etc)——构建期直接
# mkdir /var/home/... 会在开机被丢弃,登录仍 "Could not chdir /var/home/daedalus"。
# 改用 systemd-tmpfiles drop-in(落在 /etc,随部署持久):开机 systemd-tmpfiles-setup
# 会在 getty/登录之前把 /var/home 与用户 home 建出来并设好属主/权限。KDE/xrdp
# 会话需要可写 home,缺了连上会黑屏。
cat > /etc/tmpfiles.d/daedalus-dev-user.conf <<TMPF
d /var/home              0755 root       root       -
d /var/home/${DEV_USER}  0700 ${DEV_USER} ${DEV_USER} -
TMPF

# 设密码 + 免密 sudo:临时关 xtrace 避免密码进 build log
set +x
echo "${DEV_USER}:${DEV_PASS}" | chpasswd
# root 也设密码(便于 virsh console 救援);bootc 默认 root 是 !! 锁定,设密码同时解锁
echo "root:${DEV_PASS}" | chpasswd
passwd -u root >/dev/null 2>&1 || true
set -x

printf '%s\n' "${DEV_USER} ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/daedalus-dev
chmod 0440 /etc/sudoers.d/daedalus-dev
visudo -cf /etc/sudoers.d/daedalus-dev

# 开 SSH(VM 在 libvirt NAT,宿主经 192.168.122.x 可达;dev 机走 ssh -L 隧道)
command -v sshd >/dev/null 2>&1 || [ -x /usr/sbin/sshd ] || dnf install -y openssh-server
systemctl enable sshd >/dev/null 2>&1 || systemctl enable sshd.service

echo "78-dev-user.sh: 已建用户 ${DEV_USER}(sudo NOPASSWD)+ 设 root 密码 + 启用 sshd(凭据经环境变量注入,仓库不含明文)"
