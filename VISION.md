# Daedalus AIOS — 愿景与架构总览

| 项目 | Diva-OS / Daedalus(AI 原生、原子化桌面操作系统) |
| --- | --- |
| 版本日期 | 2026-09-24(内容演进时同步更新) |
| 读者 | 评估者、贡献者、未来的我们 |
| 文档分工 | README.md 管上手,AGENTS.md 管操作,本文管愿景与设计 |
| 架构图 | **一张完整架构图**:[docs/architecture.svg](docs/architecture.svg) · 由 `python3 scripts/arch-diagram.py` 生成(纯标准库,可复现) |
| 锚点约定 | 代码锚前缀 `daedalus-core/…`、`daedalus-sdk/…`、`daedalus-plugins/…` 指三仓平级布局下的各仓根 |

## §1 宣言

Daedalus 是把 AI agent 当一等公民的桌面操作系统底座。

给 agent 一条裸 shell 再祈祷,不可持续。agent 需要的是:类型化的资源、有边界的通道、可验证的保证——而不是一段没人能事后解释的命令字符串。

核心论题一句话:

> 平台首推路径 = 类型化资源的标准操作 = 有日志事务 = 哈希链审计 = 可回滚。
> 保证附着于通道;其余通道在硬边界内存在,平台不背书。

背后是八个字:**硬边界,软偏好**。

- **硬边界**:沙箱、命令白名单、哈希链审计、凭证隔离。安全底线,无商量,任何通道不得绕开。
- **软偏好**:平台在硬边界之内准备一条带全套保证的默认变更路径(类型化资源 + 事务通道)。推荐走,不禁止其它做法。

一句话:底座不替 agent 做决定。诊断 shell、临时脚本、资源形状之外的长尾操作,在硬边界内永远有位置;走首选通道得全套保证,走其它通道按既有策略自行运作,平台不拦也不背书。

## §2 问题域

原始动机一句话:**系统变更不应该零散、不可恢复。**

agent 上桌后,三种失效从偶发变成常态:

| 失效 | 表现 |
| --- | --- |
| 不可恢复 | `&&` 串五步,第三步失败,前两步改动就地留存;改坏了什么、怎么退回去,没人说得清 |
| 不可审计 | 谁在何时改了什么、依据什么授权——shell 历史给不出答案 |
| 不可组合、不可验证 | 命令字符串是自由文本:无类型、无期望态、无预览 diff;两个 agent 的变更无法叠加,也无法应用前预演 |

对照答案是**声明式资源 API**:资源带类型,操作收敛为标准动词,变更包装成事务。三要素逐条消解:事务给回滚点;审计落到每次资源操作;类型与期望态让 diff 和叠加成为可能。

OS 层早有这个文化:bootc/OSTree 让整个部署可一键原子回滚。Daedalus 把这种保证从「整个部署」推进到「单次变更」粒度。

时机:agent 数量与自主性在上升,人肉逐条审查不可扩展;变更粒度必须自带保证。

## §3 三面架构

系统按三个问题拆成三个面,另有两道横切。**完整架构图见 [docs/architecture.svg](docs/architecture.svg)**(由 `scripts/arch-diagram.py` 生成,下同,不再内嵌分图)。

| 面 | 回答的问题 | 内容 |
| --- | --- | --- |
| 声明面 Spec | 系统应该是什么样 | Resource(kind/name/desired_state);动词 set / delete(→absent) |
| 执行面 Tx | 怎么改、怎么恢复 | 事务五步;日志 + 快照 + 回滚计划 |
| 观测面 Observe | 系统现在是什么样 | 只读查询 + state.jsonl 缓存 |
| 横切 · 证据 | 操作是否可追溯 | audit.jsonl 哈希链,唯一写入口 `daedalus-audit` |
| 横切 · 策略 | 通道是否被允许 | policy.toml,fail-closed:文件缺失或损坏一律拒启(回退内置 `Default()` 需 `DAEDALUS_POLICY_MODE=development` 显式 opt-in) |

**平台不变量**:凡声明为资源、并经事务通道执行的变更,由平台保证可回滚、有操作日志、有哈希链审计。这条保证不覆盖全部通道——保证附着于通道,而非平台每个角落。

「硬边界软偏好」在 §1 是原则,在这里落成结构:四件套(沙箱、白名单、哈希链、凭证隔离)作为横切作用于每一面;声明面 + 执行面这条资源加事务通道就是那条默认路径。

### 三仓拓扑

三面架构的物理载体是三个平级仓库(决策 23/24/25):

| 仓 | 职责 |
| --- | --- |
| `daedalus-core` | 镜像编排 + 5 个 core runtime + copilot 源码 + 契约缝 `internal/{controller,tx}` |
| `daedalus-sdk` | 公开 contract 仓:对象模型、策略、审计、观测缓存等 |
| `daedalus-plugins` | 现役 7 个 `type=capability` 插件 monorepo |

制品链:`just plugin-pack` 编译打包 → 解压校验落安装态 → `just sync` 同步 vendor 树 → `just build` 出镜像。`verify-dev-layout.sh` 守兄弟仓就位。release zip 是分发载体,无运行时联网安装。

### 运行时进程

| 角色 | 行为 |
| --- | --- |
| `daedalus-host` | list / inspect / verify / run-plugin / render-unit——只打印启动命令,绝不 spawn(决策 16) |
| systemd 单元 | 构建期由 76 脚本渲染 ExecStart;DynamicUser + Landlock + seccomp + LoadCredential |
| capability × 7 | Go 静态二进制;受治理的命令式/探索性能力,读写边界由 policy 分级强制;声明式状态变更走 `daedalus-tx` |
| copilot 双通道 | L0 经 y/n 进 shell 沙箱或事务五步;L1/L2 仅展示 |
| 审计 | 一切落盘经 `daedalus-audit` CLI |

## §4 术语两轴表

两组易混的词:插件 `type`(打包维度)与资源 `kind`(对象模型维度)。互为正交。

**轴一 · 插件 `type`**:

| 值 | 含义 | 现状 |
| --- | --- | --- |
| `copilot` | 命令顾问 CLI | v1 已落地(`daedalus.copilot`) |
| `capability` | 受治理的命令式/探索性系统能力包(imperative / exploratory capability under policy boundaries):fs/shell/pkg/sysinfo/service/blueprint/dupe 七件。安全性由 policy 与 E/D 分级保证,不由"只读"假设保证 | v1 已落地 |
| `controller` | 外部接入预留 | v1.5 声明性预留 |

**轴二 · 资源 `kind`**(封闭枚举共七类):

| 值 | 含义 | 现状 |
| --- | --- | --- |
| `service` | systemd 单元 | 已落地:provider `daedalus.service` + tx `service.set` |
| `package` | 软件包 | 已落地:provider `daedalus.pkg` + tx `package.set` |
| `container` | 容器 | 保留位,无 provider |
| `capability` | OS 能力 | 保留位,无 provider(七件 capability 插件是打包维度,不声明 capability 资源) |
| `task` | 任务 | 保留位,无 provider |
| `transaction` | 事务 | 保留位,无 provider(tx 原语另位) |
| `policy` | 策略 | 保留位,无 provider |

两轴正交:一个 `type` 可管多种 `kind`(现役两例);一种 `kind` 也可被多种 `type` 声明。type 答「包怎么装、怎么被宿主发现」,kind 答「资源在对象模型里是什么」——两张表,各答各的。

### 歧义消解(三处同名)

| 同名 | 含义区分 |
| --- | --- |
| capability | `type=capability` = 受治理的能力服务器**包**(命令式/探索性,非"只读");`kind=capability` = 对象模型**保留位**。互不引用,同名巧合(已记于 `internal/controller/doc.go`) |
| transaction | ① 枚举位 `KindTransaction`;② 状态机 token `tx.Status`;③ 文档里的「事务」= 执行面架构概念。三处按上下文落位 |
| controller | ① 插件 type 预留值;② k8s Controller 概念(§5);③ `internal/controller` 契约包 |

**为什么不重命名**:type/kind 的值是 manifest 与 policy.toml 里的跨进程协议字面量,重命名破坏所有既有插件与策略文件;v1 零插件使用 collision 面,混淆只发生在人眼。宁留 documented 巧合,不做破坏性正名。

## §5 k8s-parity map

左列 k8s 概念,右三列 Daedalus 回应。现状格只写事实或「无」;分歧展开在 §7,缺口盘点在 §8。

| k8s 概念 | Daedalus 现状 | 缺口分级 | 归属 |
| --- | --- | --- | --- |
| CRD | `objectmodel.go` Kind 封闭枚举 + manifest `resources` | A 定义层 | v1 已交付 |
| apiVersion | 信封有 `api_version` 键,无版本化需求,投影恒空 | A | when-needed |
| metadata.labels / annotations / generation | 信封 `objectmodel.Metadata` 已在场,读侧 `MatchLabels`;尚无填充方 | A | P4 填值 |
| metadata.uid | 无 | A | when-needed |
| metadata.resourceVersion | 无(tx 单写者) | A | when-needed |
| spec/status 分离 | 信封 `objectmodel.Object{api_version,kind,metadata,spec,status}`,Resource 与 ServiceState 双投影 | A | v1 已交付 |
| Conditions | `objectmodel.Condition` 三态 + `UpsertCondition`;`service.query` 产出 `Ready` | A | v1 已交付 |
| controller / reconcile | 无 controller 循环(tx 用户显式调用);期望投影已裁决+落码 `internal/desiredview`(裁决文 `docs/desired-state-projection.md`,sdk#4) | B | 契约缝 ReconcileFunc → P4 |
| list-watch / informer | 无(state.jsonl 最新值) | B | P4 |
| workqueue / backoff | 无 | B | P4 |
| admission webhook | 静态:policy.toml 启动加载、fail-closed | B | AdmissionFunc 契约缝 → P4 |
| etcd / apiserver | **故意无**(journal 存期望、OS 存现实、tx 是桥) | — | 故意无 |
| scheduler | **故意无**(桌面单机) | — | 故意无 |
| namespace | 无(DynamicUser 隔离替代) | C | P4+ |
| RBAC | 无 | C | 开放问题 |
| finalizer / ownerRef / GC | 无 | A | when-needed(随 delete 适配器) |
| events | audit 是证据链,非事件流 | B | P4 |
| server-side apply | 无(tx 全量快照) | C | 开放问题 |
| kubectl diff | tx status 可回放日志;preview 限事务 | C | P4 |
| --dry-run=server | copilot --dry-run(零系统调用) | C | v1 已交付 |
| explain / OpenAPI | 无 | C | 开放问题 |
| operator 打包 | daedalus-plugin:zip + manifest + sha256 | 无缺口 | v1 已交付 |
| feature gate | `enabled_kinds` fail-closed,现启用 `service`、`package` | 无缺口 | v1 已交付 |
| controller-runtime | 类型已钉:`ReconcileFunc` | B | 契约缝 → P4 |
| ServiceAccount | LoadCredential + `/etc/credstore` | 无缺口 | v1 已交付 |
| kubelet 类比 | systemd 直接执行(76 脚本渲染 ExecStart) | 无缺口 | v1 已交付 |
| rollback | **强于 k8s**:tx 快照 + bootc 双层 | 无缺口 | v1 已交付 |
| 重启策略 watcher | 无(tx 显式变更,无常驻) | B | 故意无(决策 25) |

两处归属说明:

- **契约缝 · 零运行时**:函数类型已在 `internal/controller` 钉成 Go 签名,测试与文档看守,但运行时无循环调用它们;接入时改实现不改协议。
- **故意无**:etcd/apiserver、scheduler 在桌面单机没有存在理由——journal + OS 自身状态替代集中存储,systemd 直接执行替代调度(论证见 §7)。

## §6 标准动词文法

资源操作收敛为七个标准动词(与 k8s 仅 `get` ≈ `query` 一句可比):

| 动词 | 语义 | 面 | v1 状态 |
|------|------|-----|---------|
| `query` | 读单实例 | 观测 | 已实现 |
| `list` | 读集合 | 观测 | 已实现 |
| `set` | 驱动向 desired_state | 声明·写 | service/package 经事务已实现 |
| `delete` | 达到缺席态(`absent` 哨兵) | 声明·写 | 词汇预留;无 delete 适配器 |
| `apply` | 事务提交 | 执行 | 已实现 |
| `rollback` | 事务回滚 | 执行 | 已实现 |
| `status` | 事务状态 | 执行 | 已实现 |

要点:

- **delete** 不是「执行破坏性命令」,而是把 `desired_state` 写成 `absent`——可声明、可回滚、可审计的普通期望。absent 之后的 per-kind 语义是 v2 设计题。
- **per-kind 词汇**归各 provider,不进本文法。现役:service = `active`/`inactive`;package = `present`/`absent`/`latest`(`packageSetVerbs`,映射 dnf,表外拒绝)。service 的 `enabled`/`disabled` 是开机自启维度,与运行态正交。
- **权威源**:动词 token 唯一规范在 `internal/controller/types.go` 的 Op 常量表;文档与代码冲突时以代码为准。
- **不入表**:`begin`/`propose` 是事务生命周期原语,不是资源动词;copilot 的 L0/L1/L2 是策略层,不因动词表增减。

一次变更的完整走位(声明 → 事务五步 → 副作用 → 观测 → 审计)见架构图主链。

## §7 与 k8s 的分歧

§5 表里三行「故意无」,不是偷懒,是设计:

### ① 无 etcd:OS 即事实源

k8s 需要集中库存期望态,因为集群里没有别的组件能回答「系统应该是什么样」。Daedalus 不需要:systemd 单元、rpm 数据库、磁盘文件本身就是现实状态。再造一个期望态库 = 第二事实源,两份记录必然漂移。

公式:**journal 存期望,OS 存现实,事务是桥。** 定位是「命令式地基之上的声明式叠加」,不是把集群软件搬进桌面。

### ② 人在环先行

v1 事务每次过 y/n,L0/L1/L2 由本地静态分类器定级,L1/L2 只展示。没有 reconcile 循环——不是做不了,是次序:先证稳回滚、快照、状态机,再谈放手给常驻进程。reconcile 归 P4(§10)。

### ③ 桌面 ≠ 集群

scheduler 解决多节点调度,桌面单机无此问题。namespace/RBAC 解决多租户,桌面单归属者画不出租户边界。v1 的上下文隔离由 systemd DynamicUser 承担;跨上下文可见性是已承认边界,记在 §11 开放问题。

### ④ 强于 k8s 的一点:双层回滚

k8s 改完 spec 没有「撤销上一个变更」。Daedalus 每笔事务自带 before/after 快照与逆序回滚计划;其上再叠 bootc 部署级原子回滚。单粒度与全粒度两层兜底。

小结:三分歧是主动设计,不是未完成的 k8s。

## §8 对象模型缺口盘点

分级:A 定义层(资源形状)、B 架构层(驱动资源的机器)、C 体感层(使用手感)。公共前提:契约缝形状的唯一事实源已在 SDK `objectmodel`(core 的 `internal/controller` 是其别名),P4 是让形状长出行为。

### A 定义层

| 缺口 | 现在 | 为什么后做 |
| --- | --- | --- |
| metadata 块 | 信封 `Metadata{name,labels,annotations,generation}` 已在;`Resource` 声明仍 3 字段 | 按标签筛选的消费者是调和循环,声明侧预先造值只会得到空格 |
| spec/status 信封 | **已闭合**:`Object{api_version,kind,metadata,spec,status}`,`Resource.Object()` 与 `ServiceState.Object()` 双投影 | — |
| Conditions | **已闭合**:`Condition` 三态 + `UpsertCondition`(同状态写回不刷新转换时刻);`service.query` 产出 `Ready` | 执行面转换时刻随 tx apply 落地 |
| ownerRef / finalizer | 无父子、无延迟删除 | 先有 delete 适配器,才谈延迟删除 |

### B 架构层(P4)

| 缺口 | 现在 | 为什么后做 |
| --- | --- | --- |
| reconcile 循环 | 用户显式调用 | 次序选择(§7②);ReconcileFunc 类型已钉 |
| list-watch / informer | 快照缓存 | 单机快照够用;reconcile 出现后才有必要 |
| Generation 比较 | 字段位已在 | 语义只在 reconcile 循环里成立 |
| CRD 式动态注册 | 新增 kind 要改四处 | 外部 controller 生态前置;第一个外部 controller 前属空转 |
| events 流 | 只有 audit 证据链 | 两者目的不同;生产者是常驻控制器,机器未上线 |

「故意无」(etcd、scheduler)不在此列——主动设计,论证在 §7。

### C 体感层

- **P4**:diff 一等命令(现仅 tx status 回放);server-side dry-run(现仅 copilot 翻译层 --dry-run)。
- **开放**:explain / OpenAPI schema;namespace(现 DynamicUser 隔离);RBAC(单归属桌面)。

### 资源模型职责有界

至少三类不该资源化:一次性诊断查询、无稳定身份的过程脚本、纯人读报告。强行资源化 = controller 职责无限扩展。不是万物皆资源,是万物皆守边界——资源模型管类型化声明,其余在硬边界内各就各位。

## §9 现状盘点

一行一锚,取材 AGENTS.md 与插件 README:

1. **事务状态机 + 适配器**(`internal/tx/` + `cmd/daedalus-tx/adapter.go`):begin→propose→apply→rollback 全程落 journal;`service.set`、`package.set` 注册;每笔带快照与回滚计划;package Apply 以捕获 dnf history id + sidecar 落盘为成功必要条件。
2. **service 只读观测**(`daedalus-plugins/service/`):`service.query`/`list` 严格只读;状态变更一律走 tx。
3. **audit 哈希链**(`daedalus-sdk/audit/`):SHA-256 链 + Flock;begin/apply/rollback 盖 TxID+TxStep 二级链;金样钉字节兼容。
4. **state.jsonl 观测缓存**(`daedalus-sdk/state/`):追加式最新值;派生缓存与证据层分离;按上下文隔离。
5. **policy.toml 网关**(`files/.../policy.toml`):`enabled_kinds` 现启用 `service`、`package`;校验器接受保留 kind 而网关拒执行;三点漂移测试守门。
6. **copilot 双通道 L0-L2**(`plugin/copilot/`):本地静态 classifier;LLM 不自标注;L0 经 y/n 进沙箱或事务;L1/L2 仅展示。
7. **插件格式 + 宿主零 spawn**(`daedalus-sdk/plugin/` + `cmd/daedalus-host/`):zip + manifest + sha256;宿主只发现、校验、打印启动命令。
8. **i18n P0**(`plugin/copilot/i18n.ts`):UI 字符串经 `t()`;en_US 兜底;声明与实物双向校验。(Go 侧翻译属 P1,未做。)
9. **blueprint 插件**(`daedalus-plugins/blueprint/`):6 工具、6 蓝图;apply 走 v1-direct(`tx_id="(v1-direct)"`,`blueprint.write` 适配器归后续 plan)。
10. **dupe 插件**:第七个 capability,随 plugins 仓 v0.1.1 分发。
11. **三仓布局与制品链**:fetch-plugins / verify-dev-layout 守门;just plugin-pack 重建 zip 落镜像;无运行时联网安装。
12. **demo 构建层 + dev 兜底**(`paths_demo.go`):build-tag 互斥;生产字面量零 dev 代码。
13. **daedalus-smoke**(`cmd/daedalus-smoke/`):镜像内端到端,v3 构建机补跑。

## §10 路线图

> 路线文字非承诺:每阶段独立 plan、独立批准门,不排期、不设日期。

| 阶段 | 内容 | 理由 |
| --- | --- | --- |
| P2 | package kind + 事务适配器 | **已交付 v1.1**(plan daedalus-pkg-kind,2026-09-15) |
| P3 | 契约类型被真实消费(Condition、Generation) | **Condition 已交付**(2026-09-26:信封升入 SDK、`service.query` 产出 `Ready`);Generation 字段在场但无填充方,其消费者是 P4 投影管道 |
| P4 | controller runtime + 外部 controller 插件 | list-watch/reconcile 把已钉的契约缝长出行为 |
| P5 | 文档/示例/评估 harness 运营化 | 三仓各自演进,无强制迁移 |

### 能力扩展轴(插件轨道)

对象模型轴之外的第二条产品轨道:每个 `type=capability` 插件 = 一种带硬边界的 OS 观测/操作面。不排期,只挂轨(入册标准:已交付或已开 issue)。

- **已交付(7)**:fs / shell / pkg / sysinfo / service / blueprint / dupe——全部构建期内建,zip + sha256 分发。
- **已开 issue(31)**:plugins 仓 #6–#33,覆盖蓝图 P2 与长尾能力;每个 issue = 一个候选能力提供者。
- **同轨已知未做**:Go 侧 MCP 字符串翻译(i18n P1);`blueprint.write` 事务适配器。
- **长尾边界**:什么留 capability、什么升格为资源——判断原则在 §8,裁决是 §11 开放问题。

## §11 反模式边界与开放问题

### 边界汇总(刻意不做)

细则以 AGENTS.md 为准,此处只汇总:

- 不内置 LLM 模型或推理引擎(技术栈只有云端适配器 + 本地风险分类器)。
- 不做多 agent 编排(copilot 身份钉死为命令顾问)。
- 不做运行时联网插件安装(构建期内建,镜像即完整清单;特权/User 分层见下节,分层不是推翻)。
- 不做插件开发脚手架(属另一个仓)。
- 不直接改 `base_image/` vendor 树(改 `files/`、`plugin/` 与兄弟仓,经 `just sync`)。
- 宿主绝不 spawn、绝不做 MCP 父进程(决策 16)。
- 审计只进哈希链 CLI,禁直写审计文件。
- 不引入第二策略事实源(白名单只走 `policy.toml`,与 `policy.Default()` 联动)。
- 不恢复 Python/Deno 能力服务器(七件能力与审计只有 Go 实现)。
- 不手改镜像内插件安装态(一律 `just plugin-pack` 重生成)。
- 不让资源模型吞掉长尾(诊断 shell、临时脚本、纯人读输出留 capability 通道)。

### 双层 Extension 模型(daedalus-sdk#2 裁决:边界分层,不是推翻)

化解 immutable OS(构建期内建)与插件生态(runtime 可装)的张力:

| 层 | 形态 | 落点 |
| --- | --- | --- |
| System Extensions | image-built / signed / privileged / immutable,参与 system capability 与 controller | `/opt/daedalus/plugins` |
| User Extensions | runtime installable / unprivileged / sandboxed / user-scoped,承载 MCP / Skill / Agent / UI | `~/.local/share/daedalus/plugins` |

规则:**特权 provider(SecretProvider/MemoryProvider 实现)永远走 System
Extension**,User Extension 不得申请特权 slot 接线;provider 接线发生在装配
构造期,注册 ≠ 运行时安装——"镜像即完整清单"对特权面保持字面成立,非特权
生态才谈 runtime install。远期 **Daedalus Control Center** 的 panel/route/
schema/action 按 extension 注册(Blueprint #6 只是其中一 panel),不预埋实现。
Contract 细节与 Core 直依赖改造清单见 `../daedalus-sdk/docs/provider-slot.md`。

### 开放问题

只记录、不裁决;每条指得出裁决时机:

- v2 runtime 直接消费动词常量的零返工赌注是否成立。
- resource 化长尾边界如何裁决(哪些永远留 capability,何时值得升格)。
- 多上下文状态可见性:何时需要跨上下文视图。
- admission 链:policy.toml 静态段与未来动态段的优先级与组合。
- kind 扩展的第三方注册协议(命名冲突、治理归属)。
- 文档 staleness 同步:人工之外要不要自动校验(历史上 4/5/6/7 件计数、blueprint.write 曾漂移)。

### 引用致谢

- Kubernetes 官方文档:controller、CRD、admission 概念框架(§5 左列)。
- Michael Nygard, *Documenting Architecture Decisions*:ADR 体例。
- Leon Rosenshein, *Writing It Down*:vision doc 先于 RFC 的体裁依据。
- HLD Handbook:设计文档分段结构参照。
- Model Context Protocol 规范:capability 服务器与 copilot 通道协议依据。

### 架构图

全文一张图:[docs/architecture.svg](docs/architecture.svg)。生成方式:

```bash
python3 scripts/arch-diagram.py          # → docs/architecture.svg
python3 scripts/arch-diagram.py --png    # 同时出 docs/architecture.png(需 rsvg-convert)
```

纯 Python 标准库,零依赖,可复现(同代码同输出)。禁止另行维护 drawio/手绘第二事实源;改架构改脚本重跑。
