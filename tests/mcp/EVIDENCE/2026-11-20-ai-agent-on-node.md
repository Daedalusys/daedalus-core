# 「外部 AI agent 住在节点上」能不能用 —— 真实客户端实测(2026-11-20)

| 项目 | 内容 |
| --- | --- |
| **status** | 已执行:5 个真实 AI 会话 × 13 个能力服务器,审计链逐次验证 |
| **问题** | 外部 AI agent 接入 Daedalus 后,能不能真的开始运维?缺什么? |
| **驱动** | `tests/mcp/spike-ai-agent.sh`(建立 policy 副本 + 客户端配置 + 5 个用例) |
| **摘要器** | `tests/mcp/spike-ai-agent-report.py`(把 JSON 事件流压成「实际调用了什么」) |
| **客户端** | opencode v2.3.2(真实模型),MCP 走 **stdio** |
| **服务端** | 本仓 HEAD 现构建的 15 个二进制(13 能力服务器 + `daedalus-audit` + `daedalus-tx`) |
| **策略** | `files/system/opt/daedalus/shared/policy.toml` 的**逐字副本**,只改三条路径(审计落点、允许目录) |
| **环境** | AlmaLinux 9.8 宿主,`/dev/kvm` 可用但**未起 guest**(见 §6 未覆盖项) |

## 1. 怎么搭的

```bash
bash tests/mcp/spike-ai-agent.sh build   # 15 个二进制 → $WS/bin
bash tests/mcp/spike-ai-agent.sh all     # policy 副本 + 客户端配置 + t0..t4
python3 tests/mcp/spike-ai-agent-report.py t0 t1 t2 t3 t4
```

两个 agent 是本次要对照的两种「接入形态」:

| agent | 权限 | 对应现实 |
| --- | --- | --- |
| `daedalus-mcp-only` | `bash/edit/write/webfetch/task = deny`,其余 allow | A-沙箱:客户端不给 shell,只能走 MCP |
| `daedalus-full` | 全 allow | A-裸奔:客户端自带 shell(Claude Code / Codex / opencode 的默认形态) |

## 2. 五个用例与结果

| # | 任务 | 结果 | 关键事实 |
| --- | --- | --- | --- |
| t0 | 列目录 | ✅ | `daedalus-fs.list_dir` 直接可用;`[]` 是因为目录本来就空,而**这次调用自己让 `audit.jsonl` 出现了**;越界路径(`$WS` 本身)被 `Access denied` 明确拒 |
| t1 | 诊断故障单元 | ✅(有摩擦) | `systemd_failed` 定位到 `dnf-makecache.service`;`journalctl` **不在 15 条白名单**里,agent 改用 `fs.read_file` 直读 `/var/log/hawkey.log` 拿到日志;`systemctl` 一条命令都没用上 |
| t2 | 写文件 + 读回 | ✅ | `fs.write_file` 成功且落链(`identity=daedalus-fs`),读回一致 |
| t3 | 把 nginx 拉起来 | ❌(能力缺口) | 没有通用服务生命周期入口;agent 判定「没有可走的路」后**切到自己的 shell**,跑 `systemctl` / `docker` / `rpm` / `curl` —— 完全离开能力层 |
| t4 | MCP 写 + 自己 shell 写 | ✅/❌ | MCP 写成功并落链;自己 shell 在 `mcp-only` 下确实不可用(`shell-probe.txt` 不存在) |

### 审计链(每个用例前清空,故计数只用来看「这一步写了什么」)

| 用例 | 条目 | verify |
| --- | --- | --- |
| t0 | 5 | intact |
| t1 | 16 | intact |
| t2 | 2 | intact |
| t3 | 5(**只有 MCP 调用**) | intact |
| t4 | 2 | intact |

t3 的条目里**没有** agent 自己 shell 跑的那些命令 —— 旁路动作天然不在链上(链只记能力层的动作)。这意味着:**一旦客户端有 shell,链就不再是全量证据。**

## 3. 结论

### 3.1 已经能用的(比预期好)

1. **接入是通的**:一个真实 AI 客户端能 stdio 连上 13 个服务器,发现工具、调用、拿结果,不需要改产品代码。
2. **写操作有审计且 fail-closed**:`fs.write_file` 落链(`fs/cmd/daedalus-fs/audit.go` 的 `recordAudit`),写链失败时返回错误结果(`auditBrokenResult`)而不是静默继续。**注意别只看 `main.go`** —— 审计面在 `audit.go`,只看 `main.go` 会误判为「fs 写没有审计」。
3. **只读诊断面够宽**:systemd 失败单元、依赖、定时器、硬件、进程/端口/文件描述符、包查询、OS 状态、事务轨迹都能一次拿到。
4. **策略边界在服务端是硬的**:填错目录被 `Access denied` 明确拒(`pathguard`);不在白名单的命令返回 rc=126(`../daedalus-sdk/shellpolicy`,Go 单测钉死),超时 rc=124,不是静默失败。

### 3.2 还差的三件事(按影响排序)

1. **没有通用运维动作入口 —— 能诊断、能改配置文件,不能「把服务拉起来」。**
   13 个服务器里能改状态的只有这 4 处,全部围绕「文件/配置」,唯一能碰服务状态的只有 `blueprint_apply` 的 reload 副作用:
   - `fs.write_file` / `fs.move_file`(/home、/var/log、/tmp 内,服务端 pathguard);
   - `organize_apply`(文件整理,经 tx `organize.move` 适配器);
   - `diskclean` 的写工具(经 tx `disk.clean` 适配器);
   - `blueprint_apply`(写 `[blueprints].output_dirs` 内的配置 + 可选 `systemctl restart` 4 个白名单服务;但它是 **v1-direct**,不经 tx,`tx_id` 恒为 `(v1-direct)`)。
   而 `service.set` / `package.set` / `task.set` / `container.set` / `policy.set` 全部只在 `daedalus-tx` CLI 里,MCP 与 shell 白名单都触达不到。**结论:agent 没有任何「拉起/停止任意服务」「装包」「改任务」「改策略」的入口。** t3 就是它撞在这堵墙上的实录。
2. **错误语义把有效数据吞掉。** `shell/cmd/daedalus-shell/main.go` 里 `IsError: res.Returncode != 0` —— **任何非零退出码都变成 MCP error**,而「跑完了但命令失败」(rc=1/3)、「被策略拦」(rc=126)、「超时」(rc=124)在客户端看来形状完全一样,都是 `Error: {stdout, stderr, returncode}` 字符串。`systemctl status <failed>` 就是 rc=3 + stdout 里有完整日志的例子:数据在,却被标成错误。对 AI 客户端来说,这三者必须可区分。
3. **接入边界只能靠服务端守。** t3/t4 证明:客户端自己的 permission 只约束它自己的工具,MCP 子进程的副作用不受约束;反过来说,**客户端自带 shell 时,policy / pathguard / 审计都可被绕开**。要真隔离,必须由宿主侧把「客户端能做什么」限死(见 3.3)。

### 3.3 一个必须说清的细节:所谓 `mcp-only` 并不只走 MCP

即使把 `bash/edit/write` 全 deny,t4 里 agent 自己盘出来的原生工具仍然有:`read` / `glob` / `grep` / `execute`(Code Mode 运行时)/ `websearch` / `question` / `skill`,并且它确实用 `read` 和 `glob` 交叉验证了 MCP 写出来的文件。它们的权限来自 `*: allow`,而**仓库里没有任何一份「外部 AI 客户端应该 deny 什么」的配置样例**(`webfetch` deny 了,`websearch` 没 deny,就是现成的不一致)。要真隔离,必须逐项枚举 deny,而不是只 deny `bash`。

### 3.4 搬运成本(agent 侧观察)

- **工具发现靠猜**:5 个会话里 agent 一共用了 **13 次 `search`** 来定位「有没有某个工具 / 某工具的参数是什么」;t4 为确认「有没有 shell」单独花了 3 次。13 个服务器 × 3~6 个工具,对上下文是真实开销。
- **能力边界要自己摸**:agent 不知道白名单是哪 15 条命令,只能试;t1 最终绕开 `journalctl` 走 `fs.read_file`,是它自己试出来的路径,不是文档告诉它的。

## 4. 建议的下一步(按依赖顺序)

1. **补一个受控变更入口**:把 `service.set`(先这一个)包成 MCP 工具,内部走 `begin→propose→apply` + 确认令牌,审计由 tx 侧盖章。这是「agent 能运维」的最小闭环,也是风险最高的一步,必须先定死「谁能确认」。
2. **修 `shell_exec` 的退出码语义**:非零退出码返回「结构化结果 + returncode」而不是 `Error`,并在结果里区分 `denied`(126)/`timeout`(124)/`ran-and-failed`。
3. **写一份《外部 AI 客户端接入》文档**:配置样例(按本机实际路径生成,而不是手抄 `examples/mcp_client_config.json`)、必须 deny 的工具清单、审计覆盖范围(能力层内 vs 客户端 shell)、以及「有 shell 就等于没有边界」这句要写在最前面。
4. **再跑一次真 guest 版**:本脚本原样可用,只需把 `WS` 指到 guest 内、二进制在 guest 内构建。

## 5. 顺带发现的文档漂移

`AGENTS.md` §4c 原写「渲染内容经 `daedalus-tx` `blueprint.write` 适配器落盘」——**该适配器从未落地**,tx 注册表里只有 `service.set`/`package.set`/`disk.clean`/`organize.move`/`task.set`/`container.set`/`policy.set`;`blueprint_apply` 是 v1-direct。已在本次提交里改正(`VISION.md` 本来就写的是 v1-direct)。

## 6. 未做 / 不确定

- **没有起 KVM guest**:本机没有 `qemu-system-x86_64`,也没有可用 qcow2。所以本次是「宿主 + 真实二进制」,不是「真 Daedalus 镜像」。差别在于:镜像里的 systemd 单元 / DynamicUser / policy 落点 / 只读根,这些没被覆盖。
- **只测了一个客户端**:opencode。Claude Code / Codex 的权限模型不同,但「有 shell 就能绕开」这条与客户端无关。
- **没有测多轮/长会话**:单次 `opencode run`,不评估上下文压力与重复调用成本。

## 7. 复现

```bash
# 宿主版(本次)
bash tests/mcp/spike-ai-agent.sh build
bash tests/mcp/spike-ai-agent.sh all
python3 tests/mcp/spike-ai-agent-report.py t0 t1 t2 t3 t4

# guest 版(需要先有镜像;把 WS 指到 guest 内,binary 需在 guest 内构建或拷贝)
WS=/path/in/guest bash tests/mcp/spike-ai-agent.sh all
```
