# KVM 靶场：AI 巡检能力基准（lab-a / lab-b，2026-11-23）

## 实验矩阵
| # | 场景 | 严重 | 警告 | 正常 | 未覆盖 | 耗时 | 工具调用 | 错误 | tokens(in/out) |
|---|---|---|---|---|---|---|---|---|---|
| P | pilot 单工具 | - | - | - | - | ~20s | 1 | 0 | 401 / 54 |
| B1 | lab-a 健康 | 1* | 6 | 7 | 9 | 316s | 58 | 19 (33%) | 72,017 / 4,667 |
| F | lab-a 注入6故障 | 3 | 4 | 8 | 6 | 160s | 56 | 19 (34%) | 179,805 / 4,728 |
| B2 | lab-b 健康对照 | 0 | 2 | 9 | 7 | 420s | 92 | 25 (27%) | 87,959 / 6,496 |

*B1 的 1 条严重 = 把更早一次会话（orchestrator 探针）的审计记录判为可疑行为；真实现象，非入侵。
  同一现象在 F 中未复现 → 非确定性。

## 故障检出（盲测，提示词不含故障信息）
| 故障 | 签名 | 结果 | 关键证据 |
|---|---|---|---|
| F1 失败单元 lab-fault-fail | failed unit | 命中 | 严重2；systemd_failed + status + exit 1；指出是 degraded 唯一原因 |
| F2 0.0.0.0:18080 监听 | 多余监听 | 命中 | 严重1；pids=[] 后用 proc_cgroup(3083) 反查到 unit 名 |
| F3 /tmp 1.2G 文件 | 大文件/df | 命中 | 警告5；1,258,291,200 B + df 62% |
| F4a /var/log 250 行 ERROR/FATAL (644) | 新文件+内容 | 命中 | 严重3；list_dir 发现 → read_file 全文 → 归类 |
| F4b journal-only 错误 | journal | 漏检 | 未覆盖；journalctl 被白名单拒 + journal 文件不可读 |
| F5 chronyd 停止 | 服务 inactive | 命中 | 警告4；含停止时间戳、无替代源推理 |

检出 5/6 = 83%；「按设计可检出」集合内 5/5 = 100%。
严重级精确率 3/3 = 100%（lab-b 健康 0 条严重）。

## 产品缺陷（可复现）
1. fs 插件读大文件会打死 MCP server：读 system.journal → "MCP server lab-a-fs wrote more than 16 MiB" → connection closed。
2. 非零退出码一律标 error：`systemctl status <failed>`(3)、`is-system-running`(1=degraded)、`status <not-found>`(4) 全判 error。
   F 轮 19 个 error 里 6 个属此类（假错误占 32%）。
3. listener→PID 归因不可用：proc_fds 读 /proc/<pid>/fd permission denied；proc_listen 恒 pids=[]。
   模型用 proc_cgroup 旁路成功 → 缺口可低成本补齐。
4. 审计写 /tmp（tmpfs），重启即失；/var/log/daedalus/audit.jsonl root:root，agent 自己读不到。
5. SSH 会话不回收：开机 15min 内 81 个 sshd 进程（~65 个 daedalus@notty）、约 0.8GB 常驻。

## 结论
状态轴（单元/服务/端口/磁盘/大文件）有效且可复核；日志轴全黑且如实标注。
