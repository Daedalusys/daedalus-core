# 项级检出矩阵（9 项故障清单）

这是 `../RESULTS.md` 第 8 节所说「未覆盖」的补充：同一条巡检链路，换成一张 9 项的故障
清单，在同一个真机节点（`lab-a-1e5d63a9`）上跑「全故障 → 复跑 → 清故障 → 恢复 → 稳态」。
清单见 `patrol.toml`，逐项要求模型用 `shell_exec` 取证并报 `problem` / `ok`。

## 轮次与结果

| 轮次 | 现场状态 | 分类 | 邮件 |
| --- | --- | --- | --- |
| B1 | 9 项故障全部在场（failed units / 两个 rogue listen / 1.2G 临时文件 / 应用日志 ERROR / journal 磁盘错误 / chronyd 停止 / crash loop / 3.8MB 普通 journal） | 新增 8 · 1 封 | `mails/B1-新增8.eml` |
| B1-repeat | 同上，紧接一轮 | 持续 8 · 0 封 | 无 |
| B3 | 清掉故障（停单元、删文件、清日志、起 chronyd、vacuum journal） | 恢复 5 · 持续 3 · 1 封 | `mails/B3-恢复5.eml` |
| B4a | 停掉两个 listener 的 systemd 单元（`pkill` 会被单元拉起，必须 `systemctl stop`） | 恢复 3 · 1 封 | `mails/B4a-恢复3.eml` |
| B4b | 稳态复跑（最终二进制，含 evidence.tool 别名与「失败调用不算观测」） | 新增 0 · 恶化 0 · 恢复 0 · 持续 0 · 0 封 | 无 |

每轮目录下保留 `findings.jsonl`（模型逐项原始报告）与 `prompt.txt`（本轮提示词）。
`opencode.json`（客户端配置，含 provider 凭据）**刻意不入库**。

## 逐项检出

B1 的 9 项全部有结论，8 项 `problem` 且每条至少一条工具返回原文：

| item_id | 结论 | 证据要点 |
| --- | --- | --- |
| `failed-units` | problem | `systemctl --failed` 原文（bootc-fetch-apply-updates / lab-fault-crash / lab-fault-fail） |
| `rogue-listen-18080` | problem | `/proc/net/tcp` + `ps`（`ss` 不在节点白名单，模型自行改用可读路径） |
| `root-listen-18081` | problem | 同上，并给出 root 属主 |
| `big-tmp-file` | problem | `ls -lh /tmp/lab-fault-big.bin` 原文（1.2G） |
| `app-log-errors` | problem | `read_file` 取到的 ERROR/FATAL 行原文（`grep` 不在白名单） |
| `journal-disk-errors` | problem | `journalctl -p err` 里的 DISK FAILURE / I/O error 原文 |
| `time-sync` | problem | `systemctl is-active chronyd` → `inactive`（exit 3） |
| `crash-loop` | problem | `journalctl -u lab-fault-crash` 里 status=78 / 启动过快的原文 |
| `bulk-journal-readable` | ok | 3.8MB journal 正常返回，未中断本轮（info 项，只关心不炸） |

## 这一轮暴露的两个问题（已修）

1. **`evidence.tool` 的命名有三套**：客户端显示 `<server>_<网关名>`，网关校验网关名，
   提示词又写了第三种。B1/B3 每轮因此有 8–9 次「不是本轮调用过的工具」被拒。
   修法是两种写法都认（`6aa86dd`）；B4b 的同类拒绝次数为 **0**。
2. **「工具全坏/全被拒」也会算成有观测**：失败调用原先也计入 `CallCount`。
   修法是失败记 `RecordFailure`、只数成功调用（`c1fea31`）；失败文案仍可当 problem 的证据。
