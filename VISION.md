# Daedalus AIOS — 愿景与架构总览(2026-10-17 fleet 改造版)

| 项目 | Daedalus(AI 原生 fleet 控制面 + Atomic 节点) |
| --- | --- |
| 版本日期 | 2026-10-17(本版取代 2026-09-21 单机桌面版) |
| 读者 | 评估者、贡献者、客户、未来的我们 |
| 上游文档 | `docs/superpowers/specs/2026-10-17-fleet-control-plane-design.md` 是当前产品定义;本 VISION 是其对内对外的统一叙事 |
| 文档分工 | README 上手 / AGENTS 操作 / 本稿愿景 |
| 架构图 | 架构图随本版生产化更新;历史图见 `docs/archive/2026-09-21-vision.md` 引用的旧版 |
| 锚点约定 | 代码锚前缀 `daedalus-core/…` / `daedalus-sdk/…` / `daedalus-plugins/…` 指三仓平级布局下的各仓根 |

> **changelog**:
> - 2026-10-17: 本版。取代 2026-09-21 单机桌面版(spec:`docs/superpowers/specs/2026-10-17-fleet-control-plane-design.md`)。**§1 删「桌面」改「fleet 控制面 + Atomic 节点」**;§2/§3 重写为「小团队 + 大量机器」;§5「故意无」三行在 hub-spoke 下重新解释;§7 整章区分「仍故意无 / 不再故意无」;§8 增「跨机意图 + Plan + RPC 协议」段;§9 加 12(daedalus-agent / RPC / 凭证下发 / Plan 调度);§10 加 P0.5 阶段(fleet 控制面 MVP = G1+G4 端到端 demo);§11 加「daedalus-agent 长期定位」+「控制面单点 HA」开放问题。旧 VISION 入 `docs/archive/2026-09-21-vision.md`(verbatim)。
> - 2026-09-21: 初版(单机 dady,自维护 atomic base,kde 变体)。

## §1 宣言

Daedalus 是把 AI agent 当一等公民的 fleet 控制面底座。

核心论题一句话:

> 给一个运维多台机器的小团队一套「控制面出 Plan、人在 Plan 层批审、各机本地执行、最坏能回滚」的硬边界通道。
> 保证附着于通道;其余通道在硬边界内存在,平台不背书。

具体形态:**两产品 + 一个拓扑 + 一组边界**。

- **两产品**。`daedalus-atomic` = 自维护 Atomic OS,既是控制面节点,也是被控机器的终态(用户主动迁移后)。`daedalus-agent` = 单 Go 二进制,装在任意 Linux(Ubuntu / CentOS / 各发行版)上,只做四件事:**读系统信息 + 接受包/文件意图 + best-effort 回滚 + 状态回报**。**入场券**:用户迁到 `daedalus-atomic` 后 agent 退场。
- **一个拓扑**。hub-spoke。**一个控制面节点 + N 个被控节点**(N 实测可达 200)。控制面与被控之间是命令流,不是同步协议。
- **一组边界**(硬边界,软偏好)。

  - **硬边界**:沙箱、命令白名单、哈希链审计、凭证隔离。安全底线,无商量,任何通道不得绕开。
  - **软偏好**:平台在硬边界之内准备一条带全套保证的默认变更路径(类型化资源 + 事务通道)。推荐走,不禁止其它做法。

背后仍是八个字:**硬边界,软偏好**。但应用对象从「一台桌面」变成「一组机器」——agent 不替人做决定,跨机变更需要人在 Plan 层兜底,任何机器不能自动滚后续批。

## §2 问题域:小团队 + 大量机器

原始动机一句话:**小团队运维多机的变更不应该零散、不可恢复。**

agent 上桌后,「单团队 + N 台机器 + AI 提案 + 人兜底 + 不瘫」这个组合的失效模式,从偶发变成常态:

| 失效 | 单机版 | fleet 版 |
| --- | --- | --- |
| 不可恢复 | `&&` 串五步,第三步失败,前两步改动就地留存;改坏了什么、怎么退回去,没人说得清 | N 台机器上各跑一遍同样的五步,其中三台半成功两台持久失败——半成功的三台如何回滚?两台失败的是否需要全批暂停? |
| 不可审计 | 谁在何时改了什么、依据什么授权——shell 历史给不出答案 | 同样的改动推到 200 台机器,审计链要逐台完整,而不是 200 份 shell 历史散落 |
| 不可组合、不可验证 | 命令字符串是自由文本:无类型、无期望态、无预览 diff;两个 agent 的变更无法叠加,也无法应用前预演 | 单次变更的 intent 要在控制面**先批审再下推**,而不是 200 个 agent 各自决定;Plan 必须能预演、能 blast-radius 评估 |

对照答案是**声明式资源 API + 跨机 Plan**。资源带类型,操作收敛为标准动词(§6),变更包装成事务(单机 / 各机本地),跨机变包装成 Plan(控制面协调各机落地结果)。三要素逐条消解:单机事务给回滚点;审计落到每次资源操作;类型与期望态让 diff 和叠加成为可能;Plan 让跨机变更有「批审、暂停、分桶」的可能。

OS 层早有这个文化:bootc/OSTree 让整个部署可一键原子回滚。Daedalus 把这种保证从「整个部署」推进到「单次变更」粒度;在 fleet 版本里,又把它从「单机」推进到「跨机 Plan 各机本地事务」粒度。

时机:agent 数量与自主性在上升,人肉逐条审查不可扩展;变更粒度必须自带保证,且保证要随批跨机器复刻。

## §3 三面架构

系统按三个问题拆成三个面,另有两道横切。**架构图随本版生产化更新;旧图见 `docs/archive/2026-09-21-vision.md` 引用的旧版。**

| 面 | 回答的问题 | 内容 |
| --- | --- | --- |
| 声明面 Spec | 系统应该是什么样 | Resource(kind/name/desired_state);动词 set / delete(→absent) |
| 执行面 Tx | 怎么改、怎么恢复 | 事务五步;日志 + 快照 + 回滚计划;**daedalus-agent 上事务 = best-effort**(理由见 §7④) |
| 观测面 Observe | 系统现在是什么样 | 只读查询 + state.jsonl 缓存(agent 端经 RPC 上报) |
| 横切 · 证据 | 操作是否可追溯 | audit.jsonl 哈希链,唯一写入口 `daedalus-audit`;**控制面汇总各机逐台的完整哈希链** |
| 横切 · 策略 | 通道是否被允许 | policy.toml,fail-closed;**Plan 调度层加 (kind, target) 风险分级**(spec G9) |

**平台不变量**:凡声明为资源、并经事务通道执行的变更,由平台保证可回滚、有操作日志、有哈希链审计。在 fleet 版里,这条不变量逐机字面成立——不变量附着于每台机器的事务通道,不附着于跨机一致性。

「硬边界软偏好」在 §1 是原则,在这里落成结构:四件套(沙箱、白名单、哈希链、凭证隔离)作为横切作用于每一面;声明面 + 执行面这条资源加事务通道就是那条默认路径;控制面的 Plan 层是「跨机的批审与分桶入口」,不绕开硬边界。

### 三仓拓扑

三面架构的物理载体仍是三个平级仓库(决策 23/24/25):

| 仓 | 职责 |
| --- | --- |
| `daedalus-core` | 镜像编排 + 5 个 core runtime + copilot 源码 + 契约缝 `internal/{controller,tx}` + **新增** `cmd/daedalus-agent/`(spec core#38) |
| `daedalus-sdk` | 公开 contract 仓:对象模型、策略、审计、观测缓存、**新增** `daedalus/agent/v1.proto` + `Plan` 类型(spec sdk#26, sdk#27) |
| `daedalus-plugins` | 现役 7 个 `type=capability` 插件 monorepo;**新增** `daedalus.os-state`(spec plugins#34,异构被控 OS 状态)、`daedalus.snapshot`(spec plugins#35,btrfs / LVM / snapper / rsync 能力检测) |

制品链:`just plugin-pack` 编译打包 → 解压校验落安装态 → `just sync` 同步 vendor 树 → `just build` 出镜像。`verify-dev-layout.sh` 守兄弟仓就位。release zip 是分发载体,无运行时联网安装。**daedalus-agent 单 Go 二进制**(spec core#38)随 daedalus-core 构建,但产物独立分发,可装到任意 Linux。

### 两产品拓扑(fleet 改造新增)

```
                          ┌────────────────────────┐
                          │   控制面(1 节点)       │
                          │   daedalus-atomic       │
                          │  ┌──────────────────┐  │
                          │  │  daedalus-host   │  │
                          │  │  + daemon        │  │
                          │  │  + Plan 调度器   │  │
                          │  └────────┬─────────┘  │
                          └───────────┼────────────┘
                                      │ RPC(mTLS + protobuf, spec §4.4)
                ┌─────────────────────┼─────────────────────┐
                │                     │                     │
          ┌─────▼─────┐         ┌─────▼─────┐         ┌─────▼─────┐
          │  daedalus- │         │  daedalus- │   ...   │  daedalus- │
          │   atomic   │         │   agent    │         │   agent    │
          │  (被控 N)  │         │  (异构 M)  │         │            │
          └───────────┘         └───────────┘         └────────────┘
              ↑                       ↑                       ↑
              │ 全部保证               │ best-effort            │ best-effort
              │ (bootc 双层)           │ (无 bootc)             │
```

- **控制面节点必是 daedalus-atomic**(自维护、可字面保证)。它跑 `daedalus-host` + reconcile + Plan 调度器,是「事实汇总与批审点」。
- **被控节点两种形态**:`daedalus-atomic`(用户已迁移,全部保证)或 `daedalus-agent`(异构宿主,best-effort)。**两种 kind 不抽象成同一种节点**(spec G8):`daedalus-host node list` 输出 `kind: daedalus-atomic | daedalus-agent`;Plan 调度按 kind 分桶。
- **两产品互斥**(spec G7):daedalus-atomic 真机 `rpm -q daedalus-agent` 不存在;安装脚本从 atomic 节点上**强制卸载** agent。
- **入场券 ≠ 一等公民**:daedalus-agent 是「等命令 + 做」,**不是**「对账 + 收敛」(spec N5)。reconcile 在控制面。

### 运行时进程

| 角色 | 行为 |
| --- | --- |
| `daedalus-host` | list / inspect / verify / run-plugin / render-unit——只打印启动命令,绝不 spawn(决策 16);**控制面还管** node list / plan submit / plan status |
| 控制面 daemon | reconcile 循环 + Plan 调度器(持久失败整批转 dirty、瞬时失败按风险等级重试或不重试) |
| systemd 单元 | 构建期由 76 脚本渲染 ExecStart;DynamicUser + Landlock + seccomp + LoadCredential |
| capability × 7 | Go 静态二进制;受治理的命令式/探索性能力,读写边界由 policy 分级强制;声明式状态变更走 `daedalus-tx` |
| **daedalus-agent**(新增) | 单 Go 二进制;只做四件事:读系统信息 + 接受包/文件意图 + best-effort 回滚 + 状态回报(spec core#38, G3 best-effort) |
| copilot 双通道 | L0 经 y/n 进 shell 沙箱或事务五步;L1/L2 仅展示 |
| 审计 | 一切落盘经 `daedalus-audit` CLI;控制面汇总各机哈希链 |

## §4 术语两轴表

两组易混的词:插件 `type`(打包维度)与资源 `kind`(对象模型维度)。fleet 版再加一组:**节点 `kind`(daedalus-atomic / daedalus-agent)**——这是部署形态维度,与资源 `kind` 正交。

**轴一 · 插件 `type`**:

| 值 | 含义 | 现状 |
| --- | --- | --- |
| `copilot` | 命令顾问 CLI | v1 已落地(`daedalus.copilot`) |
| `capability` | 受治理的命令式/探索性系统能力包(imperative / exploratory capability under policy boundaries):fs/shell/pkg/sysinfo/service/blueprint/dupe 七件。安全性由 policy 与 E/D 分级保证,不由"只读"假设保证 | v1 已落地 |
| `controller` | 外部接入预留 | v1.5 声明性预留 |

**轴二 · 资源 `kind`**(封闭枚举共七类,**不变**):

| 值 | 含义 | 现状 |
| --- | --- | --- |
| `service` | systemd 单元 | 已落地:provider `daedalus.service` + tx `service.set` |
| `package` | 软件包 | 已落地:provider `daedalus.pkg` + tx `package.set` |
| `container` | 容器 | 保留位,无 provider |
| `capability` | OS 能力 | 保留位,无 provider(七件 capability 插件是打包维度,不声明 capability 资源) |
| `task` | 任务 | 保留位,无 provider |
| `transaction` | 事务 | 保留位,无 provider(tx 原语另位) |
| `policy` | 策略 | 保留位,无 provider |

**轴三 · 节点 `kind`**(fleet 版新增,正交于轴二;spec G8):

| 值 | 含义 | 现状 |
| --- | --- | --- |
| `daedalus-atomic` | 自维护 Atomic OS;既是控制面节点也是被控机器终态;**全部保证**(bootc 双层回滚) | spec core#37 |
| `daedalus-agent` | 单 Go 二进制,装在任意 Linux;best-effort;**入场券**(迁到 atomic 即退场,spec N8) | spec core#38 |

三轴正交:一个 `type` 可管多种 `kind`;一种 `kind` 也可被多种 `type` 声明;节点 `kind` 与资源 `kind` 完全无关——节点 `kind` 答「这台机器跑什么形态的 Daedalus」,资源 `kind` 答「OS 资源在对象模型里是什么」。

### 歧义消解(三处同名)

| 同名 | 含义区分 |
| --- | --- |
| capability | `type=capability` = 受治理的能力服务器**包**(命令式/探索性,非"只读");`kind=capability` = 对象模型**保留位**。互不引用,同名巧合(已记于 `internal/controller/doc.go`) |
| transaction | ① 枚举位 `KindTransaction`;② 状态机 token `tx.Status`;③ 文档里的「事务」= 执行面架构概念。三处按上下文落位 |
| controller | ① 插件 type 预留值;② k8s Controller 概念(§5);③ `internal/controller` 契约包 |

**为什么不重命名**:type/kind 的值是 manifest 与 policy.toml 里的跨进程协议字面量,重命名破坏所有既有插件与策略文件;v1 零插件使用 collision 面,混淆只发生在人眼。宁留 documented 巧合,不做破坏性正名。节点 `kind` 同理——`daedalus-host node list` 输出里这两个值是 wire 协议字面量。

## §5 k8s-parity map

左列 k8s 概念,右三列 Daedalus 回应。现状格只写事实或「无」;分歧展开在 §7,缺口盘点在 §8。

| k8s 概念 | Daedalus 现状 | 缺口分级 | 归属 |
| --- | --- | --- | --- |
| CRD | `objectmodel.go` Kind 封闭枚举 + manifest `resources` | A 定义层 | v1 已交付 |
| apiVersion | 信封有 `api_version` 键,无版本化需求,投影恒空 | A | when-needed |
| metadata.labels / annotations / generation | 信封 `objectmodel.Metadata` 已在场;读侧 `MatchLabels` + `FilterByLabels` 列表筛选;写侧 `SetLabel` / `SetAnnotation` / `BumpGeneration` 已交付(sdk#1 P3,2026-10-10) | A | v1 已交付 |
| metadata.uid | `Metadata.UID` 已交付;校验非空时无控制字符 + 长度 ≤ 253(sdk#1 P3) | A | v1 已交付 |
| metadata.resourceVersion | `Metadata.ResourceVersion` 已交付;`BumpGeneration` 不动它,由 provider 在 set spec 后回填(sdk#1 P3) | A | v1 已交付 |
| spec/status 分离 | 信封 `objectmodel.Object{api_version,kind,metadata,spec,status}`,Resource 与 ServiceState 双投影 | A | v1 已交付 |
| Conditions | `objectmodel.Condition` 三态 + `UpsertCondition`;`service.query` 产出 `Ready` | A | v1 已交付 |
| controller / reconcile | 控制面有 reconcile 循环(P4 skeleton,2026-10-10 PR #35);daedalus-agent **无** reconcile(spec N5,「等命令 + 做」) | B | P4 + spec core#38 |
| list-watch / informer | 控制面有;daedalus-agent 端无(state.jsonl 最新值经 RPC 上报) | B | P4 |
| workqueue / backoff | 控制面有;**Plan 调度器的风险分级 backoff 由 spec G9 钉死** | B | spec core#40 |
| admission webhook | 静态:policy.toml 启动加载、fail-closed | B | AdmissionFunc 契约缝 → P4 |
| etcd / apiserver | **故意无**:journal 存期望、OS 存现实、tx 是桥。hub-spoke 下「OS 即事实源」仍逐机字面成立——控制面汇总各机哈希链,不做集中库存 | — | 故意无 |
| scheduler | **故意无**(控制面直发命令,不调度的对象是 OS 不是 container;spec N1/N2) | — | 故意无 |
| namespace | 无(DynamicUser 隔离替代;hub-spoke 单归属者小团队仍不画租户边界,spec N3) | C | 故意无 |
| RBAC | 无(spec N3:小团队单归属者不画租户边界);但 v1 已声明为「开放问题」——若未来扩到多团队/多租户,需要在控制面重新设计 | C | 开放问题 |
| **操作风险等级**(risk_class) | **新增**:Plan 的每个 `intent` 标 `risk_class: low \| high`;Plan 整体取 `max(intents.risk_class)` 展示给人审;policy.toml 可声明哪些 (kind, target) 默认 high(spec G9, core#40) | B | spec core#40 |
| finalizer / ownerRef / GC | `Metadata.OwnerReferences` / `Metadata.Finalizers` 字段 + `Validate` 已交付(sdk#1 P3);GC 触发逻辑归 P4 | A | P4 |
| events | audit 是证据链,非事件流 | B | P4 |
| server-side apply | 无(tx 全量快照) | C | 开放问题 |
| kubectl diff | tx status 可回放日志;preview 限事务 | C | P4 |
| --dry-run=server | copilot --dry-run(零系统调用) | C | v1 已交付 |
| explain / OpenAPI | 无 | C | 开放问题 |
| operator 打包 | daedalus-plugin:zip + manifest + sha256 | 无缺口 | v1 已交付 |
| feature gate | `enabled_kinds` fail-closed,现启用 `service`、`package` | 无缺口 | v1 已交付 |
| controller-runtime | 类型已钉:`ReconcileFunc` | B | 契约缝 → P4 |
| ServiceAccount | LoadCredential + `/etc/credstore`;**新增** `SecretProvider 下发契约`(spec sdk#28):凭证只下发不落,agent 进程崩了 = 凭证不外泄(spec N6) | 无缺口 | v1 已交付(spec sdk#28) |
| kubelet 类比 | 控制面 = control plane;daedalus-atomic 被控节点 = 类似 kubelet on bootc;daedalus-agent 被控节点 = 类似 best-effort kubelet(无 bootc,无保证镜像层) | 无缺口 | spec core#38 |
| rollback | 控制面与 daedalus-atomic 被控:tx 快照 + bootc 双层(**强于 k8s**);**daedalus-agent 被控:best-effort,文档显式标注**(spec G3,「best-effort rollback, not bootc」) | 无缺口(atomic)/ C(agent) | v1 已交付 + spec G3 |
| 重启策略 watcher | 无(tx 显式变更,无常驻) | B | 故意无(决策 25) |
| **RPC 协议**(新增) | 控制面 ↔ daedalus-agent:mTLS + gRPC + protobuf schema(spec §4.4, core#39 / sdk#26) | A | spec core#39 |

两处归属说明:

- **契约缝 · 零运行时**:函数类型已在 `internal/controller` 钉成 Go 签名,测试与文档看守,但运行时无循环调用它们;接入时改实现不改协议。
- **故意无**:etcd/apiserver、scheduler、namespace 在 hub-spoke 下仍无存在理由——「OS 即事实源」逐机字面成立;调度是被调度的对象是 OS 不是 container(spec N1);小团队单归属者不画租户边界(spec N3,沿用旧 §7③ 的故意无但语境变化)。RBAC 同样不画租户边界,但**已声明为开放问题**——若未来扩到多团队,需要在控制面重新设计。

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

一次变更的完整走位(声明 → 事务五步 → 副作用 → 观测 → 审计)见架构图主链。**Plan 级**走位在 §8「跨机意图 + Plan + RPC 协议」展开。

## §7 与 k8s 的分歧

§5 表里三行「故意无」,不是偷懒,是设计。本章按 hub-spoke 重写:哪些**仍故意无**、哪些**不再故意无**、哪些**新长出来**。

### ① 无 etcd:OS 即事实源(**仍故意无**)

k8s 需要集中库存期望态,因为集群里没有别的组件能回答「系统应该是什么样」。Daedalus 在 hub-spoke 下**仍不需要集中库存**:每台机器的 systemd 单元、rpm 数据库、磁盘文件本身就是现实状态;控制面汇总各机哈希链,但不替代各机成为事实源。再造一个期望态库 = 第二事实源,两份记录必然漂移。

公式(逐机):**journal 存期望,OS 存现实,事务是桥。** 定位仍是「命令式地基之上的声明式叠加」,不是把集群软件搬进桌面。

### ② 人在环先行(**仍故意无** reconcile 的跨机放手)

v1 事务每次过 y/n,L0/L1/L2 由本地静态分类器定级,L1/L2 只展示。fleet 版**升级**人在环的层级:**Plan 层**过 y/n(批审整个跨机 intent 的清单 + blast_radius + 风险等级),而不是每台机器每笔事务过 y/n。**Plan 落地后任何机器不自动滚后续批**;半成功整批暂停,等人在控制面点(spec G4)。daedalus-agent **不跑 reconcile 循环**(spec N5):agent 是「等命令 + 做」,不是「对账 + 收敛」。

### ③ 控制面 ≠ 集群(**重写;此节是 §4.6 段映射的核心**)

旧版对 k8s 的「故意无」三件套(scheduler / namespace / RBAC)在「单机桌面」语境下成立(无多节点、无多租户);在「hub-spoke + N 台机器」语境下,**多节点已是现实**,但「故意无」仍然成立,理由变了:

| 概念 | 旧版(单机)理由 | 新版(hub-spoke)理由 |
| --- | --- | --- |
| scheduler | 桌面单机无多节点调度问题 | **调度对象不是 container**(spec N1),是 OS;**没有跨主调度的对象**;控制面直发命令即可(spec N2:不要集群调度器、不要多主) |
| namespace | 桌面单归属者画不出租户边界 | **小团队单归属者不画租户边界**(spec N3,沿用);v1 的上下文隔离由 systemd DynamicUser 承担 |
| RBAC | 单归属者桌面不需要 | **v1 仍不需要**(spec N3,沿用);但**已声明为开放问题**(§11)——若未来扩到多团队/多租户,需要在控制面重新设计 |

**新版独有**:**控制面单点**(见 §11 开放问题)。hub-spoke 单一控制面是显式选择(spec N2),不接受「把控制面也做集群」来解 HA;HA 走「故障 → 控制面在单次 boot 中恢复 + 审计链字节可验」,不是「控制面也做集群」。多主带来的复杂度远超小团队运维多机的收益。

### ④ 强于 k8s 的一点:双层回滚(**仍故意无** 的升级版)

k8s 改完 spec 没有「撤销上一个变更」。Daedalus:

- **控制面 + daedalus-atomic 被控**:每笔事务自带 before/after 快照与逆序回滚计划;其上再叠 bootc 部署级原子回滚。单粒度与全粒度两层兜底。
- **daedalus-agent 被控**:**best-effort**(spec G3,**没有 bootc 层**)。`daedalus-tx rollback` 尽力回滚;失败时显式标注「best-effort rollback, not bootc」,产品叙事与 `--help` 输出都钉死(spec G3 字面要求)。

小结:四点是主动设计,不是未完成的 k8s。**「仍故意无」(etcd、scheduler、namespace、reconcile 全放手)与「不再故意无」(bootc 双层在 atomic 上字面成立)是同一组不变量**——不变量附着于每台机器的事务通道,不附着于跨机一致性。

## §8 对象模型缺口盘点

分级:A 定义层(资源形状)、B 架构层(驱动资源的机器)、C 体感层(使用手感)。公共前提:契约缝形状的唯一事实源已在 SDK `objectmodel`(core 的 `internal/controller` 是其别名),P4 是让形状长出行为。

### A 定义层

| 缺口 | 现在 | 为什么后做 |
| --- | --- | --- |
| metadata 块 | 信封 `Metadata{name,labels,annotations,generation,uid,resource_version,owner_references,finalizers}` 全字段已交付;writer 端 `BumpGeneration`/`SetLabel`/`SetAnnotation`/`AddFinalizer` 在场(sdk#1 P3) | 按标签筛选的消费者是调和循环(P4);声明侧预先造值只会得到空格 |
| spec/status 信封 | **已闭合**:`Object{api_version,kind,metadata,spec,status}`,`Resource.Object()` 与 `ServiceState.Object()` 双投影 | — |
| Conditions | **已闭合**:`Condition` 三态 + `UpsertCondition`(同状态写回不刷新转换时刻);`service.query` 产出 `Ready` | 执行面转换时刻随 tx apply 落地 |
| ownerRef / finalizer | 字段在场 + 校验在场(sdk#1 P3);无 GC 触发逻辑 | GC 归 P4 |
| **跨机意图字段**(新增) | `Plan{intents[], machines[], batching, blast_radius, risk_class}`(spec sdk#27) | 跨机意图需要新增「机器清单」「批处理边界」「影响半径」语义;不能在单机 7 类资源里塞 |
| **RPC 协议 schema**(新增) | `daedalus/agent/v1.proto`(spec sdk#26)+ Go SDK;字段 = `ReadSystemInfo / ApplyIntent / BestEffortRollback / ReportState` 四件 | 协议字面量跨进程,定一次后改不动;提前钉 |

### B 架构层(P4)

| 缺口 | 现在 | 为什么后做 |
| --- | --- | --- |
| reconcile 循环 | 用户显式调用 | 次序选择(§7②);ReconcileFunc 类型已钉 |
| list-watch / informer | 快照缓存 | 单机快照够用;reconcile 出现后才有必要 |
| Generation 比较 | 字段位已在 | 语义只在 reconcile 循环里成立 |
| CRD 式动态注册 | 新增 kind 要改四处 | 外部 controller 生态前置;第一个外部 controller 前属空转 |
| events 流 | 只有 audit 证据链 | 两者目的不同;生产者是常驻控制器,机器未上线 |
| **Plan 调度器**(新增) | 无(spec core#40);控制面 daemon 的 reconcile 还没接到跨机 Plan | P0.5 fleet MVP 是把 Plan 调度器起出来;失败分桶 + 暂停语义是产品字面成立的最低集 |
| **RPC 服务端/客户端**(新增) | 无(spec core#39);daedalus-agent 二进制没起,protobuf schema 钉了但没接 | 与 Plan 调度器同批起 |

「故意无」(etcd、scheduler、namespace、agent reconcile)不在此列——主动设计,论证在 §7。

### C 体感层

- **P4**:diff 一等命令(现仅 tx status 回放);server-side dry-run(现仅 copilot 翻译层 --dry-run)。
- **P0.5(fleet MVP)**:`daedalus-host plan submit --intent=upgrade pkg nginx --machines=A,B,C..200` 端到端 demo(spec G1+G4)。`Plan.status` 展示「整批暂停 / 半成功 dirty / 完成」三态。
- **开放**:explain / OpenAPI schema;namespace(现 DynamicUser 隔离);RBAC(单归属 fleet;若扩到多团队,见 §11 开放问题);server-side apply。

### 资源模型职责有界

至少三类不该资源化:一次性诊断查询、无稳定身份的过程脚本、纯人读报告。强行资源化 = controller 职责无限扩展。不是万物皆资源,是万物皆守边界——资源模型管类型化声明,其余在硬边界内各就各位。**fleet 版特别强调**:跨机意图不进资源模型,进 Plan 类型(spec sdk#27)。Plan 是「批处理容器」,不是资源。

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
14. **P4 controller skeleton**(2026-10-10 PR #35):reconcile 循环骨架已起;list-watch 与 reconcile 接入归 P4 + P0.5。
15. **P3 object model**(2026-10-10):Condition / Generation / UID / ResourceVersion / OwnerReferences / Finalizers 字段 + Validate 全交付(sdk#1)。
16. **daedalus-agent 二进制骨架**(新增,spec core#38;P0.5 端到端范围):单 Go 二进制,装在任意 Linux;RPC 接口 = `ReadSystemInfo / ApplyIntent / BestEffortRollback / ReportState` 四件;best-effort 回滚,显式标注「best-effort rollback, not bootc」(spec G3 字面要求)。
17. **RPC 协议 + SecretProvider 下发契约**(新增,spec core#39 / sdk#26 / sdk#28):mTLS + gRPC + protobuf schema(`daedalus/agent/v1.proto`);凭证下发不落 agent 端(spec N6:agent 进程崩了 = 凭证不外泄)。
18. **Plan 类型 + 调度器骨架**(新增,spec sdk#27 / core#40):`Plan{intents[], machines[], batching, blast_radius, risk_class}`;控制面 reconcile 接到 Plan 调度器;失败分桶(transient 重试 / persistent dirty / 整批暂停)+ 风险等级默认高(触内核/security 包 / `/etc` `/boot` `/usr` `/var/lib` 等路径 / systemd / dbus / NetworkManager)。

## §10 路线图

> 路线文字非承诺:每阶段独立 plan、独立批准门,不排期、不设日期。

| 阶段 | 内容 | 理由 |
| --- | --- | --- |
| P2 | package kind + 事务适配器 | **已交付 v1.1**(plan daedalus-pkg-kind,2026-09-15) |
| P3 | 契约类型被真实消费(Condition、Generation) | **已完成**(2026-10-10):Condition 已交付(2026-09-26,`service.query` 产出 `Ready`);Generation 字段 + `BumpGeneration` writer 已交付;`Metadata.UID` / `ResourceVersion` / `OwnerReferences` / `Finalizers` 字段 + `Validate` 已交付(sdk#1 plan `2026-10-10-vision-p3-object-model.md`);其消费者(controller 调和循环 + finalizer GC)归 P4 投影管道 |
| P4 | controller runtime + 外部 controller 插件 | list-watch/reconcile 把已钉的契约缝长出行为 |
| **P0.5 fleet 控制面 MVP**(**新增**) | **端到端 demo**:一个控制面节点 + N 个被控节点(N 实测可达 200)。`Plan{intent=upgrade pkg nginx, machines=[A,B,C..200]}` 经 y/n 后,200 机器各自跑本地事务(spec G1)。高危操作的任何失败(transient 或 persistent)立刻暂停;低危操作的瞬时失败才允许自动重试(spec G9)。半成功整批状态转 dirty + 后续批排队(spec G4)。daedalus-agent 端 best-effort,显式标注(spec G3)。落到具体产物:**daedalus-atomic headless 变体裁剪**(core#37)+ **daedalus-agent 二进制 + daemon**(core#38)+ **RPC 协议 + protobuf schema**(core#39 / sdk#26)+ **Plan 类型 + 调度器**(sdk#27 / core#40)+ **异构被控 E2E**(core#41)+ **os-state / snapshot 插件**(plugins#34 / plugins#35) | 「小团队 + 大量机器」logiclevel 不成立的代表;本阶段完成 = v1 上半部分字面成立(atomic 上全部保证)+ v1 下半部分 best-effort 边界字面成立(agent 上显式标注);**单机上下文验证在 atomic headless 上重做,不依赖 KDE 桌面**(core#36 改后子任务) |
| P5 | 文档/示例/评估 harness 运营化 | 三仓各自演进,无强制迁移 |

### 能力扩展轴(插件轨道)

对象模型轴之外的第二条产品轨道:每个 `type=capability` 插件 = 一种带硬边界的 OS 观测/操作面。不排期,只挂轨(入册标准:已交付或已开 issue)。

- **已交付(7)**:fs / shell / pkg / sysinfo / service / blueprint / dupe——全部构建期内建,zip + sha256 分发。
- **已开 issue(31+)**:plugins 仓 #6–#33,覆盖蓝图 P2 与长尾能力;每个 issue = 一个候选能力提供者;**新增** plugins#34 `daedalus.os-state`(异构被控 OS 状态)、plugins#35 `daedalus.snapshot`(btrfs / LVM / snapper / rsync 能力检测与调度)。
- **同轨已知未做**:Go 侧 MCP 字符串翻译(i18n P1);`blueprint.write` 事务适配器。
- **长尾边界**:什么留 capability、什么升格为资源——判断原则在 §8,裁决是 §11 开放问题。

### Fleet 拓扑轨道(新增)

第三条产品轨道,加在「对象模型」与「能力扩展」之间:**节点 kind + Plan 调度 + RPC 协议**。

- **节点 kind**:daedalus-atomic / daedalus-agent(§4 轴三,spec G8)。
- **Plan 调度**:控制面 reconcile 接到跨机 Plan,失败分桶 + 暂停语义(spec G4 / G9, core#40)。
- **RPC 协议**:控制面 ↔ daedalus-agent,mTLS + gRPC + protobuf(spec §4.4, core#39 / sdk#26)。

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
- 不让资源模型吞掉长尾(诊断 shell、临时脚本、纯人读输出留 capability 通道;跨机意图留 Plan,不进资源)。
- **daedalus-agent 不是「轻 daedalus」**(新增,spec §1 论证):变更入口不可拦 / 回滚要靠宿主 FS / pkg mgr 能力,这两条缝在 daedalus 本体上不存在;agent 因此是**结构上降级**的「够用就好」。
- **不做容器编排**(新增,spec N1):调度的对象不是 container,是 OS。
- **不做集群调度器 / 多主**(新增,spec N2):hub-spoke 单一控制面;不要把复杂度再加一层。
- **不做强一致跨机事务**(新增,spec N4):跨机原子性 = 各机本地事务 + 控制面协调结果分桶;不做 2PC / 共识。
- **daedalus-agent 不跑 reconcile 循环**(新增,spec N5):agent 是「等命令 + 做」,不是「对账 + 收敛」;reconcile 在控制面。
- **daedalus-agent 不存 SecretProvider 仓**(新增,spec N6):凭证只下发不落;agent 进程崩了 = 凭证不外泄。
- **daedalus-agent 不跑 Blueprint Web UI / copilot / sddm**(新增,spec N7):谁在异构机器上按按钮?无人值守场景不需要。
- **daedalus-agent 不是永久一等公民**(新增,spec N8):A 定位 = 入场券,迁到 daedalus-atomic 即退场;产品叙事与代码命名都钉死。

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
- **RBAC**(沿用但需重新设计,§5 表注):若 fleet 扩到多团队 / 多租户,**控制面要重新设计 RBAC**;v1 单归属者小团队场景下不画租户边界(spec N3)。
- **daedalus-agent 长期定位**(新增,spec N8):A 定位 = 入场券,迁到 daedalus-atomic 即退场。**何时是「用户在异构宿主上长期留住 agent」的产品意图**?这一条要拍:**若 agent 留久,best-effort 的产品叙事是否仍字面成立**(回滚、变更入口拦截两条缝在宿主上不可消);**若 agent 始终是入场券,产品叙事与代码命名要继续钉死入场券语义**。
- **控制面单点 HA**(新增,§7③):hub-spoke 单一控制面是显式选择(spec N2)。HA 走「故障 → 控制面在单次 boot 中恢复 + 审计链字节可验」,不是「控制面也做集群」。**控制面本身的故障演练 + 字节可验审计链是 P0.5 必须交付**;是否需要「温备控制面」是后续拍。

### 引用致谢

- Kubernetes 官方文档:controller、CRD、admission 概念框架(§5 左列)。
- Michael Nygard, *Documenting Architecture Decisions*:ADR 体例。
- Leon Rosenshein, *Writing It Down*:vision doc 先于 RFC 的体裁依据。
- HLD Handbook:设计文档分段结构参照。
- Model Context Protocol 规范:capability 服务器与 copilot 通道协议依据。
- **新增**:`docs/superpowers/specs/2026-10-17-fleet-control-plane-design.md` §4.6 段映射表(本版 §1-§11 改写依据)。

### 架构图

架构图随本版生产化更新;**旧图见 `docs/archive/2026-09-21-vision.md` 引用的旧版**。新图生成方式(TBD by P0.5):脚本入口待定,生成产物暂列 `docs/architecture.svg` 同名位置。纯标准库,可复现(同代码同输出);禁止另行维护 drawio/手绘第二事实源;改架构改脚本重跑。