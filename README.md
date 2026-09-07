# Diva-OS

AI-native 桌面操作系统：在 AlmaLinux 10 bootc（KDE 变体）的不可变、原子、可回滚底座上，
叠加一层 Model Context Protocol（MCP）能力中间件，含三道安全边界（模型/能力、执行/沙箱、
证据/审计）、哈希链审计、systemd credential 隔离与原子回滚。能力服务器与插件宿主是 Go
静态二进制，安全策略集中在单一 `policy.toml`。

构建/测试/发布统一用 `just` 编排（见 `justfile`）。

## ⚠️ 开发默认登录账号（本地开发镜像专用）

bootc 基础镜像出厂**没有任何普通用户、root 锁定**（`almalinux-bootc` 的不可变约定），
且本项目 KVM 用的是 `bootc-image-builder` 直烧 qcow2，不走 Anaconda 建用户向导。为了本机
dev（KVM 控制台 / RDP / SSH）能直接登录，构建脚本 `daedalus/files/scripts/78-dev-user.sh`
会在镜像里**注入一个 dev 账号**（凭据经环境变量在构建时注入，仓库不含明文）：

| 项 | 值 |
|----|----|
| 用户名 | 由 `DAEDALUS_DEV_USER` 环境变量指定（默认建议 `daedalus`） |
| 密码 | 由 `DAEDALUS_DEV_PASS` 环境变量指定（构建时注入，不进仓库） |
| 权限 | `wheel` 组成员 + `/etc/sudoers.d/daedalus-dev` 免密 `NOPASSWD:ALL` sudo |
| root | 密码与 `DAEDALUS_DEV_PASS` 同值（便于 `virsh console` 救援，root 已解锁，构建时设置） |

登录方式：
- **KVM 控制台 / VNC**：`daedalus` / `<DAEDALUS_DEV_PASS>`
- **RDP（xrdp，3389）**：用 `daedalus` 账号（xrdp 拒 root 直登；VM 在 libvirt NAT，dev 机
  经 SSH 隧道连：`ssh -N -L 3390:<vm-ip>:3389 <构建机>` → `xfreerdp /v:localhost:3390`）
- **SSH**：`ssh daedalus@<vm-ip>`（VM 在宿主 `192.168.122.x`，从构建机直连或 dev 机隧道）

> 🔒 **仅供本地开发**。凭据仅经 `DAEDALUS_DEV_USER` / `DAEDALUS_DEV_PASS` 环境变量在构建时
> 注入，仓库与镜像均不含明文密码；未设置环境变量时构建不注入任何账号。**对外分发 / 上生产 /
> 推公开 ghcr 镜像前**请确认未在构建环境中遗留真实凭据。

## 快速上手

```bash
just --list              # 所有编排 recipe
just build               # 同步 vendor 树 + podman build 出 localhost/daedalus-os:latest
just test                # Go 单测 + Deno 测试
# 本机构建 → qcow2 → 装 KVM → RDP 一条龙(私有,见 justfile.local):
just -f justfile.local remote-all
```

更多约定与结构见 `AGENTS.md`。
