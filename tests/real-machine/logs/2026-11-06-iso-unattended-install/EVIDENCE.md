# 2026-11-06 · ISO 无人值守装机闭环(rev-2:live 环境自装)

> 对应计划 `docs/superpowers/plans/2026-11-06-iso-unattended-install.md` 的 **rev-2 修订**,
> 以及 issue **core#84** 的 P2-2 收尾。
> 脚本:`daedalus-core/tests/real-machine/iso-verify.sh`(Phase 1/3 重写)、
> `tests/real-machine/iso-autoinstall.{sh,service}`、静态门禁 `tests/ci/iso-autoinstall-test.sh`。
> CI 门禁 `deploy-verify-iso`。**本文件是靶机实跑的真实产物**,不是预期值。

## 结论

**`判定: INSTALLED_LOGIN`,退出码 0** —— 两段判据都达成:

| 段 | 判据 | 实测结果 |
|---|---|---|
| P2-2a(必做) | ISO 引导出**可用的 live 系统**:SSH 登录成功 + 根是 live 介质 + 系统就是本镜像 | ✅ 全绿 |
| P2-2b(尽力) | live 环境**无人值守**把系统装到空虚拟盘 → 装完重启 → 引导该盘出现 `login:` + 目标盘有分区表 | ✅ 全绿 |

关键实测输出(原文):

```
   OK SSH 登录成功(live 自证的第 1 条)
   live 根: source=[LiveOS_rootfs] fstype=[overlay] /run/live=[/run/initramfs/live]
   OK 根确为 live 介质
   OK live 系统就是本镜像(os-release 逐字节一致)
   OK live 根里有 Daedalus 的安全策略(/opt/daedalus/shared/policy.toml)
   OK live 内核参数里带上了装机目标盘(iso.yaml 的 kernel_args 生效)
== Phase 3.2 · 等无人值守装机完成(live 里 bootc install to-disk) ==
   OK guest 已重启(QEMU 随之退出)—— 装机流程自行跑完
== Phase 3.3 · 引导装好的盘,等串口 login: ==
   GPT=True ENTRY_COUNT=128 NONEMPTY_PARTITIONS=3
   OK 目标盘有分区表(GPT + 非空分区条目)
   OK 装好的盘引导到 login:
   判定         : INSTALLED_LOGIN
```

- live 串口(`serial-live.log`)里安装器自己留下的两行:
  `daedalus-iso-autoinstall: 目标盘 /dev/vda,源镜像 registry:ghcr.io/daedalusys/daedalus-os:latest,开始无人值守安装`
  → `daedalus-iso-autoinstall: 安装完成,重启进装好的系统`
- 装好的盘串口(`serial-installed.log`)结尾:`Kernel 6.12.0-273.el10.x86_64 on x86_64` / `10 login:`
- 目标盘分区表(`target-partition-nbd.txt`,对同一块 qcow2 用 `qemu-nbd` 只读挂出的真实块设备视图):

```
Disklabel type: gpt
Device        Start      End  Sectors  Size Type
/dev/nbd0p1    2048     4095     2048    1M BIOS boot
/dev/nbd0p2    4096  1052671  1048576  512M EFI System
/dev/nbd0p3 1052672 41940991 40888320 19.5G Linux root (x86-64)
```

即 `bootc install` 的典型布局(BIOS boot + ESP + root),三个分区都是**装出来的**,
新建的空盘上是没有的。

## 复现命令

```bash
# 靶机(x86-64-v3 + KVM;共享机,故用 flock 串行化)
scp tests/real-machine/iso-verify.sh tests/real-machine/iso-autoinstall.sh \
    tests/real-machine/iso-autoinstall.service Masterwet_Server:/tmp/iso-r2/
ssh Masterwet_Server "flock -w 7200 /tmp/daedalus-target.lock bash -lc \
  'sudo VERIFY_WORK=/tmp/iso-verify-r2 bash /tmp/iso-r2/iso-verify.sh'"
# 退出码 0,判定 INSTALLED_LOGIN;要 P2-2b 也必须绿则加 VERIFY_REQUIRE_INSTALL=1
```

耗时(实测):Phase 1 派生 payload ~1.5 min,Phase 2 出 ISO ~1.5 min,
Phase 3 引导 live ~1 min + 装机(含 guest 内拉 2.2 GB 镜像)~6 min + 引导装好的盘 ~1 min。

## 为什么不走 kickstart(本轮最贵的教训)

原计划的机制是「用 blueprint 给 generic-iso 注入 kickstart」。**Task 1 探查实测推翻**:
`generic-iso` 不消费 blueprint(传了构建仍 exit=0,但 manifest 里零 kickstart 痕迹;
源码 `manifestForGenericISO` 根本不接 `bp`),而且它**不是安装器** ——
那张 ISO 只是 payload 容器 rootfs 的 live ISO(payload 里 `rpm -q anaconda` = not installed)。
`anaconda-iso` 被新 CLI 硬拒;唯一消费 kickstart 的 `bootc-installer` 要求 payload 自带 Anaconda
且当前 builder 缺 `python3-mako`。完整探针记录:
`docs/superpowers/plans/notes/2026-11-06-iso-blueprint.md`(gitignore,本地)。

## 实施期踩到的三个坑(都已在代码里钉住)

1. **payload 的 initramfs 没有 `dmsquash-live`**(`lsinitrd | grep -c` = 0)。
   没有它挂不起 `root=live:CDLABEL=...` —— 这才是旧证据里「GRUB 之后串口再无输出、
   挂空盘 20 分钟目标盘零写入」的真因(那张 ISO 连 live 根都起不来)。
   修法:派生层装 `dracut-live` 后 `dracut -f --no-hostonly --add "dmsquash-live livenet"`。
2. **容器里 `iso.yaml` 的字段名是 `kernel_args`**(上游 `isoYAML` 有显式 yaml tag),
   写成 `kernelargs` **不报错、被静默忽略**,内核参数一个都进不去。
   第一次实跑的症状:live `/proc/cmdline` 只有 `root=live:CDLABEL=... rd.live.image quiet rhgb enforcing=0`,
   装机单元因「没有 daedalus.install.disk」按设计 fail-closed 拒绝执行(退出码 1)。
   现在 Phase 3 会**运行时校验** `/proc/cmdline` 里有没有那个参数,不再等 40 分钟超时才发现。
3. **`bootc install to-disk --source-imgref` 必须带显式 transport 前缀**,裸 ref 直接被判:
   `error: Installing to disk: Creating source info from a given imageref: Invalid transport: ghcr.io/daedalusys/daedalus-os`。
   修法:配置存裸 ref,执行前补 `registry:` 前缀(`--target-imgref` 仍接受裸 ref)。

前两次实跑都是**失败记录**,保留在此以免后来者重走:attempt1 = 坑 2,attempt2 = 坑 3。

## 安全性质(与 Review Focus 对应)

- **目标永远是虚拟盘**:`qemu-img create` 新建 qcow2 → `-drive ...if=virtio`(guest 内 `/dev/vda`)。
  装机脚本**不做任何选盘推测**:目标盘只来自内核参数 `daedalus.install.disk=`,
  没给就 fail-closed 拒绝;并要求 `lsblk TYPE=disk`(整盘)、显式排除 live 介质自身所在设备。
  仓库里没有任何 `losetup` / `dd` 到块设备的写法(静态门禁规则 4 钉住)。
- **凭据不进仓库不进日志**:live 环境用**每次运行随机生成**的 SSH 密钥对登录
  (证据里只有公钥指纹 `SHA256:4pitBDzHpxVhnH+7E98uZu9cIs7aCNYFJiXy7uGSXZU`),
  `verify` 用户不设口令;证据目录里 `PRIVATE KEY` / `id_ed25519` / `rootpw` 命中数都是 0
  (`password` 的 4 处命中全是 `systemd-ask-password-*` 单元名)。
- **装机成功后不会再重装自己**:系统单元的触发条件含 `ConditionKernelCommandLine=rd.live.image`,
  装好的系统没有这个参数(见 `serial-installed.log`)。
- **不改发布镜像**:全部补在一次性派生 payload 层;派生层不推 registry。

## 尚未覆盖 / 已知边界(照实写)

- **CI 尚未端到端跑过这条腿**:本证据是手工在靶机跑出来的,不是自托管 runner 的产物。
  CI 侧要等一次出货档(main/tag/dispatch)触发 `deploy-verify-iso` 才能确认。
- P2-2b 在 CI 里是**尽力项**(判定停在 `LIVE_OK` 也算绿);要它必须绿,在 job 里设 `VERIFY_REQUIRE_INSTALL=1`。
- ISO 里**烧死了目标盘设备名**(`daedalus.install.disk=/dev/vda`,来自派生层的 `iso.yaml`)——
  这是「不做选盘推测」的代价:换设备名要重新出 ISO。装机脚本本身不依赖这个默认值。
- 装好的系统 **root 是锁定的**(发布镜像的 shadow 里 `root:*:`),本计划不设任何口令,
  故「装完能用」的判据停在**串口出现 `login:`**,不做登录后的交互验证。
- `anaconda-iso` / `bootc-installer` 两条 Anaconda 路线未验证(前者被 CLI 硬拒,
  后者需要 payload 自带 Anaconda + builder 补 `python3-mako`,属另一条独立的改造)。

## 文件清单

| 文件 | 是什么 |
|---|---|
| `iso-verify.log` | 脚本完整日志(611 行,含 Phase 1/2 全过程) |
| `serial-live.log` | 段一串口:live 引导 + 装机脚本两行自述 |
| `serial-installed.log` | 段二串口:装好的盘引导到 `login:` |
| `live-cmdline.txt` / `live-lsblk.txt` / `live-os-release.txt` | live 系统自证的原始事实(在 guest 内 SSH 采集) |
| `target-gpt.txt` | 目标盘 GPT 结构断言(`GPT=True ENTRY_COUNT=128 NONEMPTY_PARTITIONS=3`) |
| `target-fdisk.txt` | 盘首副本上的 `fdisk -l`(注:只拷了盘首 2 MiB,故 fdisk 报保护性 MBR 的 `dos`;权威结论以 `target-gpt.txt` 与 `target-partition-nbd.txt` 为准) |
| `target-partition-nbd.txt` | 对同一块 qcow2 只读 `qemu-nbd` 挂出后的 `fdisk -l`(真实 GPT 表) |
