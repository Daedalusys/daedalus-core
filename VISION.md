# Daedalus AIOS — 愿景与架构总览

| 项目 | Diva-OS / Daedalus（AI 原生、原子化桌面操作系统） |
| --- | --- |
| 版本日期 | 2026-09-13（内容演进时同步更新） |
| 读者 | 评估者（判断这套设计是否值得押注）、贡献者（在正确的层做正确的事）、未来的我们（多年后仍读得懂当初为何如此设计） |
| 文档分工 | README.md 管开发上手，AGENTS.md 管操作知识库，本文管愿景与设计层 |

## §1 宣言

Daedalus 是一个把 AI agent 当作一等公民的桌面操作系统底座。"给 agent 一个裸 shell，然后祈祷"不是可持续的人机协作方式：agent 需要的是类型化的资源、有边界的通道、可验证的保证，而不是一段谁也无法事后解释的命令字符串。核心论题一句话：平台首推路径 = 类型化资源的标准操作 = 有日志事务 = 哈希链审计 = 可回滚；保证附着于通道，其余通道在硬边界内存在但平台不背书。

这条论题背后是八个字的原则：硬边界、软偏好。所谓硬边界，指沙箱、命令白名单、哈希链审计、凭证隔离这些安全底线，没有商量余地，任何通道、任何 agent、任何工具都不得绕开。所谓软偏好，指平台在硬边界之内为 agent 开发者准备一条带全套保证的默认变更路径，类型化资源加事务通道，推荐走，但不禁止其它做法。一句话概括：底座不替 agent 做决定。

诊断用的 shell、临时脚本、资源形状之外的长尾操作，在硬边界之内永远有容身之处。平台只是诚实相告：走首选通道，你得到可回滚、有日志、可审计的全套保证；走其它通道，你按既有策略边界自行运作，平台不拦你，也不为你背书。选择权在 agent 开发者手里，这就是底座应有的姿态。

## §2 问题域

这套系统的原始动机可以压缩成一句话：系统变更不应该零散、不可恢复。过去几十年，运维把服务器养成了手工雕琢的活物；如今 agent 上了桌，同样的操作被换了一双手来执行，三种失效模式随之从偶发变成了常态。

**不可恢复**。组合命令没有事务，也没有回滚点。一行 `&&` 串起的五步操作，走到第三步失败，前两步的改动就地留存。改坏了什么？没人知道。怎么退回去？更没人说得清。人和 agent 都在同一片沙滩上留脚印，潮水不会帮忙抹平。

**不可审计**。谁在什么时候改了什么、依据什么授权，命令字符串本身给不出答案。一段事后翻出来的 shell 历史，既无法证明执行者的身份，也无法证明操作者的意图，取证时只剩一串裸文本。

**不可组合、不可验证**。命令字符串是自由文本：无类型、无期望态、没有预览 diff。两个 agent 的变更无法叠加，也无法在应用前预演一次看后果。自由文本天然排斥机器推理。

对照的回答是"声明式资源 API"。资源带类型，操作收敛为标准动词，变更包装成事务——三个要素逐条消解上面的三种失效：事务给出回滚点，消解不可恢复；审计记录落到每次资源操作，消解不可审计；类型与期望态让 diff 和叠加成为可能，消解不可组合与不可验证。这不是发明新哲学，OS 层早有这个文化：bootc/OSTree 让整个系统部署可以一键原子回滚。Daedalus 想做的，是把这种回滚保证从"整个部署"推进到"单次变更"粒度。

现在做这件事，是因为时机变了。agent 的数量与自主性都在上升，靠人肉逐条审查命令的方式不可扩展。变更粒度必须自带保证，协作才能继续扩容。

## §3 三面架构

系统按三个问题的分工拆成三个面：声明面回答"系统应该是什么样"，
执行面回答"怎么改、怎么恢复"，观测面回答"系统现在是什么样"。
另有两道横切，不专属于任何一面。

```
                       用户 / AI / copilot
                                │
    ┌───────────────────────────▼────────────────────────────┐
    │ 声明面 Spec   "系统应该是什么样"                       │
    │ 动词: set / delete(→absent)                           │
    │ v1: 资源清单声明 Resource{kind,name,desired_state}     │
    └───────────────────────────┬────────────────────────────┘
                                │ 所有写 = 一个事务
    ┌───────────────────────────▼────────────────────────────┐
    │ 执行面 Tx    "怎么改、怎么恢复"                        │
    │ v1: 事务日志 + 快照 + 回滚计划                         │
    └───────────────────────────┬────────────────────────────┘
                                │ 改的是真实系统
    ┌───────────────────────────▼────────────────────────────┐
    │ 观测面 Observe "系统现在是什么样"                      │
    │ v1: 只读查询服务器 + 观测缓存                          │
    └────────────────────────────────────────────────────────┘

    横切: 证据面 audit 哈希链  |  策略面 policy.toml (缺省 fail-closed)
```

声明面持有资源清单，每个资源是 kind、name、desired_state 三元组，
变更表达为标准动词落在声明上，而不是命令字符串落在系统上。
执行面把这些声明变更包成事务：先落事务日志，再取快照，
备好回滚计划，然后才触碰真实系统。
观测面由只读查询服务器与观测缓存构成，负责回答"现在是什么样"，
为 diff、对账与下一步决策供料。
两道横切各有位置：证据面用哈希链把每次操作钉进不可篡改的日志，
策略面是所有通道共用的安全底线，配置缺失时一律 fail-closed。

> **平台不变量**：凡声明为资源、并经事务通道执行的变更，
> 由平台保证可回滚、有操作日志、有哈希链审计。
> 这条保证不覆盖全部通道：资源形状之外的变更按既有策略边界自行运作，
> 平台不拦，也不额外背书。保证附着于通道，而非平台的每个角落。

**硬边界软偏好**在 §1 是原则，在这里落成架构语义。
四件套构成硬边界：沙箱、命令白名单、哈希链审计、凭证隔离，
以横切面方式作用在每一面、每一条通道上，不随通道选择而松动。
软偏好的落点在结构上：声明面加执行面这条资源加事务通道，
就是那条带全套保证的默认变更路径。
平台把它修得最平、守得最严，推荐走，但不禁止；
走其他通道，横切面照常生效，只是通道级保证不再附带。

**各得其所**：诊断用 shell、临时脚本、资源形状之外的长尾操作，
在硬边界内永远有容身之处。
平台不因事务通道的存在而收窄任何既有通道，三面架构是加法，不是替换。
控制器的职责有界，只管类型化资源，不吞掉命令行与脚本的合法生态。
万物在边界内各得其所：不是万物皆资源，是万物皆守边界。


## §4 术语两轴表

这套文档里最容易缠住读者的，是两组各管一摊、却时有同名的词：
插件 `type` 与资源 `kind`。它们各有一条轴，互为正交，
先给定义表，再做歧义消解。

**轴一 · 插件 `type`**（打包/分发维度，manifest 声明这个包是什么形态的安装物）：

| `type` 值 | 含义 | 现状 |
| --- | --- | --- |
| `copilot` | 命令顾问 CLI：翻译意图为带风险标注的命令提案 | v1 已落地（`daedalus.copilot`） |
| `capability` | 只读能力服务器包：fs/shell/pkg/sysinfo/service 五件 | v1 已落地（五个 `daedalus.<cap>` 插件） |
| `controller` | 外部接入的未来形态：声明性预留 | v1.5 预留，校验放行即全部语义，runtime 无分支 |

**轴二 · 资源 `kind`**（对象模型维度，封闭枚举共七类，声明一个资源是什么）：

| `kind` 值 | 含义 | 现状 |
| --- | --- | --- |
| `service` | systemd 单元 | v1 唯一有 provider 的类别 |
| `package` | 软件包 | 保留枚举位，当前无 provider |
| `container` | 容器 | 保留枚举位，当前无 provider |
| `capability` | OS 能力 | 保留枚举位，当前无 provider |
| `task` | 任务 | 保留枚举位，当前无 provider |
| `transaction` | 事务 | 保留枚举位，当前无 provider（tx 原语另有其位，见下文消解②） |
| `policy` | 策略 | 保留枚举位，当前无 provider |

两轴正交，用全枚举说话：3 type × 7 kind，理论上四格各居其位，谁也不从属谁。
一个插件 `type` 可以管理多种 `kind`（现役 capability 插件 `daedalus.service`
已声明 `service` 一种，未来加 `package` 无需改 type）；
一种 `kind` 也可以被多种 `type` 声明管理（`service` 既可挂在 capability
服务器的 `resources` 里，也可出现在未来 controller 插件的声明中）。
轴间没有从属关系：type 回答"这个包怎么装、怎么被宿主发现"，
kind 回答"这条资源在对象模型里是什么"。两个问题，两张表，各答各的。

### 歧义消解

三处同名，各有一段要说清。这不是命名事故，是已记录的巧合。

**① "capability" 两现。**
插件 `type=capability` 指 fs/shell/pkg/sysinfo/service 这类只读能力服务器的**包**，
是打包分发维度的分类；资源 `kind=capability` 指对象模型里的**能力声明位**，
是七类资源枚举之一。一个在 manifest 里，一个在资源声明里，
二者互不引用：capability 插件不声明 capability 资源，
capability 资源也不要求由 capability 插件管理。同名巧合，已记录在
`daedalus-core/internal/controller/doc.go`，此处正式入册。

**② "transaction" 三义。**
其一，`KindTransaction`（值 `transaction`）：七类资源枚举中的一个保留位，
当前无 provider，不代表任何已落地的执行通道。
其二，`tx.Status`：事务生命周期状态机上的 token，
沿 proposed→applying→applied→rolled_back/failed 迁移，
表外迁移一律拒绝，这是 `daedalus-core/internal/tx` 包里的类型。
其三，本文说"执行面"时的文词"事务"，指的是把声明变更包成一次可回滚变更的
架构概念（§3），不指向任何具体类型。三义分属三处：枚举位、状态机 token、
架构名词，读时按上下文落位，不混用。

**③ "controller" 三义。**
其一，插件 `type=controller`：manifest 枚举里为未来外部接入者预留的值，
v1.5 声明性预留，校验放行即全部语义。
其二，k8s Controller 概念：控制循环、观测与调谐的那套机制，
在 §5 的 k8s-parity map 里展开，本文不提前展开。
其三，`daedalus-core/internal/controller` 契约包：类型锚点，
登记上述巧合、防止命名漂移的文档落点。三处只在字面上相像，
指的东西互不相干。

### 为什么同名不重命名

线协议 token 的稳定性大于命名洁癖。`type` 与 `kind` 的值不是普通变量名，
它们出现在 manifest（`daedalus.plugin.json`）与 `policy.toml` 里，
是跨进程、跨文件、跨时间的协议字面量。重命名即破坏所有既有插件与策略文件，
换来的只是"读起来更顺"，这笔账不划算。

再看碰撞的实际伤害：v1 里零插件使用 collision 面。
capability 这个 type 与 capability 这个 kind 从不互指，
没有任何一行配置或代码把二者绑在一起，混淆只发生在人眼，
不发生在机器。宁留 documented 巧合，不做破坏性正名；
`daedalus-core/internal/controller/doc.go` 已经把话记在类型旁边，
本文再记一遍，两处口径一致。

## §5 k8s-parity map

这张表是全文的地图骨架。左列是读者已知的 k8s 概念，右三列给出 Daedalus 的精确回应：
现状格只写事实（代码锚点或"无"），缺口分级与归属回答"缺不缺、谁来做"。
读法建议：先扫"归属"列，v1 已交付与故意无的行可以跳过，
契约缝·零运行时的行是 v2 的类型地基，其余按分级挑感兴趣的纵列看。
表格不做论证，分歧的展开归 §7，缺口的盘点归 §8。

| k8s 概念 | Daedalus 现状 | 缺口分级 | 归属 |
| --- | --- | --- | --- |
| CRD（自定义资源定义） | `daedalus-sdk/objectmodel/objectmodel.go:30-51` Kind 封闭枚举 + manifest `resources` 声明 | A 定义层 | v1 已交付 |
| apiVersion | 无（Resource 无版本段） | A 定义层 | P3 |
| metadata.labels | 无 | A 定义层 | P3 |
| metadata.annotations | 无 | A 定义层 | P3 |
| metadata.uid | 无 | A 定义层 | P3 |
| metadata.generation | 无 | A 定义层 | P3 |
| metadata.resourceVersion（乐观并发） | 无（tx journal 为单写者模型） | A 定义层 | P3 |
| spec/status 显式分离 | Resource（3 字段声明）与 ServiceState（4 字段查询载荷）分离承载，`daedalus-sdk/objectmodel/objectmodel.go:74-91` | A 定义层 | P3 |
| Conditions（status.conditions[]） | ServiceState.Properties 为平铺字符串 map，无条件列表 | A 定义层 | P3 |
| controller / reconcile 循环 | 无（tx 由用户显式调用） | B 架构层 | 契约缝·零运行时（`daedalus-core/internal/controller` ReconcileFunc）→ P4 |
| list-watch / informer | 无（state.jsonl 最新值观测缓存，`daedalus-sdk/state/state.go:38-146`） | B 架构层 | P4 |
| workqueue / backoff | 无 | B 架构层 | P4 |
| admission webhook（mutating/validating） | 现状 = 静态准入：policy.toml 启动加载、fail-closed（`daedalus-sdk/policy/`） | B 架构层 | AdmissionFunc 契约缝·零运行时 → P4 |
| etcd / apiserver | **故意无**（journal 存期望、OS 自身存现实、tx 是两者之间的桥，见 §7 分歧①） | — | 故意无 |
| scheduler | **故意无**（桌面单机，无节点调度问题，见 §7 分歧③） | — | 故意无 |
| namespace | 无（DynamicUser 上下文隔离替代，见 AGENTS.md Object Model 段） | C 体感层 | P4+ |
| RBAC | 无 | C 体感层 | 开放问题 |
| finalizer / ownerRef / GC | 无 | A 定义层 | P3+ |
| events.k8s.io（操作性事件） | audit.jsonl 是证据链而非事件流（`daedalus-sdk/audit/`） | B 架构层 | P4 |
| server-side apply / field-ownership | 无（tx 为全量快照替换） | C 体感层 | 开放问题 |
| kubectl diff | daedalus-tx status 子命令可回放事务日志（`daedalus-core/cmd/daedalus-tx/main.go:8-9`），preview 语义仅限事务 | C 体感层 | P4 |
| --dry-run=server | copilot --dry-run（翻译、校验、展示三步零系统调用） | C 体感层 | v1 已交付 |
| kubectl explain / OpenAPI | 无 | C 体感层 | 开放问题 |
| operator 打包 | daedalus-plugin 格式：zip + manifest + sha256（`daedalus-sdk/plugin/`） | 无缺口 | v1 已交付 |
| feature gate | policy.toml `[objectmodel].enabled_kinds` fail-closed 放行（`policy.toml:58`） | 无缺口 | v1 已交付 |
| controller-runtime SDK | 契约类型已钉：`daedalus-core/internal/controller/types.go:79`（ReconcileFunc） | B 架构层 | 契约缝·零运行时 → P4 |
| ServiceAccount / 凭证隔离 | systemd LoadCredential + `/etc/credstore` | 无缺口 | v1 已交付 |
| node / kubelet 类比 | systemd 单元直接执行（`daedalus-core/files/scripts/76-daedalus-plugin-gen.sh` 构建期渲染 ExecStart） | 无缺口 | v1 已交付 |
| rollback（k8s 无对应物） | **强于 k8s**：tx rollback_plan + bootc 双层兜底 | 无缺口 | v1 已交付 |
| owner 工作负载重启策略（Restart=on-failure） | 无（tx 为显式变更，无常驻 watcher） | B 架构层 | 故意无（v1 决策 25） |

两处归属需要单独说清。"契约缝·零运行时"指缝已缝好、机器未转：
调和与准入的函数类型已经在 `daedalus-core/internal/controller/types.go` 钉死成 Go 签名，
测试与文档共同看守，但运行时里没有任何循环去调用它们，
实现整体归 P4。好处是将来接入时改的是实现而非协议，类型不漂移。
"故意无"指主动设计而非缺口：etcd/apiserver 与 scheduler 在桌面单机场景
没有存在理由，Daedalus 用事务日志加 OS 自身的现实状态替代集中式存储，
用 systemd 直接执行替代调度，理由分别在 §7 分歧①与③，
本文只在表里标记立场，不重复论证。

## §6 标准动词文法

对资源的全部操作收敛为七个标准动词。它们是资源操作词汇，不是任何一门现有系统的照搬；
与 k8s 的对应只有一句：`get` ≈ `query`，其余不再逐项比附。

| 动词 | 语义 | 所属面 | v1 落地状态 |
|------|------|--------|------------|
| `query` | 读单实例 | 观测面 | 已实现（`service.query` 等工具） |
| `list` | 读集合 | 观测面 | 已实现（`service.list`） |
| `set` | 驱动向 desired_state | 声明面·写 | v1 真实现仅 service（经事务通道） |
| `delete` | 达到缺席态 | 声明面·写 | 词汇预留；哨兵 `absent`；v1 无任何 delete 适配器 |
| `apply` | 事务提交 | 执行面 | 已实现（`daedalus-tx apply`，逐字一致） |
| `rollback` | 事务回滚 | 执行面 | 已实现（`daedalus-tx rollback`，逐字一致） |
| `status` | 事务状态 | 执行面 | 已实现（`daedalus-tx status`，逐字一致） |

**delete 的语义。** 删除不表达"执行一条破坏性命令"，而是把资源的 desired_state
写成哨兵值 `absent`（常量 `DesiredStateAbsent`，`daedalus-core/internal/controller/types.go`），
让"这个资源不应存在"成为一条可声明、可回滚、可审计的普通期望。
absent 之后的 per-kind 删除词汇归各 provider 领域：service 的 `absent`
具体意味着什么——移除用户单元文件？仅停用？连同 `enable` 一起撤销？——
是 v2 的设计题，此处只提问，不定案。

**per-kind 词汇现状。** desired_state 的取值词汇不进本文法，归各 provider 自领。
目前只有 service 有词汇表：`active` / `inactive`（经 `service.set` 的冻结映射表，
`daedalus-core/cmd/daedalus-tx/service_set.go`）。`enabled` / `disabled` 属开机自启维度，
与运行态正交，不是同一词汇轴上的值，引用时注意区分。

**单向规范性。** 本节是给人读的注脚，不是机器可读的权威。
动词 token 的唯一规范源是 `daedalus-core/internal/controller/types.go` 的
Op 常量表：OpQuery↔`query`、OpList↔`list`、OpSet↔`set`、OpDelete↔`delete`、
OpApply↔`apply`、OpRollback↔`rollback`、OpStatus↔`status`，七枚逐字对应。
本表与代码冲突时，以代码为准并修文。写作此处只为防第二事实源：
文档可以先行解释，不得先行改名。

**与事务生命周期的区分。** `begin` / `propose` 是事务生命周期原语
（begin→propose→apply→rollback 状态机的推进步骤），不是资源操作动词，
故意不入表：它们由 `daedalus-tx` 子命令承载，语义属于执行面的流程控制，
不属于"对一个资源做什么"。

**与 copilot 风险分级的关系。** 动词表是接口词汇，风险分级（L0/L1/L2）是策略层：
shell 白名单与事务保留动词（`tx.propose` 等）两条通道各自已有的把关，
不因动词表的存在而增减一分。

## §7 与 k8s 的三分歧

§5 的 parity 表是逐行对齐的账目，但有三行标着"故意无"。故意不是偷懒，是分歧：
读者若把 k8s 范式当成标准答案，这三处会像缺陷。本节正面回答"为什么这样设计"，
四段论证，逐条交代每个分歧背后的取舍。

### ① 无 etcd：OS 即事实源

k8s 需要一个集中式数据库存期望态，因为集群里没有别的组件能回答"系统应该是什么样"。
Daedalus 不需要，也不想要。systemd 单元文件、rpm 数据库、磁盘上的文件系统，
这些本身就是系统的真实状态，一直都在，从未缺席。再造一个"期望态数据库"等于
在 OS 之上立第二个事实源：两份记录并存，就必然漂移；漂移之后，谁来仲裁？
本仓库的文化是单一事实源，policy.toml 如此，事实源更该如此。

于是公式是：journal 存期望，OS 存现实，事务是桥。
声明面写入 journal，执行面经事务把期望落到 systemd 与文件系统上，
观测面回头读 OS 自身对账。期望与现实各住各处，桥保证两边对得上。
所以 Daedalus 的定位是"命令式地基之上的声明式叠加"（imperative substrate
上的 declarative overlay），不是把集群软件搬进桌面。声明式层负责描述意图、
打包保证，底下的命令式地基照常运转，几十年积累的工具链一分不动。

### ② 人在环先行：安全次序，不是进度落后

v1 的事务通道，每一次都过 y/n 确认，由本地静态风险分类器（L0/L1/L2）把关，
L1/L2 只展示不执行，L0 也要人点头。有人问：这不是没有 reconcile 循环吗？
对，但原因不是做不了，是次序不该乱。

自动调和的前提，是事务语义已经被证稳：回滚计划真的能回滚，
快照真的覆盖了每一次变更，状态机真的拒绝一切表外迁移。
这些保证还没在足够多的真实变更里打磨之前，把扳机交给一个无人值守的
常驻进程，等于把尚未验证的承诺放进了不需要人在场的通道。
这是安全次序：先证明每一步变更可解释、可撤回，再谈放手。
因此 reconcile 归入 v2 路线（§10），v1 选择人在环先行。

### ③ 桌面≠集群：不做不存在的场景

k8s 的 scheduler 解决的是"几十个节点、上千个工作负载，谁放哪儿"的调度问题。
桌面是单机，一个内核，一组本地资源，没有"放哪儿"这个问题，scheduler 自然无事可做。
namespace 与 RBAC 解决的是多租户隔离：谁看得见哪些资源，谁能动哪些资源。
桌面单机只有一个归属者，租户边界画不出来，也就不需要 RBAC。
v1 里真实存在的隔离需求，上下文之间互不串扰，由 systemd DynamicUser 命名空间承担，
已有的机制用足，不另立一套。

代价要诚实列出：状态记忆按上下文隔离，跨上下文的可见性在 v1 是已知边界。
一个上下文里的 agent 看不到另一个上下文的观测缓存，这带来些许不便，
换来的是默认更强的隔离。这个边界如何演进，作为开放问题记在 §11，此处只承认，不展开。

### ④ 强于 k8s 的一点：双层回滚兜底

分歧不全是减法，有一处是加法。k8s 里改完 spec，无法"撤销上一个变更"：
`kubectl apply` 一版新 YAML 就地生效，想回去只能自己写回旧 YAML 再 apply 一次，
集群不会替你保留"改之前是什么样"的证据，回滚是使用者的私人劳动。

Daedalus 的每个事务自带 before/after 快照与逆序回滚计划。
变更落地之前，"改之前是什么样"已经被钉进事务日志，`daedalus-tx rollback`
按计划逆序执行，把系统送回上一个状态。这层之上再叠 bootc 的部署级原子回滚：
事务层撤单次变更，bootc 层撤整个部署，单粒度与全粒度两层兜底，
坏了哪一层就在哪一层退。§5 表里"rollback（k8s 无对应物）"那行，说的就是这件事。

### 小结

三分歧是把 k8s 范式移植到 OS/AI 域时的主动设计决策，不是未完成的 k8s。

## §8 对象模型缺口盘点

§5 的 parity 表按行记账，本节把其中标着缺口的行展开成叙事：每一项现在是什么样，为什么现在不做，归到路线图的哪一段。行集与 §5 表同出一源，本节不新增表外条目，两处若对不上即为账目错误。分级沿用表的口径：A 定义层关乎资源的形状，B 架构层关乎驱动资源的那台机器，C 体感层关乎使用手感。这些缺口的公共前提先交代一句：契约层面的形状已先行钉死，`daedalus-core/internal/controller` 里的类型与函数签名就是它们未来的接入位。

### A 定义层（4 项，归 P3）

**metadata 块整体缺失（P3）。** Resource 现在只有 kind、name、desired_state 三个字段，apiVersion、labels、annotations、uid、generation、resourceVersion 全都没有落点。不急着补，是因为 v1 的声明消费者只有事务通道自己，没有第三方按标签筛选资源的场景；先摆字段再找用途，只会得到一排无人填充的空格。契约包的 Metadata 类型已把 name、labels、annotations、generation 钉进 v1.5 形态，P3 要做的是让这些字段被真实读写。

**spec/status 显式分离未做（P3）。** 现状由两个类型分载：Resource 承载期望（3 字段声明），ServiceState 承载观测（4 字段查询载荷）。分离的事实已经存在，缺的是一个统一的 Object 信封把它们装进去。单一 kind 加单一通道的现状下，两个类型各司其职并未产生歧义；等第二个 kind 有了 provider、同一资源需要同时暴露期望与观测时，统一信封才有真实压力。

**Conditions 未引入（P3）。** ServiceState.Properties 是平铺字符串 map，表达不了"多维条件加时间戳加原因码"这种结构化的状态判断。Condition 类型（type/status/reason/message/last_transition_time）已在契约包定义，但 v1 没有任何消费者需要对观测状态做多维裁决：风险分类器读的是命令形态，y/n 把关读的是提案，都不经过 conditions。此项随 status 写回一起落地。

**owner references/finalizers 未引入（P3）。** 没有父子关系，也没有延迟删除，资源目前是一组平铺的声明。v1 只有 service 一种资源有 provider，且 delete 适配器尚不存在，垃圾回收问题无从发生；absent 哨兵的 per-kind 语义在 §6 还只是提问，finalizer 的语义更无从谈起。先有删除，才谈得上延迟删除。

### B 架构层（5 项，全归 P4）

这一层缺的不是字段，是机器：让声明被持续驱动起来的运行时构件。§5 表里标"故意无"的行（etcd/apiserver、scheduler）不在此列，它们是主动设计而非缺口，论证已在 §7 交付。

**reconcile 循环（P4）。** v1 的事务由用户显式调用，没有常驻进程观测差异并驱动调和。这不是能力缺口，是次序选择（§7 分歧②）：先证稳事务语义，再把扳机交给无人值守的循环。ReconcileFunc 的 Go 签名已在 `daedalus-core/internal/controller/types.go` 钉死，P4 接的是实现，不是协议。

**list-watch/informer（P4）。** 观测缓存 state.jsonl 是最新值快照，没有事件流与 watch 语义。单机单写者模型下，快照已经够用；reconcile 循环出现之后，无 watch 的反复轮询才是无谓开销，informer 机制届时才有存在的必要。

**Generation/ObservedGeneration（P4）。** generation 的字段位归 P3 的 metadata 块，但"期望代数与观测代数相比较"这个语义，只有在 reconcile 循环里才成立。脱离循环先造比较机制，得到的只是两个无人比较的整数，故随 B 层的机器一起到位。

**CRD 式动态注册（P4）。** v1 新增一个资源种类要改四处：objectmodel 的 Kind 常量与注册表、校验分支、policy.Default()、policy.toml 的 enabled_kinds，三点防漂移测试守着不许漏改。这条路径只为底座作者敞开，插件作者加不了新类型。动态注册是 v2 外部 controller 生态的前置件，但注册协议涉及第三方治理（谁能注册、命名冲突归谁裁决），在第一个外部 controller 出现之前设计它属于空转。

**events-vs-audit 分离（P4）。** audit.jsonl 是哈希链证据：append-only、不可改写、为事后取证服务。k8s 的 events 是操作性事件流：为"刚刚发生了什么"的实时观察服务，可过期、可清理。两者目的不同，不该混在一处；v1 只有前者。操作性事件流的生产者是常驻控制器，机器没上线，事件流自然缺席。

### C 体感层（2 项归 P4，3 项列开放问题）

这一层决定"用起来像不像一个资源 API"。归类的标准很朴素：机器到位就能做的，归 P4；要等真实需求裁决的，列开放问题。

**diff 一等命令（P4）。** daedalus-tx 的 status 子命令可以回放事务日志，但 preview 语义仅限事务，没有独立的"声明 vs 观测"通用 diff 命令。事务在 apply 之前已有快照与回滚计划兜底，独立 diff 的真正消费者是人工审阅与未来的 reconcile，两者到位时再做不迟。

**server-side dry-run（P4）。** copilot 的 --dry-run 只覆盖命令翻译预览（§5 表把 --dry-run=server 一行记为 v1 已交付，指的正是翻译层面）；资源层面的服务端预演，即在触碰系统之前完整验证一份声明，还不存在。声明验证的规则面本身还在演进，次序上先钉验证器，再暴露预演入口。

**explain/OpenAPI schema（开放）。** 没有机器可读的 schema 自描述，资源的字段与词汇目前只能读源码获知。要不要补、以什么形态补（OpenAPI 还是别的），取决于外部消费者的真实出现，列为开放问题。

**namespace（开放）。** v1 无命名空间，上下文隔离由 systemd DynamicUser 承担（§7 分歧③），跨上下文的可见性是已承认的边界。§5 表给它的归属是 P4+，最早 P4、上不封顶，与开放问题同义；何时需要 namespace 形态的隔离，跟随真实需求裁决，不预设。

**RBAC（开放）。** 单归属者的桌面画不出租户边界，§7 已论证。它与 namespace 是绑定演进还是各自独立，同样开放。

### 资源模型职责有界

盘点缺口不等于宣告"万物皆可资源化"。至少有三类操作不该进资源模型：ad-hoc 诊断，一次性查询用完即走，建资源等于给碎片发身份证；过程性脚本，没有稳定身份可供声明，硬套资源形状只能靠编造名字；纯人读的报告输出，读者是人不是机器，既不需要期望态，也不需要调和。强行资源化的代价是 controller 职责无限扩展，违背底座哲学：§3 说得很清楚，不是万物皆资源，是万物皆守边界。资源模型管类型化的声明，其余在硬边界内各得其所。

这些缺口共用同一个结尾：契约缝已在 `daedalus-core/internal/controller` 钉形状，P3/P4 是"让形状长出行为"。

## §9 现状盘点

前文的每一条设计承诺，都对应一处已经在仓库里落地的实现。本节按组件逐项盘点，
一行一锚，锚点指向权威实现路径；内容取材于 AGENTS.md 与插件 README，
此处只盘点现状，不新增叙事。

1. **事务状态机与三道路径安全门**（`daedalus-core/internal/tx/`）：begin→propose→apply→rollback 全程落 journal，tx.Status 沿 proposed→applying→applied→rolled_back/failed 迁移、表外迁移一律拒绝，每笔事务自带 before/after 快照与逆序回滚计划。package.set 适配器已落地（plan daedalus-pkg-kind）。
2. **service 参考 kind：query/list 只读观测**（`daedalus-plugins/service/cmd/daedalus-service/`）：`service.query`/`service.list` 对 systemd 单元严格只读（show 限定 curated 属性），回包载荷为 ServiceState 四字段；service 的状态变更不经本插件，一律走 `daedalus-tx` 事务通道。
3. **audit 哈希链，含事务二级链**（`daedalus-sdk/audit/`）：genesis 起点加 SHA-256 逐条 chaining，追加持 syscall.Flock 互斥，begin/apply/rollback 盖 TxID+TxStep 形成事务二级链，金样向量钉住字节级兼容。
4. **state.jsonl 观测缓存**（`daedalus-sdk/state/`）：StateEntry 追加式最新值缓存，payload 为序列化的 ServiceState；state 是派生缓存、与哈希链证据层分离，v1 状态记忆按上下文隔离（DynamicUser 命名空间）。
5. **policy.toml enabled_kinds 网关**（`daedalus-core/files/system/opt/daedalus/shared/policy.toml`）：`[objectmodel].enabled_kinds` v1 仅启用 `service`，校验器接受保留 kind 而网关 fail-closed 拒执行，构建期再交叉核对声明 kind ⊆ 启用集，漏改由三点漂移测试拦截。
6. **copilot 双通道 L0-L2**（`daedalus-core/plugin/copilot/`）：shell 与 transaction 两条执行通道同由本地静态 classifier 定级，LLM 从不自标注风险；L0 经 y/n 确认进 `daedalus-shell` 沙箱或事务五步（begin→propose→preview→y/n→apply），L1/L2 仅展示并提示手动执行。
7. **插件格式与宿主零 spawn**（`daedalus-sdk/plugin/` + `daedalus-core/cmd/daedalus-host/`）：zip + manifest + sha256 checksums 打包、解压即校验；宿主 list/inspect/verify/run-plugin/render-unit 只做发现、校验与打印启动命令，绝不 spawn 任何服务器。
8. **i18n P0**（`daedalus-core/plugin/copilot/i18n.ts` + locale 文件 `daedalus-core/plugin/copilot/i18n/{en_US,zh_CN}.json`）：UI 字符串一律经 `t(key, ...args)` 走，en_US 必位兜底，manifest 声明与 locale 文件实物经严模式双向校验、漂移即拒。

## §10 路线图

> 本节各行标注：路线文字非承诺。每阶段只写方向与理由，不排期、不设日期。

| 阶段 | 内容 | 选型理由（一句） |
| --- | --- | --- |
| P2 | package kind + 事务适配器（路线文字非承诺） | dnf history 天然是事务日志、undo 天然是 rollback，第二个 kind 是对事务契约的第一次外部压测 **已交付 v1.1（plan: daedalus-pkg-kind，含 review critical #1 fail-closed 修复，2026-09-15）** |
| P3 | 契约类型被真实消费（路线文字非承诺） | Condition 写回 + Generation 落地，让 §8 盘点的 A 层字段从类型存在升为语义在场，对象模型升维。**演进位置**：对象模型类型在 `daedalus-sdk/objectmodel/`（SDK 仓，公开 contract），契约缝在 `daedalus-core/internal/controller/`（core 仓，决策 25 锁定）——P3 的改动面横跨两仓，SDK 侧改类型、core 侧改消费方 |
| P4 | daedalus-controller runtime + 外部 controller 插件加载（路线文字非承诺） | list-watch/reconcile/workqueue 把 §5 钉死的契约缝长出行为，实现接的是缝不是新协议。**演进位置**：runtime 本体落 `daedalus-core/`（core 仓），外部 controller 插件按 `daedalus-plugin` 格式进 `daedalus-plugins/`（插件仓）或第三方仓，经宿主加载 |
| P5 | 各得其所的运营化（路线文字非承诺） | 文档/示例/评估 harness 让既有边界自己说话，无强制迁移，万物仍各守边界。**演进位置**：三仓各自演进——core 仓管运行时与镜像，SDK 仓管公开契约包，plugins 仓管能力插件；文档与示例随所在仓维护 |

**决策节奏**：每阶段独立 plan、独立批准门，本文路线是方向背书不是排期承诺。

## §11 反模式边界与开放问题

前文各节都在写"要做什么"，本节补齐另外两块：系统刻意不做什么，以及哪些问题暂时没有定论。
边界条目全部提炼自 AGENTS.md 的既有约定，此处只做汇总，不新增任何 AGENTS.md 之外的绝对禁令；
开放问题只提问、不定案，裁决权留给对应阶段的 plan；
最后的引用致谢交代本文站立所依据的外部文献。

### 边界汇总：本系统刻意不做

每条一行，源锚指向 AGENTS.md 的对应段，细则以彼处正文为准。

- 不内置 LLM 模型或推理引擎：技术栈里只有云端适配器与本地风险分类器，模型能力在仓外（源：AGENTS.md「OVERVIEW」）。
- 不做多 agent 编排：copilot 的身份被钉死为命令顾问而非 agent，调度多个 agent 的事归仓外调用方（源：AGENTS.md「OVERVIEW」「2. OS Capability MCP Servers & Copilot CLI」）。
- 不做运行时联网插件安装：插件一律构建期内建，镜像即完整清单（源：AGENTS.md「3. Plugin Format (`daedalus-plugin`) & Host」Build-time install only 条）。
- 不做插件开发脚手架：生成新插件骨架的工具属另一个仓库，本仓库只装运行时、官方插件与运维工具（源：AGENTS.md「CONVENTIONS · 本仓库边界」）。
- 不直接修改 `base_image/` vendor 树：变更一律落在 `daedalus/{core,plugin,files}` 后经 sync 同步（源：AGENTS.md「ANTI-PATTERNS」）。
- 宿主绝不 spawn、绝不做任何 MCP 服务器的父进程：宿主只发现、校验并打印启动命令，真正执行者是 systemd 或用户（源：AGENTS.md「ANTI-PATTERNS」，决策 16）。
- 审计只进哈希链 CLI：禁直写审计文件，禁改链上任何一行，所有写方统一经 `daedalus-audit`（源：AGENTS.md「5. Tamper-Evident Audit Logging」「ANTI-PATTERNS」）。
- 不引入第二策略事实源：白名单变更只走 `shared/policy.toml`，与 `policy.Default()` 及 shellpolicy/pathguard 常量联动，漂移测试拦截漏改（源：AGENTS.md「ANTI-PATTERNS」「Policy Single Source of Truth」）。
- 不恢复 Python/Deno 能力服务器：fs/shell/pkg/sysinfo 与审计只有 Go 实现（能力服务器在 `daedalus-plugins/`，审计库在 `daedalus-sdk/audit/`；源：AGENTS.md「CONVENTIONS · Go 唯一实现」）。
- 不手改镜像内插件安装态：`/opt/daedalus/plugins/` 是构建产物，一律经 Pack→Verify 重生成（源：AGENTS.md「ANTI-PATTERNS」）。
- 不让资源模型吞掉长尾：诊断 shell、临时脚本、纯人读输出留在 capability 通道，controller 职责有界（源：AGENTS.md「Object Model · transactable」；叙事展开见本文 §8）。

### 开放问题

以下问题本文只记录、不裁决。每条都指得出裁决时机，提前作答只会制造未经检验的承诺。

- v2 runtime 直接消费动词常量的零返工赌注是否成立：写死的形状能否扛住届时真实需求的漂移（§5 契约缝与 §6 单向规范性都押在上面）。
- resource 化的长尾边界如何裁决：哪些操作永远留在 capability 通道，哪些在什么信号出现时才值得升格为资源（§8 职责有界给的是判断原则，不是清单）。
- 多上下文状态可见性：DynamicUser 命名空间隔离的 `state.jsonl` 何时需要跨上下文视图，隔离的不便累计到什么程度才值得开一道受控的窗（§7 分歧③已承认此边界）。
- admission 链设计：`policy.toml` 静态段与未来 controller 动态段的优先级与组合语义如何定，fail-closed 兜底位落在哪里（§5 表 AdmissionFunc 行是现状）。
- kind 扩展的第三方注册协议：不改 Go 代码新增资源种类的机制长什么样，命名冲突与注册治理归谁裁决（§8 已把它钉在 P4 前置位）。
- 文档 staleness 的同步机制：AGENTS.md 里 4 能力服务器旧清单一类的历史残留靠什么纪律兜住，人工同步之外要不要自动校验。

### 引用致谢

本文的体裁与概念框架站在下列文献上，各记一句用途。

- Kubernetes 官方文档：controller、CRD、admission 的概念框架，§5 parity map 左列的出处。
- Michael Nygard, "Documenting Architecture Decisions"：ADR 体例，本文标记"故意无"与"决策节奏"的记录格式所本。
- Leon Rosenshein, "Writing It Down"：vision doc 先于 RFC 的体裁论证，本文先立愿景、细节下沉到各 plan 的分工依据。
- HLD Handbook：设计文档的分段结构（九段体例），本文骨架的编排参照，按需扩为 11 节。
- Model Context Protocol 规范：工具接口标准，capability 服务器与 copilot 通道线协议的依据。
