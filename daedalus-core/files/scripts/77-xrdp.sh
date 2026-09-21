#!/usr/bin/env bash

set -xeuo pipefail

# Daedalus 扩展:为 headless KVM guest 装真正的 RDP server —— xrdp(B1 方案)。
#
# 为什么 xrdp 而不是 gnome-remote-desktop:
#   VM 用 --graphics none 建(无虚拟显卡)。gnome-remote-desktop 是"共享已登录
#   图形会话"的用户服务,headless 没显示/没登录会话就起不来 → 之前 print-rdp
#   显示 3389 连不上就是这因。xrdp 自己经 xorgxrdp 造 Xorg 会话(sesman 拉
#   :10+),不依赖预存显示,才是无头 VM 的 RDP 正解。
#
# 为什么走 COPR:
#   EPEL10 当前未收录 xrdp(EPEL9 有)。先试 EPEL,dnf 装不上就启用 COPR
#   mlampe/xrdp(明确有 RHEL+epel 10 x86_64 构建)。copr repo 走 64-dnf-mirrors
#   写的 dnf.conf proxy 出网。都失败再回退 gnome-remote-desktop(至少不阻断 build)。
#
# firewall:bootc 构建期无 firewalld daemon,用 firewall-offline-cmd 写规则;
#   探不到该命令(拆 8 stage 后 PATH 可能不含)就 WARN 跳过,装机后 VM 内手动开。

# dnf proxy:兜底再写一次(64-dnf-mirrors 已写,幂等保证)
if [ -n "${HTTP_PROXY:-}" ]; then
    grep -q '^proxy=' /etc/dnf/dnf.conf 2>/dev/null || echo "proxy=${HTTP_PROXY}" >> /etc/dnf/dnf.conf
fi

# 1) 开 EPEL
dnf install -y epel-release \
    || dnf install -y "https://dl.fedoraproject.org/pub/epel/epel-release-latest-$(rpm -E %rhel).noarch.rpm"
dnf makecache >/dev/null 2>&1 || true

# 2) 装 xrdp:EPEL 有直接装;no match 就【手写 COPR repo 文件】重试。
#    不用 `dnf copr enable` —— 它自动探测 chroot 会挑错 `epel-10-x86_64`,而该
#    项目实际是 `rhel+epel-10-x86_64`(RHEL/AlmaLinux 走 rhel+epel 命名),且
#    set -e 下 enable 失败直接崩整个 build。手写 repo 用正确 chroot,全失败优雅
#    回退 gnome-remote-desktop,再不行置 none 只告警不阻断。
# 只需 xrdp(+ 可选 xrdp-selinux 策略)。xrdp 0.10.6 的依赖是 tigervnc-server-minimal
# (Xvnc 后端)+ xorg-x11-xinit,【不依赖 xorgxrdp】——而 mlampe/xrdp copr 里也【没有
# xorgxrdp】。之前把 xorgxrdp 写进必需包,导致 `dnf install xrdp xorgxrdp` 因缺
# xorgxrdp 整条 && 失败 → 回退 gnome-remote-desktop → headless 无会话 → 3389 不监听。
# 去掉 xorgxrdp 后,xrdp 自带 Xvnc 后端起会话,headless 也能用。
install_xrdp() { dnf install -y xrdp && { dnf install -y xrdp-selinux || true; }; }
RDP_TYPE="xrdp"
if ! install_xrdp >/dev/null 2>&1; then
    echo "==> EPEL 无 xrdp(EL10 未收录),手写 COPR mlampe/xrdp repo(chroot rhel+epel-\$releasever)"
    # baseurl 用 $releasever(AlmaLinux=10),quoted heredoc 原样写入交给 dnf 展开;
    # 避开 `rpm -E %rhel` 宏缺失时把 "%rhel" 字面拼进 URL 的脏值坑。
    cat > /etc/yum.repos.d/_copr_mlampe-xrdp.repo <<'REPO'
[mlampe-xrdp]
name=Copr repo for xrdp owned by mlampe
baseurl=https://download.copr.fedorainfracloud.org/results/mlampe/xrdp/rhel+epel-$releasever-x86_64/
type=rpm-md
skip_if_unavailable=True
gpgcheck=1
gpgkey=https://download.copr.fedorainfracloud.org/results/mlampe/xrdp/pubkey.gpg
repo_gpgcheck=0
enabled=1
enabled_metadata=1
REPO
    dnf makecache >/dev/null 2>&1 || true
    if ! install_xrdp >/dev/null 2>&1; then
        echo "==> xrdp(EPEL + COPR)皆失败,回退 gnome-remote-desktop"
        if dnf install -y gnome-remote-desktop >/dev/null 2>&1; then
            RDP_TYPE="gnome-remote-desktop"
        else
            echo "!!! xrdp 与 gnome-remote-desktop 都装不上;跳过 RDP(不阻断 build)"
            RDP_TYPE="none"
        fi
    fi
fi
echo "77-xrdp.sh: RDP_TYPE=${RDP_TYPE}"

# firewall-offline-cmd 探到才写规则,否则 WARN 跳过(装机后手动)
open_firewall_port() {
    local port_spec="$1"
    if command -v firewall-offline-cmd >/dev/null 2>&1; then
        firewall-offline-cmd --add-port="${port_spec}"
        echo "77-xrdp.sh: firewall 端口 ${port_spec} 已开"
    else
        echo "77-xrdp.sh: WARN firewall-offline-cmd 不在 PATH;装机后 VM 内手动:"
        echo "  firewall-cmd --add-port=${port_spec} --permanent && firewall-cmd --reload"
    fi
}

case "${RDP_TYPE}" in
    xrdp)
        # 3a) 会话启动器:让 sesman 起的 Xorg(:10)里跑 KDE Plasma。默认 startwm.sh
        #     会落到 twm,对 KDE 镜像无意义 → 覆写为 startplasma-x11。
        cat > /etc/xrdp/startwm.sh <<'STARTWM'
#!/bin/sh
# Daedalus:KDE Plasma on xorgxrdp(dbus-launch 包一层拿 session bus)
[ -r /etc/profile.d/lang.sh ] && . /etc/profile.d/lang.sh
exec dbus-launch --exit-with-session startplasma-x11
STARTWM
        chmod 0755 /etc/xrdp/startwm.sh

        # 3b) xrdp 服务器证书(部分版本 post-install 不自动生),生成幂等
        [ -x /usr/libexec/xrdp/genkeyrsa.sh ] && /usr/libexec/xrdp/genkeyrsa.sh >/dev/null 2>&1 || true

        # 3c) 远程访问主线是 KVM 内置 VNC;xrdp 软件保留但不开机自启(sesman/Xvnc
        #     链在 headless VM 上不可用)。需要 RDP 时一键恢复:
        #     systemctl enable --now xrdp xrdp-sesman
        systemctl disable xrdp xrdp-sesman 2>/dev/null || systemctl disable xrdp
        open_firewall_port "3389/tcp"
        echo "77-xrdp.sh: xrdp 就绪。VM 内需有本地用户(xrdp 经 PAM 认证该账号);"
        echo "  装机首次进桌面建用户:virsh console 或临时加 --graphics vnc 登录后 useradd"

        # 3d) 强制 Xvnc depth=24:TigerVNC 1.15 的 Xvnc 不支持 depth 32(RDP 客户端
        #     默认 32bpp 会被拼进 Xvnc 命令行 → Xvnc 启动即退出 → waitforx 超时 →
        #     "X server failed to start" → RDP 黑屏/秒断)。sesman.ini [Xvnc] 段里
        #     显式加 -depth 24 覆盖动态协商的 32bpp。
        if grep -q '^\[Xvnc\]' /etc/xrdp/sesman.ini && ! grep -qE '^\[Xvnc\].*\n.*-depth' /etc/xrdp/sesman.ini; then
            sed -i '/^\[Xvnc\]/a param=-depth\nparam=24' /etc/xrdp/sesman.ini
        fi
        ;;
    gnome-remote-desktop)
        open_firewall_port "3389/tcp"
        echo "77-xrdp.sh: 回退 gnome-remote-desktop(headless 需有图形会话才起):"
        echo "  登录后 systemctl --user enable --now gnome-remote-desktop.service"
        ;;
    none)
        echo "77-xrdp.sh: 未装任何 RDP server(EPEL/COPR 均不可达?),跳过;装机后可手动 dnf copr 或换网重试"
        ;;
esac
