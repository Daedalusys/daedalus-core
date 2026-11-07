# 2026-11-08 · A7-6 dupe 硬链接误报 —— 真机验证证据

> 对应 issue:`Daedalusys/daedalus-plugins#76`(scan_dupes 把同 inode 的硬链接报成重复,
> `wasted_bytes` 把没多占空间的硬链接也算成浪费)。
> 探针:`daedalus-core/tests/real-machine/probe-readonly-plugins.sh` 的 **A7-6** 段。
> 本目录的两份日志是同一条探针在**同一宿主**上、对**修复前/修复后**两个镜像的完整运行输出。

## 结论

A7-6 是可判定断言,真 guest 里跑出**双向证据**:

| 镜像 | dupe 插件 | A7-6 分组 | A7-6 判定 |
|---|---|---|---|
| `ghcr.io/daedalusys/daedalus-os:latest`(修复前) | 发布态二进制 | `a.txt` + `b.txt` + `c.txt` 同组,`wasted_bytes=54` | **FAIL** |
| `localhost/daedalus-os:plugins76-fix`(修复后) | PR 分支构建 | `a.txt` + `c.txt` 同组(硬链接代表被折叠),`wasted_bytes=27` | **PASS** |

`wasted_bytes` 的期望值 27 = 夹具载荷 `hardlink-payload-0123456789` 的字节数;
54 = 27×2,正是把「同一 inode 的两个路径」当成两份数据的结果 —— 与 issue 现场一致。

## 断言判什么(不是只看一个数)

只比 `wasted_bytes` 的总和是不够的:真副本被拆成多组、每组各自少算时,总和
可能仍然"对"。故判定分两层,两层都过才算 PASS:

1. **分组形状**:恰好 1 组、组内恰好 2 个路径、含 `c.txt`、不含 `b.txt`;
2. **数值**:该组 `wasted_bytes` 恰等于 `载荷 × (成员数 - 1)`,即只多留一份;
   字段缺失(`has("wasted_bytes")` 为假)也判不通过。

响应不可解析、或宿主缺 `jq` 时判 **FAIL(无法判定)**,不退化成通过。

## 环境与做法

- **宿主**:`Masterwet_Server`(i7-12700H / x86-64-v3 / `/dev/kvm` 可写 / Fedora 44),rootful podman。
- **guest**:`lib/guest-driver.sh` 从被测镜像派生一次性 verify 镜像(复刻 dev 用户的组/sudoers/tmpfiles,
  口令随机、不推 registry)→ `bootc-image-builder` 出 qcow2 → QEMU(`-machine q35,accel=kvm -cpu host`)。
  `DRIVER_QEMU_SNAPSHOT=1`:破坏性操作只落临时 overlay。
- **跑法**(两条都串行,worker 专属锁 `/tmp/daedalus-target-plugins76.lock`):

```bash
# 修复后(本目录 probe-readonly-after-fix.log)
sudo VERIFY_IMAGE=localhost/daedalus-os:plugins76-fix \
     VERIFY_WORK=/tmp/plugins76-a7-v3-after VERIFY_SSH_PORT=2237 \
     bash tests/real-machine/probe-readonly-plugins.sh
# 对照(修复前,本目录 probe-readonly-before-fix.log)
sudo VERIFY_IMAGE=ghcr.io/daedalusys/daedalus-os:latest \
     VERIFY_WORK=/tmp/plugins76-a7-v3-before VERIFY_SSH_PORT=2238 \
     bash tests/real-machine/probe-readonly-plugins.sh
```

## 镜像构建记录(构建步骤的输出,不在探针日志里)

修复后镜像由发布镜像 + 插件 PR 分支构建的二进制叠成(靶机 rootful podman):

```
STEP 1/3: FROM ghcr.io/daedalusys/daedalus-os:latest
STEP 2/3: COPY daedalus-dupe /opt/daedalus/plugins/daedalus.dupe/bin/daedalus-dupe
STEP 3/3: RUN set -eux; ...
+ sum=sha256:b05830154d8316345f363d8914524e98396ac6594e27e66feb57cc1810f5281d
+ sed -i 's#"bin/daedalus-dupe": "sha256:[0-9a-f]*"#..."bin/daedalus-dupe": "sha256:b0583015...f5281d"#' \
      /opt/daedalus/plugins/daedalus.dupe/daedalus.plugin.json
COMMIT localhost/daedalus-os:plugins76-fix
```

被覆盖进去的二进制摘要(本地构建产物):

```
b05830154d8316345f363d8914524e98396ac6594e27e66feb57cc1810f5281d  daedalus-dupe
```

⚠️ 这两条事实来自**构建步骤的输出**,不在探针日志里 —— 探针日志只证明「跑的哪个镜像、
报了什么」。这是**本地一次性验证镜像**,不进 registry、不进发布产物。

## 关键输出

### 修复前(`probe-readonly-before-fix.log`)

夹具的 inode 号(日志第 580 行 `A7-6 夹具 ls -li`;a/b 同为 inode 45、`nlink=2`):

```
45 -rw-r--r--. 2 verify verify 27 Nov  7 22:36 a.txt
45 -rw-r--r--. 2 verify verify 27 Nov  7 22:36 b.txt
46 -rw-r--r--. 1 verify verify 27 Nov  7 22:36 c.txt
```

`scan_dupes` 结果与判定:

```
      A7-6 groups=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":54,"same_dir":true}] shape=b_still_grouped=1 wasted=ok
FAIL A7-6 硬链接仍被报成重复或响应不可解析(b_still_grouped=1),分组=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/b.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":54,"same_dir":true}] — Daedalusys/daedalus-plugins#76
```

### 修复后(`probe-readonly-after-fix.log`)

夹具的 inode 号(日志第 580 行;同一造法,a/b 同为 inode 46、`nlink=2`):

```
46 -rw-r--r--. 2 verify verify 27 Nov  7 22:33 a.txt
46 -rw-r--r--. 2 verify verify 27 Nov  7 22:33 b.txt
47 -rw-r--r--. 1 verify verify 27 Nov  7 22:33 c.txt
```

`scan_dupes` 结果与判定:

```
      A7-6 groups=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":27,"same_dir":true}] shape=ok wasted=ok
PASS A7-6 硬链接未被误报:恰好一组、组内 2 个路径、wasted_bytes 只算一份真实副本(27B),分组=[{"group_id":"sha256-4ad0b661c6aa4ac1","files":["/tmp/a7-dupe/a.txt","/tmp/a7-dupe/c.txt"],"wasted_bytes":27,"same_dir":true}]
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

把探针里的 A7-6 判定段**原样抽出**(不重写),喂八种响应形态,每种都落到正确的判定上:

| # | 形态 | 期望 | 实际 |
|---|---|---|---|
| ① | 1 组 2 路径 `wasted=27`(修复后) | PASS | **PASS** |
| ② | `a/b/c` 同组 `wasted=54`(修复前) | FAIL | **FAIL** |
| ③ | 拆成 2 组,`wasted` 20+7 总和仍 27 | FAIL | **FAIL**(`group_count=2`) |
| ④ | 1 组 1 路径 | FAIL | **FAIL**(`c_missing`) |
| ⑤ | 无 `c.txt` | FAIL | **FAIL**(`b_still_grouped=1`) |
| ⑥ | 缺 `wasted_bytes` 字段 | FAIL | **FAIL**(`missing`) |
| ⑦ | 不可解析文本 | FAIL | **FAIL**(`<解析失败>`) |
| ⑧ | 空分组 `[]` | FAIL | **FAIL**(`group_count=0`) |

③ 是只有「逐组判形状」才能抓住的退化 —— 旧的「只比 `wasted_bytes` 总和」判法在
该形态下会误判为通过。

## 已知缺口

- **修复后镜像是本地派生**,不是 registry 里的发布镜像:本次没有新插件 release,
  因此 `ghcr.io/daedalusys/daedalus-os:<合并后 sha>` 尚未存在。权威结论仍需
  `daedalus-plugins` 发版 → 主仓重建镜像 → CI 门禁 `deploy-verify-guest-probes`
  在真镜像上跑出 A7-6 PASS。
- **双 PR 合并顺序**:A7-6 的 PASS 依赖 `daedalus-plugins#76` 的修复先进镜像。
  若探针这侧先合并而插件修复尚未发版,A7-6 会在 CI 上红 —— 这是**预期**的失败信号
  (探针不再"如实记录"而是判定),不是探针缺陷。
- 本探针断言依赖宿主有 `jq`(用于把 JSON-RPC 响应还原成可判定事实);宿主缺 jq 时
  该条判 **FAIL(无法判定)** 而非放行 —— 宁可红也不退化成恒过。
- 符号链接不在本次范围(issue 只讲硬链接);`scan_dupes` 的符号链接语义未改动。
- `wasted_bytes` 语义仍是 `total - largest`(按折叠后的成员算),不变。
