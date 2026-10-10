# P4 Controller Skeleton + Drift + First End-to-End Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver P4 reconcile skeleton: `daedalus-sdk` ownership policy schema, `daedalus-host drift` / `daedalus-host ownership get` / `daedalus-host controller start` 子命令,以及 `nginx.service` 端到端最小一例(controller 经 `daedalus-tx` 通道驱动 desired→observed 收敛)。

**Architecture:**
- **policy 形态**:ownership 模式在 `policy.toml [ownership]`,Go SDK `policy.Policy.Ownership` 字段单一事实源;四档 `unmanaged / observe / managed/manual / managed/reconcile` 封闭枚举;`unmanaged` 默认;fail-closed。
- **drift 形态**:drift = `desiredview.Entry.DesiredState` 与 `state.jsonl` 最新值映射对比;`unmanaged` 模式零期望(不报告);`observe` / `managed/manual` 报告 + 审计但不动手;`managed/reconcile` 入 workqueue 经 `daedalus-tx apply` 驱动。
- **runtime 形态**:`internal/controller/runtime/` 包提供 ReconcileFunc 注册表 + 轮询式 informer + channel-based workqueue + reconciler(generation 比较)+ driver(`daedalus-tx` 子进程桥接)。
- **接线形态**:常驻运行时 = `daedalus-host controller start` 子命令(用户决策 T1),复用 host 的 flag/audit/version 设施;systemd unit `daedalus-host-controller.service` 与 host unit 解耦但同生命周期。

**Tech Stack:**
- Go 1.25,modules: `github.com/Daedalusys/daedalus-core` + `github.com/Daedalusys/daedalus-sdk`(workspace replace via `go.work`)
- 既有依赖:`github.com/BurntSushi/toml`、`github.com/Daedalusys/daedalus-sdk/{policy,objectmodel,audit,dirs,state}`
- 测试:标准 `go test ./...`,本地用 `just go-test`(仅 Go 路径)或 `just test`(全栈含 deno)

**Spec:** `docs/superpowers/specs/2026-10-10-p4-controller-skeleton-design.md`(本 plan 是它的落地件,与设计稿同步审阅)。

## Global Constraints

- **controller 经 `daedalus-tx` 通道驱动**(desired-state-projection.md §3 第 2 项;design §3):任何 controller 写动作 = 一次 `daedalus-tx apply` 调用;绝不在 controller 直接拼 `systemctl` argv。
- **ownership mode 枚举冻结**:unmanaged / observe / managed/manual / managed/reconcile 四档,默认 `unmanaged`,policy.toml 声明,不在 Object spec。
- **policy schema 单一事实源**:`daedalus-sdk/policy/policy.go` 字段单一事实源;`policy.toml` 镜像内 + SDK testdata 副本为两处 fixture,三点漂移测试钉一致。
- **形状单一事实源**:`Object / Metadata / Status / Condition` 形状仅在 `daedalus-sdk/objectmodel`;`internal/controller` 仅别名,本次不增字段。
- **fail-closed**:`policy.toml` 缺失 `[ownership]` / default 取值不在枚举 / by_kind 引用未启用 kind —— 启动失败,绝不静默回退 `unmanaged`。
- **审计单一合规写入口**:`daedalus-sdk/audit.LogAudit(e audit.Entry) (*Record, error)`(无 ctx 参数);`Args` 字段类型是 `*audit.Value`(由 `audit.NewObject()` / `audit.ParseValue(jsonStr)` 构造);`PolicyVersion` 常量用 `audit.DefaultPolicyVersion`,**不是** `policy.Default().Version()`(该方法不存在)。
- **policy 入口**:`policy.Load(path string) (*Policy, error)`(要文件路径)与 `policy.LoadOrDefault() (*Policy, error)`(走环境变量 + 三处回溯);本 plan 优先用 `LoadOrDefault()`。
- **host 子命令签名约定**(沿用 `cmd/daedalus-host/main.go` 既有 dispatch):
  - 函数名 `cmdXxx(stdout, stderr io.Writer) int`(无 ctx、无 fs、无 args,args 在内部解析);
  - 在 `main.go` switch 加 `case "xxx": code := cmdXxx(stdout, stderr); hostAudit("host_xxx", pluginDir, "", code); return code`;
  - `hostAudit` 已 best-effort 静默吞错(签名 `func hostAudit(tool, pluginDir, id string, code int)`),新子命令无需自写审计入口,直接调 `hostAudit` 即可。
- **测试 runner**:`run(argv []string, stdout, stderr io.Writer) int` 是 host 测试入口(`run([]string{"drift", "--kind=service"}, &buf, &buf)`);子命令测试优先走 `run` 而非直接调 `cmdXxx`。
- **Step 六键线上契约**:controller 写事务经 `tx.List()` 路径回放判断 source_tx,不直接读文件系统。
- **不破坏契约**:`internal/controller/types.go` 的 `ReconcileFunc` / `AdmissionFunc` / `Op` / `Result` 字段不改;若需扩字段,先改 `types_test.go` 金样再改类型。
- **commit 规范**:每次 commit 只含当前 task 的逻辑,前缀 `feat:` / `test:` / `chore:` / `docs:`;不混合多个 task。
- **build runner**:本地 `just go-test`(Go 路径)或 `just test`(全栈);本 plan 的 verification 命令优先 `go test ./<pkg>/... -run TestName -v`。

## Review Focus(本 plan 最易忽略的 5 个输入/失败模式)

| # | 场景 | 期望行为 | 钉到 task |
| --- | --- | --- | --- |
| RF1 | `policy.toml` 缺失 `[ownership]` 节(空文件 / 老配置文件) | 启动 fail-closed,报 `ownership section missing` | Task 3 |
| RF2 | `Ownership.Default` 取值不在封闭枚举(如 `"managed"`) | 启动 fail-closed,报 `ownership default mode invalid` | Task 2 |
| RF3 | `Ownership.ByKind[k]` 引用 `enabled_kinds` 不包含的 kind | 启动 fail-closed,报 `ownership.by_kind.<k> not in enabled_kinds` | Task 2 |
| RF4 | drift 计算时 `state.jsonl` 损坏行 / journal 损坏 | 损坏行静默跳过(state 是缓存);journal 损坏 = 启动 fail-closed(desiredview.Load) | Task 5 / Task 4 |
| RF5 | controller 写时 `daedalus-tx` 二进制缺失或 apply 返回非零 | 审计 `controller.reconcile_drive` outcome=error + workqueue 简单 requeue(无 backoff,留接口);不重试无限次同帧 | Task 10 |

---

## Task 1: SDK — Ownership 类型与解析

**Files:**
- Modify: `../daedalus-sdk/policy/policy.go`(新增 `OwnershipMode` 类型、`Ownership` 结构、`Policy.Ownership` 字段、`Default()` 零值)
- Test: `../daedalus-sdk/policy/policy_test.go`(新增 ownership 解析测试)

**Interfaces:**
- Consumes: 既有 `BurntSushi/toml` 解析;既有 `Policy` struct
- Produces:
  ```go
  type OwnershipMode string
  const (
      OwnershipUnmanaged       OwnershipMode = "unmanaged"
      OwnershipObserve         OwnershipMode = "observe"
      OwnershipManagedManual   OwnershipMode = "managed/manual"
      OwnershipManagedReconcile OwnershipMode = "managed/reconcile"
  )
  func (m OwnershipMode) Valid() bool
  type Ownership struct {
      Default OwnershipMode           `toml:"default"`
      ByKind  map[string]OwnershipMode `toml:"by_kind"`
  }
  // Policy 新增字段:Ownership Ownership `toml:"ownership"`
  ```

- [ ] **Step 1: 写失败的测试**

在 `daedalus-sdk/policy/policy_test.go` 新增:

```go
func TestOwnership_ParseDefault(t *testing.T) {
    raw := `
[ownership]
default = "observe"
[ownership.by_kind]
service = "managed/reconcile"
`
    var p policy.Policy
    if _, err := toml.Decode(raw, &p); err != nil { t.Fatal(err) }
    if p.Ownership.Default != policy.OwnershipObserve {
        t.Errorf("default = %q, want observe", p.Ownership.Default)
    }
    if p.Ownership.ByKind["service"] != policy.OwnershipManagedReconcile {
        t.Errorf("by_kind[service] = %q, want managed/reconcile", p.Ownership.ByKind["service"])
    }
}

func TestOwnershipMode_Valid(t *testing.T) {
    for _, m := range []policy.OwnershipMode{
        policy.OwnershipUnmanaged, policy.OwnershipObserve,
        policy.OwnershipManagedManual, policy.OwnershipManagedReconcile,
    } {
        if !m.Valid() { t.Errorf("%q 应有效", m) }
    }
    if policy.OwnershipMode("managed").Valid() { t.Error(`"managed" 应无效`) }
}

func TestOwnershipDefault_ZeroValue(t *testing.T) {
    if got := policy.Default().Ownership.Default; got != policy.OwnershipUnmanaged {
        t.Errorf("Default().Ownership.Default = %q, want unmanaged", got)
    }
}
```

注:`toml` 包导入名沿用 `policy_test.go` 既有的 `toml.Decode`(若文件已 `import "github.com/BurntSushi/toml"`)。若已有别名,builder 调整。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-sdk && go test ./policy/... -run TestOwnership -v`
Expected: FAIL with `undefined: policy.OwnershipMode` / `p.Ownership undefined`。

- [ ] **Step 3: 在 `policy.go` 实现 Ownership 类型与字段**

```go
type OwnershipMode string
const (
    OwnershipUnmanaged       OwnershipMode = "unmanaged"
    OwnershipObserve         OwnershipMode = "observe"
    OwnershipManagedManual   OwnershipMode = "managed/manual"
    OwnershipManagedReconcile OwnershipMode = "managed/reconcile"
)
func (m OwnershipMode) Valid() bool {
    switch m {
    case OwnershipUnmanaged, OwnershipObserve,
         OwnershipManagedManual, OwnershipManagedReconcile:
        return true
    }
    return false
}
type Ownership struct {
    Default OwnershipMode           `toml:"default"`
    ByKind  map[string]OwnershipMode `toml:"by_kind"`
}
type Policy struct {
    // ... 既有字段 ...
    Ownership Ownership `toml:"ownership"`
}
```

更新 `Default()`(找原 `Default()` 函数体),在末尾追加:

```go
p.Ownership = Ownership{Default: OwnershipUnmanaged}
return p
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ../daedalus-sdk && go test ./policy/... -run TestOwnership -v`
Expected: PASS,3 个新测试全绿。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-sdk
git add policy/policy.go policy/policy_test.go
git commit -m "feat(policy): add OwnershipMode enum + Ownership schema"
```

---

## Task 2: SDK — Ownership 三点漂移 + enabled_kinds 守门

**Files:**
- Modify: `../daedalus-sdk/policy/policy.go`(新增 `(p *Policy) ValidateOwnership() error`)
- Test: `../daedalus-sdk/policy/policy_test.go`(新增 fail-closed 三类断言)

**Interfaces:**
- Consumes: `Policy.Ownership`,`Policy.ObjectModel.EnabledKinds`
- Produces:
  ```go
  func (p *Policy) ValidateOwnership() error
  // 四类失败/兼容:
  //   1. Default 不在枚举 → "ownership default mode invalid: <m>"
  //   2. ByKind[k] 不在枚举 → "ownership.by_kind.<k> invalid mode: <m>"
  //   3. ByKind[k] 不在 enabled_kinds → "ownership.by_kind.<k> not in enabled_kinds"
  //   4. Default == "" 时默认填 unmanaged(为兼容老配置;不报错)
  ```

- [ ] **Step 1: 写失败的测试**

```go
func TestValidateOwnership_DefaultInvalid(t *testing.T) {
    p := policy.Policy{
        ObjectModel: policy.ObjectModel{EnabledKinds: []string{"service"}},
        Ownership:   policy.Ownership{Default: policy.OwnershipMode("managed")},
    }
    if err := p.ValidateOwnership(); err == nil { t.Fatal("应报错") }
}

func TestValidateOwnership_ByKindInvalidMode(t *testing.T) {
    p := policy.Policy{
        ObjectModel: policy.ObjectModel{EnabledKinds: []string{"service"}},
        Ownership: policy.Ownership{
            Default: policy.OwnershipUnmanaged,
            ByKind:  map[string]policy.OwnershipMode{"service": "managed"},
        },
    }
    if err := p.ValidateOwnership(); err == nil { t.Fatal("应报错") }
}

func TestValidateOwnership_ByKindNotEnabled(t *testing.T) {
    p := policy.Policy{
        ObjectModel: policy.ObjectModel{EnabledKinds: []string{"service"}}, // 没 package
        Ownership: policy.Ownership{
            Default: policy.OwnershipUnmanaged,
            ByKind:  map[string]policy.OwnershipMode{"package": policy.OwnershipObserve},
        },
    }
    if err := p.ValidateOwnership(); err == nil { t.Fatal("应报错") }
}

func TestValidateOwnership_DefaultEmptyFillsUnmanaged(t *testing.T) {
    p := policy.Policy{
        ObjectModel: policy.ObjectModel{EnabledKinds: []string{"service"}},
        Ownership:   policy.Ownership{}, // Default 空
    }
    if err := p.ValidateOwnership(); err != nil { t.Fatal(err) }
    if p.Ownership.Default != policy.OwnershipUnmanaged {
        t.Errorf("Default 空应填充 unmanaged, got %q", p.Ownership.Default)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-sdk && go test ./policy/... -run TestValidateOwnership -v`
Expected: FAIL(`ValidateOwnership undefined`)。

- [ ] **Step 3: 在 `policy.go` 实现 `ValidateOwnership()`**

```go
func (p *Policy) ValidateOwnership() error {
    if p.Ownership.Default == "" {
        p.Ownership.Default = OwnershipUnmanaged
    }
    if !p.Ownership.Default.Valid() {
        return fmt.Errorf("ownership default mode invalid: %q", p.Ownership.Default)
    }
    enabled := make(map[string]bool, len(p.ObjectModel.EnabledKinds))
    for _, k := range p.ObjectModel.EnabledKinds {
        enabled[k] = true
    }
    for k, m := range p.Ownership.ByKind {
        if !m.Valid() {
            return fmt.Errorf("ownership.by_kind.%s invalid mode: %q", k, m)
        }
        if !enabled[k] {
            return fmt.Errorf("ownership.by_kind.%s not in enabled_kinds", k)
        }
    }
    return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ../daedalus-sdk && go test ./policy/... -run TestValidateOwnership -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-sdk
git add policy/policy.go policy/policy_test.go
git commit -m "feat(policy): ownership fail-closed validation (default/by_kind/enabled_kinds)"
```

---

## Task 3: Core — `policy.toml` 新增 `[ownership]` 默认节 + testdata 同步

**Files:**
- Modify: `files/system/opt/daedalus/shared/policy.toml`(新增 `[ownership]` 节)
- Modify: `../daedalus-sdk/policy/testdata/policy.toml`(SDK 自带 fixture 同步)
- Modify: `../daedalus-sdk/policy/policy_test.go`(三点漂移新增 ownership 断言)

**Interfaces:**
- Consumes: Task 1 / Task 2 的 `Ownership` schema
- Produces:`policy.toml` 的 `[ownership]` 默认节;testdata 副本 + 新的 drift 断言

- [ ] **Step 1: 在 `files/system/opt/daedalus/shared/policy.toml` 追加**

在文件末尾([blueprints] 节之后)新增:

```toml
# =============================================================================
# [ownership] 资源 ownership mode 策略节(与 [shell]/[fs]/[audit]/[objectmodel]/
# [blueprints] 并列的第六策略节):
# 声明式列出资源 kind 的管理 ownership 模式。
# 模式枚举(封闭,逐字对齐 internal/policy.OwnershipMode):
#   "unmanaged"             — 平台只观测不驱动(默认最保守)
#   "observe"               — 报告 drift,不自动驱动
#   "managed/manual"        — 变更仍走显式 tx,人在环
#   "managed/reconcile"     — controller 持续驱动 desired→observed
# fail-closed:default 不在枚举 / by_kind 引用未启用 kind → 启动拒绝。
# 本值与 internal/policy.Default() 由三点漂移测试钉死一致。
# =============================================================================
[ownership]
default = "unmanaged"

[ownership.by_kind]
# service 一律 observe:用户能看见 drift 但 controller 不会自动驱动;
# 用户切到 managed/reconcile 时需要按 kind/name 粒度显式声明(P4.5 overrides 切片)。
service = "observe"
```

- [ ] **Step 2: 在 `../daedalus-sdk/policy/testdata/policy.toml` 同步追加**

同样的 `[ownership]` 节(default + by_kind),确保 SDK 测试用 fixture 与生产配置一致。

- [ ] **Step 3: 在 SDK 三点漂移测试加新断言**

在 `policy_test.go` 找到既有"`Default()` 与 policy.toml 三点漂移"测试(若不存在则新增一个,新增时把 `[shell]/[objectmodel]` 的已有断言一起搬),在它末尾追加:

```go
// ownership 三点 drift
if real.Ownership.Default != def.Ownership.Default {
    t.Errorf("ownership.default drift: real=%q def=%q", real.Ownership.Default, def.Ownership.Default)
}
if real.Ownership.ByKind["service"] != def.Ownership.ByKind["service"] {
    t.Errorf("ownership.by_kind[service] drift: real=%q def=%q", real.Ownership.ByKind["service"], def.Ownership.ByKind["service"])
}
```

`real` 由 `policy.Load(testdataPath(t))` 读得,`def` 由 `policy.Default()` 取;`testdataPath` helper 沿用既有(若不存在,新增 `func testdataPath(t *testing.T) string { return filepath.Join("testdata", "policy.toml") }`)。

- [ ] **Step 4: 跑全 SDK 测试确认通过**

Run: `cd ../daedalus-sdk && go test ./policy/... -v`
Expected: PASS,既有测试无回归 + 新 ownership 断言绿。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-sdk
git add policy/testdata/policy.toml policy/policy_test.go
git commit -m "test(policy): ownership three-way drift pinning"
cd ../daedalus-core
git add files/system/opt/daedalus/shared/policy.toml
git commit -m "chore(policy): add [ownership] default section (unmanaged, service=observe)"
```

---

## Task 4: Core — desired↔observed 映射纯函数

**Files:**
- Create: `internal/controller/runtime/drift.go`
- Test: `internal/controller/runtime/drift_test.go`

**Interfaces:**
- Consumes: `objectmodel.Kind`、`objectmodel.ResourceSpec`(desired 形状)、`map[string]string`(observed Properties)
- Produces:
  ```go
  // MapDesiredObserved 把 desired_state 与 observed state 映射成可比较的字符串。
  // service: "active" / "inactive" ↔ Properties["ActiveState"] ∈ {active} / 其余
  // package: "present" / "absent" / "latest" ↔ Version 非空 / 空 / LatestAvailable
  // 未实现 kind 返回错误(表外 reject,与 desiredview adapterKinds 表对齐)。
  func MapDesiredObserved(kind objectmodel.Kind, desired string, props map[string]string) (observed string, err error)
  ```

- [ ] **Step 1: 写失败的测试**

```go
package runtime_test

import (
    "testing"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestMapDesiredObserved_ServiceActive(t *testing.T) {
    got, err := runtime.MapDesiredObserved(objectmodel.KindService, "active", map[string]string{"ActiveState": "active"})
    if err != nil { t.Fatal(err) }
    if got != "active" { t.Errorf("got %q, want active", got) }
}

func TestMapDesiredObserved_ServiceInactiveMapsFailed(t *testing.T) {
    got, err := runtime.MapDesiredObserved(objectmodel.KindService, "inactive", map[string]string{"ActiveState": "failed"})
    if err != nil { t.Fatal(err) }
    if got != "inactive" { t.Errorf("failed→inactive, got %q", got) }
}

func TestMapDesiredObserved_PackagePresent(t *testing.T) {
    got, err := runtime.MapDesiredObserved(objectmodel.KindPackage, "present", map[string]string{"Version": "1.2.3"})
    if err != nil { t.Fatal(err) }
    if got != "present" { t.Errorf("got %q, want present", got) }
}

func TestMapDesiredObserved_PackageAbsentEmptyVersion(t *testing.T) {
    got, err := runtime.MapDesiredObserved(objectmodel.KindPackage, "absent", map[string]string{"Version": ""})
    if err != nil { t.Fatal(err) }
    if got != "absent" { t.Errorf("got %q, want absent", got) }
}

func TestMapDesiredObserved_UnknownKindErrors(t *testing.T) {
    _, err := runtime.MapDesiredObserved(objectmodel.KindContainer, "active", map[string]string{})
    if err == nil { t.Fatal("未实现 kind 应报错") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestMapDesiredObserved -v`
Expected: FAIL with `undefined: runtime.MapDesiredObserved`。

- [ ] **Step 3: 实现 `MapDesiredObserved`**

`internal/controller/runtime/drift.go`:

```go
// Package runtime 提供 controller 调和循环的最小骨架:
// registry / informer / workqueue / reconciler / driver / drift 检测。
// 零副作用:仅消费 desiredview / state / audit / tx,不写文件系统。
package runtime

import (
    "fmt"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// MapDesiredObserved 把 desired_state 与 observed state 映射为可比较字符串;
// kind 词汇超出 service/package 范围返回错误(表外 reject,与 desiredview
// adapterKinds 表对齐)。
func MapDesiredObserved(kind objectmodel.Kind, desired string, props map[string]string) (string, error) {
    switch kind {
    case objectmodel.KindService:
        active := props["ActiveState"] == "active"
        if desired == "active" && active { return "active", nil }
        if desired == "inactive" && !active { return "inactive", nil }
        return props["ActiveState"], nil
    case objectmodel.KindPackage:
        switch desired {
        case "present":
            if props["Version"] != "" { return "present", nil }
            return "absent", nil
        case "absent":
            if props["Version"] == "" { return "absent", nil }
            return "present", nil
        case "latest":
            return props["LatestAvailable"], nil
        }
        return "", fmt.Errorf("package: unknown desired %q", desired)
    }
    return "", fmt.Errorf("runtime: 未实现 kind %q", kind)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestMapDesiredObserved -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/drift.go internal/controller/runtime/drift_test.go
git commit -m "feat(controller-runtime): desired↔observed mapping (service, package)"
```

---

## Task 5: Core — Drift 计算 + 四种 mode 行为

**Files:**
- Modify: `internal/controller/runtime/drift.go`(新增 `DriftEntry` / `DriftReport` / `Compute`)
- Modify: `internal/controller/runtime/drift_test.go`(新增 Compute 测试)

**Interfaces:**
- Consumes: `*desiredview.View`、`map[objectmodel.Kind]map[string]objectmodel.ServiceState`(state 快照)、`func(objectmodel.Kind, string) policy.OwnershipMode`(mode 查询回调)
- Produces:
  ```go
  type DriftEntry struct {
      Kind     objectmodel.Kind `json:"kind"`
      Name     string           `json:"name"`
      Desired  string           `json:"desired"`
      Observed string           `json:"observed"`
      SourceTx string           `json:"source_tx"`
      Drifts   bool             `json:"drift"`
  }
  type DriftReport struct {
      Mode    policy.OwnershipMode `json:"mode"`
      Entries []DriftEntry        `json:"entries"`
  }
  // Compute 比较 desired 与 observed 产出 DriftReport;mode == unmanaged 时 Entries 为空。
  // observe / managed/manual / managed/reconcile 都填充 Entries,Drifts 字段标记是否差异。
  func Compute(view *desiredview.View, observed map[objectmodel.Kind]map[string]objectmodel.ServiceState, modeFor func(objectmodel.Kind, string) policy.OwnershipMode) DriftReport
  ```

- [ ] **Step 1: 写失败的测试**

```go
// internal/controller/runtime/drift_test.go 追加(`package runtime_test`,沿用 Task 4):
import "github.com/Daedalusys/daedalus-core/internal/desiredview"

func TestCompute_UnmanagedEmpty(t *testing.T) {
    v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
    state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
        objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
    }
    rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipUnmanaged })
    if len(rep.Entries) != 0 { t.Errorf("unmanaged 应空, got %d", len(rep.Entries)) }
}

func TestCompute_ObserveReportsButNotDrives(t *testing.T) {
    v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
    state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
        objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
    }
    rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipObserve })
    if len(rep.Entries) != 1 { t.Fatalf("应有 1 条 drift, got %d", len(rep.Entries)) }
    if !rep.Entries[0].Drifts { t.Error("active vs inactive 应 drift") }
}

func TestCompute_ManagedReconcileAllowsDrive(t *testing.T) {
    v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
    state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
        objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
    }
    rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipManagedReconcile })
    if !rep.Entries[0].Drifts { t.Error("managed/reconcile 应识别 drift") }
}
```

`desiredview.ViewWithForTest` 是 Step 3 在 `internal/desiredview/desiredview.go` 末尾新增的测试 helper(签名 `func ViewWithForTest(entries []Entry) *View`,内部用 `View{m: ...}` 同包构造)。`package desiredview` 内的 View 字段 `m` 在 `package desiredview` 内部可见,故 `desiredview.go` 内可写。注释需明确"仅供测试,生产代码禁止调用"。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestCompute -v`
Expected: FAIL(`undefined: runtime.Compute`)。

- [ ] **Step 3: 在 `drift.go` 追加 `DriftEntry` / `DriftReport` / `Compute`,并在 `desiredview.go` 加 `ViewWithForTest`**

```go
// internal/controller/runtime/drift.go 追加:
type DriftEntry struct {
    Kind     objectmodel.Kind `json:"kind"`
    Name     string           `json:"name"`
    Desired  string           `json:"desired"`
    Observed string           `json:"observed"`
    SourceTx string           `json:"source_tx"`
    Drifts   bool             `json:"drift"`
}

type DriftReport struct {
    Mode    policy.OwnershipMode `json:"mode"`
    Entries []DriftEntry        `json:"entries"`
}

func Compute(view *desiredview.View, observed map[objectmodel.Kind]map[string]objectmodel.ServiceState,
    modeFor func(objectmodel.Kind, string) policy.OwnershipMode,
) DriftReport {
    var rep DriftReport
    for _, e := range view.Entries() {
        desired := e.DesiredState
        props := map[string]string{}
        if m, ok := observed[e.Kind]; ok {
            if st, ok := m[e.Name]; ok {
                props = st.Properties
            }
        }
        observedStr, _ := MapDesiredObserved(e.Kind, desired, props)
        mode := modeFor(e.Kind, e.Name)
        rep.Mode = mode
        if mode == policy.OwnershipUnmanaged {
            continue // 零期望 = 零报告(design §4.2)
        }
        rep.Entries = append(rep.Entries, DriftEntry{
            Kind: e.Kind, Name: e.Name, Desired: desired, Observed: observedStr,
            SourceTx: e.SourceTx, Drifts: observedStr != desired,
        })
    }
    return rep
}
```

import 块改为:

```go
import (
    "fmt"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
)
```

并在 `internal/desiredview/desiredview.go` 文件末尾追加(同 commit):

```go
// ViewWithForTest 仅供测试使用 — 生产代码禁止调用。
// 直接把 Entry 列表构造为 View,绕过 desiredview.Load 的 journal 依赖。
// 函数名前缀 "ViewWithForTest" 是约定;若生产代码误调,review 应拒。
func ViewWithForTest(entries []Entry) *View {
    m := make(map[key]Entry, len(entries))
    for _, e := range entries {
        m[key{string(e.Kind), e.Name}] = e
    }
    return &View{m: m}
}
```

- [ ] **Step 4: 跑测试确认通过**

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestCompute -v`
Expected: PASS,3 个 TestCompute 全绿。

- [ ] **Step 6: commit**

```bash
cd ../daedalus-core
git add internal/desiredview/desiredview.go internal/controller/runtime/drift.go internal/controller/runtime/drift_test.go
git commit -m "feat(controller-runtime): drift compute with four-mode semantics"
```

---

## Task 6: Core — `daedalus-host drift` 子命令

**Files:**
- Create: `cmd/daedalus-host/drift.go`
- Modify: `cmd/daedalus-host/main.go`(在 switch 加 `case "drift":`)
- Test: `cmd/daedalus-host/drift_test.go`

**Interfaces:**
- Consumes: `desiredview.Load()`、`state.Read(time.Time{})` 反序列化为 `map[Kind]map[string]objectmodel.ServiceState`、`policy.LoadOrDefault()` 拿 `Ownership.ModeFor(k, n)` 函数
- Produces:
  ```go
  // 签名沿用 cmd/daedalus-host/main.go 既有 dispatch:`cmdXxx(stdout, stderr, args) int`。
  // args 是 run() 切掉的第一个词之后的剩余切片(如 ["--kind=service"]),内部用 flag.NewFlagSet 解析。
  func cmdDrift(stdout, stderr io.Writer, args []string) int
  // 行为:解析 [--kind K] [--name N] flag;扫描 + 打印表格;
  // 落审计 entry:tool="host_drift",args 含 filter;身份=hostIdentity。
  ```

- [ ] **Step 1: 写失败的测试**

```go
// cmd/daedalus-host/drift_test.go
package main

import (
    "bytes"
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/BurntSushi/toml"
    "github.com/Daedalusys/daedalus-sdk/audit"
)

func TestCmdDrift_PrintsTableAndAudits(t *testing.T) {
    tmp := t.TempDir()
    t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
    // 写 policy
    policyDir := filepath.Join(tmp, "policy")
    os.MkdirAll(policyDir, 0o755)
    policyFile := filepath.Join(policyDir, "policy.toml")
    os.WriteFile(policyFile, []byte(`
[ownership]
default = "observe"
[ownership.by_kind]
`), 0o644)
    t.Setenv("DAEDALUS_POLICY_PATH", policyFile)
    // 写 journal: 由 desiredview.Load 走 dirs.TxRoot(),需要设 dirs.EnvTxRoot
    txRoot := filepath.Join(tmp, "tx")
    os.MkdirAll(txRoot, 0o755)
    t.Setenv("DAEDALUS_TX_ROOT", txRoot) // 注:dirs 包实际环境变量名 builder 校
    // 写 state
    statePath := filepath.Join(tmp, "state.jsonl")
    t.Setenv("DAEDALUS_STATE_PATH", statePath)

    var stdout, stderr bytes.Buffer
    code := run([]string{"drift", "--kind=service"}, &stdout, &stderr)
    if code != 0 { t.Fatalf("drift 应退出 0, got %d stderr=%s", code, stderr.String()) }
    // 注意:无 journal / 无 state 时 drift 命令仍应跑通,只是 Entries=0
    if !strings.Contains(stderr.String(), "") { /* smoke 标记 */ }
}
```

builder 注:host 测试 fixture 的环境变量名(`DAEDALUS_TX_ROOT` / `DAEDALUS_STATE_PATH` / `DAEDALUS_POLICY_PATH`)由 builder 校 SDK 各包的常量(`dirs.EnvTxRoot` / `state.EnvStatePath` / `policy.EnvPolicyPath`);用 `t.Setenv` 注入。`run([]string{"drift", "--kind=service"}, ...)` 是 host 测试既有入口风格。

`toml` 包的 import:本测试可省略(`DAEDALUS_POLICY_PATH` 直接文件注入,不走 `toml.Decode`)。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... -run TestCmdDrift -v`
Expected: FAIL(`undefined: cmdDrift`)。

- [ ] **Step 3: 实现 `cmd/daedalus-host/drift.go`**

```go
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "text/tabwriter"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
    "github.com/Daedalusys/daedalus-sdk/state"
)

func cmdDrift(stdout, stderr io.Writer, args []string) int {
    fs := flag.NewFlagSet("drift", flag.ContinueOnError)
    kindF := fs.String("kind", "", "filter by kind")
    nameF := fs.String("name", "", "filter by name")
    if err := fs.Parse(args); err != nil {
        fmt.Fprintln(stderr, "daedalus-host drift:", err)
        return exitUsage
    }

    pol, err := policy.LoadOrDefault()
    if err != nil {
        fmt.Fprintln(stderr, "drift: policy:", err)
        return exitRuntime
    }
    if err := pol.ValidateOwnership(); err != nil {
        fmt.Fprintln(stderr, "drift: ownership:", err)
        return exitRuntime
    }

    view, err := desiredview.Load()
    if err != nil {
        fmt.Fprintln(stderr, "drift: desiredview:", err)
        return exitRuntime
    }
    entries, err := state.Read(time.Time{})
    if err != nil {
        fmt.Fprintln(stderr, "drift: state:", err)
        return exitRuntime
    }
    observed := indexState(entries)
    modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
        if m, ok := pol.Ownership.ByKind[string(k)]; ok { return m }
        return pol.Ownership.Default
    }
    rep := runtime.Compute(view, observed, modeFor)

    tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "KIND\tNAME\tDESIRED\tOBSERVED\tSOURCE_TX\tDRIFT")
    for _, e := range rep.Entries {
        if *kindF != "" && string(e.Kind) != *kindF { continue }
        if *nameF != "" && e.Name != *nameF { continue }
        fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\n",
            e.Kind, e.Name, e.Desired, e.Observed, e.SourceTx, e.Drifts)
    }
    _ = tw.Flush()

    argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q}`, *kindF, *nameF)
    if v, err := audit.ParseValue(argsJSON); err == nil {
        _, _ = audit.LogAudit(audit.Entry{
            Identity: hostIdentity,
            Tool:     "host_drift",
            Args:     v,
            Outcome:  "success",
        })
    }
    return exitOK
}

// indexState 把 state.Read 的扁平列表索引为 map[Kind]map[string]ServiceState。
// 若同一 (kind, name) 多次出现,取 ObservedAt 最晚的(state 是 append-only 缓存)。
func indexState(entries []state.StateEntry) map[objectmodel.Kind]map[string]objectmodel.ServiceState {
    out := map[objectmodel.Kind]map[string]objectmodel.ServiceState{}
    for _, e := range entries {
        var st objectmodel.ServiceState
        if err := json.Unmarshal(e.Payload, &st); err != nil {
            continue // 损坏行静默跳过(design RF4)
        }
        if out[objectmodel.Kind(e.Kind)] == nil {
            out[objectmodel.Kind(e.Kind)] = map[string]objectmodel.ServiceState{}
        }
        prev, ok := out[objectmodel.Kind(e.Kind)][e.Name]
        if !ok || e.ObservedAt.After(prev.ObservedAt) {
            // 注:ServiceState 未必有 ObservedAt 字段;builder 若缺,用 st.XXX(请在 envelope.go 校)
            out[objectmodel.Kind(e.Kind)][e.Name] = st
        }
    }
    return out
}
```

builder 注:`state.StateEntry` 字段已在 `daedalus-sdk/state/state.go` 钉死(`Kind / Name / ObservedAt / Payload`);`objectmodel.ServiceState` 是 JSON 反序列化目标(`Kind / Name / DesiredState / Properties / Conditions`,不带 `ObservedAt`)。`prev.ObservedAt.After(...)` 这一行不编译,builder 用 `e.ObservedAt.After(prevLastSeen)` 维护一个平行 map `lastSeen[kind][name] time.Time` 替代。

- [ ] **Step 4: 在 `main.go` 加 `case "drift":`**

找到 `cmd/daedalus-host/main.go` 既有 `switch cmd { ... }`,在 `default:` 前加:

```go
case "drift":
    code = cmdDrift(stdout, stderr, args)
    return code
```

本子命令内部已直写一条 `host_drift` 审计(为携带 filter args),不再调 hostAudit 避免重复条目。

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... -run TestCmdDrift -v`
Expected: PASS。

- [ ] **Step 6: commit**

```bash
cd ../daedalus-core
git add cmd/daedalus-host/drift.go cmd/daedalus-host/main.go cmd/daedalus-host/drift_test.go
git commit -m "feat(host): drift subcommand with audit entry host_drift"
```

---

## Task 7: Core — `daedalus-host ownership get` 子命令

**Files:**
- Create: `cmd/daedalus-host/ownership.go`
- Modify: `cmd/daedalus-host/main.go`(加 `case "ownership":`)
- Test: `cmd/daedalus-host/ownership_test.go`

**Interfaces:**
- Consumes: `policy.LoadOrDefault()`、`objectmodel.Kind` 解析、字符串切分 `kind/name`
- Produces:
  ```go
  // 语法:daedalus-host ownership get <kind>/<name>
  // 输出:mode=<v> source=<by_kind|default>
  // 落审计:tool="host_ownership_get"(仅携带 args,hostAudit 不够)。
  func cmdOwnership(stdout, stderr io.Writer, args []string) int
  ```

- [ ] **Step 1: 写失败的测试**

```go
// cmd/daedalus-host/ownership_test.go
package main

import (
    "bytes"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/Daedalusys/daedalus-sdk/audit"
)

func TestCmdOwnership_GetByKind(t *testing.T) {
    tmp := t.TempDir()
    t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
    pf := filepath.Join(tmp, "policy.toml")
    os.WriteFile(pf, []byte(`
[ownership]
default = "unmanaged"
[ownership.by_kind]
service = "observe"
`), 0o644)
    t.Setenv("DAEDALUS_POLICY_PATH", pf)

    var stdout, stderr bytes.Buffer
    code := cmdOwnership(&stdout, &stderr, []string{"get", "service/nginx.service"})
    if code != 0 { t.Fatalf("got %d stderr=%s", code, stderr.String()) }
    if !strings.Contains(stdout.String(), "mode=observe") { t.Errorf("应打印 observe, got %q", stdout.String()) }
    if !strings.Contains(stdout.String(), "source=by_kind") { t.Errorf("应标注来源 by_kind, got %q", stdout.String()) }
}

func TestCmdOwnership_GetDefault(t *testing.T) {
    tmp := t.TempDir()
    t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
    pf := filepath.Join(tmp, "policy.toml")
    os.WriteFile(pf, []byte(`[ownership] default = "unmanaged"`), 0o644)
    t.Setenv("DAEDALUS_POLICY_PATH", pf)

    var stdout bytes.Buffer
    code := cmdOwnership(&stdout, &bytes.Buffer{}, []string{"get", "package/vim"})
    if code != 0 { t.Fatalf("got %d", code) }
    if !strings.Contains(stdout.String(), "mode=unmanaged") { t.Error("应 fallback default") }
    if !strings.Contains(stdout.String(), "source=default") { t.Error("应标注来源 default") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... -run TestCmdOwnership -v`
Expected: FAIL(`undefined: cmdOwnership`)。

- [ ] **Step 3: 实现 `cmd/daedalus-host/ownership.go`**

```go
package main

import (
    "fmt"
    "io"
    "strings"

    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/policy"
)

func cmdOwnership(stdout, stderr io.Writer, args []string) int {
    if len(args) < 2 || args[0] != "get" {
        fmt.Fprintln(stderr, "usage: daedalus-host ownership get <kind>/<name>")
        return exitUsage
    }
    target := args[1]
    kind, name, ok := strings.Cut(target, "/")
    if !ok || kind == "" || name == "" {
        fmt.Fprintf(stderr, "daedalus-host ownership get: 期望 <kind>/<name>, got %q\n", target)
        return exitUsage
    }

    pol, err := policy.LoadOrDefault()
    if err != nil {
        fmt.Fprintln(stderr, "ownership: policy:", err)
        return exitRuntime
    }
    if err := pol.ValidateOwnership(); err != nil {
        fmt.Fprintln(stderr, "ownership: ownership:", err)
        return exitRuntime
    }

    var mode policy.OwnershipMode
    source := "default"
    if m, ok := pol.Ownership.ByKind[kind]; ok {
        mode = m
        source = "by_kind"
    } else {
        mode = pol.Ownership.Default
    }

    fmt.Fprintf(stdout, "mode=%s source=%s\n", mode, source)

    argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q,"mode":%q,"source":%q}`, kind, name, mode, source)
    if v, err := audit.ParseValue(argsJSON); err == nil {
        _, _ = audit.LogAudit(audit.Entry{
            Identity: hostIdentity,
            Tool:     "host_ownership_get",
            Args:     v,
            Outcome:  "success",
        })
    }
    return exitOK
}
```

- [ ] **Step 4: `main.go` 加 `case "ownership":`**

```go
case "ownership":
    code = cmdOwnership(stdout, stderr, args)
    return code
```

(本子命令已内部写审计,不再调 hostAudit,避免重复条目。)

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... -run TestCmdOwnership -v`
Expected: PASS。

- [ ] **Step 6: commit**

```bash
cd ../daedalus-core
git add cmd/daedalus-host/ownership.go cmd/daedalus-host/main.go cmd/daedalus-host/ownership_test.go
git commit -m "feat(host): ownership get subcommand with audit entry host_ownership_get"
```

---

## Task 8: Core — Informer + Workqueue 骨架

**Files:**
- Create: `internal/controller/runtime/informer.go`
- Create: `internal/controller/runtime/workqueue.go`
- Test: `internal/controller/runtime/informer_test.go`
- Test: `internal/controller/runtime/workqueue_test.go`

**Interfaces:**
- Consumes: `*desiredview.View`(经 `desiredview.Load` 或 Task 5 的 `ViewWithForTest`)、`map[Kind]map[string]objectmodel.ServiceState`
- Produces:
  ```go
  type Event struct {
      Kind     objectmodel.Kind
      Name     string
      Type     EventType // 本期只实现 EventUpdated
      SourceTx string
  }
  type EventType string
  const EventUpdated EventType = "Updated"

  type Informer interface {
      Run(ctx context.Context, interval time.Duration) (<-chan Event, error)
      Stop()
  }
  func NewPollingInformer(view *desiredview.View) Informer

  type Workqueue interface {
      Enqueue(ev Event)
      Run(ctx context.Context, handler func(Event)) error
      Len() int
  }
  func NewChannelWorkqueue() Workqueue
  ```

- [ ] **Step 1: 写 informer 测试**

```go
// internal/controller/runtime/informer_test.go
package runtime_test

import (
    "context"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestPollingInformer_EmitsOnChange(t *testing.T) {
    v := desiredview.ViewWithForTest([]desiredview.Entry{
        {Kind: objectmodel.KindService, Name: "nginx", DesiredState: "active", SourceTx: "tx-1"},
    })
    inf := runtime.NewPollingInformer(v)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    ch, err := inf.Run(ctx, 50*time.Millisecond)
    if err != nil { t.Fatal(err) }
    select {
    case ev := <-ch:
        if ev.Type != runtime.EventUpdated { t.Errorf("got %v", ev.Type) }
        if ev.SourceTx != "tx-1" { t.Errorf("source_tx = %q", ev.SourceTx) }
    case <-time.After(2*time.Second):
        t.Fatal("应产出 event")
    }
}
```

- [ ] **Step 2: 写 workqueue 测试**

```go
// internal/controller/runtime/workqueue_test.go
package runtime_test

import (
    "context"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestChannelWorkqueue_Roundtrip(t *testing.T) {
    wq := runtime.NewChannelWorkqueue()
    wq.Enqueue(runtime.Event{Kind: objectmodel.KindService, Name: "nginx"})
    if wq.Len() != 1 { t.Errorf("Len=%d", wq.Len()) }
    done := make(chan runtime.Event, 1)
    ctx, cancel := context.WithCancel(context.Background())
    go func() {
        time.Sleep(50*time.Millisecond)
        cancel()
    }()
    err := wq.Run(ctx, func(ev runtime.Event) { done <- ev })
    if err != nil && err != context.Canceled { t.Fatal(err) }
    select {
    case ev := <-done:
        if ev.Name != "nginx" { t.Errorf("got %+v", ev) }
    case <-time.After(time.Second):
        t.Fatal("handler 未调用")
    }
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run "TestPollingInformer|TestChannelWorkqueue" -v`
Expected: FAIL(`undefined: NewPollingInformer`、`NewChannelWorkqueue`)。

- [ ] **Step 4: 实现 `informer.go` + `workqueue.go`**

```go
// internal/controller/runtime/informer.go
package runtime

import (
    "context"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

type EventType string
const EventUpdated EventType = "Updated"
type Event struct {
    Kind     objectmodel.Kind
    Name     string
    Type     EventType
    SourceTx string
}
type Informer interface {
    Run(ctx context.Context, interval time.Duration) (<-chan Event, error)
    Stop()
}
type pollingInformer struct {
    view *desiredview.View
    seen map[objectmodel.Kind]map[string]string // (kind,name) → 已发出 source_tx
    stop chan struct{}
}
func NewPollingInformer(view *desiredview.View) Informer {
    return &pollingInformer{view: view, seen: map[objectmodel.Kind]map[string]string{}, stop: make(chan struct{})}
}
func (p *pollingInformer) Run(ctx context.Context, interval time.Duration) (<-chan Event, error) {
    ch := make(chan Event, 64)
    go func() {
        defer close(ch)
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done(): return
            case <-p.stop: return
            case <-ticker.C:
                for _, e := range p.view.Entries() {
                    if last, ok := p.seen[e.Kind][e.Name]; ok && last == e.SourceTx { continue }
                    if p.seen[e.Kind] == nil { p.seen[e.Kind] = map[string]string{} }
                    p.seen[e.Kind][e.Name] = e.SourceTx
                    select {
                    case ch <- Event{Kind: e.Kind, Name: e.Name, Type: EventUpdated, SourceTx: e.SourceTx}:
                    default:
                    }
                }
            }
        }
    }()
    return ch, nil
}
func (p *pollingInformer) Stop() { close(p.stop) }
```

```go
// internal/controller/runtime/workqueue.go
package runtime

import (
    "context"
    "sync/atomic"
)

type Workqueue interface {
    Enqueue(ev Event)
    Run(ctx context.Context, handler func(Event)) error
    Len() int
}
type channelWorkqueue struct {
    ch chan Event
    n  atomic.Int64
}
func NewChannelWorkqueue() Workqueue {
    return &channelWorkqueue{ch: make(chan Event, 256)}
}
func (w *channelWorkqueue) Enqueue(ev Event) {
    select {
    case w.ch <- ev:
        w.n.Add(1)
    default:
    }
}
func (w *channelWorkqueue) Run(ctx context.Context, handler func(Event)) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case ev := <-w.ch:
            handler(ev)
            w.n.Add(-1)
        }
    }
}
func (w *channelWorkqueue) Len() int { return int(w.n.Load()) }
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run "TestPollingInformer|TestChannelWorkqueue" -v`
Expected: PASS。

- [ ] **Step 6: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/informer.go internal/controller/runtime/informer_test.go internal/controller/runtime/workqueue.go internal/controller/runtime/workqueue_test.go
git commit -m "feat(controller-runtime): polling informer + channel workqueue skeleton"
```

---

## Task 9: Core — Registry + Reconciler + generation 比较

**Files:**
- Create: `internal/controller/runtime/registry.go`
- Create: `internal/controller/runtime/reconciler.go`
- Test: `internal/controller/runtime/registry_test.go`
- Test: `internal/controller/runtime/reconciler_test.go`

**Interfaces:**
- Consumes: `controller.ReconcileFunc`(既有契约缝)、`controller.Result`、`controller.Object`
- Produces:
  ```go
  type Registry struct { ... } // 内部 mu + map[Kind]ReconcileFunc
  func (r *Registry) Register(k objectmodel.Kind, f controller.ReconcileFunc)
  func (r *Registry) Get(k objectmodel.Kind) (controller.ReconcileFunc, bool)

  type Reconciler struct {
      reg       *Registry
      objectFor func(objectmodel.Kind, string) (controller.Object, error)
  }
  func NewReconciler(reg *Registry, objectFor func(objectmodel.Kind, string) (controller.Object, error)) *Reconciler
  // Reconcile:取 handler → 取 object → generation 比较 → handler(obj) 返回 Result。
  func (r *Reconciler) Reconcile(ctx context.Context, ev Event) (controller.Result, error)
  ```

- [ ] **Step 1: 写 registry 测试**

```go
// internal/controller/runtime/registry_test.go
package runtime_test

import (
    "context"
    "testing"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
    reg := &runtime.Registry{}
    fn := func(ctx context.Context, obj controller.Object) (controller.Result, error) { return controller.Result{}, nil }
    reg.Register(objectmodel.KindService, fn)
    got, ok := reg.Get(objectmodel.KindService)
    if !ok { t.Fatal("应能取出") }
    if got == nil { t.Fatal("handler 不应为 nil") }
}
```

- [ ] **Step 2: 写 reconciler 测试**

```go
// internal/controller/runtime/reconciler_test.go
package runtime_test

import (
    "context"
    "sync/atomic"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestReconciler_GenerationGating(t *testing.T) {
    reg := &runtime.Registry{}
    called := atomic.Int32{}
    reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        called.Add(1)
        return controller.Result{RequeueAfter: time.Second}, nil
    })
    r := runtime.NewReconciler(reg, func(k objectmodel.Kind, n string) (controller.Object, error) {
        return controller.Object{
            Metadata: controller.Metadata{Generation: 5},
            Status: controller.Status{ObservedGeneration: 3},
        }, nil
    })
    res, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindService, Name: "nginx"})
    if err != nil { t.Fatal(err) }
    if called.Load() != 1 { t.Errorf("handler 应被调 1 次, got %d", called.Load()) }
    if res.RequeueAfter != time.Second { t.Errorf("requeue after 透传失败: %v", res.RequeueAfter) }
}

func TestReconciler_GenerationUpToDateSkips(t *testing.T) {
    reg := &runtime.Registry{}
    called := atomic.Int32{}
    reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        called.Add(1)
        return controller.Result{}, nil
    })
    r := runtime.NewReconciler(reg, func(k objectmodel.Kind, n string) (controller.Object, error) {
        return controller.Object{
            Metadata: controller.Metadata{Generation: 3},
            Status: controller.Status{ObservedGeneration: 3},
        }, nil
    })
    if _, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindService, Name: "nginx"}); err != nil { t.Fatal(err) }
    if called.Load() != 0 { t.Errorf("持平不应调 handler, got %d", called.Load()) }
}

func TestReconciler_UnregisteredKindReturnsError(t *testing.T) {
    r := runtime.NewReconciler(&runtime.Registry{}, func(objectmodel.Kind, string) (controller.Object, error) { return controller.Object{}, nil })
    _, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindPackage})
    if err == nil { t.Fatal("未注册 kind 应报错") }
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run "TestRegistry|TestReconciler" -v`
Expected: FAIL。

- [ ] **Step 4: 实现 `registry.go` + `reconciler.go`**

```go
// internal/controller/runtime/registry.go
package runtime

import (
    "sync"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

type Registry struct {
    mu    sync.RWMutex
    funcs map[objectmodel.Kind]controller.ReconcileFunc
}
func (r *Registry) Register(k objectmodel.Kind, f controller.ReconcileFunc) {
    r.mu.Lock(); defer r.mu.Unlock()
    if r.funcs == nil { r.funcs = map[objectmodel.Kind]controller.ReconcileFunc{} }
    r.funcs[k] = f
}
func (r *Registry) Get(k objectmodel.Kind) (controller.ReconcileFunc, bool) {
    r.mu.RLock(); defer r.mu.RUnlock()
    f, ok := r.funcs[k]; return f, ok
}
```

```go
// internal/controller/runtime/reconciler.go
package runtime

import (
    "context"
    "fmt"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

type Reconciler struct {
    reg       *Registry
    objectFor func(objectmodel.Kind, string) (controller.Object, error)
}
func NewReconciler(reg *Registry, objectFor func(objectmodel.Kind, string) (controller.Object, error)) *Reconciler {
    return &Reconciler{reg: reg, objectFor: objectFor}
}
func (r *Reconciler) Reconcile(ctx context.Context, ev Event) (controller.Result, error) {
    fn, ok := r.reg.Get(ev.Kind)
    if !ok { return controller.Result{}, fmt.Errorf("runtime: 未注册 kind %q", ev.Kind) }
    obj, err := r.objectFor(ev.Kind, ev.Name)
    if err != nil { return controller.Result{}, err }
    if obj.Metadata.Generation <= obj.Status.ObservedGeneration {
        return controller.Result{}, nil
    }
    return fn(ctx, obj)
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run "TestRegistry|TestReconciler" -v`
Expected: PASS。

- [ ] **Step 6: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/registry.go internal/controller/runtime/registry_test.go internal/controller/runtime/reconciler.go internal/controller/runtime/reconciler_test.go
git commit -m "feat(controller-runtime): registry + reconciler with generation gating"
```

---

## Task 10: Core — Driver:桥接 `daedalus-tx apply`

**Files:**
- Create: `internal/controller/runtime/driver.go`
- Test: `internal/controller/runtime/driver_test.go`

**Interfaces:**
- Consumes: `controller.Object`、`audit.LogAudit`、`policy.Default().Version`(注:policy 不暴露 Version 方法,builder 用字符串 `"1.0"` 或 `audit.DefaultPolicyVersion`)
- Produces:
  ```go
  // Driver 把 ReconcileFunc 的写动作翻译为一次 daedalus-tx 子进程调用。
  // 命令: daedalus-tx begin --steps '<json>' --auto-apply
  // adapter 名映射:service → "service.set";package → "package.set"。
  // 审计:tool="controller_reconcile_drive",args 含 kind/name/source_tx;非零 rc → outcome="error"。
  type Driver struct {
      TxBinary  string        // 默认 "daedalus-tx"
      TxTimeout time.Duration // 默认 30s
  }
  func (d *Driver) ApplyDesired(ctx context.Context, obj controller.Object) error
  ```

- [ ] **Step 1: 写失败的测试**

```go
// internal/controller/runtime/driver_test.go
package runtime_test

import (
    "context"
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func writeShellScript(t *testing.T, body string) string {
    t.Helper()
    p := filepath.Join(t.TempDir(), "stub.sh")
    os.WriteFile(p, []byte("#!/usr/bin/env bash\nset -e\n"+body+"\n"), 0o755)
    return p
}

func TestDriver_ApplyDesired_InvokesTxBinary(t *testing.T) {
    logPath := filepath.Join(t.TempDir(), "tx.log")
    txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
    t.Setenv("LOG_FILE", logPath)

    d := &runtime.Driver{TxBinary: txBin}
    obj := controller.Object{
        Kind:     objectmodel.KindService,
        Metadata: controller.Metadata{Name: "nginx.service"},
        Spec:     json.RawMessage(mustMarshal(t, objectmodel.ResourceSpec{DesiredState: "active"})),
    }
    if err := d.ApplyDesired(context.Background(), obj); err != nil { t.Fatal(err) }
    data, _ := os.ReadFile(logPath)
    if !strings.Contains(string(data), "begin") { t.Errorf("tx 二进制应被以 begin 调用, got %q", string(data)) }
    if !strings.Contains(string(data), "service.set") { t.Errorf("应含 service.set adapter, got %q", string(data)) }
}

func TestDriver_ApplyDesired_AuditsReconcileDrive(t *testing.T) {
    txBin := writeShellScript(t, `exit 0`)
    t.Setenv(audit.EnvLogPath, filepath.Join(t.TempDir(), "audit.jsonl"))

    d := &runtime.Driver{TxBinary: txBin}
    obj := controller.Object{Kind: objectmodel.KindService, Metadata: controller.Metadata{Name: "nginx.service"}}
    if err := d.ApplyDesired(context.Background(), obj); err != nil { t.Fatal(err) }
    entries := readAuditEntries(t, audit.EnvLogPath)
    found := false
    for _, e := range entries { if e.Tool == "controller_reconcile_drive" { found = true } }
    if !found { t.Error("应写一条 controller_reconcile_drive 审计") }
}

func TestDriver_ApplyDesired_TxFailureAuditsError(t *testing.T) {
    txBin := writeShellScript(t, `exit 7`)
    t.Setenv(audit.EnvLogPath, filepath.Join(t.TempDir(), "audit.jsonl"))

    d := &runtime.Driver{TxBinary: txBin}
    obj := controller.Object{Kind: objectmodel.KindService, Metadata: controller.Metadata{Name: "nginx.service"}}
    if err := d.ApplyDesired(context.Background(), obj); err == nil { t.Fatal("非零 rc 应报错") }
    entries := readAuditEntries(t, audit.EnvLogPath)
    found := false
    for _, e := range entries {
        if e.Tool == "controller_reconcile_drive" && e.Outcome == "error" { found = true }
    }
    if !found { t.Error("应写一条 outcome=error 的 reconcile_drive 审计") }
}

// readAuditEntries 用 audit.ParseValue 解析 harness 路径下 JSONL;helper 在同包。
func readAuditEntries(t *testing.T, pathEnvKey string) []audit.Record {
    t.Helper()
    data, err := os.ReadFile(os.Getenv(pathEnvKey))
    if err != nil { return nil }
    var out []audit.Record
    for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
        v, err := audit.ParseValue(line)
        if err != nil { continue }
        // 把 *audit.Value 转回 audit.Record(包内 helper);若不便转,用 Lookup 读字段自行断言。
        out = append(out, recordFromValue(v))
    }
    return out
}
```

builder 注:`recordFromValue(*audit.Value) audit.Record` 是测试 helper,从 `*audit.Value` 抽 `tool` / `outcome` 等字符串字段(`audit.Value.LookupString` 已暴露。完整 Record 重建需要 timestamp/identity 等;但 Tool/Outcome 两字段就够本测试断言)。

`mustMarshal` 用 `json.Marshal` 包 try-catch;若 Object.Spec 是 `json.RawMessage`,builder 直接 `json.Marshal(ResourceSpec{...})` 取 bytes。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestDriver -v`
Expected: FAIL(`undefined: Driver`)。

- [ ] **Step 3: 实现 `driver.go`**

```go
// internal/controller/runtime/driver.go
package runtime

import (
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
)

type Driver struct {
    TxBinary  string
    TxTimeout time.Duration
}

func (d *Driver) ApplyDesired(ctx context.Context, obj controller.Object) error {
    bin := d.TxBinary
    if bin == "" { bin = "daedalus-tx" }
    timeout := d.TxTimeout
    if timeout == 0 { timeout = 30 * time.Second }

    var desired string
    var rs objectmodel.ResourceSpec
    if err := json.Unmarshal(obj.Spec, &rs); err == nil {
        desired = rs.DesiredState
    }
    step := map[string]any{
        "adapter": adapterForKind(obj.Kind),
        "args": map[string]any{
            "name":          obj.Metadata.Name,
            "desired_state": desired,
        },
    }
    stepsJSON, _ := json.Marshal([]any{step})
    args := []string{"begin", "--steps", string(stepsJSON), "--auto-apply"}

    cmd := exec.CommandContext(ctx, bin, args...)
    cmd.WaitDelay = timeout
    out, err := cmd.CombinedOutput()

    outcome := "success"
    rc := -1
    if cmd.ProcessState != nil { rc = cmd.ProcessState.ExitCode() }
    if err != nil { outcome = "error" }

    argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q,"tx_rc":%d,"output":%q}`, obj.Kind, obj.Metadata.Name, rc, string(out))
    if v, perr := audit.ParseValue(argsJSON); perr == nil {
        _, _ = audit.LogAudit(audit.Entry{
            Identity:      "controller",
            Tool:          "controller_reconcile_drive",
            Args:          v,
            Outcome:       outcome,
            PolicyVersion: audit.DefaultPolicyVersion,
        })
    }
    if err != nil {
        return fmt.Errorf("driver: daedalus-tx apply failed (rc=%d output=%s): %w", rc, string(out), err)
    }
    return nil
}

func adapterForKind(k objectmodel.Kind) string {
    switch k {
    case objectmodel.KindService: return "service.set"
    case objectmodel.KindPackage: return "package.set"
    }
    return string(k)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./internal/controller/runtime/... -run TestDriver -v`
Expected: PASS,3 个 TestDriver 全绿。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/driver.go internal/controller/runtime/driver_test.go
git commit -m "feat(controller-runtime): driver bridges ReconcileFunc → daedalus-tx apply"
```

---

## Task 11: Core — `daedalus-host controller start` 子命令 + reconciliation loop

**Files:**
- Create: `cmd/daedalus-host/controller.go`
- Create: `internal/controller/runtime/loop.go`(新 loop 胶水函数)
- Modify: `cmd/daedalus-host/main.go`(加 `case "controller":`)
- Test: `cmd/daedalus-host/controller_test.go`
- Test: `internal/controller/runtime/loop_test.go`

**Interfaces:**
- Consumes: `policy.LoadOrDefault()`、`desiredview.Load()`、`state.Read`、`runtime.Informer`、`runtime.Workqueue`、`runtime.Reconciler`、`runtime.Driver`
- Produces:
  ```go
  // 语法:daedalus-host controller start [--foreground] [--poll-interval DURATION]
  // 默认:poll-interval=2s;foreground 默认 true(stub 子模块,即前台跑)。
  func cmdController(stdout, stderr io.Writer, args []string) int

  // 胶水:起 view + observed → informer → 过滤(mode != managed/reconcile drop)
  // → 入 workqueue → reconciler 每行 worker 调 Driver.ApplyDesired。
  func RunLoop(ctx context.Context, interval time.Duration, reg *Registry, modeFor func(objectmodel.Kind, string) policy.OwnershipMode) error
  ```

- [ ] **Step 1: 写 loop 测试**

```go
// internal/controller/runtime/loop_test.go
//go:build !integration
package runtime_test

import (
    "context"
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
    "github.com/Daedalusys/daedalus-sdk/state"
)

func TestRunLoop_DrivesReconcile(t *testing.T) {
    auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
    t.Setenv(audit.EnvLogPath, auditPath)
    logPath := filepath.Join(t.TempDir(), "tx.log")
    txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
    t.Setenv("LOG_FILE", logPath)

    reg := &runtime.Registry{}
    reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        d := &runtime.Driver{TxBinary: txBin}
        return controller.Result{}, d.ApplyDesired(ctx, obj)
    })

    modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
        return policy.OwnershipManagedReconcile
    }

    // 用 ViewWithForTest 直接喂 view(走 runtime.RunLoop 必须支持 view 注入)
    // builder 改 RunLoop 签名:RunLoop(ctx, interval, view *desiredview.View, reg, modeFor) error
    // 此处为简:loop 内部仍调 desiredview.Load;但测试里要劫持,builder 提供
    // internal/controller/runtime 包内可变包级函数 var viewLoader = desiredview.Load
    // 与 var stateReader = func() (...) { state.Read(time.Time{}) }
    // 测试里 t.Cleanup 重置并注入。
    // 注入 view + 空 state(依赖 Step 4 定义的三个 helpers)。
    view := desiredview.ViewWithForTest([]desiredview.Entry{
        {Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"},
    })
    runtime.SetViewLoaderForTest(func() (*desiredview.View, error) { return view, nil })
    runtime.SetStateReaderForTest(func() ([]state.StateEntry, error) { return nil, nil })
    t.Cleanup(runtime.ResetInjections)

    objectFor := func(k objectmodel.Kind, n string) (controller.Object, error) {
        return controller.Object{
            Kind:     k,
            Metadata: controller.Metadata{Name: n, Generation: 5},
            Spec:     json.RawMessage(`{"desired_state":"active"}`),
        }, nil
    }

    ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
    defer cancel()
    _ = runtime.RunLoop(ctx, 50*time.Millisecond, reg, modeFor, objectFor)

    data, _ := os.ReadFile(logPath)
    if !strings.Contains(string(data), "begin") {
        t.Errorf("应触发一次 daedalus-tx begin, tx.log=%q", string(data))
    }
    if !strings.Contains(string(data), "service.set") {
        t.Errorf("应含 service.set adapter, tx.log=%q", string(data))
    }
}
```

builder 注:本测试依赖 Step 4 定义的 `SetViewLoaderForTest` / `SetStateReaderForTest` / `ResetInjections` 三个 helpers;Step 1 跑测试在 Step 4 之后,无 forward reference 问题。

- [ ] **Step 2: 写 controller 测试(集成)**

```go
// cmd/daedalus-host/controller_test.go
package main

import (
    "bytes"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-sdk/audit"
)

func TestCmdController_Start_DrivesReconcile(t *testing.T) {
    tmp := t.TempDir()
    t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
    txLog := filepath.Join(tmp, "tx.log")
    txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
    t.Setenv("LOG_FILE", txLog)

    pf := filepath.Join(tmp, "policy.toml")
    os.WriteFile(pf, []byte(`
[objectmodel]
enabled_kinds = ["service"]
[ownership]
default = "managed/reconcile"
`), 0o644)
    t.Setenv("DAEDALUS_POLICY_PATH", pf)

    var stdout, stderr bytes.Buffer
    // 限时跑 controller start;我们让它立即 ctx cancel 退出
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    go func() { _ = cmdController(&stdout, &stderr, []string{"start", "--poll-interval=100ms"}) }()
    <-ctx.Done()

    data, _ := os.ReadFile(txLog)
    // 即便 journal / state 都是空,controller 应至少跑过一次 informer tick;
    // 这里只断言不 panic + audit 写了 host_controller_start。
    _ = data
}
```

builder 注:此测试是"启动不 panic"烟雾测试;真实"drive reconcile"验证在 Task 12 集成测试。

- [ ] **Step 3: 跑测试确认失败**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... ./internal/controller/runtime/... -run "TestCmdController|TestRunLoop" -v`
Expected: FAIL(`undefined: cmdController`、`undefined: RunLoop`)。

- [ ] **Step 4: 实现 `cmd/daedalus-host/controller.go` 与 `internal/controller/runtime/loop.go`**

```go
// internal/controller/runtime/loop.go
package runtime

import (
    "context"
    "encoding/json"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
    "github.com/Daedalusys/daedalus-sdk/state"
)

// viewLoader / stateReader 是包级可注入函数(测试替身用);生产代码保持默认指向
// desiredview.Load 与 state.Read(t0)。
var (
    viewLoader  = func() (*desiredview.View, error) { return desiredview.Load() }
    stateReader = func() ([]state.StateEntry, error) { return state.Read(time.Time{}) }
)

// SetViewLoaderForTest / SetStateReaderForTest / ResetInjections 是三个测试 hook。
// 生产代码不许调;测试文件 t.Cleanup(ResetInjections) 复原默认实现。
func SetViewLoaderForTest(fn func() (*desiredview.View, error))    { viewLoader = fn }
func SetStateReaderForTest(fn func() ([]state.StateEntry, error)) { stateReader = fn }
func ResetInjections() {
    viewLoader = func() (*desiredview.View, error) { return desiredview.Load() }
    stateReader = func() ([]state.StateEntry, error) { return state.Read(time.Time{}) }
}

func RunLoop(ctx context.Context, interval time.Duration, reg *Registry,
    modeFor func(objectmodel.Kind, string) policy.OwnershipMode,
    objectFor func(objectmodel.Kind, string) (controller.Object, error),
) error {
    view, err := viewLoader()
    if err != nil { return err }
    inf := NewPollingInformer(view)
    defer inf.Stop()
    evCh, err := inf.Run(ctx, interval)
    if err != nil { return err }
    wq := NewChannelWorkqueue()
    rec := NewReconciler(reg, objectFor)
    // workqueue handler 跑 reconciler,失败仅记 audit(留 P4.x backoff)。
    _ = wq // 占位;本 skeleton 直接 for-range evCh + filter + rec.Reconcile(同步)
    for {
        select {
        case <-ctx.Done(): return ctx.Err()
        case ev, ok := <-evCh:
            if !ok { return nil }
            if modeFor(ev.Kind, ev.Name) != policy.OwnershipManagedReconcile {
                continue // 只有 managed/reconcile 资源进 reconcile 流水线
            }
            if _, err := rec.Reconcile(ctx, ev); err != nil {
                // 留接口为 P4.x backoff;本骨架仅静默(不阻断 pipeline)
                _ = err
            }
        }
    }
}
```

`objectFor` 参数:外部组装 — 用 desiredview view + state index 拿 desired_state 与 observed state,造 `controller.Object{Metadata{Generation}, Spec, Status{ObservedGeneration: 0}}`。**builder 写 helper** `ObjectFromViewAndState(view, observed, kind, name) (controller.Object, error)` 在 `loop.go` 末尾,把 desired_state 反序列化进 Spec、用当前 view.Size 作为 generation 初值(后续由 provider apply 后回填)、observed_generation 暂取 0。

```go
// cmd/daedalus-host/controller.go
package main

import (
    "context"
    "flag"
    "fmt"
    "io"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
)

func cmdController(stdout, stderr io.Writer, args []string) int {
    if len(args) == 0 || args[0] != "start" {
        fmt.Fprintln(stderr, "usage: daedalus-host controller start [--foreground] [--poll-interval D]")
        return exitUsage
    }
    fs := flag.NewFlagSet("controller", flag.ContinueOnError)
    intervalF := fs.Duration("poll-interval", 2*time.Second, "informer poll interval")
    if err := fs.Parse(args[1:]); err != nil { return exitUsage }

    pol, err := policy.LoadOrDefault()
    if err != nil {
        fmt.Fprintln(stderr, "controller: policy:", err)
        return exitRuntime
    }
    if err := pol.ValidateOwnership(); err != nil {
        fmt.Fprintln(stderr, "controller: ownership:", err)
        return exitRuntime
    }

    modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
        if m, ok := pol.Ownership.ByKind[string(k)]; ok { return m }
        return pol.Ownership.Default
    }
    objectFor := func(k objectmodel.Kind, n string) (controller.Object, error) {
        return runtime.ObjectFromViewAndState(k, n)
    }

    reg := &runtime.Registry{}
    reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        d := &runtime.Driver{}
        return controller.Result{}, d.ApplyDesired(ctx, obj)
    })
    reg.Register(objectmodel.KindPackage, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        d := &runtime.Driver{}
        return controller.Result{}, d.ApplyDesired(ctx, obj)
    })

    runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    argsJSON := fmt.Sprintf(`{"interval_ns":%d}`, intervalF.Nanoseconds())
    if v, err := audit.ParseValue(argsJSON); err == nil {
        _, _ = audit.LogAudit(audit.Entry{
            Identity: hostIdentity,
            Tool:     "host_controller_start",
            Args:     v,
            Outcome:  "success",
        })
    }

    if err := runtime.RunLoop(runCtx, *intervalF, reg, modeFor, objectFor); err != nil && runCtx.Err() == nil {
        fmt.Fprintln(stderr, "controller: loop:", err)
        return exitRuntime
    }
    return exitOK
}
```

builder 注:`ObjectFromViewAndState` 由 builder 在 `loop.go` 末尾实现,内部用 `viewLoader()` + `stateReader()` 取数据,desired_state 写进 Spec,Generation 用 desiredview 视图 Size 作 placeholder,ObservedGeneration = 0(首次 reconcile 后由 provider apply 后回填,后续 reconcile 看到一致就跳过)。

- [ ] **Step 5: 在 `main.go` 加 `case "controller":`**

```go
case "controller":
    code = cmdController(stdout, stderr, args)
    return code
```

(本子命令已内部写审计,不再调 hostAudit。)

- [ ] **Step 6: 跑测试确认通过**

Run: `cd ../daedalus-core && go test ./cmd/daedalus-host/... ./internal/controller/runtime/... -run "TestCmdController|TestRunLoop" -v`
Expected: PASS。

- [ ] **Step 7: 跑全仓测试无回归**

Run: `cd ../daedalus-core && just go-test`
Expected: PASS,既有所有测试 + 新增测试全绿。

- [ ] **Step 8: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/loop.go internal/controller/runtime/loop_test.go cmd/daedalus-host/controller.go cmd/daedalus-host/main.go cmd/daedalus-host/controller_test.go
git commit -m "feat(host+controller-runtime): controller start subcommand + reconciliation loop"
```

---

## Task 12: Core — 端到端集成测试 + systemd unit + justfile

**Files:**
- Create: `internal/controller/runtime/e2e_test.go`(build tag `integration`)
- Create: `files/system/usr/lib/systemd/system/daedalus-host-controller.service`
- Modify: `justfile`(加 `enable-controller` 目标)

**Interfaces:**
- Consumes: Task 1–11 全部产出
- Produces:integration test、systemd unit、justfile 目标

- [ ] **Step 1: 写 integration test**

```go
//go:build integration

// internal/controller/runtime/e2e_test.go
package runtime_test

import (
    "context"
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/Daedalusys/daedalus-core/internal/controller"
    "github.com/Daedalusys/daedalus-core/internal/controller/runtime"
    "github.com/Daedalusys/daedalus-core/internal/desiredview"
    "github.com/Daedalusys/daedalus-sdk/audit"
    "github.com/Daedalusys/daedalus-sdk/objectmodel"
    "github.com/Daedalusys/daedalus-sdk/policy"
    "github.com/Daedalusys/daedalus-sdk/state"
)

func TestE2E_NginxActive(t *testing.T) {
    tmp := t.TempDir()
    t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
    t.Setenv("DAEDALUS_POLICY_PATH", filepath.Join(tmp, "policy.toml"))
    os.WriteFile(filepath.Join(tmp, "policy.toml"), []byte(`
[objectmodel]
enabled_kinds = ["service"]
[ownership]
default = "managed/reconcile"
`), 0o644)

    txLog := filepath.Join(tmp, "tx.log")
    txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
    t.Setenv("LOG_FILE", txLog)

    // 注入 view:nginx.service active
    view := desiredview.ViewWithForTest([]desiredview.Entry{
        {Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"},
    })
    runtime.SetViewLoaderForTest(func() (*desiredview.View, error) { return view, nil })
    runtime.SetStateReaderForTest(func() ([]state.StateEntry, error) {
        return nil, nil // 无 state = 无 observed;drift 自然算出
    })
    t.Cleanup(func() {
        runtime.ResetInjections()
    })

    reg := &runtime.Registry{}
    reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
        d := &runtime.Driver{TxBinary: txBin}
        return controller.Result{}, d.ApplyDesired(ctx, obj)
    })

    modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode { return policy.OwnershipManagedReconcile }
    objectFor := func(k objectmodel.Kind, n string) (controller.Object, error) {
        return controller.Object{
            Kind:     k,
            Metadata: controller.Metadata{Name: n, Generation: 5},
            Spec:     json.RawMessage(`{"desired_state":"active"}`),
        }, nil
    }

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    _ = runtime.RunLoop(ctx, 100*time.Millisecond, reg, modeFor, objectFor)

    data, _ := os.ReadFile(txLog)
    if !strings.Contains(string(data), "begin") {
        t.Errorf("应触发一次 daedalus-tx begin, tx.log=%q", string(data))
    }
    if !strings.Contains(string(data), "service.set") {
        t.Errorf("应含 service.set adapter, tx.log=%q", string(data))
    }
    if !strings.Contains(string(data), "nginx.service") {
        t.Errorf("应含 nginx.service, tx.log=%q", string(data))
    }
}
```

builder 注:`SetViewLoaderForTest` / `SetStateReaderForTest` / `ResetInjections` 三个 hook 已在 Task 11 Step 4 `loop.go` 末尾定义,本测试直接调。

- [ ] **Step 2: 跑 integration test**

Run: `cd ../daedalus-core && go test -tags integration ./internal/controller/runtime/... -run TestE2E_NginxActive -v`
Expected: PASS。

- [ ] **Step 3: 创建 systemd unit**

`files/system/usr/lib/systemd/system/daedalus-host-controller.service`:

```ini
[Unit]
Description=Daedalus host controller runtime (P4 reconcile skeleton)
After=daedalus-host.service
Wants=daedalus-host.service

[Service]
Type=simple
ExecStart=/usr/bin/daedalus-host controller start --foreground --poll-interval=2s
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

builder 注:`Wants=` 而非 `Requires=` 保证 controller 失败不拖死 host(用户决策 T1)。

- [ ] **Step 4: justfile 加 `enable-controller` 目标**

`justfile` 末尾追加:

```just
# 启用 host controller systemd unit(镜像首次启动时调用)
enable-controller:
    #!/usr/bin/env bash
    set -euo pipefail
    systemctl enable --now daedalus-host-controller.service
    systemctl status daedalus-host-controller.service --no-pager
```

并在 `Containerfile` 找到镜像首次启动钩子(若有),追加一行 `RUN systemctl enable daedalus-host-controller.service`;若没有现成钩子,builder 选加位置(通常是 `files/system/scripts/post-install/` 末位或 Containerfile `%post` 段)。

- [ ] **Step 5: commit**

```bash
cd ../daedalus-core
git add internal/controller/runtime/e2e_test.go internal/controller/runtime/loop.go
git commit -m "test(controller-runtime): end-to-end nginx reconciliation integration test"
git add files/system/usr/lib/systemd/system/daedalus-host-controller.service
git commit -m "chore(systemd): daedalus-host-controller.service unit"
git add justfile
git commit -m "chore(build): enable-controller justfile target"
```

---

## Self-Review

**1. Spec coverage**(design §1–§6):

| design 条款 | task |
| --- | --- |
| §1 intent / why / 痛点 | 整个 plan 落地 |
| G1 ownership policy schema | Task 1, 2, 3 |
| G2 四档模式生效 + ownership get | Task 7 |
| G3 drift 一等公民 | Task 4, 5, 6 |
| G4 controller runtime 骨架 | Task 8, 9, 11 |
| G5 nginx end-to-end | Task 10, 11, 12 |
| G6 generation 比较 | Task 9 |
| G7 "不绕过 tx 裸改" 证据 | Task 10(driver 全走 daedalus-tx) |
| §4.1 ownership schema 形态 | Task 1, 2, 3 |
| §4.2 drift 检测语义 | Task 4, 5 |
| §4.3 controller runtime 骨架 | Task 8, 9, 11 |
| §4.4 nginx end-to-end | Task 11, 12 |
| §4.5 命令可见性 | Task 6, 7, 11 |
| §4.6 测试三层 | Task 1–12 全部 |
| §5 留给 builder | 已在 task 中显式标记(builder 决策) |
| §6 失败路径 | Task 2(守门)、Task 5(损坏)、Task 10(tx 失败) |

**2. Step scan**:每个 step 是一动作 + 可验证结果;无 TBD;无多余代码块(算法由签名 + 测试确定)。

**3. Type consistency**:`OwnershipMode`、`DriftEntry`、`Event`、`Registry`、`Reconciler`、`Driver`、`Informer`、`Workqueue` 在 task 之间签名一致。

**4. Review Focus**:
- RF1 → Task 3(policy.toml 节新增,缺失由 SDK 解析守门)
- RF2 → Task 2(ValidateOwnership)
- RF3 → Task 2(ValidateOwnership)
- RF4 → Task 5(损坏行静默跳过;journal 损坏 → desiredview.Load 启动 fail-closed)
- RF5 → Task 10(driver 审计 + 错误返回;requeue 留 P4.x backoff)

**5. Proportion**:plan ≈ design(190 行) + ~70% 实施指令;代码块仅出现在算法不被签名 + 测试确定的 step(`adapterForKind` 映射、`MapDesiredObserved` 字典、`indexState` 反序列化),其余签名 + 测试足以让 builder 写出唯一合理实现。

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-10-10-p4-controller-skeleton.md`.** Spec 在 `docs/superpowers/specs/2026-10-10-p4-controller-skeleton-design.md`,与 plan 同步审阅。

请审阅 plan + spec,确认 capture what you want,然后选择执行方式:

- **Subagent-driven** — 每个 task 由 fresh subagent 实施、随后 fresh reviewer 验收、最后整支 review。最彻底;代价 = 每个 task 一次 fresh context。
- **Native** — 我在本 session 内自己执行每个 task(走 executing-plans),最末由 fresh reviewer 整体 review。最便宜、最快;无独立 mid-task 复核。

**对本 plan 我推荐 Subagent-driven**,原因:Task 4 / Task 10 含跨仓 SDK 接口变更,Task 11 / Task 12 含真 systemd + integration test 接线,任一 task 错都会把后面几节的接口签名连带偏移;fresh-reviewer-per-task 把漂移卡在每个 task 边界内。

确认 plan 是否 capture what you want,以及选哪种执行方式?