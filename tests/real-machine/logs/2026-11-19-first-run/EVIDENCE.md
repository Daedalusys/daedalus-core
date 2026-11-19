# 第一次跑起来 —— v0.1.0 前置实跑(2026-11-19)

**被测产物**:`ghcr.io/daedalusys/daedalus-os:78ee0c39a2dbef736bcbbcbdefc661318f2d4c5b`
(= `daedalus-core` release `78ee0c39a2db`,即当时的 `main` HEAD `78ee0c3`)
**宿主**:`Masterwet_Server`(Fedora 44 / 20 核 / 15G / `/dev/kvm` 可写 / x86-64-v3)
**做法**:`tests/real-machine/lib/guest-driver.sh` 派生一次性镜像(随机口令,只在本机)
→ `bootc-image-builder` 出 qcow2 → QEMU(`-machine q35,accel=kvm -cpu host`,
`snapshot=on`,hostfwd 2223)→ SSH 进 guest 执行。破坏性操作只落临时 overlay。

**原始输出**(本目录):

| 文件 | 内容 |
|---|---|
| `first-run.log` | `probe-first-run.sh` 第一轮(T0–T5) |
| `dnf-rootcause.log` | dnf 在 bootc 上崩的根因(`import gi`) |
| `dnf-after-gobject.log` | 装上 `python3-gobject-base` 后 dnf 的真正答复(`--transient`) |
| `prove-gobject-fix.log` | 派生修复镜像复测:gi 可用,但包交易仍被只读部署拒绝 |
| `adapters.log` | 四个 tx 适配器逐个真跑 |
| `destructive-plugins.log` | `probe-destructive-plugins.sh`(blueprint / diskclean / organize 真写 + 真回滚) |
| `copilot-keyed.log` | `probe-copilot-keyed.sh`(真 provider:A11-1 / A11-2 / A11-4) |

## 结论:能不能发第一个版本

功能面成立,但**当时那份产物不能直接给人用**。实跑暴露出 3 个「CI 全绿也看不见」的问题,
两个直接决定「别人能不能用」:

| # | 问题 | 影响面 | 处置 |
|---|---|---|---|
| 1 | 发布镜像**无任何可登录账号**(root 口令字段 `*`),sshd 却开着 | 人下下来开起来只有 `login:` 提示符 | 已修:`build.yml` 出货档注入公开 alpha 试用账号 |
| 2 | 镜像缺 `python3-gobject` → dnf 在 bootc 上 `import gi` 崩,每次交易吐 traceback | 镜像里 dnf 整体不可用 | 已修:`66-bootc-dnf-gobject.sh` + 构建期 `import gi` 自证 |
| 3 | `package.set` 在 immutable(bootc)节点上**永远写不进去** | `package` kind 在 atomic 节点上是空操作 | 已修:Propose 期清晰 fail-closed(边界文案见下) |

另开 issue,不在 v0.1 修:
[#188](https://github.com/Daedalusys/daedalus-core/issues/188) `policy.set` 写 `/opt`(composefs 只读)、
[#189](https://github.com/Daedalusys/daedalus-core/issues/189) copilot 非 root 审计静默全丢、
[#190](https://github.com/Daedalusys/daedalus-core/issues/190) copilot 配置错误 rc=0、
[#191](https://github.com/Daedalusys/daedalus-core/issues/191) 发布的 ISO 是 CI 验证夹具。

## 能力矩阵(真 guest 实测,不是推演)

| 面 | 判定 | 证据 |
|---|---|---|
| 引导 + 原子部署 | ✅ | `bootc status --json` 有 `booted`;kernel `6.12.0-273.el10.x86_64` |
| 插件面 | ✅ | `daedalus-host list` = **14 个插件全 ok,degraded 0** |
| 镜像自带自检 | ✅ | `/usr/local/bin/daedalus-smoke` → `SMOKE OK`,rc=0 |
| 事务闭环(service.set) | ✅ | begin→propose→apply→`is-enabled=enabled`→rollback→`disabled` |
| 事务闭环(container.set) | ✅ | rootless `absent` 幂等,rc=0 |
| 破坏性插件回滚 | ✅ | blueprint 失败后回滚复原原 sha256;diskclean/organize 清单 diff 归位;宿主 lsblk 逐字节一致 |
| 审计链 | ✅ | `daedalus-audit verify` → `verified N entries; hash chain intact` |
| copilot 真 provider | ✅ | 翻译 `show os version` → `cat /etc/os-release`;watchdog rc=124 且无残留 ping;审计链 11 条 intact |
| **package.set** | ❌→已拒 | dnf 崩(问题 2);immutable 上现由 Propose 清晰拒绝(问题 3) |
| **policy.set** | ❌ | `/opt` 只读,[#188](https://github.com/Daedalusys/daedalus-core/issues/188) |
| copilot(无 key / 非 root) | ⚠️ | 缺 key 时 rc=0([#190](https://github.com/Daedalusys/daedalus-core/issues/190));非 root 审计静默丢([#189](https://github.com/Daedalusys/daedalus-core/issues/189)) |

## 问题 1 · 发布产物没有可登录账号(宿主侧对发布镜像取证)

```console
$ podman run --rm ghcr.io/daedalusys/daedalus-os:78ee0c39a2db... getent passwd | awk -F: '$3>=1000{print}'
（无）
$ podman run --rm ghcr.io/daedalusys/daedalus-os:78ee0c39a2db... grep '^root:' /etc/shadow
root:*::0:99999:7:::
$ podman run --rm ghcr.io/daedalusys/daedalus-os:78ee0c39a2db... systemctl is-enabled sshd
enabled
```

`sshd` 开着、口令一个都没有。不是「忘了删测试账号」:`78-dev-user.sh` 的凭据门控在 CI 里
没传值 → 走「公开构建模式」→ 镜像里根本没有账号。对 CI 无所谓(探针自己派生带账号的镜像),
对**下载产物的人**就是死路。

## 问题 2 · dnf 在 bootc 节点上崩(与 tx 适配器无关)

```console
$ sudo dnf -y install zsh
...
  File "/usr/lib/python3.12/site-packages/dnf/cli/cli.py", line 235, in do_transaction
    bootc_system = dnf.util._BootcSystem()
  File "/usr/lib/python3.12/site-packages/dnf/util.py", line 654, in __init__
    import gi
ModuleNotFoundError: No module named 'gi'
```

`dnf 4.20.0-28.el10.alma` 检测到 `/run/ostree-booted` → 走 bootc 分支 →
`dnf.util._BootcSystem.__init__` 里 `import gi` → 镜像里没装 `python3-gobject(-base)` →
**每次 dnf 交易都吐 Python traceback**。`dnf install` 直接跑也崩,所以不是 tx 适配器的锅。

装上 `python3-gobject-base` 后(`prove-gobject-fix.log`),dnf 给出它自己的话:

```
This bootc system is configured to be read-only. Pass --transient to perform this
transaction in a transient overlay which will reset when the system reboots.
Operation aborted.
```

即:即使修好 gi,**immutable 节点上的包安装也只有 `--transient` 一条路**(重启即丢)。
`--transient` 不能作为本产品的修法 —— atomic 节点的包变更语义是 bootc(新部署 +
`bootc rollback`),transient overlay 会让「变更已持久」这句话变成假的。

## 问题 3 · package.set 在 immutable 节点上写不进去

`daedalus-tx package.set zsh present` 的 apply:`步骤 0(package.set) returncode=1`,
`before_state.installed=false`,`after_state` 没落地,事务判 failed。

裁决:`package` kind 是 **agent 节点**(普通发行版)的能力;atomic 节点改包走
`bootc upgrade`/镜像重建。适配器现在在 Propose 期 fail-closed,逐字文案:

```
package.set 在 immutable(bootc)节点上不可用:atomic 节点的包变更走 bootc upgrade(新部署
+ bootc rollback);package kind 仅适用于 daedalus-agent 节点
```

## 问题 4 · policy.set 写不进去(已开 issue)

```console
$ touch /opt/daedalus/shared/.probe
touch: cannot touch '/opt/daedalus/shared/.probe': Read-only file system
$ mount | grep ' / '
composefs on / type overlay (ro,relatime,...)
```

`policy` kind 的真源路径 `/opt/daedalus/shared/policy.toml` 在 bootc 上是 composefs 只读,
「policy.set = kill switch」这个说法在 atomic 节点上不成立。

## copilot 真链路(有 key,`copilot-keyed.log`)

key 以**文件**送进 guest(`/root/.llmkey`,0600),不进任何命令行;provider / model / base_url
由 guest 侧 helper 导出。三条判定:

```
A11-1 PASS  [dry-run] Proposed command (not run): cat /etc/os-release      rc=0
A11-2 PASS  → ping -c 5 -i 2 127.0.0.1 / Timeout: command execution exceeded 1s   rc=124,残留 ping 0
A11-4 PASS  → cat /etc/os-release + verified 11 entries; hash chain intact
```

审计链上能看到完整三段:`copilot_translate` → `copilot_confirm` → `shell_exec`,
`prev_hash` 逐条相接。这就是 [#104](https://github.com/Daedalusys/daedalus-core/issues/104)
里长期 `BLOCKED: no key` 的那两条(A11-2 / A11-4)的真判定。

## 附:tx 各适配器在发布镜像上的实测矩阵(`adapters.log`)

| 适配器 | 作用域 | 实测 |
|---|---|---|
| `service.set` | user-scope | ✅ begin→propose→apply→rollback 全通 |
| `container.set` | rootless | ✅ `absent` 幂等 rc=0 |
| `package.set` | root | ❌ dnf 崩(问题 2);immutable 上现为清晰拒绝(问题 3) |
| `policy.set` | root | ❌ `/opt` 只读(问题 4) |
