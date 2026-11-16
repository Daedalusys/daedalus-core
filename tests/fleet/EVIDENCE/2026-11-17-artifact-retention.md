# CI 证据包瘦身 + Release 自动保留(issue #167)—— 证据

日期:2026-11-17
改动:PR #168(`c0195cf0`)、PR #169(`f094896d`)—— 均已合并到 main
验证 run:`43908965143`(main `c0195cf0`,**8/8 全绿**);本轮存量清理在此基础上做

## 一、结论

| 主张 | 证据 | 结果 |
|---|---|---|
| 证据包不再含磁盘镜像 | 同一仓相邻两次 run 的 artifact 字节数(§2) | 10.67 GB → **188 KB** |
| 证据没丢 | 改造后的 artifact 里仍有全部关键日志 + `MANIFEST.txt`(§3) | ✅ |
| Release 有自动保留策略 | `Prune old releases (keep 3)` 这条腿的日志(§4) | 7 → 3 |
| main 全绿 | `43908965143` 逐 job 结论(§5) | 8/8 success,`Deploy Verify (real KVM boot)` **历史上第一次真跑通** |
| 存量已清 | GitHub 与宿主的 before/after(§6) | artifacts 370.08 GiB → 1.18 GiB;宿主磁盘 132G → 118G |

## 二、证据包:10.67 GB → 188 KB

同一仓相邻两次 run,同一批 artifact 名:

```
$ gh api repos/Daedalusys/daedalus-core/actions/runs/43869487130/jobs ...
deploy-verify-bootc-switch-evidence   5304451705
deploy-verify-guest-probes-evidence   2640351160
deploy-verify-iso-evidence             2728926457
daedalus-os-qcow2-headless            1327849395

$ gh api repos/Daedalusys/daedalus-core/actions/runs/43908965143/jobs ...
daedalus-os-qcow2-headless            1323589555
deploy-verify-bootc-switch-evidence       25637
deploy-verify-guest-probes-evidence       33662
deploy-verify-iso-evidence              121019
deploy-verify-kvm-evidence                8054
```

| artifact | 改造前(`43869487130`) | 改造后(`43908965143`) | 倍数 |
|---|---|---|---|
| `deploy-verify-bootc-switch-evidence` | 5,304,451,705(5.30 GB) | **25,637**(25 KB) | 207× |
| `deploy-verify-guest-probes-evidence` | 2,640,351,160(2.64 GB) | **33,662**(33 KB) | 78× |
| `deploy-verify-iso-evidence` | 2,728,926,457(2.73 GB) | **121,019**(118 KB) | 23× |
| `deploy-verify-kvm-evidence` | 0(目录是空的,腿没跑成) | **8,054**(8 KB) | — |
| **证据合计** | **10,673,729,322(10.67 GB)** | **188,372(184 KB)** | **~58,000×** |

`daedalus-os-qcow2-headless`(1.32 GB)体积不变 —— 它是本次 run 内给 `deploy-verify-kvm` 用的中间产物,
**保留期从默认 90 天改成 3 天**(重跑失败腿时复用原 run 的 artifact,3 天足够)。

## 三、证据没丢:改造后的 artifact 里有什么

```
$ gh run download 43908965143 --name deploy-verify-bootc-switch-evidence -D ev3
    102405  serial-console.log
     39529  bootc-switch-verify.log
     24229  output/qcow2/manifest-qcow2.json
       700  Containerfile
       499  MANIFEST.txt
       109  askpass.sh
        89  advice.log
```

`MANIFEST.txt` 逐条记下被排除的大文件及其字节数(所以「什么没传」是可审计的,不是静默丢):

```
# stage-evidence manifest
# src: /opt/actions-runner/_work/daedalus-core/daedalus-core/switch-verify-43908965143
# max_bytes: 8388608
# 格式: <动作>	<字节>	<相对路径>
SKIPPED	4135452672	output/qcow2/qcow2/disk.qcow2
SKIPPED	1329836544	switch-target.tar
```

(8 条 `COPIED`,2 条 `SKIPPED`。筛选口径是**大小上限 8 MiB**,不是扩展名白名单 —— 各腿的日志名各不相同,
按名字筛必然漏;磁盘则一定大于上限。)

## 四、Release 自动保留:`Prune old releases (keep 3)`

该腿在 `43908965143` 里第一次真跑,日志原文:

```
KEEP(最新 3 个之内)	c0195cf0bb7c	2026-11-16T16:34:19Z
KEEP(最新 3 个之内)	6bf2485aa943	2026-11-16T12:45:16Z
KEEP(最新 3 个之内)	d48ff6cedc33	2026-11-16T12:12:04Z
DELETED	1ad4c3f20bda
DELETED	9d85ee153b48
DELETED	1fb53c1e93bc
DELETED	30fc589cd69e
prune-releases: 共 7 个,保留 3 个,删除 4 个,失败 0 个(dry-run=0)
```

脚本的四条安全边界(读不出列表不删 / 非 sha tag 与 draft、prerelease 永不删 / 最新 N 个永不删 /
待删超上限拒跑)由 `tests/ci/prune-releases-test.sh` 钉住,已接进 CI 的 selftest 步骤。

## 五、main 全绿(`43908965143`)

```
run: completed success
  Test (go / deno / i18n / drift)                | success
  Build & Publish Daedalus Image (headless)      | success
  Build qcow2 disk image (bootc-image-builder)   | success
  Deploy Verify (ISO boot + unattended install)  | success
  Deploy Verify (guest probes A3/A4/A5/A6/A7/…)  | success
  Deploy Verify (bootc switch + rollback)        | success
  Deploy Verify (real KVM boot)                  | success
  Prune old releases (keep 3)                    | success
```

`Deploy Verify (real KVM boot)` 此前从未真跑通:上一次(`43869487130`)它死在 `actions/checkout` 的
`git fetch`(`curl 28 ... after 136238 ms`),一步都没跑;再上一次腿全是 `skipped`。

同一轮里实机腿的判定(原文,节选):

```
PASS  A13-1 单元装进镜像且 ExecStart 是 'daedalus-blueprint ui --listen 127.0.0.1:8787'(回环:8787)
PASS  A13-3 单元起得来且真的在服务:is-active=active,GET / = 200
PASS  A13-5 沙箱落在单元上:DynamicUser=yes / ProtectSystem=strict / NoNewPrivileges=yes / SystemCallFilter 声明 @system-service 且已展开生效
PASS  A14-2 switch 不带 --yes 被闸门挡住:rc=2、stderr 含 'would run'、未联网、部署 id 未变
PASS  A14-5 rollback 不带 --yes 被闸门挡住:rc=2、stderr 含 'would run'、部署 id 未变
```

## 六、存量清理 before/after

### GitHub

| 项 | 之前 | 之后 |
|---|---|---|
| artifacts | **187 个 / 370.08 GiB** | 5 个 / 1.18 GiB(本次 run 的) |
| releases | 7 个 | 3 个 |

删除前用 `gh api --paginate` **翻完所有分页**统计(不是只读第一页 —— 只读第一页会得到
"206.71 GB / 100 个"这种少一半的数),187 个合计 **370.08 GiB**。其中 183 个 / 358.90 GiB
按名字归类:

```
134.53 GiB  x28  deploy-verify-bootc-switch-evidence
 69.84 GiB  x37  deploy-verify-iso-evidence
 68.60 GiB  x56  daedalus-os-qcow2-headless
 68.54 GiB  x29  deploy-verify-guest-probes-evidence
 17.40 GiB  x5   daedalus-os-qcow2-kde
  0.00 GiB  x28  deploy-verify-kvm-evidence
```

另 4 个 / 11.18 GiB 属于上一轮 run `43869487130`(它的证据包也是 GB 级,见 §2)。

删除结果:`187 OK`(零失败)。

### runner 宿主(`Masterwet_Server` / runner `undef-dev`)

| 项 | 之前 | 之后 |
|---|---|---|
| 磁盘 | 132G/475G(28%) | **118G/475G(25%)** |
| `/opt/actions-runner/_work` | 24 GB | **13 GB** |
| root podman 镜像 | 125 个 / 53 GB | **14 个 / 6.3 GB** |
| user podman 镜像 | 25 个 / 12 GB | **18 个 / 9.6 GB** |

动作:删 84 个悬空镜像层;删 28 个旧的 `ghcr.io/daedalusys/daedalus-os:*` tag(保留最新的
`c0195cf0bb7c…` 与 `:latest`);`localhost/*` 验证缓存镜像不动(删了只是下次重建);用仓里现成的
`cleanup-verify-work.sh --keep 1` 收掉四个前缀各 1 个残留 run 目录。

## 七、这轮**没有**证明的事(不夸大)

1. **网络类故障没有被消除,只是变得可见且有界**:
   - `podman pull` 42 分钟那次:2.25 GB 按实测 7.41 MB/s 只要 5 分钟,8 倍差距最可能是宿主争用
     (当时有 6 台 fleet KVM 同时在跑,已停掉),不是网络本身;
   - GitHub 托管 runner 的 `apt-get` 挂死 51 分钟,自托管 runner 上的任何配置都够不着它 ——
     PR #169 用 `Acquire::*::Timeout` + 外层 `timeout 300` 让它**几分钟内红掉**而不是无声挂着;
   - 自托管 runner 的 `git fetch` 停滞:宿主侧给 runner 用户与 root 设了
     `http.lowSpeedLimit=1000` / `http.lowSpeedTime=60`(低于 1KB/s 持续 60 秒就放弃),
     该配置已写进 `scripts/cd-runner-setup.sh`,重装 runner 可复现。
2. **实测「runner 走代理」是更慢的,故未采用**:同一时刻从 runner 宿主量:
   GitHub release 资产 100 MB,直连 7.41 MB/s(14.2 s)、走 mihomo 代理 2.63 MB/s(39.8 s);
   `git clone --depth 1` 本仓,直连 3.82 s、代理 11.03 s。代理只在极小的 API 请求上快。
3. **`prune-releases.sh` 只删 sha 形态的 tag**:人手建的语义版本号(`v0.1.2` 这类)、draft、
   prerelease 永不删;「保留最新 N」按创建时间算,不保证「最新的是 latest 标记的那个」。
4. **证据筛选口径是大小上限 8 MiB**:如果哪天出现比 8 MiB 大的**文本**证据(例如超大串口日志),
   它会被跳过 —— `MANIFEST.txt` 里会记一条 `SKIPPED`,不会静默丢,但也不会上传。
