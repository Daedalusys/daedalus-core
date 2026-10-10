# P4 Controller Skeleton + Drift + First End-to-End Design

> **状态**: 待用户审批(brainstorming 阶段产物;通过后由 writing-plans 落地实施计划)。
> **上游裁决**: `docs/desired-state-projection.md`(sdk#4)、`VISION.md §10 P4`、决策 25(契约缝锁定)。
> **核心痛点**: P3 已交付把对象模型全部字段钉死(Condition、Generation、UID、ResourceVersion、OwnerReferences、Finalizers),`internal/controller/types.go` 的 `ReconcileFunc`/`AdmissionFunc` 也已落码,但运行时为 0 接线;P4 reconcile 是把"形状长出行为"的最后一跃。当前用户既看不到自己的系统是否漂在期望,也无法让 controller 自动驱动一条 desired→observed 收敛路径——本设计补这两刀可见性。

## 1. Intent & why

VISION §10 P4 的完整范围:`controller runtime + 外部 controller 插件` + list-watch / reconcile 把已钉的契约缝长出行为;同时配 ownership mode 落地、drift 一等公民化、finalizer GC、events 流、workqueue backoff、动态 admission、kubectl-diff 一等命令、server-side dry-run。一次交付所有 = 大爆炸,违反 superpowers 的小步可验证原则;且 `docs/desired-state-projection.md §3` 的裁决(`unmanaged` 默认不漂、`managed/reconcile` 经 `daedalus-tx` 驱动)需要先让 schema 落地才能让后面任何 controller 实现都站在同一 policy 事实源之上。

**为什么是现在**:P3 收尾(2026-10-10)意味着 generation / finalizers / ownerRefs 都已有 writer,但**没有读者**——`BumpGeneration` 递增的字段值从未被比较过、`Metadata.Finalizers` 从未触发 GC、`SourceTx` 从未参与 generation 关联(`desired-state-projection.md §4` 已点名 "P4 B3 前置")。P4 早一步动工,这部分字段先获得语义生命。

**为什么是骨架 + 可见**:用户明确信号 = "最快搭建好骨架,然后也快速完成可见部分,其他的可以慢慢再补上"。骨架 = 把 controller runtime 的目录结构 + ReconcileFunc 注册中心 + 最小 informer 立起来,后续每补一块只需扩一个文件;可见 = 在"骨架没有 controller 真驱动"和"P4 完整 controller"两个极端之间,先让用户能用 `daedalus-host drift` 看见任何模式下 desired≠observed 的事实,再用一个最小端到端示例(service + managed/reconcile)证明 controller 经 `daedalus-tx` 真能收敛一条路径。其余缺口(list-watch 完整改造 / workqueue backoff / dynamic admission / finalizer GC / events 流)留 P4.x 慢补。

## 2. Goals / non-goals / anti-goals

### Goals(本次交付,可见可测)

| # | 目标 | 验收 |
| --- | --- | --- |
| G1 | `[ownership]` policy schema 落地 | `policy.toml` 解析新增 `Ownership` 节,Go SDK `policy.Policy.Ownership` 字段,`Default()` 返回零值;`enabled_kinds` 同款三点漂移测试钉死 `policy.toml ↔ policy.Default() ↔ ownership 常量表` |
| G2 | 四档 `unmanaged` / `observe` / `managed/manual` / `managed/reconcile` 模式生效(任一模式下都能查到 mode) | `daedalus-host ownership get <kind>/<name>` 子命令,返回当前 mode + 审计 `ownership_query` 条目 |
| G3 | drift 一等公民(任意模式都能看见) | `daedalus-host drift [--mode <mode>]` 子命令,扫描 journal applied 集合 vs 当前观测,输出 `kind/name desired observed source_tx drift` 行;同时落审计 `reconcile_drift` 条目;`unmanaged` 模式下输出空集合(因为无 ownership 模式 = 视为零期望)|
| G4 | controller runtime 骨架立起来(常驻但默认无 reconcile 行为) | `daedalus-host controller start [--foreground …]` 子命令 + `internal/controller/runtime/` 包;接 `ReconcileFunc` 注册中心;轮询式 informer(每 N 秒拉 state.jsonl 与 desiredview 增量对比);workqueue 用一个 channel 即可,backoff 留接口不实现 |
| G5 | end-to-end 最小一例:service + `managed/reconcile` 自动驱动 | 用 `nginx.service` 改 desired → controller 经 `daedalus-tx` 通道跑 `service.set` → observed 收敛;审计 `reconcile_drive` 条目可证;`status.observed_generation == metadata.generation` 钉死 |
| G6 | generation 比较语义第一次有真实消费点 | controller 只对 `metadata.generation > status.observed_generation` 的资源触发 reconcile;与 `desired-state-projection.md §4` 钉的"建 source_tx → generation 关联是 P4 B3 前置"对齐 |
| G7 | "绝不绕过 tx 裸改系统"承诺在 controller 层有审计证据 | 任何 controller 写动作 = 一次 `daedalus-tx apply` 调用;`daedalus-audit` 链上能看到 `reconcile_drive → tx.begin → tx.apply → tx.status=applied` 完整链 |

### Non-goals(本次明确不做)

| # | 不做 | 为什么 |
| --- | --- | --- |
| N1 | list-watch 完整改造(informer 真正事件驱动) | 骨架阶段用轮询足够(单机 OS,扫描周期 ≤ 5s 用户无感);事件驱动要重写 desiredview 的消费面,留 P4.x |
| N2 | workqueue 指数退避 + 速率限制 | 骨架阶段 channel + 简单重排队足够;指数退避要带 jitter 与持久化,留 P4.x |
| N3 | dynamic admission(运行时动态插入 AdmissionFunc) | VISION 已声明"v1 静态准入 = policy.toml 单实现";本设计维持 v1 静态,AdmissionFunc 契约缝保留 |
| N4 | finalizer GC / OwnerReference GC 触发逻辑 | 字段 + Validate 已交付(sdk#1 P3),逻辑归 P4 后续切片 |
| N5 | events 流(events API 而非 audit) | audit 是证据链;events 流由常驻 controller 生产,本次 skeleton 不引入第二事件流 |
| N6 | kubectl-diff 一等命令、server-side dry-run | VISION §8 C 体感层;独立 PR 后续开 |
| N7 | 第一个外部 `type=controller` 插件 | 本次 controller 跑在 core 仓内置;外部 controller 插件是 v2.0 起点,等本设计落地后再开 |

### Anti-goals(失败模式 = 即使技术上工作也算失败)

- **A1: 静默覆盖**:shell 通道修改 managed/reconcile 资源时,系统不能被 controller 偷偷改回去(硬边界软偏好不变);每改一次都必须产生可审计的 tx + 审计条目。
- **A2: 第二事实源**:ownership mode 不能既在 policy.toml 又在 Object spec;view schema 不能既走 desiredview 又走 ad-hoc 文件。
- **A3: 破坏契约**: `internal/controller/types.go` 的 `ReconcileFunc` / `AdmissionFunc` / `Op` / `Result` 字段增删改必须先经 builder review 后改;任何漂移必须先改 `types_test.go` 金样再改类型。
- **A4: fail-closed 失守**:policy.toml 缺失 `[ownership]` 节、`default` 值不在枚举内、kind override 引用未启用 kind——都必须 fail-closed 拒绝启动,而非静默回退到 `unmanaged`。
- **A5: generation 语义崩坏**:controller 不能改 spec 字段(包括 `desired_state`);它只能经 `daedalus-tx` 通道触发一次 apply;apply 完成后由 provider(`service.set` 等)回填 generation / status。

## 3. Constraints(verbatim from upstream)

- **controller 经 `daedalus-tx` 通道驱动**(desired-state-projection.md §3 第 2 项):"drift 立即成为一等公民... `managed/reconcile`: controller 经 `daedalus-tx` 通道驱动回 desired(每一次驱动都是可审计的显式事务,绝不绕过 tx 裸改系统)"。本设计的 G5/G7 直接复述。
- **ownership 模式枚举冻结**(desired-state-projection.md §3 表格):unmanaged / observe / managed/manual / managed/reconcile 四档,默认 `unmanaged`,policy.toml 声明,不在 Object spec。
- **policy schema 单一事实源**(vision §3):ownership 与 `[objectmodel].enabled_kinds` 同族先例(fail-closed 白名单);新增策略节必须配三点漂防。
- **形状单一事实源**(ARCHITECTURE.md §90 / sdk envelope.go header):Object / Metadata / Status / Condition 形状仅在 `daedalus-sdk/objectmodel`,`internal/controller` 走别名;本设计不改 SDK envelope 字段,只在 controller runtime 增加使用。
- **fail-closed**(vision §3 / policy.go LoadOrDefault):策略文件缺失或损坏,生产语义拒绝启动;只有 `DAEDALUS_POLICY_MODE=development` 才允许回退 Default();ownership 节继承同款语义。
- **审计单一合规写入口**(AGENTS.md):审计必须经 `daedalus-sdk/audit.LogAudit`;controller 不另开写入口。
- **Step 六键线上契约 + 适配器注册表**(internal/tx):controller 写事务必须经 `tx.List()` 路径回放后判断 source_tx,绝不直接读文件系统。

## 4. The parts of the how they decided

### 4.1 ownership schema 形态(钉死)

`policy.toml` 新增 `[ownership]` 节:

```toml
[ownership]
# 默认模式;未列入 [ownership].by_kind 的 kind 使用此值。枚举:
#   "unmanaged" / "observe" / "managed/manual" / "managed/reconcile"。
default = "unmanaged"

[ownership.by_kind]
# 按 kind 粒度覆盖默认;未列出的 kind 走 default。
# 示例(本设计默认值,非强制):service 一律 observe(可见不控)。
service = "observe"
package = "unmanaged"

# 可选:按 kind/name 粒度的 overrides(本设计 v1 不实现,留接口)
# [[ownership.overrides]]
# kind = "service"
# name = "nginx.service"
# mode = "managed/reconcile"
```

Go SDK `policy.Policy` 新增字段:

```go
type OwnershipMode string
const (
    OwnershipUnmanaged     OwnershipMode = "unmanaged"
    OwnershipObserve       OwnershipMode = "observe"
    OwnershipManagedManual OwnershipMode = "managed/manual"
    OwnershipManagedReconcile OwnershipMode = "managed/reconcile"
)
type Ownership struct {
    Default OwnershipMode            `toml:"default"`
    ByKind  map[string]OwnershipMode  `toml:"by_kind"`
}
type Policy struct {
    // ... 既有字段 ...
    Ownership Ownership `toml:"ownership"`
}
```

`policy.Default()` 返回 `Ownership{Default: OwnershipUnmanaged, ByKind: nil}`;`enabled_kinds` 同款三点漂移测试。

### 4.2 drift 检测语义

drift = `desiredview.Entry` 与 `state.jsonl` 最新值(经 `service.query` / `package.query` 观测)对比:同一 `(kind, name)`,`Entry.DesiredState` 与当前 `Properties[daedalus_runtime_state]` 不一致。

- `unmanaged` 模式:`Ownership.Default = unmanaged` 的资源 = 平台零期望 = `drift = []`(对照面默认空);只报告"`by_kind` 显式声明 unmanaged 但期望存在"的反向异常(本期不实现,留接口)。
- `observe` / `managed/manual` 模式:drift 报告 + 审计 `reconcile_drift` 条目,**永不自动驱动**。
- `managed/reconcile` 模式:drift 报告 + 审计 + controller 入 workqueue。

drift 行字段(命令行输出 + 审计条目共享):

```
kind=<Kind> name=<Name> desired=<v> observed=<v> source_tx=<tx-id> drift=<bool>
```

### 4.3 controller runtime 骨架

`internal/controller/runtime/` 包布局:

```
internal/controller/runtime/
├── registry.go        # ReconcileFunc 注册表(按 Kind 分发)
├── informer.go        # 最小 informer(轮询 state.jsonl + desiredview;返回 []Event)
├── workqueue.go       # channel-based workqueue,接口为 Enqueue/Run;backoff 留接口
├── reconciler.go      # for/range 调 ReconcileFunc,Result{Requeue/RequeueAfter}
├── policy.go          # ownership 查询 + drift 计算
├── drift.go           # drift 检测 + 报告格式
└── doc.go             # 包注释:零副作用承诺 + 接线指引
```

`cmd/daedalus-host/controller.go`(`main.go` 添加 `case "controller":` 分派):常驻进程;读 `daedalus-policy` → `daedalus-tx` 路径 → 起 informer → 起 workqueue worker;**v1 默认只对 `managed/reconcile` 资源驱动**;其他模式只跑 drift 报告(后台周期打印 + 审计)。子命令形式复用 host 既有 flag/audit/version 设施;常驻形态由 systemd `daedalus-host-controller.service` 单元拉起(在 host unit `After=` 依赖下,与 host 同生命周期;controller 崩溃重启 = host unit 也重启,本期接受)。

### 4.4 end-to-end 最小一例:nginx.service

1. `policy.toml` 添加 `[ownership].by_kind.service = "managed/reconcile"`(局部修改,其余不动)。
2. 用户手动 `daedalus-tx begin → propose service.set nginx.service desired_state=inactive → apply`(或经 copilot 翻译层)。
3. controller informer 在 ≤ 5s 内观测到 `desiredview.Entry{Nginx, inactive, source_tx=<tx-id>}` 新增。
4. ownership 查询:`service/nginx.service` mode = `managed/reconcile` → 入 workqueue。
5. reconciler 跑 `ReconcileFunc`:经 `daedalus-tx apply` 通道跑 `service.set nginx.service desired_state=inactive`(本例已是 inactive,所以一次 no-op apply + provider 回填 `status.observed_generation`)。
6. 审计链:`reconcile_drive` → `tx.begin` → `tx.apply` → `tx.status=applied` → `reconcile_idle`。

(实战示例应跑 `nginx → active` 一次让 observer 看到 status.observed_generation 从 0 升到 1。)

### 4.5 命令可见性

新增 `daedalus-host` 子命令:

- `daedalus-host drift [--kind <kind>] [--name <name>]`:打印 drift 表。
- `daedalus-host ownership get <kind>/<name>`:返回 mode。
- `daedalus-host controller start [--foreground / --log-file / --poll-interval <duration>]`:骨架阶段常驻;由 systemd unit `daedalus-host-controller.service`(与 `daedalus-host.service` 解耦,失败不拖死 host)拉起。

### 4.6 测试三层(钉死)

| 层 | 范围 | 例 |
| --- | --- | --- |
| unit | ownership 解析、drift 计算、informer event 转换 | `policy_test.go` 三点漂移;`drift_test.go` desired≠observed 三组;`informer_test.go` 轮询产出 |
| integration | journal + state + controller 三方联动 | `runtime_e2e_test.go`:mock 一份 journal + 一份 systemctl state,起 controller,断言 generation 收敛 |
| smoke(daedalus-smoke) | 真 systemctl + 真 journal + 真 controller 跑 `nginx` 一次 | 镜像内端到端 |

## 5. What's left to the builder

- **轮询周期**:N=2s 默认值是否合适 / 配置文件项名(留 `--poll-interval` flag,默认由 builder 选定)。
- **controller 进程身份**:euid=0 vs user-scope;本期 service / package kind 决定 euid=0 守门,与 `service.set` / `package.set` 同款镜像对称(builder 选定 systemd 单元文件)。
- **systemd unit 模板**:`daedalus-host-controller.service` 与 `daedalus-host.service` 解耦(Wants= + Restart=),但 command line 都经 `daedalus-host` 二进制;`Type=simple` 即可。
- **drift 表输出格式**:表格 / JSON / `kubectl get` 风格 Yaml(builder 选,本设计不预埋)。
- **覆盖粒度 `[[ownership.overrides]]`** 字段:本次 v1 不实现 schema 字段,但 Go struct 留 `Overrides []OwnershipOverride` 字段为待 patch 占位(builder 在 task 中显式留 `// TODO(p4.x): overrides v1 stub`)。

## 6. 失败路径(全部被测试钉死)

| 路径 | 行为 |
| --- | --- |
| `policy.toml` 缺失 `[ownership]` 节 | 启动 fail-closed,报 `ownership section missing` |
| `Ownership.Default` 取值不在枚举 | 启动 fail-closed,报 `ownership default mode invalid` |
| `Ownership.ByKind[k]` 引用未启用 kind | 启动 fail-closed,报 `ownership.by_kind.<k> not in enabled_kinds` |
| `Ownership.Default = managed/reconcile` 但 `enabled_kinds = []` | 启动 fail-closed,报 `ownership.mode=managed/reconcile requires enabled_kinds` |
| controller 写时 `daedalus-tx` 二进制缺失 | reconciler 报告 `tx binary not found`,审计 `reconcile_error`,workqueue requeue(无 backoff)|
| controller 写时 `daedalus-tx apply` 返回非零 | 同上,审计条目带 `tx_rc` |
| journal 损坏 | desiredview.Load 报错;controller 启动 fail-closed |
| service 未安装 + managed/reconcile | drift 报告;controller 试图 apply → systemctl adapter fail → 审计 `reconcile_error` + workqueue requeue |

## 7. 引用

- `VISION.md §10 P4`(完整 controller runtime 范围)
- `VISION.md §7②`(人在环先行 → P4 才放手给常驻进程)
- `VISION.md §8 B 架构层`(reconcile 循环 / list-watch / workqueue / admission / finalizer GC / events 全部归 P4)
- `docs/desired-state-projection.md`(期望投影 + ownership 模式 + drift 一等公民语义)
- `internal/controller/{doc,types}.go`(契约缝锁定,本次仅消费不改)
- `internal/desiredview/`(期望投影纯函数,本次新增消费方)
- `daedalus-sdk/policy/policy.go`(ownership 字段落点 + 三点漂移测试)
- `daedalus-sdk/objectmodel/envelope.go`(generation / status 形态,本次新增比较语义)