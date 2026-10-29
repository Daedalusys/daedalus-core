# 2026-10-29 · bootc qcow2 真机引导证据

对应 `.plans/real-machine-verification.md` **Todo 9**。

## 环境

| 项 | 值 |
|---|---|
| 宿主 | `ThinkPad` — AlmaLinux 9.8，Intel i7-4800MQ（avx2/bmi1/bmi2/fma/movbe/xsave = x86-64-v3），8 核 |
| 虚拟化 | `/dev/kvm` 可读写，QEMU 10.1.0（`qemu-kvm-10.1.0-17.el9_8.5.alma.1`，二进制 `/usr/libexec/qemu-kvm`） |
| 引导参数 | `-machine q35,accel=kvm -cpu host -m 4096 -smp 2 -nographic -no-reboot` |
| 磁盘 | `/tmp/disk.qcow2`，1 297 612 800 B，`sha256 e5ee83fcbaeffa6de1040cea76b02166ba5ccf70b35d3b29f39de36246e959ad` |
| 来源 | CI run `39998868000` artifact `daedalus-os-qcow2-headless`（`daedalus-core@1304ae2`，2026-10-23） |

## 结论：**可引导（LOGIN）**

串口末尾：

```
AlmaLinux Kitten 10 (Lion Cub)
Kernel 6.12.0-273.el10.x86_64 on x86_64

localhost login:
```

- 全程 **0** 处 `Kernel panic` / `Call Trace` / `failed` / `error`（`grep -icE` 计数为 0）。
- 无 failed unit。

## 真机上实际起来的 Daedalus 单元（这是首次有证据）

```
[  OK  ] Started  daedalus-audit-rotate.timer      审计日志轮转定时器
[  OK  ] Started  daedalus-host-controller.service P4 reconcile skeleton
[  OK  ] Finished daedalus-audit.service           审计日志初始化(chattr +a 那条)
[  OK  ] Finished daedalus-env.service             环境与凭据初始化
```

另外 `sshd.service` 正常启动。

两条值得记的推论：

1. **`daedalus-host-controller.service` 的 `Wants=daedalus-host.service` 悬空不阻塞启动** —— 该 target 单元在整棵树里不存在，`Wants=`（弱依赖）只是被忽略，controller 照常 active。它只影响 `deploy-test/headless-test.sh` 的 B2 探针（「停掉 controller，host 仍 active」）永远拿不到 `daedalus-host.service`，只能 SKIP。
2. **8 个设备插件的 unit 在镜像里根本没有被 enable** —— 启动日志里只出现上面 4 个 Daedalus 单元，`daedalus-{avc,gpu,hwmon,integrity,journal,search,smart,triage}.service` 一个都没起，所以「有 manifest 无二进制」不会在开机时炸成 failed unit。它们只在被显式 `systemctl start` 时才会暴露 `ExecStart` 指向缺失文件。

## 宿主未被写入

`lsblk.diff` 为空（`lsblk-before.txt` 与 `lsblk-after.txt` 逐字节相同），`cmdline-before.txt` / `fstab-before.txt` 留档。全程未执行 `bootc install`，qcow2 仅以 `-drive` 挂给 guest。

## 文件

| 文件 | 内容 |
|---|---|
| `serial-console.log` | 完整串口输出（29 865 B），含 bootloader → systemd → login 提示符 |
| `bootc-verify.log` | 脚本自身日志（含 qcow2 sha256、qemu 命令行要素、判定过程） |
| `lsblk-before.txt` / `lsblk-after.txt` / `lsblk.diff` | 宿主盘基线对照（diff 为空） |
| `cmdline-before.txt` / `fstab-before.txt` | 宿主引导参数与挂载表基线 |

## 复现

```bash
scp daedalus-core/tests/real-machine/bootc-verify.sh ThinkPad:/tmp/
ssh -t ThinkPad 'sudo bash /tmp/bootc-verify.sh --qcow2 /tmp/disk.qcow2'
```
