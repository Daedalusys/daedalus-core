# Plan C 异构被控 E2E 证据(模板)

> **状态**:待真机执行后填写。**照实写**,包括与期望不符之处;发现缺陷就开 issue
> 并把 issue 号写进本文件。执行入口:`tests/fleet/plan-c-e2e.sh --hosts ...`。
> 未填写的字段保持 `<待填>`。

**执行日期**:<待填>
**执行人**:<待填>
**控制面版本**:`daedalus-host` <commit/version>
**矩阵**(spec §7.1 已拍):Ubuntu 24.04 / Debian 12 / RHEL 9

---

## 1. 命令原文

```bash
# 逐台安装 daedalus-agent(Plan B 的 install.sh)后:
tests/fleet/plan-c-e2e.sh --hosts ubuntu24=<ip> debian12=<ip> rhel9=<ip>
```

粘贴实际执行的完整命令(含真实 IP/主机名):<待填>

## 2. 被控机基础信息

每台机的 `/usr/lib/os-release` 摘录(至少 `ID` / `VERSION_ID` / `PRETTY_NAME`):

| 机器 | ID | VERSION_ID | PRETTY_NAME |
| --- | --- | --- | --- |
| ubuntu24 | <待填> | <待填> | <待填> |
| debian12 | <待填> | <待填> | <待填> |
| rhel9 | <待填> | <待填> | <待填> |

agent 版本(`daedalus-agent --version` 或单元 `ExecStart` 里的版本):<待填>

## 3. `node list` 输出

```json
<待填:daedalus-host node list 的单行 JSON 原文>
```

对照检查:

- [ ] 每行都有 `support_level`
- [ ] 三台的 `support_level` 实际取值:ubuntu24=<待填> debian12=<待填> rhel9=<待填>
- [ ] 与 spec §7.1 的矩阵一致(三台都应在 `supported`;若某台落在 `experimental`,
      记录原因,不要改判定表去迁就)
- [ ] `snapshot_capability` 各是多少(能 snapshot / 不能):<待填>

## 4. 低危 Plan(`plan-low-nginx.json`)

```json
<待填:daedalus-host plan submit 的 stdout JSON 原文>
```

- [ ] `status == "complete"`
- [ ] 每台机的结果都有非空 `tx_id`
- [ ] 三台机上 `rpm -q nginx` / `dpkg -l nginx` 的实际版本:<待填>

## 5. 高危 Plan(`plan-high-kernel.json`,含一台无回滚点)

```json
<待填>
```

- [ ] `status == "dirty"`
- [ ] 无回滚点那台的 `message` 点名 snapshot 能力(粘贴原文):<待填>
- [ ] 有回滚点的机器是否已经落地(dirty 之前的部分):<待填>

## 6. 人审恢复(三选一,记录走了哪条)

- [ ] `plan advance <plan-id> --skip <node-id>`(接受失败、摘掉该节点)
- [ ] `plan advance <plan-id> --force`(不摘节点,强行续跑)
- [ ] 本地 `daedalus-tx rollback <tx_id>`(控制面**不**代执行远程回滚)

实际走的路径与命令:<待填>

```json
<待填:advance 之后的 stdout JSON 原文>
```

被接受节点的结果是否带 `skipped-by-human:` 前缀:<待填>

## 7. 与期望不符之处

**照实写**。每条格式:`现象 → 期望 → 实际 → 影响 → issue 号`。

<待填>

## 8. 结论

- [ ] 三台异构机都能被登记并如实分级(`support_level`)
- [ ] 低危 Plan 全量落地
- [ ] 高危 Plan 遇无回滚点节点当场暂停且文案点名原因
- [ ] 人在控制面显式选择恢复路径,控制面未自动滚后续批
- [ ] 控制面未代执行任何远程回滚

<待填:补充说明>
