# 修复后复测结果(2026-11-24)

对照基准在 `../2026-11-23-ai-inspection-lab/RESULTS.md`。方法与局限见 `EVIDENCE.md`。

## 三条硬断言

| # | 断言 | 上一轮 | 本轮 | 依据 |
| --- | --- | --- | --- | --- |
| 1 | journal 故障必须检出 | **漏检**(journalctl 被白名单拒) | **检出**(读到并交叉验证) | `report-lab-a-faulted-v3.txt` 警告 #8;`probes-direct.txt` 探针 1 |
| 2 | 假错误率 32% → 0 | 6/19 = 32% | **0/42 = 0%** | `tool-calls-lab-a-faulted-v3.txt` 逐条核对 |
| 3 | 读 8 MiB journal 不断连 | 45 MiB 回包 → 连接断开 | **6,283,930 字节回包,不断连** | `probes-direct.txt` 探针 6 |

断言 1 的细节(别混成「全绿」):故障 F4b 的两行 journal 错误**被读到了**并写进了报告,
模型随后用 `journalctl -k -p err`(无条目)与 `df`(无 vdb)交叉验证,判定为「注入式日志噪声,
非真实硬件故障」,归入警告而非严重。**证据链通了**(上一轮是 100% 看不到),但严重级定级
由模型判断,不等于「严重级检出」。

## 假错误率的算法与逐条核对

口径与上一轮一致:**命令真的执行了、并给出了答案,却被包成 MCP error**。

lab-a(F3)共 42 次工具调用,6 次 error,逐条如下:

| # | 调用 | error 内容 | 算假错误吗 |
| --- | --- | --- | --- |
| 1 | `skill` | `Permission denied: skill`(opencode 侧权限,非 Daedalus) | 否 |
| 2 | `fs_read_file /var/log/daedalus/audit.jsonl` | `permission denied`(root 0640) | 否 |
| 3 | `fs_read_file /var/log/boot.log` | `permission denied`(root 0600) | 否 |
| 4 | `shell_exec rpm -Va` | `Command 'rpm' is not in ALLOW_COMMANDS` | 否(策略拒绝) |
| 5 | `shell_exec lsblk -o ...` | `Command 'lsblk' is not in ALLOW_COMMANDS` | 否(策略拒绝) |
| 6 | `shell_exec dnf check --quiet` | `Command 'dnf' is not in ALLOW_COMMANDS` | 否(策略拒绝) |

**假错误 0 条**。上一轮同一口径下有 6 条(`systemctl status <unit>` rc=3/4、
`is-system-running` rc=1 等),本轮全部变成 `isError=false` + `outcome=ran_and_failed`。

lab-b(B3)共 110 次调用,24 次 error:全是 allowlist 拒绝、路径越界、root 文件权限不足、
以及 `service_query` 查不到单元(该插件的既定行为)。同样**没有一条**是「命令跑了但被标 error」。

## 故障检出

| 故障 | 签名 | 上一轮 | 本轮 | 关键证据 |
| --- | --- | --- | --- | --- |
| F1 `lab-fault-fail` | failed unit | 命中 | **命中** | 警告 #2,failed 单元表 |
| F2 `0.0.0.0:18080` | 多余监听 | 命中 | **命中** | 严重 #1,`attribution=cmdline_hint` |
| F3 /tmp 1.2G | 大文件 | 命中 | **命中** | 警告 #4,1,258,291,200 B + tmpfs 62% |
| F4a /var/log 250 行 | 日志文件 | 命中 | **命中** | 警告 #3,`read_file` 读到 200 ERROR + FATAL |
| F4b journal 磁盘错误 | journal | **漏检** | **读到并交叉验证** | 警告 #8,`journalctl -p err` 命中两行 |
| F5 chronyd 停止 | 服务 inactive | 命中 | **命中** | 警告 #6,`ActiveState=inactive` |
| N1 崩溃循环 | journal-only 归因 | — | **命中** | 警告 #2 + `journalctl -u lab-fault-crash` 给出 `status=78/CONFIG` 与缺配置文件的原因 |
| N2 2.58 MB journal | 输出超上限 | — | **不报为故障**(正常) | 探针 5:回包 1,060,996 B,`stdout_truncated=true`,不断连 |
| N3 root 监听 18081 | 归属线索 | — | **命中** | 严重 #1,`cmdline_hint` → `proc_cgroup` → `lab-fault-rootlisten.service` |

8/9 被点名(N2 是「正常应有」的现象,不计检出)。严重级:F3 = 1 条;健康对照 B3 = **0 条严重**。

B3 的 5 条警告都是该机真实状态,不是误报:bootc 自动更新失败(registry `localhost:443`
不存在)、审计日志落在 tmpfs 而 `/var/log/daedalus/audit.jsonl` 不增长、无主机防火墙、
auditd 未安装、`daedalus-audit-rotate.service` 曾因新旧二进制混用失败约 6.5 小时(已恢复)。
其中第 5 条被盲测**独立**诊断出「policy.toml 含未知键: fs.max_read_bytes,
shell.max_output_bytes」——与本轮记录的发版顺序约束完全对上。

## 量化指标

| 指标 | 上一轮 F | 本轮 F3 | 上一轮 B2 | 本轮 B3 |
| --- | --- | --- | --- | --- |
| 检出故障 | 5/6 | 8/9 | — | — |
| 严重级误报 | 0(健康机) | 0(健康机) | 0 | 0 |
| 假错误率 | 32%(6/19) | **0%(0/42)** | 27% | 0%(0/110) |
| 工具调用 | 56 | 42 | 92 | 110 |
| 耗时 | 160s | 136s | 420s | 359s |
| tokens(in/out) | 179,805 / 4,728 | 87,614 / 3,720 | 87,959 / 6,496 | 202,908 / 8,644 |
| 8 MiB journal 回包 | 45 MiB → 断连 | 6,283,930 B | — | — |

B3 的 token 与调用数比上一轮高,原因是它把能用的工具都用了一遍(含 `journalctl -k`、
`dnf_query`、多次 `dupe_scan_*`);F3 反而更省——上一轮反复重试被拒的 `journalctl` 与
被断连打断的 `read_file`,本轮一次就成。

## 本轮新发现的问题

1. **`journalctl -g <正则>` 被当成路径拒绝**(SDK `shellpolicy.IsPathLike` 对任何含 `/`
   的参数返回 true)。已开 [daedalus-sdk#72](https://github.com/Daedalusys/daedalus-sdk/issues/72)。
   靶场里健康对照机的 `journalctl -g "...I/O error..."` 就是这样被拒的。
2. **新策略 + 旧二进制 = 插件拒启动**(实测 `daedalus-audit-rotate.service` 因
   `policy.toml 含未知键: fs.max_read_bytes, shell.max_output_bytes` 每 10 分钟失败一次)。
   这不是 bug,是发版顺序的硬约束;已写进 `EVIDENCE.md`。
3. **`service_query` 查不到单元时返回工具 error**(既定行为,有单测钉住)。对巡检语义而言
   「单元不存在」是答案而非失败,会让 error 计数失真。本轮未改,记为待议。
4. 上一轮记的「审计日志写 /tmp(tmpfs) 重启即失」「SSH 会话不回收」两条本轮未复测,仍待处理。

## 复现

```bash
# 1) 把新二进制与新策略装进 guest(见 EVIDENCE.md 的「就地替换」)
# 2) 注入 9 个故障
tests/real-machine/logs/2026-11-24-ai-inspection-lab-fixed/fault-injection-v2.sh 9601
# 3) 盲测巡检(提示词不含故障信息)
~/daedalus-lab/run-inspect.sh lab-a 9601 faulted-v3
# 4) 直连探针(量字节数与 outcome)
tests/real-machine/logs/2026-11-24-ai-inspection-lab-fixed/probe-capabilities.sh 9601
```

## 镜像侧(与本目录的证据分开看)

本机 CPU 不支持 x86-64-v3,镜像构建改由 CI 做;同一 commit `95b65df` 的 run
`45295995487` 全绿:Test(go/deno/i18n/drift)、镜像构建与推送(插件 zip 取
daedalus-plugins v0.9.0、SDK 取 v0.5.0,镜像内 76 门禁自校验)、bootc switch + rollback、
ISO 无人值守安装、guest 探针 A3–A12、qcow2 构建、real KVM boot。

这证明「镜像能构建、能启动、插件清单自校验通过」;**不证明**靶场跑的就是新镜像(靶场仍是
v0.1.1 + 就地替换)。
