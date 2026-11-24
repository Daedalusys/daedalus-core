# 靶场证据:AI 巡检能力修复后的复测(2026-11-24)

上一轮的证据在 `../2026-11-23-ai-inspection-lab/`(基准)。本目录是**修复后**的复测:
同两台 guest、同一个巡检提示词(盲测)、同一批故障(6 个旧的 + 3 个新的)。

## 靶场拓扑

沿用上一轮,未变:

| 节点 | 角色 | 端口 | 内存 | 来源 |
| --- | --- | --- | --- | --- |
| `lab-a` | 故障靶机 | 9601 | 4096M | `qcow2-v0.1.1.qcow2` + overlay |
| `lab-b` | 健康对照 | 9602 | 3072M | 同上 |

两台都跑在 `Masterwet_Server` 的 KVM 上,SSH 只监听宿主 `127.0.0.1`,guest 内
`daedalus` 用户免密 sudo。

## 本轮与上一轮的关键差别:新二进制怎么进 guest

**没有重建镜像**。本机 CPU 不支持 x86-64-v3,`just build` 在
`/ctx/build_files/build.sh` 处直接 `Fatal glibc error: CPU does not support x86-64-v3`,
镜像构建改由 CI 承担(见「不能证明什么」)。

所以本轮采用**就地替换**:

1. `/opt` 在 bootc 的 composefs 上,只读;先在 guest 内
   `mount --bind /var/lib/daedalus-lab/opt-daedalus /opt/daedalus` 开一个可写影子目录
   (原 `/opt/daedalus` 原样拷进去),再往影子里装新文件。
2. 替换的二进制(全部由本地 `go build -trimpath -buildvcs=false` 从
   `daedalus-plugins@v0.9.0` 与 `daedalus-core@main` 现构):
   - `/opt/daedalus/plugins/daedalus.{fs,shell,service,dupe}/bin/*`
   - `/opt/daedalus/plugins/daedalus.proc/bin/daedalus-proc`
   - `/usr/local/bin/daedalus-{audit,host,tx,shell,service}`
3. 同步替换 `/opt/daedalus/shared/policy.toml`(core `main` 的生产策略,含
   `journalctl` 与两个上限键)。

**这一步本身踩出了一个真实缺陷,值得单独记**:第一次只换了 fs/shell/proc 三个插件,
没换 `daedalus-audit`。老 `daedalus-audit`(按 SDK v0.4.0 构建)读到新 policy.toml 的
`fs.max_read_bytes` / `shell.max_output_bytes` 两个新键,按 fail-closed 规则**拒绝启动**:

```
policy: /opt/daedalus/shared/policy.toml 含未知键(fail-closed,拒绝拼写错误/第二事实源):
fs.max_read_bytes, shell.max_output_bytes
```

于是 `daedalus-audit-rotate.service` 每 10 分钟失败一次,被上一轮的巡检如实报成「严重」。
换成新二进制后同一服务 `rotated=false / Deactivated successfully`。

这正是「SDK 发版 → plugins 发版 → 主仓抬 pin」顺序不可颠倒的实证:**新策略 + 旧二进制
= 插件拒启动**,现场看起来像「插件没应答」。

## 巡检客户端

与上一轮完全一致,便于对比:

- 外部 AI 客户端经 SSH stdio 连 guest 内的 10 个 MCP server,只允许 `lab-a-*` /
  `lab-b-*` 工具,不许本机 read / glob / grep / shell。
- 提示词固定为「只读巡检,检查当前是否存在异常」,**不含任何故障信息**(盲测前提)。

## 运行矩阵

| 运行 | 节点 | 场景 | 文件 |
| --- | --- | --- | --- |
| F3 | lab-a | 注入 9 故障 | `report-lab-a-faulted-v3.txt` / `tool-calls-lab-a-faulted-v3.txt` |
| B3 | lab-b | 健康对照 | `report-lab-b-baseline-v3.txt` / `tool-calls-lab-b-baseline-v3.txt` |

另有 8 条**直连插件**的探针(`probe-capabilities.sh` → `probes-direct.txt`),不经 AI:
断言里的字节数、outcome 字段必须由原始回包判定,巡检报告证明不了。

## 注入的 9 个故障

`fault-injection-v2.sh` = 上一轮 6 个 + 本轮针对修复点的 3 个:

| 编号 | 故障 | 预期签名 | 覆盖的能力 |
| --- | --- | --- | --- |
| F1 | `lab-fault-fail.service` 启动即失败 | failed unit | 状态轴(防回归) |
| F2 | `lab-fault-listen.service` 起 `0.0.0.0:18080` | 多余监听 | 状态轴(防回归) |
| F3 | `/tmp` 1.2G 文件 | 容量轴 | 状态轴(防回归) |
| F4a | `/var/log/lab-fault-app.log` 250 行 ERROR/FATAL | 日志文件轴 | 状态轴(防回归) |
| F4b | journal-only 磁盘错误 | **日志轴(上一轮 100% 漏检)** | journal 白名单 |
| F5 | chronyd 停止 | 服务轴 | 状态轴(防回归) |
| N1 | `lab-fault-crash` 崩溃循环,解释只在 journal 里 | **journal-only 归因** | journal 白名单 + outcome |
| N2 | journal 里 2.58 MB 文本 | **单次输出超上限** | `max_output_bytes` |
| N3 | root 进程监听 `0.0.0.0:18081` | **fd 不可读时的归属** | proc 归属线索 |

## 探针(直连插件,不经 AI)

`probes-direct.txt` 逐条记录:

1. `journalctl -p err` → `outcome=ok`,回包 556 字节(上一轮:白名单拒绝)。
2. `systemctl is-system-running`(rc=1)→ `isError=false`,`outcome=ran_and_failed`。
3. `systemctl status no-such-unit`(rc=4)→ `isError=false`,`outcome=ran_and_failed`。
4. `journalctl -f` → `isError=true`,`outcome=denied`(该拒的仍然拒)。
5. `journalctl -t lab-fault-bulk`(2.58 MB)→ 回包 1,060,996 字节,`stdout_truncated=true`,
   `isError=false`。
6. `read_file` 8 MiB `system.journal` → 回包 6,283,930 字节,首行
   `daedalus: truncated=true total_bytes=8388608 read_bytes=1048576`,`isError=false`。
7. `proc_listen 18081`(root 进程)→ `attribution=cmdline_hint`,hints 带 pid / cmdline /
   `cgroup_unit=lab-fault-rootlisten.service`。
8. `proc_fds 1`(读不到 root 的 fd)→ `entries:[]` + `denied:true` + `denied_reason`,
   `isError=false`(上一轮:整个调用报错)。

## 能证明什么 / 不能证明什么

**能证明**

- 三条硬断言在真机 guest 上成立(见 `RESULTS.md`)。
- 9 个注入故障中 8 个在盲测报告里被点名;第 9 个(F4b)被读到了但被模型判为「注入噪声」
  —— **读到了**与**判为真实故障**是两件事,这里如实分开记。
- 健康对照机上严重级误报 0 条。

**不能证明**

- **没有重建镜像**。本机 CPU 不支持 x86-64-v3,`just build` 跑不起来;镜像构建与
  `76-daedalus-plugin-gen.sh` 门禁由 CI 承担。本目录的证据证明的是「替换掉二进制与策略
  之后,插件行为正确」,**不证明**「发布镜像里就带着这批二进制」。
- **不是发行版验收**。guest 仍是 v0.1.1 镜像 + 就地替换,不是 v0.2.0 镜像。
- **归属线索不是归因**。`cmdline_hint` 只是「某可见进程的 cmdline 里出现了这个端口」,
  不是「这个 socket 属于该进程」;两条 `lab-fault-*` 监听靠这条线索定位到 unit,
  但对没有端口 token 的进程(如 sshd)依然 `unresolved`——健康对照机的报告如实写了这一点。
- **N2 的截断量取自策略值**(1 MiB),没有验证不同上限下的行为。
