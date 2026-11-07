# 2026-11-08 · A7-6 dupe 硬链接误报 —— 真机验证证据

> 对应 issue:`Daedalusys/daedalus-plugins#76`(scan_dupes 把同 inode 的硬链接报成重复,
> `wasted_bytes` 把没多占空间的硬链接也算成浪费)。
> 探针:`daedalus-core/tests/real-machine/probe-readonly-plugins.sh` 的 **A7-6** 段。
> 本目录的两份日志是同一条探针在**同一宿主**上、对**修复前/修复后**两个镜像的完整运行输出。

## 结论

A7-6 由「如实记录误报」升级为**可判定断言**,并在真 guest 里跑出了**双向证据**:

| 镜像 | dupe 插件 | A7-6 分组 | A7-6 判定 |
|---|---|---|---|
| `ghcr.io/daedalusys/daedalus-os:latest`(修复前) | 发布态二进制 | `a.txt` + `b.txt` + `c.txt` 同组,`wasted_bytes=54` | **FAIL** |
| `localhost/daedalus-os:plugins76-fix`(修复后) | PR 分支构建 | `a.txt` + `c.txt` 同组(硬链接代表被折叠),`wasted_bytes=27` | **PASS** |

`wasted_bytes` 的期望值 27 = 夹具载荷 `hardlink-payload-0123456789` 的字节数;
54 = 27×2,正是把「同一 inode 的两个路径」当成两份数据的结果 —— 与 issue 现场一致。

## 环境与做法

- **宿主**:`Masterwet_Server`(i7-12700H / x86-64-v3 / `/dev/kvm` 可写 / Fedora 44),rootful podman。
- **guest**:`lib/guest-driver.sh` 从被测镜像派生一次性 verify 镜像(复刻 dev 用户的组/sudoers/tmpfiles,
  口令随机、不推 registry)→ `bootc-image-builder` 出 qcow2 → QEMU(`-machine q35,accel=kvm -cpu host`)。
  `DRIVER_QEMU_SNAPSHOT=1`:破坏性操作只落临时 overlay。
- **被测镜像(修复后)**:`localhost/daedalus-os:plugins76-fix`,= `ghcr.io/daedalusys/daedalus-os:latest`
  + 覆盖 `/opt/daedalus/plugins/daedalus.dupe/bin/daedalus-dupe` 为 PR 分支构建的二进制
  (`sha256:b05830154d8316345f363d8914524e98396ac6594e27e66feb57cc1810f5281d`,静态链接),
  并把 manifest 内的该条 checksum 同步成同一摘要(保持安装态自洽)。
  这是**本地一次性验证镜像**,不进 registry、不进发布产物。
- **跑法**(两条都串行,worker 专属锁 `/tmp/daedalus-target-plugins76.lock`):

```bash
# 修复后(本目录 probe-readonly-after-fix.log)
sudo VERIFY_IMAGE=localhost/daedalus-os:plugins76-fix \
     VERIFY_WORK=/tmp/plugins76-a7-work-final VERIFY_SSH_PORT=2233 \
     bash tests/real-machine/probe-readonly-plugins.sh
# 对照(修复前,本目录 probe-readonly-before-fix.log)
sudo VERIFY_IMAGE=ghcr.io/daedalusys/daedalus-os:latest \
     VERIFY_WORK=/tmp/plugins76-a7-work-final-before VERIFY_SSH_PORT=2234 \
     bash tests/real-machine/probe-readonly-plugins.sh
```

这两份日志是**提交后**的探针脚本跑出来的(脚本经 tar 送到靶机,与提交内容逐字节一致),
不是改到一半的中间版本。

## 关键输出

### 修复前(`probe-readonly-before-fix.log`)

夹具的 inode 号(guest 内 `ls -li`):

```
46 -rw-r--r--. 2 verify verify 27 Nov  7 18:26 a.txt
46 -rw-r--r--. 2 verify verify 27 Nov  7 18:26 b.txt
47 -rw-r--r--. 1 verify verify 27 Nov  7 18:26 c.txt
```

`scan_dupes` 结果与判定:

```
      A7-6 groups=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":54,"same_dir":true}] hardlink_grouped=1 wasted=54
FAIL A7-6 硬链接仍被报成重复:a.txt 与 b.txt(同 inode)出现在同一组,分组=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":54,"same_dir":true}] — Daedalusys/daedalus-plugins#76
```

### 修复后(`probe-readonly-after-fix.log`)

夹具的 inode 号(同一造法,`a.txt`/`b.txt` 同为 inode 46):

```
46 -rw-r--r--. 2 verify verify 27 Nov  7 18:23 a.txt
46 -rw-r--r--. 2 verify verify 27 Nov  7 18:23 b.txt
47 -rw-r--r--. 1 verify verify 27 Nov  7 18:23 c.txt
```

`scan_dupes` 结果与判定:

```
      A7-6 groups=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":27,"same_dir":true}] hardlink_grouped=0 wasted=27
PASS A7-6 硬链接未被误报,wasted_bytes 只算真实副本(27B),分组=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":27,"same_dir":true}]
```

### 两条运行的同套件其它判定

A7-1..A7-5 在两次运行中输出一致(均 PASS),证明 A7-6 的判定翻转不是环境漂移:

```
PASS A7-1a 如实记录:真 timer 有 next_elapse_usec_realtime
PASS A7-1b 如实记录:masked 单元的 LoadState 可见
PASS A7-2 如实记录(本地有/本地无 repo 有/两边都无 三段输出见上;本地无时兜底可能因仓库元数据不可达返回空)
PASS A7-3 如实记录(降级路径仍产出网络信息)
PASS A7-4 如实记录(proc_fds 有响应;unreadable 计数见上)
PASS A7-5 如实记录(真日志可被 trace 读取;单测遇真日志会 t.Skip)
```

## 断言本身的证伪能力

除真机双向证据外,还在开发机上做过一次**断言级**自检:把探针里的 A7-6 判定段原样抽出
(不重写),分别喂「修复前」「修复后」两个二进制在**同一目录**上的 `scan_dupes` 响应,
判定随之翻转 —— 证明新断言不是恒过。该自检执行于判定逻辑定稿后、仅追加 issue 号
后缀之前,判定条件本身与提交版本相同:

```
########## BUGGY(修复前,期望 FAIL) ##########
     分组结果(已还原 JSON): [{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":54,"same_dir":true}]
FAIL  A7-6 硬链接仍被报成重复:a.txt 与 b.txt(同 inode)出现在同一组,分组=[...wasted_bytes":54...]

########## FIXED(修复后,期望 PASS) ##########
     分组结果(已还原 JSON): [{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":27,"same_dir":true}]
PASS  A7-6 硬链接未被误报,wasted_bytes 只算真实副本(27B),分组=[...wasted_bytes":27...]
```

## 已知缺口

- **修复后镜像是本地派生**,不是 registry 里的发布镜像:本次没有新插件 release,
  因此 `ghcr.io/daedalusys/daedalus-os:<合并后 sha>` 尚未存在。权威结论仍需
  `daedalus-plugins` 发版 → 主仓重建镜像 → CI 门禁 `deploy-verify-guest-probes`
  在真镜像上跑出 A7-6 PASS。
- **双 PR 合并顺序**:A7-6 的 PASS 依赖 `daedalus-plugins#76` 的修复先进镜像。
  若本 PR 先合并而插件修复尚未发版,A7-6 会在 CI 上红 —— 这是**预期**的失败信号
  (探针不再"如实记录"而是判定),不是探针缺陷。
- 本探针断言依赖宿主有 `jq`(用于把 JSON-RPC 响应还原成可判定事实);宿主缺 jq 时
  该条判 **FAIL(无法判定)** 而非放行 —— 宁可红也不退化成恒过。
- 符号链接不在本次范围(issue 只讲硬链接);`scan_dupes` 的符号链接语义未改动。
- `wasted_bytes` 语义仍是 `total - largest`(按折叠后的成员算),不变。
