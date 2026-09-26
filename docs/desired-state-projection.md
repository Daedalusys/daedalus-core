# Desired-State Projection + Reconcile Ownership

**状态**: 裁决成文(daedalus-sdk#4);实现落点:投影 = `internal/desiredview` +
`internal/tx.List()`;controller 本体归 daedalus-core#2 (VISION §10 P4)。
**权威代码路径**: `internal/tx/tx.go`、`internal/desiredview/`;本文只写语义,不复述实现。

## 1. 投影管道

journal 仍是期望的唯一权威历史(event-sourced desired-state store);
Current Desired View 是派生缓存,不重造第二事实源(不引入 etcd/apiserver):

```
Transaction Journal → Desired-State Projector → Current Desired View → diff → reconcile
   <root>/<tx-id>.json      纯函数 replay            派生缓存,可删可重建
```

投影规则(v1 冻结):

1. **回放顺序**: 全部 journal 文件按 `created_at` 升序、同刻按 `id` 升序;
   单个事务内按 steps 顺序。后写覆盖前写(last-write-wins)。
2. **唯一提交点是 applied**: 只有 `status == applied` 的事务贡献期望值;
   `proposed` / `applying` / `rolled_back` / `failed` 一律跳过。propose 只是
   意图,人在环的 apply 才是期望的commit 时刻。
3. **贡献内容**: 步骤 `adapter` 经封闭映射表转成资源 kind
   (`service.set`→service、`package.set`→package),取 `args{name,
   desired_state}`;两键缺一即损坏(`desired_state` 缺失≠空期望)。
   表外 adapter(如未来的 blueprint.write)不是对象模型
   资源,**忽略**;但已知 adapter 的 args 解析失败 = journal 损坏,
   fail-closed 整体报错,绝不静默丢弃一条期望。

## 2. 失败路径语义(五类,全部被测试钉死)

| 路径 | journal 状态 | 对期望视图的影响 |
| --- | --- | --- |
| apply failed(首步即败) | failed | 无影响;期望仍是上一条 applied 的值 |
| partial apply(多步中途败) | failed | 整体无影响(事务原子);实际态可能已漂移,由观测面 diff 可见,处置靠 rollback 计划或人工 |
| rollback failed | 保持 applied(迁移表上 applied 唯一去向是 rolled_back) | 期望不变(仍贡献);物理半恢复由重试 `tx rollback` 收敛,视图不掺入"回滚进行中"猜测 |
| tx abandoned(proposed 烂尾 / applying 崩溃遗留) | 停在非终态 | 永不进视图;清理归运维(P4 可加提示),不引入新状态枚举(线上契约) |
| tx superseded(同资源重复变更) | 各自 applied | 隐式成立:回放序 last-write-wins,无需显式状态 |

**T1/T2/T3 锚例**: T1 `nginx→active` applied、T2 `nginx→inactive` applied、
T3 = rollback T2(状态翻为 rolled_back)⇒ 重放后当前期望 = **active**。
applied→rolled_back 的状态翻转不需要任何补偿记录——重放读的是最新状态。

## 3. Reconcile Ownership(management mode)

**裁决:mode 声明在 policy.toml,不放 Object spec。** 理由:spec 由提案方
(copilot/agent)填写,若期望视图的强制级别跟着 spec 走,提案方就能自行升格
自己被 reconcile 驱动——治理面与数据面必须分离;`[objectmodel].enabled_kinds`
同族先例(fail-closed 白名单)。

模式枚举(v1 四档,默认最保守):

| mode | 语义 |
| --- | --- |
| `unmanaged`(默认) | 平台只观测,controller 永不驱动 |
| `observe` | 报 drift(事件 + 审计),不动手 |
| `managed/manual` | 变更仍走显式 tx,人在环 |
| `managed/reconcile` | controller 持续驱动 desired→observed |

policy schema(归 P4 落地,连带三点漂移测试与 76 脚本,本文不预埋代码):

```toml
[ownership]
default = "unmanaged"
[ownership.by_kind]
service = "unmanaged"
# [[ownership.overrides]] 支持 kind/name 粒度 → mode
```

**shell 通道修改 managed/reconcile 资源时的系统行为**(拒绝"软偏好偷偷变成
eventual enforcement"):

1. 修改本身**不被禁止**(硬边界软偏好不变),经 shell/fs 通道照常执行并审计;
2. drift 立即成为一等公民:观测面 diff 发现 desired≠observed →
   `unmanaged`: 无动作;`observe`/`managed/manual`: 记 drift 事件 + 审计
   `reconcile_drift` 条目,永不自动驱动;`managed/reconcile`: controller 经
   `daedalus-tx` 通道驱动回 desired(每一次驱动都是可审计的显式事务,
   绝不绕过 tx 裸改系统)。

即:**静默覆盖只发生在用户显式 opt-in 的 managed/reconcile 资源上**,其余
模式 drift 永远可见但不可被平台擅动。

## 4. 快照、重建与上界

- Current Desired View 可删可重建,重建 = 全量 replay,复杂度
  O(journal 事务文件数 N),N 由运维清理(证据层归档)单独治理,投影层不
  承诺增量。
- **为什么不做 watermark 增量 checkpoint**: 事务状态存在 applied→rolled_back
  的合法翻转窗口,任何"已回放到哪"的水位线都会在翻转发生时失效,增量重放
  不 sound;全量重放是唯一正确姿势,快照只是读缓存(生成即整体重写,
  损坏即删除重建)。P4 若需降读延迟,优化的是快照生成频率,不是重放语义。
- generation 关联(P4 B3 前置):视图条目携带 `source_tx`(贡献该期望的最后
  事务 id),是 spec 变更→generation 递增的第一个真实消费点。

## 5. 非目标

- 不实现 controller 本体(daedalus-core#2 B1);本文与 `internal/desiredview`
  是它的前置裁决与输入件。
- 不引入第二策略事实源;`[ownership]` 落 policy.toml 时走三点漂移链。
- 不改 tx 状态枚举与 Step 六键线上契约。
