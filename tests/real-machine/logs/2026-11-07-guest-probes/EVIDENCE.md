# guest 实机探针套件 —— 真机运行证据(2026-11-07)

**被测镜像**:`ghcr.io/daedalusys/daedalus-os:latest`
**宿主**:`Masterwet_Server`(i7-12700H / x86-64-v3 / `/dev/kvm` 可写 / Fedora 44)
**做法**:`lib/guest-driver.sh` 派生**一次性** verify 镜像(复刻 `78-dev-user.sh` 的
组/用户/sudoers/tmpfiles,口令随机、不推 registry、不进发布产物)→ `bootc-image-builder`
出 qcow2 → QEMU(`-machine q35,accel=kvm -cpu host`,hostfwd **2223**)→ SSH 进 guest 执行。
guest 以 `snapshot=on` 引导:破坏性操作只落临时 overlay,QEMU 退出即丢。

**原始输出**:`guest-probes-run.log`(八条探针一次连续跑完的完整 stdout,含每条命令的
返回与日志行;无口令)。下面每个数字都能在该文件里逐条对上。

## 判定总表

| 探针 | 面 | 结果 | 说明 |
|---|---|---|---|
| `probe-systemd-sandbox.sh` | A3 | **PASS(5/5)** | 五条反证全部观测到内核/系统的拒绝 |
| `probe-audit-inode.sh` | A4 | **3 PASS / 1 FAIL** | 追加写保护成立;轮转被 `core#92` 挡住 |
| `probe-destructive-plugins.sh` | A5 | **PASS(4/4)** | blueprint 回滚复原 sha256;disk.clean / organize.move 三段生命周期成立 |
| `probe-fs-write-guard.sh` | A6 | **REPRO_DONE** | 缺陷复现成立 → `core#91`(本轮为复现任务恒退 0;现已升级为判据任务,修复后结果见 `../2026-11-08-a6-fs-write-guard/`) |
| `probe-readonly-plugins.sh` | A7 | **PASS(6/6)** | 六条真数据源差异如实记录;dupe 硬链接误报 → `plugins#76` |
| `probe-plugin-inventory.sh` | A12 | **PASS(2) + NOTE** | root 口径 12 插件全 ok;非 root 口径 copilot degraded → `core#89` |
| `probe-copilot.sh` | copilot | **1 PASS / 1 FAIL / 2 BLOCKED** | Deno 边界成立;CLI 被 `core#89` 挡住;watchdog 与真 provider 缺 LLM key |
| `probe-bootc-install-upgrade.sh` | A2 残余 | **PROBE_WIP + NOTE + 1 PASS** | install 的 `--source-imgref` 形态未对齐;upgrade 无 registry 可查;宿主盘未被碰 |

## A3 · systemd 沙箱五条反证 —— 全部成立

```
PASS  A3-1 写 /opt/daedalus/shared/policy.toml 被拒(ProtectSystem=strict):
      /opt/daedalus/shared/policy.toml: Read-only file system
PASS  A3-2 DynamicUser:UID=61865 在 dynamic 区间且 /etc/passwd 文件无此条目(两次 61865 vs 61784)
PASS  A3-3 $CREDENTIALS_DIRECTORY 含 daedalus_token_default
PASS  A3-4 回环可通(lo-rc=0)而外部 TCP 被拒(net-rc=124)
PASS  A3-5 seccomp 已装(Seccomp: 2);特权调用被杀 mount-rc=159 hostname-rc=159(SIGSYS=159)
```

要点:沙箱内 `/proc/self/status` 的 `Seccomp: 2` + `Seccomp_filters: 22`;**`mount` 与
`hostname` 都被 SIGSYS(159)杀死** —— 这是 `SystemCallFilter=~@privileged` 真的生效,
不是服务日志。`getent passwd 61865` 能查到条目(那是 nss-systemd 给 DynamicUser 合成的),
所以判据改用「UID ∈ 61184..65519」+「`/etc/passwd` **文件**里没有该行」,两次启动 UID
不同(61865 vs 61784)。

## A4 · audit 真 inode —— 3/4(轮转被产品缺陷挡住)

```
PASS  A4-1 lsattr 含 a: -----a---------------- /var/log/daedalus/audit.jsonl
PASS  A4-2 truncate -s 0 被拒(rc=1, Operation not permitted)
PASS  A4-3 verify: verified 6 entries; hash chain intact
FAIL  A4-4 轮转在清 append-only 属性时中止(Daedalusys/daedalus-core#92):
      audit: 清除 append-only 失败(中止轮转): open /var/log/daedalus/audit.jsonl: operation not permitted
```

根因线索(同一次运行里实测):

```
O_RDWR 打开(无 O_APPEND): ... Operation not permitted   # exec 3<> file
O_APPEND 打开          : (成功)                          # exec 3>> file
```

`ClearAppend`/`SetAppend` 用 `os.OpenFile(p, os.O_RDWR, 0)` —— 内核对 append-only
inode 在「带 MAY_WRITE 但不带 O_APPEND」时直接 EPERM,于是轮转永远中止。→ **core#92**。

## A5 · 破坏性插件回滚(仅 guest 内)—— 全部成立

```
PASS  A5-1 blueprint_apply 失败后回滚把 /etc/nginx/conf.d/probe-a5.example.com.conf
      恢复到原 sha256(8e48ba85daed2b98970feaad831fc2296f96c154b60f4c465cce441caa541065)
PASS  A5-2a disk.clean 真删除了目标(keep.txt 留存),清单 diff 已记录
PASS  A5-2b organize.move apply 生效且 rollback 归位(src↔dst 清单 diff 已记录)
PASS  A5-3 宿主 lsblk 前后逐字节一致(未碰宿主盘)
```

`blueprint_apply` 的 post_check 在 guest 里必然失败(无 nginx)→ 触发 rollback 闭包 →
文件内容恢复到 apply 前的 sha256。disk.clean / organize.move 走的是真
`daedalus-tx begin → propose → apply → rollback` 四步(`tx.Step` 线上契约),不是 mock。

## A6 · fs 二档写守卫 —— 复现成立(core#91)

```
/etc/shadow (默认策略)                isError=yes source=一档 pathguard(AllowedDirs 白名单)
  text=Error: Access denied: path '/etc/shadow' ... is outside allowed directories (/home, /var/log, /tmp).
/proc/sys/kernel/hostname (默认策略)  isError=yes source=一档 pathguard(AllowedDirs 白名单)
/etc/pam.d/probe-a6-guard (白名单含 /etc) isError=no
  text=Successfully wrote 16 characters to /etc/pam.d/probe-a6-guard
/etc/shadow (白名单含 /etc)           isError=no  text=Successfully wrote 16 characters to /etc/shadow
      结论: 二档写守卫对 fs.write_file 不生效:白名单含 /etc 时 /etc/pam.d/ 下的写入成功
            (该前缀在 ValidateWritePath 的 forbiddenWritePatterns 里,write 形参被丢弃)
      issue: Daedalusys/daedalus-core#91
```

默认策略下的拒绝来自**一档**(`/etc` 本就不在 `allowed_dirs` 里);把白名单临时放宽
(guest 内 `/tmp/a6-policy.toml`,不碰镜像里的 policy.toml)后,`/etc/pam.d/` 与
`/etc/shadow` 的写入**都成功** —— 二档 `ValidateWritePath` 从未参与。

## A7 · 只读插件真数据源 —— 6/6 如实记录

```
PASS  A7-1a 真 timer 有 next_elapse_usec_realtime(systemd_timer_next)
PASS  A7-1b masked 单元的 LoadState 可见(systemctl show → LoadState=masked)
PASS  A7-2 dnf_query 三段(本地有 / 本地无 repo 有 / 两边都无)
PASS  A7-3 network_status 三级兜底(PATH 里移走 ip 仍产出网络信息)
PASS  A7-4 proc_fds 对他人 fd 标 unreadable / proc_listen 有响应
PASS  A7-5 trace_summary 读真 /var/log/daedalus/audit.jsonl(total=6,by_tool 有分布)
PASS  A7-6 dupe 对硬链接的行为
      结论: a.txt 与 b.txt 是同一 inode,却被一起报为重复(wasted_bytes 虚高)
      issue: Daedalusys/daedalus-plugins#76
```

A7-6 的原始响应(`ls -li` 显示 a/b 同 inode `46`,`nlink=2`):

```json
[{"group_id":"sha256-4ad0b661c6aa4ac1",
  "files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],
  "wasted_bytes":54,"same_dir":true}]
```

## A12 · 插件盘点 —— root 口径全绿;非 root 口径暴露 core#89

```
PASS  A12-1 插件集合恰为 CAPS + copilot(12 个)
PASS  A12-2 无 degraded 插件(root 口径)
NOTE  A12-3 非 root 口径出现 degraded: daedalus.copilot(issue Daedalusys/daedalus-core#89)
```

期望集合从 `76-daedalus-plugin-gen.sh` 的 `CAPS` 派生(不写死名单)。guest 内的目录位:

```
755 /opt/daedalus/plugins/daedalus.blueprint/
700 /opt/daedalus/plugins/daedalus.copilot/      <-- 只有它(镜像里是 0755)
755 /opt/daedalus/plugins/daedalus.diskclean/
... (其余全 755)
/sysroot 侧:drwx------ .../deploy/<csum>.0/opt/daedalus/plugins/daedalus.copilot
```

## copilot —— Deno 边界成立;CLI 被 core#89 挡住;其余缺 key

```
FAIL  A11-1 CLI 在 guest 内不可用:copilot 插件被判 degraded(目录权限不足,
            普通用户读不到 manifest;见 Daedalusys/daedalus-core#89)
NOTE  A11-2 BLOCKED: no key(watchdog 需经 LLM 翻译产出可执行命令;sleep 亦不在 15 条白名单内)
PASS  A11-3 Deno 拒写 --allow-write 之外的 /etc(白名单内路径可写)
NOTE  A11-4 BLOCKED: no key(未跑真 provider)
```

两处与计划默认假设不符、已如实记录:

1. `--dry-run` **不是**「不依赖 key」的路径 —— `readConfigFn` 在翻译之前就因缺 key 抛错;
   本轮它更早一步被 `core#89` 挡住(`daedalus-host run-plugin` 判 degraded)。
2. 计划设想的「配一个 `sleep 5` 的 allowlisted 命令」不成立:`sleep` 不在 15 条白名单里,
   且要被执行必须先经 LLM 翻译 → 无 key 时 watchdog 无法驱动。

## A2 残余 · bootc install to-disk / upgrade —— PROBE_WIP + BLOCKED

```
PROBE_WIP  A2-1 install to-disk 未成功(rc=1,source=docker://ghcr.io/daedalusys/daedalus-os:latest):
           error: Installing to disk: Creating source info from a given imageref:
           Subprocess failed: ExitStatus(unix_wait_status(256))
NOTE       A2-2 无 registry 可查(部署来源是本地派生 ref)—— 真 upgrade 未验证
PASS       A2-3 宿主 lsblk 前后逐字节一致(install 目标是宿主上的一个 qcow2 文件)
```

四种 `--source-imgref` 形态全部试过(bootc 1.16.13):

```
oci-archive:/home/verify/a2-source.tar              -> Multiple commit objects found
oci:/home/verify/a2-source.tar                      -> Multiple commit objects found
registry:ghcr.io/daedalusys/daedalus-os:latest      -> Multiple commit objects found
docker://ghcr.io/daedalusys/daedalus-os:latest      -> Multiple commit objects found
(此前还试过 ostree-unverified-image: / ostree-unverified-registry: -> Invalid transport)
```

`--wipe` 且目标盘每次重建,故不是残留状态;尚不能区分「探针的调用姿势不对」与
「bootc 对 bootc 派生镜像直接 install 有缺陷」→ 记 **PROBE_WIP**,不冒充通过。

`bootc upgrade` 的对象是 `localhost/daedalus-verify:base`(本地派生 ref),本机没有该
ref 的 registry 可查(`pinging container registry localhost: connection refused`),故
「真 upgrade」未验证;计划明确允许把这种情形记为「无新 digest 可升」而非失败。

## 未验证 / 受阻清单

| 项 | 状态 | 原因 |
|---|---|---|
| A4-4 轮转 | FAIL(产品缺陷) | `core#92`:ClearAppend 以 O_RDWR 打开 `+a` 文件被 EPERM |
| A11-1 copilot CLI | FAIL(产品缺陷) | `core#89`:部署里 copilot 插件目录 0700 |
| A11-2 watchdog | BLOCKED | 缺 `DAEDALUS_LLM_API_KEY`;且无 key 无法产出可执行命令 |
| A11-4 真 provider | BLOCKED | 缺 `DAEDALUS_LLM_API_KEY` |
| A2-1 install to-disk | PROBE_WIP | `--source-imgref` 取值形态未对齐 |
| A2-2 真 upgrade | BLOCKED | 需 registry 上存在更新 digest(本轮镜像为本地派生 ref) |
