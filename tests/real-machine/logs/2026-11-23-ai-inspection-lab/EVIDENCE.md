# 靶场证据:AI 巡检能力基准(2026-11-23)

本目录是 `docs/superpowers/specs/2026-11-23-ai-inspection-capability-fixes-design.md`
的实测证据。

## 靶场拓扑

两台 atomic guest,从 v0.1.1 qcow2 起独立 overlay,跑在 `Masterwet_Server` 的 KVM 上:

| 节点 | 角色 | 端口 | 内存 |
| --- | --- | --- | --- |
| `lab-a` | 故障靶机 | 9601 | 4096M |
| `lab-b` | 健康对照 | 9602 | 3072M |

SSH 只监听宿主 `127.0.0.1`,未暴露公网;guest 内 `daedalus` 用户免密 sudo。

## 巡检客户端

外部 AI 客户端经 SSH stdio 连 guest 内的 MCP server(10 个插件),只允许 `lab-a-*` /
`lab-b-*` 工具,不许本机 read / glob / grep / shell。提示词固定为「只读巡检,检查当前是否
存在异常」,**不含任何故障信息**。

## 三次运行

| 运行 | 节点 | 场景 | 文件 |
| --- | --- | --- | --- |
| B1 | lab-a | 健康基线 | `report-lab-a-baseline.txt` / `tool-calls-lab-a-baseline.txt` |
| F | lab-a | 注入 6 个故障 | `report-lab-a-faulted.txt` / `tool-calls-lab-a-faulted.txt` |
| B2 | lab-b | 健康对照 | `report-lab-b-baseline.txt` / `tool-calls-lab-b-baseline.txt` |

汇总见 `RESULTS.md`。

## 注入的 6 个故障

`fault-injection.sh` 是注入脚本,逐个故障一个 case:

| 编号 | 故障 | 预期签名 | 检出 |
| --- | --- | --- | --- |
| F1 | `lab-fault-fail.service` 启动即失败 | failed unit | 是 |
| F2 | `lab-fault-listen.service` 起 `0.0.0.0:18080` | 多余监听端口 | 是 |
| F3 | `/tmp/lab-fault-big.bin` 1.2G | 大文件 + `df` | 是 |
| F4a | `/var/log/lab-fault-app.log` 250 行 ERROR/FATAL(0644) | 新文件 + 内容 | 是 |
| F4b | `logger` 写入 journal 的磁盘错误 | journal | **否** |
| F5 | `chronyd` 被停 | 服务 inactive | 是 |

F4b 的漏检是本轮设计的直接起因:插件运行身份能读到 journal,拦路的是 shell 白名单。

## 复现方式

```bash
# 1. 起两台 guest(宿主机上,幂等)
~/daedalus-lab/upguest.sh

# 2. 注入故障(只对 lab-a)
./fault-injection.sh 9601

# 3. 跑一次盲测巡检
./run-inspect.sh lab-a 9601 faulted

# 4. 清理
./fault-cleanup.sh 9601
```

清理后 guest 回到注入前状态:`is-system-running: running`、0 个失败单元、`/tmp` 1%、
`chronyd` active。

## 三条硬断言(本轮设计的验收口径)

1. journal 故障必须检出(之前 100% 漏检)
2. 假错误率从 32% 降到 0
3. 读 8 MiB 的 `system.journal` 不再断连
