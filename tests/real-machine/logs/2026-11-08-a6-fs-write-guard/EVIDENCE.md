# A6 · fs 写路径二档守卫 —— 修复后真机证据(2026-11-08)

**被测镜像**:`localhost/daedalus-core91:fixed`(`ghcr.io/daedalusys/daedalus-os:latest` +
本分支构建的 `daedalus-fs`,二进制 sha256 `2ff6ebfefcda520439e86ad4660ecb2c6ff4e9dbda3a1da2f1920976b5b93bba`)
**宿主**:`Masterwet_Server`(i7-12700H / x86-64-v3 / `/dev/kvm` 可写 / Fedora 44)
**做法**:`lib/guest-driver.sh` 派生一次性 verify 镜像 → `bootc-image-builder` 出 qcow2 →
QEMU(`-machine q35,accel=kvm -cpu host`,hostfwd **2226**)→ SSH 进 guest,由
`mcp-call.sh` 以 stdio JSON-RPC 直接驱动镜像内的 `daedalus-fs`。
guest 以 `snapshot=on` 引导:对 `/etc` 的写入只落临时 overlay,QEMU 退出即丢。

**原始输出**:`a6-fixed-image-run.log`(完整 stdout)。

## 判定

```
PASS  A6 二档写守卫生效:/etc/pam.d/probe-a6-guard 在白名单含 /etc 时仍被拒(二档 ValidateWritePath(只读系统目录))
PASS  A6 二档写守卫生效:/etc/sudoers.d/probe-a6-guard 在白名单含 /etc 时仍被拒(二档 ValidateWritePath(只读系统目录))

判定: PASS
exit=0
```

判据点的两个目标都在修复前可被写成功(见上一轮 `2026-11-07-guest-probes` 的
`REPRO_DONE` 记录:同一实验下 `/etc/pam.d/probe-a6-guard` 返回
`Successfully wrote 16 characters` 且落盘回读为 `probe-a6-widened`);修复后两者
均转为 `write denied`,且 **落盘回读为空**(`No such file or directory`)—— 证明拒绝
发生了,而不是只改了返回文案。

## 探针的一处判据强化

原探针是「复现任务恒退 0」,无法在门禁里判。升级为判据任务时特意避开了一个假绿:

二档 `ValidateWritePath` 强制完整 realpath,**不存在的目标**会得到
`write denied: cannot resolve realpath ...`。如果只按「被拒 + 含 `write denied`」判,
一个把写目标直接丢给二档的实现会因为「目标不存在」而拒,在探针里显示成 PASS ——
它并没有拦住敏感路径,只是拒绝了不存在的落点。故判据额外要求:

- 拒绝理由**不是** `cannot resolve realpath`,且
- 必须点名 `read-only system dir` 或 `forbidden write`(二档的两条清单)。

离线用真实 guest 响应形态跑过六条分支(修复生效×2 / 缺陷仍在 / realpath 假绿 /
一档来源不符 / 无响应),判定均符合预期。

## A/B(同容器,同一份放宽白名单的 policy)

修复前/修复后两个二进制跑同一组请求(`write_file`,白名单
`["/home","/var/log","/tmp","/etc","/proc"]`):

| 目标 | 修复前(镜像自带) | 修复后 |
|---|---|---|
| `/etc/pam.d/probe-a6-guard` | `Successfully wrote 16 characters` + 落盘 `probe-a6-widened` | `write denied: path resolves to read-only system dir "/etc"`,未落盘 |
| `/etc/sudoers.d/probe-a6-guard` | `Successfully wrote 16 characters` | `write denied: ... read-only system dir "/etc"`,未落盘 |
| `/etc/shadow` | `Successfully wrote 16 characters` | `write denied: ... read-only system dir "/etc"` |
| `/proc/sys/kernel/hostname` | `Error: open ...: read-only file system`(内核拒,非守卫) | `write denied: ... read-only system dir "/proc"` |
| `/tmp/a6-normal.txt`(白名单内新建) | `Successfully wrote 16 characters` | `Successfully wrote 16 characters` |
| `/tmp/a6-nest/deep/x.txt`(嵌套新建) | — | `Successfully wrote 16 characters` |

`move_file` 两端同测:

| 场景 | 修复前 | 修复后 |
|---|---|---|
| src 敏感(`/etc/shadow` → `/tmp`) | `Successfully moved`,落盘可读到 shadow 全文 | `write denied: ... read-only system dir "/etc"`,未落盘 |
| src 敏感(`/etc/pam.d/chfn` → `/tmp`) | — | 同上;`/etc/pam.d/chfn` 仍在原位 |
| dst 敏感(`/tmp` 文件 → `/etc/sudoers.d/a6-moved`) | `Successfully moved`,已落盘 | `write denied: ... read-only system dir "/etc"`,未落盘 |
| 正常 move(`/tmp` 内改名) | `Successfully moved` | `Successfully moved` |
| src 不存在 | `Error: rename ...: no such file or directory` | `Error: write denied: cannot resolve realpath for ...` ← **有意的契约变更** |
