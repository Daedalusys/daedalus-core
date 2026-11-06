# 2026-11-06 · ISO 交付形态:从「构建不出」到「可产出 + 可引导」

> 对应 issue **core#84**（ISO 构建失败）与 `.plans/real-machine-verification.md` 的
> **Todo 9** 的 ISO 半边（「ISO 产出 + 引导 | 零覆盖」）。
> 脚本:`daedalus-core/tests/real-machine/iso-verify.sh`,CI 门禁 `deploy-verify-iso`。

## 结论

**`判定: INSTALLER`** —— ISO 产出成功（1.2 GB）并在真 KVM 里引导到安装器菜单。

此前 `just iso`（legacy `bootc-image-builder --type iso`）**构建不出任何 ISO**
（osbuild 每个包下载都失败，上游已把该 image type 标为 legacy 不再支持）。
本次用官方推荐的新工具链把它彻底打通。

## 根因链（6 组对照实验，全部实跑）

| # | 尝试 | 结果 |
|---|---|---|
| ① | 原 daedalus 镜像 + legacy `--type iso` | ❌ 每个包都下载失败 |
| ② | ① + 补 `/etc/dnf/vars/releasever`（PR #83） | ❌ |
| ③ | ① + 恢复 kitten 仓库（osbuild manifest 里确实出现 `kitten-baseos/appstream`） | ❌ |
| ④ | legacy `--type anaconda-iso` | ❌ |
| ⑤ | **未经任何改动的上游稳定版 `almalinux-bootc:10`** | ❌ |
| ⑥ | **新 CLI `image-builder-cli` + `generic-iso`** | ✅ **通过全部包解析与下载** |

⑤ 排除了「我们的镜像配置 / 基座 / 镜像站」；⑥ 证明 **legacy 那条路本身坏了**，新路可用。

## 新路还差什么（逐步逼出来的，每一项都是实测缺口）

| 缺口 | 现象 | 处理 |
|---|---|---|
| ESP 载荷缺失 | `FileNotFoundError: '/boot/efi/EFI/almalinux/shimx64.efi'` | bootc 镜像构建期**剥空 `/boot`**（ESP 是安装时才创建的，实测 `rpm -V shim-x64` 全部 missing）→ 从 RPM 用 `rpm2cpio \| cpio` **还原** |
| shim 命名差异 | builder 找 `shimx64.efi`，AlmaLinux 提供的是 `shimx64-almalinux.efi` | 补一个 copy |
| `EFI/BOOT/*` 不在 vendor 目录 | 备用 GRUB(`fbx64.efi`)缺失 | 并入 `/boot/efi/EFI/almalinux/` |
| `mksquashfs` 缺失 | `FileNotFoundError: 'mksquashfs'` | 装 `squashfs-tools` |
| `implantisomd5` 缺失 | `FileNotFoundError: 'implantisomd5'` | 装 `isomd5sum` |
| `xorriso` / `isohybrid` | ISO 制作需要 | 装 `xorriso` / `syslinux` |

最终 ESP 布局（派生镜像内实测）：
`BOOTX64.CSV  BOOTX64.EFI  fbx64.efi  gcdx64.efi  grubx64.efi  mmx64.efi  shim.efi  shimx64-almalinux.efi  shimx64.efi`

## 关键设计决定：载荷补在**一次性派生镜像**里，发布产物一格不动

没有把 `grub2-efi-x64-cdboot` 等做进发布镜像，理由：

- `cdboot` 只对 ISO 有用，做进发布镜像等于让所有下游（容器 / qcow2 / registry 拉取）
  都多带一份用不上的东西；
- 构建期往 `/boot/efi/EFI/…` 放东西会与 bootc 自己的 `/boot` 处理逻辑打架；
- 连累已跑通的 `deploy-verify-kvm` / `deploy-verify-bootc-switch` 两条实机门禁重跑。

派生层内容（= (b) 方案的全部代价）：

```
releasever=10
+ cpio squashfs-tools xorriso isomd5sum syslinux grub2-efi-x64-cdboot
+ rpm2cpio|cpio 还原被剥离的 /boot/efi 文件
+ EFI/BOOT/* 并入 vendor 目录
+ shimx64-almalinux.efi → shimx64.efi
```

## 已知边界（照实写，未做成绿灯）

**`generic-iso` 的安装流程不是无人值守的。** 实测：挂 25G 空盘引导 ISO，等 20 分钟，
目标盘**仍为 196K、无分区表**，随后引导该盘 → `No bootable device`。
即安装器停在交互态，没有自动往盘上写。

所以本轮只判到「**ISO 是可引导的安装器**」这一层。要验证完整的
「安装 → 重启 → `login:`」，需要给 ISO 配 kickstart / blueprint
（上游 `anaconda-iso` 才是「unattended, installs to the first disk found」的那种），
属产品级配置，不在本轮范围。

## 证据

- `serial-console.log` —— ISO 引导的串口输出（SeaBIOS → iPXE → GRUB 2.12 → 安装器菜单）。
- `iso-verify.log` —— 脚本完整日志（派生层输出 + builder 输出 + 引导判定）。
