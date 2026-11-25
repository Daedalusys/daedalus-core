# 巡检 + 邮件通知靶场证据（2026-11-24 / 执行于 2026-11-25）

本目录记录 `daedalus-patrol` 在真机靶场上的一次端到端验收：控制面跑一轮只读巡检，
节点侧 agent 经 mTLS gRPC 暴露只读工具，AI 客户端（opencode）只经本轮 MCP 网关动手，
发现走 `report_finding` 落盘，变化经本机 SMTP 抓取器投递。

**结论先行**：巡检 → 网关 → agent → 插件 → 证据 → 分类 → 邮件这条链路在真机上成立；
「只有变化才发信」「持续不发」「恢复发一封」「节点不可达不误报恢复」四条硬要求都有
邮件原文与状态文件佐证。**未覆盖**：计划里 F1/F2/F4a/F5/N1/N2/N3 的逐项检出矩阵
（原因见末节）。

## 1. 靶场拓扑

| 角色 | 位置 | 说明 |
| --- | --- | --- |
| 节点 `lab-a` | KVM guest，宿主 `Masterwet_Server`，SSH 端口 9601 | AlmaLinux bootc guest；agent 监听 `:9443` |
| 控制面 | 本机（`/var/lofibass_ssd/code/Daedalus/daedalus-core`） | 跑 `daedalus-patrol`；经 SSH 隧道 `127.0.0.1:9443` 连节点 |
| SMTP 抓取器 | 本机 `127.0.0.1:2529` | `smtp-sink.py`，不投递、不鉴权，原样落 `.eml` |

节点侧 agent 是 `daedalus-agent v0.2.0`（release 资产
`daedalus-agent_0.2.0_linux_amd64.tar.gz`，sha256
`95e5f98bca0ec2300a45ee14b635eeb2032d9417cd056cdd19a7272c9dc304ef`）。

## 2. 与生产形态的两处差异（必须说清）

1. **agent 没有走 `install.sh`**：guest 的 `/opt` 在 composefs 上是只读的，`install.sh`
   在 `/opt/daedalus-agent` 建目录会失败。本轮改为把二进制装到 `/usr/local/bin/daedalus-agent`，
   单元文件按 `install.sh` 的 `UNIT_BODY` 原样写（只改 `ExecStart` 路径）。这不是产品缺陷，
   是靶场的安装面差异；镜像里由 CI 的打包路径负责。
2. **巡检进程以普通用户 `lofibass` 运行**，不是 systemd 单元里的 root。因此
   `StateDirectory=`、`LoadCredential=`、`ProtectSystem=strict` 这些单元侧行为
   **没有被这条证据覆盖**，只覆盖了 `daedalus-patrol` 二进制本身。单元文件的解析级校验由
   CI 的 `systemd-units` 门禁承担。

## 3. 节点侧配置

CA 由本轮现场自签（同一张 CA 签服务端证书与客户端证书），铺进
`/etc/daedalus-agent/tls/{ca.pem,agent.pem,agent.key,ca.key}`；服务端证书 SAN 含
`IP:127.0.0.1`（控制面经 SSH 隧道拨号）。

远程工具白名单见 `agent-policy.toml`：

```toml
[remote_tools]
"daedalus.shell"   = ["shell_exec"]
"daedalus.service" = ["service.query", "service.list"]
"daedalus.fs"      = ["read_file", "list_dir"]
```

控制面 `patrol.toml` 见本目录（4 个清单项 + SMTP 指向抓取器 + `opencode` 作为 AI 客户端）。
清单刻意写得字面化（「输出非空就 problem」），为的是让「检出」与「模型文风」解耦。

## 4. 运行矩阵与结果

同一节点连续 8 轮，状态目录跨轮保留。`mails` 列是本轮新增邮件数（抓取器计数）。

| 轮次 | 注入 / 前置 | 期望 | 实际 | 证据 |
| --- | --- | --- | --- | --- |
| A1 | 无（基线；节点上已有 2 个 failed 单元） | 新增 1（service-health），发 1 封 | 新增 1 · 持续 0 · 邮件 1 | `mails/001.eml` |
| A2 | 无变化 | 不发信 | 新增 0 · 持续 1 · 邮件 0 | `rounds/*-011315Z/` |
| A3 | `dd` 造 1.2G 的 `/tmp/lab-fault-big.bin` | 新增 1（fault-file），发 1 封 | 新增 1 · 持续 1 · 邮件 1 | `mails/002.eml` |
| A4 | 无变化 | 不发信 | 新增 0 · 持续 2 · 邮件 0 | `rounds/*-011805Z/` |
| A5 | 删除 `/tmp/lab-fault-big.bin` | 恢复 1（fault-file），发 1 封 | 恢复 1 · 持续 1 · 邮件 1 | `mails/003.eml` |
| A6 | journal-only 磁盘错误（F4b：`logger -p kern.err "I/O error, dev vdb, sector 2048 op 0x0"` + `DISK FAILURE …`） | 新增 1（journal-disk-errors），证据是 journal 原文 | 新增 1 · 邮件 1；邮件正文含 `DISK FAILURE predicted on /dev/vdb, array degraded, 3 of 5 members offline` | `mails/004.eml` |
| A7 | `systemctl stop daedalus-agent`（节点不可达） | 本轮 exit 1；只报巡检失败，**不得**把上一轮两项误报「恢复」 | `新增 1 · 恶化 0 · 恢复 0 · 持续 2`；邮件只有「新增 patrol-self-check」；状态仍保留 service-health 与 journal-disk-errors | `mails/005.eml` |
| A8 | `systemctl start daedalus-agent` | 恢复 1（patrol-self-check），旧问题不重报「新增」 | 恢复 1 · 持续 2 · 邮件 1 | `mails/006.eml` |
| A7b | 再次 `stop daedalus-agent`（A7 的复跑） | 同 A7 | 同 A7（新增 1 · 恢复 0 · 持续 2，exit 1，邮件 1） | `mails/007.eml` |
| A8b | 再次 `start daedalus-agent`（A8 的复跑） | 同 A8 | 同 A8（恢复 1 · 持续 2，邮件 1） | `mails/008.eml` |
| A7b / A8b | 把 A7 / A8 再跑一遍（停 agent → 起 agent） | 与 A7 / A8 同形，证明可重复 | 失败轮 `新增 1 · 恢复 0 · 持续 2`；恢复轮 `恢复 1 · 持续 2` | `mails/007.eml` / `mails/008.eml` |

A7 是本次返工的核心回归点：修复前，节点不可达的一轮会把上一轮所有活跃项算成「恢复」
并覆盖状态；修复后失败轮只写「上一轮活跃项 ∪ 本轮 self-check」，邮件里不会出现假恢复。

## 5. 能不能证明这几件事

**能证明**：

- 控制面能起 MCP 网关（只绑 `127.0.0.1`、每轮随机端口与令牌），AI 客户端能以
  remote MCP + Bearer 连入并只看到「节点只读工具 + `report_finding`」。
- 节点侧双闸生效：`shell_exec` / `service.query` / `service.list` / `read_file` / `list_dir`
  能被远程调用（白名单 ∩ 插件自报 `ReadOnlyHint=true`），清单里没有的写工具不可见。
- 发现的结构化字段由 `report_finding` 落盘，证据是工具返回原文（A6 的磁盘错误行是
  journal 原文，邮件里逐字可见）。
- 邮件只在「新增 / 恶化 / 恢复」非空时发；持续项与无变化轮不发。
- 节点不可达时本轮失败（exit 1）、发一条巡检级发现、不误报恢复、不丢上轮状态。

**不能证明**：

- 计划里 F1（failed 单元）/ F2（0.0.0.0 监听）/ F4a（/var/log 刷屏）/ F5（chronyd 停止）/
  N1（崩溃循环 + StartLimit）/ N2（journal 灌 40MiB）/ N3（root 监听 18081）的逐项检出。
  本轮清单只有 4 项，不覆盖这些轴；要复现需要按故障类型扩清单，属于巡检内容设计，
  不是链路问题。
- 镜像内安装：单元随镜像落位、`daedalus-patrol.timer` 的调度、`ProtectSystem=strict`
  沙箱下 opencode（Bun）能否启动 —— 这些要等镜像构建（CI），本机 CPU 缺 x86-64-v3
  跑不了镜像。
- systemd 单元的 root 形态（`StateDirectory=`、`LoadCredential=`、`HOME` 只读挂载）。
- 多节点：只跑了 `lab-a` 一台。

## 6. 现场命令

- 跑一轮：`run-round.sh <tag>`（脚本里是 `daedalus-patrol run --node … --config … --state-dir … --round-root …`）。
- 连节点：`gssh.sh 9601 '<cmd>'`（ProxyCommand 经 `Masterwet_Server` 跳板）。
- 抓邮件：`smtp-sink.py <outdir> <port>`。

`rounds/` 下每轮保留 `findings.jsonl` 与 `prompt.txt`；AI 客户端的 stdout 日志（每轮约
200 KB）没有入库，正文只留结论所需的邮件与发现。
