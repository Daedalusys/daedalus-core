# daedalus/plugin — 官方预置插件定义层(源码态)

三层目录结构(决策 23/24)中的**插件层**:每个官方预置插件一个子目录,
内含清单 `daedalus.plugin.json` 与其运行所需的全部源码/二进制。
本目录是**源码态与打包输入**;镜像内的权威形态是安装态
`daedalus/files/system/opt/daedalus/plugins/<id>/`(构建产物,经 Pack→Verify 生成,
manifest 带逐条目 sha256 checksums)。本目录内容绝不直接进镜像 rootfs。

## 产品定位:command advisor(命令顾问)

copilot 插件(`daedalus.copilot`)的产品定位是 **command advisor / 命令顾问**:
理解自然语言意图 → 翻译成白名单内的 shell 命令并**生成命令** → 逐条**风险标注** →
由**用户手动执行**。不是 agent:插件自身从不替用户执行命令。

**风险分级**(本地静态 classifier 判定,LLM 不参与自标注,防 prompt injection):

| 级别 | 语义 | 处理 |
|------|------|------|
| `L0` / safe | 安全且在 15 命令白名单内 | 可经 `daedalus-shell` 沙箱执行(y/n 确认后) |
| `L1` / cautious | 谨慎(有副作用/需人工判断) | **仅展示**命令 + 风险理由,提示用户手动执行 |
| `L2` / danger | 危险(破坏性/不可逆) | **仅展示**命令 + 🚨 风险理由,提示用户手动执行 |

即:L1/L2 命令架构上不进执行通道,只有 L0(安全 + 白名单内)可在用户确认后
经 `daedalus-shell` 沙箱执行;白名单外命令一律不自动执行。

## 目录布局

```
plugin/
├── copilot/            # type=copilot, runtime=deno —— Daedalus 命令顾问 CLI (command advisor)
│   ├── daedalus.plugin.json      # 清单(entrypoint 携带 deno run 权限旗标)
│   ├── {main,policy,audit,exec,llm}.ts        # 源码(5 个,打包进插件)
│   └── {main,policy,audit,exec,llm}.test.ts   # deno test 单元测试(不打包)
├── fs/                 # type=capability, runtime=native —— 文件系统能力
│   ├── daedalus.plugin.json
│   └── bin/daedalus-fs             # 由 just plugin-pack 从 daedalus/core 构建拷入
├── shell/              # 同上:shell_exec(15 命令白名单,权威在 core/internal/shellpolicy)
├── pkg/                # 同上:dnf/rpm 只读查询
├── sysinfo/            # 同上:os-release/hardware/network 只读探测
└── service/            # 同上:service.query/service.list(systemd 单元只读观测)
    ├── daedalus.plugin.json      # 唯一在源码侧声明 resources 的官方清单
    └── bin/daedalus-service
```

## 清单格式(`daedalus.plugin.json`)

schema 与校验的单一事实源: `daedalus/core/internal/plugin/manifest.go`。

| 字段 | 必填 | 说明 |
|------|------|------|
| `id` | ✔ | 文法 `^[a-z0-9]+(\.[a-z0-9]+)*$`(无连字符),目录名 = id(`daedalus.copilot`) |
| `name` / `version` | ✔ | 展示名;version 为 semver 2.0.0 文法 |
| `type` | ✔ | 枚举 `copilot` / `capability` |
| `runtime` | ✔ | 枚举 `native`(Go 静态二进制直接 exec)/ `deno`(宿主拼 `deno run <entrypoint> <executable>`) |
| `executable` | ✔ | 包内相对路径(native 如 `bin/daedalus-shell`;deno 如 `main.ts`),打包时必须有可执行位 |
| `entrypoint` | 可选 | deno runtime 的权限旗标列表;**不要写 `run`**(宿主已自动前置 `deno run`);`$HOME` 占位由 wrapper 在 argv 层展开 |
| `permissions` | 可选 | `{read,write,run}` 路径白名单,与 entrypoint 旗标逐项一致 |
| `tools` | capability 必填 | 声明的 MCP 工具名;76-daedalus-plugin-gen.sh 构建期与二进制 stdio `tools/list` 交叉核对,漂移即拒绝 |
| `resources` | 可选 | Object Model 资源声明数组,条目为 `kind`/`name`/`desired_state` 三字段元组;schema 单一事实源 `daedalus/core/internal/objectmodel/objectmodel.go`。声明≠授权:kind 放行由 `policy.toml` `[objectmodel].enabled_kinds` 网关 fail-closed 强制;76-daedalus-plugin-gen.sh 构建期交叉核对声明 kind ⊆ 启用集,漂移即拒构建。写法详见下方「Object Model 与资源声明」 |
| `checksums` | 安装态必填 | 由 `daedalus-plugin-pack` 注入(逐条目 sha256 + manifest 规范化自摘要);源码态清单**不得手填** |

## Object Model 与资源声明(决策 25 落地)

> 本节面向**插件开发者**:新插件如何声明资源、kind 词表从哪查、变更走哪条通道。
> 全局对象模型决策与执行模型细节见仓库根 `AGENTS.md` 的 `## Object Model` 段
> (plan `.omo/plans/aios-object-model-alignment.md` 决策 25),此处不重复。

### 新插件入口:`daedalus.service`(type=capability, runtime=native)

- **工具**:`service.query` / `service.list`,systemd 单元**只读**观测
  (`systemctl show` 限定 7 项 curated 只读属性;`systemctl list-units --type=service` 列表)。
  实现在 `daedalus/core/cmd/daedalus-service/`(`main.go` 注册 + `list.go` 解析),
  回包载荷为 `objectmodel.ServiceState`(`kind`/`name`/`desired_state`/`properties`,
  Properties 键为 systemctl 属性名原文)。
- **严格只读**:本插件不暴露任何变更工具,manifest `permissions.write` 为空;
  service 资源的**状态变更一律经 `daedalus-tx` 事务通道**(`service.set` 适配器,
  begin→propose→apply→rollback),绕过即破坏审计链(根 `AGENTS.md` 反模式条款)。
- **资源声明**:源码侧 manifest 用 `"resources": [{ "kind": "service", "name": "*" }]`
  声明"我管理 service 类的全部实例";目前是**唯一**写 `resources` 的官方插件,
  其余 4 能力与 copilot 暂不声明。安装态清单里的 `resources` 补全与 checksums 注入
  同为 `just plugin-pack` 构建产物,勿手改。

### 资源声明怎么写(manifest `resources` 字段)

条目 schema 与校验的单一事实源:`daedalus/core/internal/objectmodel/objectmodel.go`;
清单校验层(`daedalus/core/internal/plugin/manifest.go`)逐条目委托 `ValidateResource`。

- **三字段元组**:`kind`(资源类别,封闭枚举)+ `name`(单段资源名:拒空、拒空字节、
  拒 `/` 与 `..`,不携带路径语义;`"*"` 为全类通配)+ `desired_state`(取值词汇归各
  provider 领域,如 service 的 `active`/`inactive`,本层允许空、不枚举)。
  id/metadata 等其它维度未进入 v1 声明模式。
- **kind 词表从哪查**:`objectmodel.AllKinds()` 共 7 类
  (`service`/`package`/`container`/`capability`/`task`/`transaction`/`policy`);
  **v1 仅 `service` 有 provider**,其余六类是保留枚举位。
- **新增资源种类的改动面**(三点漂移测试拒绝漏改任何一处):
  `objectmodel.go` 加 Kind 常量并登记 `kindRegistry` → `internal/policy` 的
  `Default()` → `policy.toml` 的 `[objectmodel].enabled_kinds`。
- **声明≠授权**:校验器接受保留 kind(类型层开放),策略网关 fail-closed 决定是否放行
  (v1 仅启用 `service`);`76-daedalus-plugin-gen.sh` 构建期再交叉核对
  声明 kind ⊆ 启用集,漂移即拒构建。
- **transactable 不是清单字段**:资源是否事务化不写在 manifest 里,由"该 kind 是否有
  `daedalus-tx` 适配器"决定(v1 仅 `service.set`);无适配器的资源连 begin→propose→apply
  序列都无法发起(策略网关 + 适配器注册表双重拒绝)。

### 状态字段归哪

`ActiveState`/`SubState` 等**观测态不在声明 schema 里**:查询结果走 `ServiceState.properties`;
成功观测另经 `daedalus/core/internal/state/` 追加进 `state.jsonl`(`StateEntry` 行,
payload 为序列化的 `ServiceState`)。state 是**派生缓存**,与哈希链审计(证据层)分离;
v1 状态记忆按上下文隔离(DynamicUser 命名空间,决策 25 补充条款,细节见根 `AGENTS.md`)。

### `daedalus-tx`:带外事务 CLI(非插件)

`daedalus-tx` **不在本目录**、无 `daedalus.plugin.json`、无 systemd 单元(v1 执行模型:
用户态调用的 CLI,`cmd/daedalus-tx/`;用户作用域由适配器路径守卫无条件强制,装饰性单元是反模式)。
`just plugin-pack` 把它装到 `daedalus/files/system/usr/local/bin/daedalus-tx`(镜像内 `/usr/local/bin/`)。
子命令 `begin`/`propose`/`apply`/`rollback`/`status`;只有 begin/apply/rollback 在审计哈希链上
盖 `TxID`+`TxStep`,propose/status 发空 TxID 条目(链校验语义见 plan 决策 25)。

### Copilot 的事务通道

命令顾问自此有**两条执行通道**,同由本地静态 classifier 把关(LLM 从不自标注风险):

| 通道 | 提议形态 | L0(safe) | L1 / L2 |
|------|---------|-----------|---------|
| shell | 白名单内普通 shell 命令 | y/n 后经 `daedalus-shell` 沙箱执行 | 仅展示 + 手动执行提示(既有约定不变) |
| transaction | 保留动词 `tx.propose` / `tx.apply` / `tx.rollback`(args = `[目标, 期望态?]`) | `classifyTxProposal`(`daedalus/plugin/copilot/policy.ts`)定级:`tx_propose` 恒 safe;`tx_apply` 的 `started`/`stopped` safe → 走事务执行通道 | 其余定级:`tx_apply` 的 `restarted`/`enabled`/`disabled` 与 `tx_rollback` 为 caution、`reload` 为 danger → **仅展示,绝不进入 apply** |

- **L0 事务五步**(`daedalus/plugin/copilot/main.ts` 的 `runTxTurn`):
  `begin`(开账取 tx_id)→ `propose`(`service.set` 适配器登记步骤,仅快照 Before/AfterState,
  无副作用)→ `preview`(展示计划前→计划后 diff)→ **y/n 确认** → `apply`(真实副作用)。
  `-y` / 非 TTY 视为已授权(审计记 `auto: true`);`--dry-run` 三步零调用(不开账、不落 journal)。
- 用户拒绝(`n`)放弃事务:journal 停在 proposed 态、零副作用,决策同样落哈希链审计。
- **事务提议不经 shell 白名单**:`tx.apply` 不在 15 命令集是设计使然;两条通道的执行
  都收敛在带外二进制(`daedalus-shell` / `daedalus-tx`)上,copilot 进程自身零裸执行。

## 打包与安装流水线

- **copilot**: `./scripts/pack-copilot-plugin.sh` —— 暂存 5 个 `.ts`(排除 `.test.ts`)+ 清单
  → Pack 注入 checksums → `-verify --keep` 解压安装态(解压即完整校验,摘要不符拒绝安装)。
  命令顾问(command advisor)插件:L0 之外的风险级别仅展示、由用户手动执行。
- **5 能力插件(fs/shell/pkg/sysinfo/service)**: `just plugin-pack` —— 构建 Go 二进制拷入各自 `bin/` → 同一 Pack→Verify 流程;
  顺带安装宿主与 copilot 运行期依赖的审计/执行/事务二进制到
  `daedalus/files/system/usr/local/bin/daedalus-{host,audit,shell,service,tx}`（task 21 + plan todo 17;
  `daedalus-tx` 即走该 out-of-band 安装位,见下方「Object Model 与资源声明」）。
- 安装态入库后经 `just sync`(rsync `files/system/` leg)进镜像 `/opt/daedalus/plugins/`;
  `just sync` 另有保守 leg 把本目录同步到 `base_image/plugin/` 仅作构建上下文,不进镜像。
- 运行期消费方: 宿主 `daedalus-host list/verify/run-plugin`;systemd 单元由
  `76-daedalus-plugin-gen.sh` 经 `render-unit` 按 manifest 渲染。

## i18n 多语言支持(强制约定)

所有插件的 UI 字符串必须经 `i18n.ts` 的 `t(key, ...args)` 走,不在源码里硬编码。

- **locale 文件**:放在插件目录 `i18n/<locale>.json` 下(POSIX 下划线命名,
  `en_US` / `zh_CN` / `ja_JP` 等;如 `i18n/en_US.json`、`i18n/zh_CN.json`)。
- **manifest 声明**:`daedalus.plugin.json` 加 `"i18n": ["en_US", "zh_CN"]` 数组字段
  (数组形式,第一个是默认 locale)。
- **en_US 必定位兜底**:任何插件都不能省;en_US 不在(声明或文件任一侧缺失且无法兜底)
  即校验失败。
- **声明 ↔ 文件严格一致**:manifest 声明的 locale 集合必须与 `i18n/` 目录实物完全一致;
  漂移 CI 拒(严模式 exit 1)。开发者加新 locale 后用
  `scripts/plugin-i18n-sync.sh --autofix`(即 `just i18n-sync-autofix`)一键同步 manifest。
- **命名约定**:key 用点分风格(如 `confirm.prompt` / `verbose.preview`),翻译文本里
  用 `{0}` `{1}` 占位符(printf 风格,由 `t(key, ...args)` 依序填充)。
- **locale 探测顺序**:env `LC_ALL` > `LANG` > `en_US`。
- **文件级 fallback 链**:精确匹配(如 `zh_CN`)→ 语言级回退(如 `zh`)→ 兜底 `en_US`。
- **工具**:`just i18n-sync`(CI 守门,严模式)/ `just i18n-sync-autofix`(开发,
  以 `i18n/` 目录实物为准改写 manifest)。打包侧 `scripts/pack-copilot-plugin.sh`
  会把 `i18n/` 目录一并打进 zip。

## 约定(强制)

- 注释一律中文(仓库根 CONVENTIONS)。
- 本目录**只进构建上下文**:源码/二进制绝不 rsync 进镜像 rootfs `/opt`。
- 未打包清单(无 checksums)在安装根下会被宿主判 degraded——这是零放宽设计,勿绕过。
- 改 `copilot/policy.ts` 冻结副本必须同步 `daedalus/core/internal/shellpolicy`(反之亦然)。
