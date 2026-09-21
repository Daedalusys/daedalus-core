# sdk-extraction-and-plugin-monorepo - Work Plan

## TL;DR (For humans)

**What you'll get:** 三个独立 GitHub 仓库：`daedalus-core`（image 编排 + 5 个 core runtime + copilot）、`daedalus-sdk`（11 个安全核心包 + 5 个 Provider/Slot 占位目录）、`daedalus-plugins`（6 个 Go 能力插件 monorepo）。所有 import 路径与 GitHub remote 对齐到 `Daedalusys`，不再有 `daedalus-os/...` 占位字符串。

**Why this approach:** 两阶段合并为单 PR 系列 = 阶段 1 在同仓内做物理布局（SDK 抽离 + 6 插件迁 monorepo），阶段 2 拆 3 仓 + go.work 桥 + 跨仓 release zip。Import 路径立刻对齐实际（C5），不留待坏债务；5 个空目录占位（C3）等 #42 落地时填实；远期自研轻量化 store 留作 Control Center 演化（C2），本期不引 OCI。

**What it will NOT do:**
- 不实现 5 个 Provider/Slot 的实际 provider（仅占位 README）
- 不引入 OCI App Store / 任何 registry
- 不重命名 GitHub org（`Daedalusys` 保留）
- 不动 copilot 插件（`daedalus.copilot` 留主仓）
- 不实现自研 store / Control Center / plugin runtime hot swap

**Effort:** Large
**Risk:** Medium - 6 插件 × 跨仓迁移 + 60 文件 import rename（含 1 go.mod）+ 3 仓 CI 拆解；漂移测试 + 镜像零残留断言双重守门，本机 CPU 不支持 v3 镜像构建（todo 18）合并后跑。

**Decisions to sanity-check:**
- 3 仓命名 `daedalus-core` / `daedalus-sdk` / `daedalus-plugins`（与 issue #46 表述一致）
- 6 插件 monorepo 形态 = 6 个子目录，各独立 `go.mod`（非 6 独立仓）
- 跨仓集成方式 = 镜像构建期 `daedalus-core` CI 拉 `daedalus-plugins` release zip（而非 OCI pull）
- 5 占位目录 = 空目录 + README.md（不带 Go 代码、不暴露 import）
- copilot 留主仓（issue #46 未列入 6 迁插件；Deno runtime 与 Go 插件形态不同）

Your next move: 审阅本 plan → 批准或提改 → 由 worker session 跑 `$start-work sdk-extraction-and-plugin-monorepo`（5.5 PD = 18 todos + 4 F-验证）。

---

> TL;DR (machine): Large effort, Medium risk; 3 仓物理拆 + 11 SDK 包迁 + 6 插件 monorepo 化 + 60 文件 import rename（含 1 go.mod）+ 7 SDK 内部交叉 import 修复 + manifest C1 升级 + go.work 桥 + 跨仓 release zip + 5 占位目录。

## Scope

### Must have
- 3 个独立 GitHub 仓：`Daedalusys/daedalus-core`、`Daedalusys/daedalus-sdk`、`Daedalusys/daedalus-plugins`
- 11 个 SDK 包从 `daedalus/core/internal/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version}/` 迁 `daedalus-sdk/<pkg>/`，独立 `go.mod`（module = `github.com/Daedalusys/daedalus-sdk`）
- 6 个 Go 能力插件从 `daedalus/plugin/{fs,shell,pkg,sysinfo,service,blueprint}/` 迁 `daedalus-plugins/<cap>/`，各独立 `go.mod`（module = `github.com/Daedalusys/daedalus-plugins/<cap>`）
- core 仓留 `controller/ dirs/ state/ tx/ internal/ 4 个包`（被 `daedalus-{host,service,tx,plugin-pack}` 专用）+ 5 个 core runtime cmd（host/audit/tx/smoke/plugin-pack）+ copilot 插件源码
- 全部 import 路径从 `github.com/daedalus-os/...` 改 `github.com/Daedalusys/...`（**60 文件**：59 `.go` + 1 `go.mod`）
- `daedalus-sdk` 仓 5 个空目录占位：`secretprovider/`、`memoryprovider/`、`modelprovider/`、`agentprovider/`、`transportprovider/`，各带 README.md 占位文档（指向 #42 Provider/Slot 抽象）
- `manifest.runtime` 改结构化对象（`{name: "deno"|"native", version: "..."}`）+ 加 `api_version` / `license` / `maintainer` 3 字段（C1 已决）
- 6 插件 manifest 按 C1 schema 改写（含 i18n 同步）
- `go.work` 本地 dev 桥（B7a 默认）含 3 个 module 引用
- 跨仓 release 流水线（B7b + C2 一致）：`daedalus-plugins` 每个 sub-module 独立 GitHub release zip，`daedalus-core` 镜像构建期 `curl -L` 拉 release zip 集成
- AGENTS.md / VISION.md / `daedalus/files/scripts/76-daedalus-plugin-gen.sh` / `daedalus/files/scripts/70-*-mcp-servers.sh` 全部改用 core/sdk/plugin/files 四层措辞
- `justfile` / `scripts/sync-daedalus.sh` 指向新路径
- 3 仓各自 CI：core 跑 `just build` 镜像构建、sdk 跑 go vet + go test + 漂移测试、plugins 跑 6 sub-module 独立 build + i18n 同步校验
- 3 点漂移测试保留：policy.toml ↔ `policy.Default()` ↔ objectmodel 常量
- 镜像零残留断言保留（`just verify-image`）

### Must NOT have (guardrails, anti-slop, scope boundaries)
- 不实现 5 个 Provider/Slot 的实际 provider（仅占位 README，不写 Go 代码）
- 不引入 OCI App Store / 任何 registry / private mirror
- 不重命名 GitHub org（`Daedalusys` 保留；远期 org rename 另议）
- 不动 copilot 插件（`daedalus.copilot` 留主仓，仍是 `daedalus/plugin/copilot/`）
- 不实现自研 store / Control Center / Blueprint UI（留 #6）
- 不分离 plugin/blueprint 的 `blueprints/` 数据（已 `//go:embed` 进二进制，保留原状）
- 不动 v3 构建机 todo 18 镜像端到端断言（合并后跑，AGENTS.md NOTES 已知 CPU 限制）
- 不实现 plugin runtime hot swap / 运行时联网安装（留 P4 + ANTI-PATTERNS 守门）
- 不做 Python / Deno 能力服务器恢复（Go 唯一实现）
- 不实现新的 Provider 替换（留 #42 + #33/#34）
- 不重写 76 脚本逻辑（仅改路径引用，不改 7 阶段校验）
- **不**保持 `internal/` 前缀保护（review Oracle H 修；`internal/policy` 等 11 个 SDK 包迁出后变为 `daedalus-sdk/policy/daedalus-sdk/` 等顶级包，**公开可被 import**；这是 SDK 设计的应有之意——SDK 消费者需要扩展策略，但失去 Go 编译器的"同模块限可见"防护；威胁面增量：攻击者可 import `policy` 包绕过 policy 加载，直接构造 `policy.Default()` 实例；本计划承认该面增量而无 mitigation，因为 SDK = 公开 contract，否则 SDK 不可用——issue #46 决定走 SDK 路线 = 接受此 trade-off；下游使用方须通过 systemd `DynamicUser=yes` + `ReadOnlyPaths=` + 沙箱 drop-in 守住运行时）
- 不改 4 个核心包（`controller/ dirs/ state/ tx/`）的归属（决策 25 契约缝锁定）
- 不引入新依赖（SDK 仓与 plugins 仓 go.mod 仅复刻 core 仓依赖，零新增）

## Verification strategy
> Zero human intervention - all verification is agent-executed.
- Test decision: tests-after（已有 100+ Go 测试 + 30+ Deno 测试保留，每 todo 跑一次）
- Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a<attempt>/task-<N>.log`

每 todo 跑：

| 阶段 | 命令 |
|------|------|
| Wave 1-2（同仓） | `cd daedalus/core && go test ./... && deno test --allow-all tests/deno/` |
| Wave 5+（3 仓物理拆） | `cd daedalus-sdk && go test ./...` + `cd daedalus-plugins && for c in fs shell pkg sysinfo service blueprint; do (cd $c && go build -trimpath -o bin/ ./cmd/daedalus-$c) && (cd $c && go test ./...); done` |
| 漂移测试 | `go test ./internal/policy/... ./internal/objectmodel/...` (3 点钉死) |
| i18n 门禁 | `bash tests/deno/i18n_keys.test.sh` |
| 镜像构建（V3 构建机） | `just build` + `just verify-image` |

QA 失败信号：任何 todo 的 happy 路径 exit 0 + 关键断言 pass；failure 路径返回具体错串（如 import 漂移、checksum 不符、单元缺失）。

## Execution strategy

### Parallel execution waves
> Target 5-8 todos per wave. Fewer than 3 (except the final) means you under-split.

| Wave | 内容 | Todos | 估计 PD |
|------|------|-------|---------|
| Wave 1 | SDK 抽离（同仓） | 1, 2, 3, 4 | 1.5 |
| Wave 2 | 6 插件 monorepo 化（同仓） | 5, 6, 7 | 1.5 |
| Wave 3 | Import 路径 rename（C5） | 8, 9 | 0.5 |
| Wave 4 | Manifest C1 schema 升级 | 10, 11 | 0.5 |
| Wave 5 | 3 仓物理拆 + go.work | 12, 13, 14 | 1.0 |
| Wave 6 | Cross-repo release + 5 占位 | 15, 16 | 0.5 |
| Wave 7 | 文档 + 终验 | 17, 18 | 0.5 |

### Dependency matrix
| Todo | Depends on | Blocks | Can parallelize with |
| --- | --- | --- | --- |
| 1 (SDK init) | - | 2, 3, 4 | 5 (no: 5+ 插件代码引 SDK 包) |
| 2 (SDK 包迁) | 1 | 3, 4 | - |
| 3 (import 重写) | 2 | 4, 8, 9, 5, 6, 7 | - |
| 4 (76 脚本+漂移测) | 3 | 8, 9, 12, 18 | 10 (no: 需 schema 稳定) |
| 5 (Plugins init) | 3 | 6, 7 | 10 (no: 6 需 5 完成) |
| 6 (插件源迁) | 5 | 7 | 10 |
| 7 (justfile+76 脚本) | 6 | 8, 9, 12, 18 | - |
| 8 (Go import rename) | 4, 7 | 12 | 9, 10, 11 |
| 9 (脚本+文档 rename) | 4, 7 | 12 | 8, 10, 11 |
| 10 (manifest schema) | 4, 7 | 11 | 8, 9, 11 |
| 11 (manifest 改写) | 10 | 12 | - |
| 12 (3 仓建) | 4, 7, 8, 9, 11 | 13, 14, 15, 18 | 16 (no: 5 占位需 SDK 仓先建) |
| 13 (go.work) | 12 | 14, 15 | 16 |
| 14 (3 仓 CI) | 12, 13 | 15, 18 | 16 |
| 15 (跨仓 release) | 12, 13, 14 | 18 | 16 |
| 16 (5 占位) | 12 | 18 | 13, 14, 15 |
| 17 (AGENTS+VISION) | 4, 7, 8, 9, 11 | 18 | 15, 16 |
| 18 (镜像构建+断言) | 12, 13, 14, 15, 16, 17 | - | - |

## Todos
> Implementation + Test = ONE todo. Never separate.
<!-- APPEND TASK BATCHES BELOW THIS LINE WITH edit/apply_patch - never rewrite the headers above. -->

- [x] 1. 创建 `daedalus-sdk/` 模块骨架 + go.mod + 11 个空子目录
  What to do / Must NOT do: 在主仓根新建 `daedalus-sdk/` 目录（与 `daedalus-core/`、`daedalus-plugins/` 同级，**注意：阶段 1 仍在主仓，目录布局物理上同仓共存**），写 `daedalus-sdk/go.mod`（module = `github.com/Daedalusys/daedalus-sdk`，go 1.25.0，依赖 review Oracle Issue 6 修：**`modelcontextprotocol/go-sdk v1.7.0` + `BurntSushi/toml v1.6.0` + `github.com/google/jsonschema-go v0.4.3`**（blueprint 包需要 schema 校验，go mod 现状为 indirect，迁 SDK 后变 direct；其他包可能还有 `golang.org/x/sys` for `syscall.Flock` 等，以 `go mod tidy` 最终落定为准），建 11 个空子目录 `audit/ blueprint/ i18n/ objectmodel/ pathguard/ pkgquery/ plugin/ policy/ shellpolicy/ sysinfo/ version/`，每目录留 `.gitkeep`。**不**复制任何代码（todo 2 负责）；**不**新建 rootfs 路径；**不**改 AGENTS.md。
  Parallelization: Wave 1 | Blocked by: - | Blocks: 2, 3, 4
  References: `daedalus/core/go.mod:1-19`（module 声明与依赖对照基线）；`daedalus/core/internal/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version}/` 11 个目录
  Acceptance criteria (agent-executable): `test -d daedalus-sdk && test -f daedalus-sdk/go.mod && head -1 daedalus-sdk/go.mod | grep -q "module github.com/Daedalusys/daedalus-sdk" && ls -d daedalus-sdk/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version} | wc -l | grep -qx 11`
  QA scenarios:
    - happy: 全部 11 个子目录 + go.mod 存在且 module 路径正确 → exit 0
    - failure: 任一子目录缺失或 module 路径写错 → exit 1 + 具体错串
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a1/task-1.log`
  Commit: Y | `chore(sdk): scaffold daedalus-sdk module skeleton with 11 placeholder dirs`

- [x] 2. 物理迁移 11 个 SDK 包源码（每包含 .go + _test.go + 嵌套子包）
  What to do / Must NOT do: 用 `git mv` 把 `daedalus/core/internal/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version}/` 下全部文件迁到 `daedalus-sdk/<同名>/`；改每包内每个 `.go` 文件的 `package <name>` 声明（保持同名，**不**改包名以保外部 API 兼容）；**不**删 `daedalus/core/internal/{controller,dirs,state,tx}/`（4 个核心包留 core）；**不**改 import 路径（todo 3 统一改）；**不**新建 `replace` 指令（todo 3 加）。每包内 nested sub-package（如 `audit/internal/`）一并迁，目录结构原样保留。
  Parallelization: Wave 1 | Blocked by: 1 | Blocks: 3, 4
  References: `daedalus/core/internal/audit/{audit.go,audit_test.go,...}` 等 11 目录全文件；保留 `daedalus/core/internal/{controller,dirs,state,tx}/` 4 目录不动
  Acceptance criteria (agent-executable): `diff -r daedalus/core/internal/audit daedalus-sdk/audit --brief | head` 应只显示 import 路径差异（无 `package` 声明差异），`for d in audit blueprint i18n objectmodel pathguard pkgquery plugin policy shellpolicy sysinfo version; do test -f daedalus-sdk/$d/*.go; done`，`test -d daedalus/core/internal/controller && test -d daedalus/core/internal/dirs && test -d daedalus/core/internal/state && test -d daedalus/core/internal/tx`（4 核心包仍留 core）
  QA scenarios:
    - happy: 11 个 SDK 目录内全部文件数 == 迁移前数（`find daedalus/core/internal/audit -type f | wc -l == find daedalus-sdk/audit -type f | wc -l`），4 核心包仍在 core 仓
    - failure: 任一文件漏迁或迁错目录 → diff 输出非空 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a1/task-2.log`
  Commit: Y | `refactor(sdk): relocate 11 security packages from core/internal to sdk/`

- [x] 3. 改写 core 仓所有引用 SDK 包的 import 路径 + SDK 包内部交叉 import + core/go.mod replace 指令
  What to do / Must NOT do: 在 **51 个 core 仓 .go 文件**（含 `cmd/`, `internal/{controller,dirs,state,tx}/`, tests）和 1 个 `daedalus/core/go.mod` 中，把 `github.com/daedalus-os/daedalus/core/internal/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version}` 全局改 `github.com/Daedalusys/daedalus-sdk/<同名>`；**同时**改 **7 个 SDK 包内部交叉 import**（review Issue A 修）—— 这些 import 写在将被迁到 `daedalus-sdk/` 的 `.go` 文件中，迁完后会指向不存在的 `daedalus-os/...` 模块：
  - `daedalus/core/internal/objectmodel/objectmodel_test.go:15` (`policy`)
  - `daedalus/core/internal/plugin/manifest.go:26` (`objectmodel`)
  - `daedalus/core/internal/plugin/manifest_resources_test.go:11` (`objectmodel`)
  - `daedalus/core/internal/policy/policy_inject_test.go:12-14` (`pathguard, policy, shellpolicy`)
  - `daedalus/core/internal/policy/policy_test.go:16-18` (`pathguard, policy, shellpolicy`)
  - `daedalus/core/internal/shellpolicy/shellpolicy.go:23` (`policy`)
  - `daedalus/core/internal/shellpolicy/shellpolicy_test.go:10` (`policy`)
  
  在 `daedalus/core/go.mod` 加 `replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk`（同仓相对路径，**阶段 1 期间**；阶段 2 todo 14 改 GitHub release tag）；**不**改 `daedalus-os` 这个旧前缀本身（todo 8 统一 rename）；**不**改 SDK 包内 `version/version.go` 的字符串常量 `const ModulePath = "github.com/daedalus-os/daedalus/core"`（那是 SDK 模块自身用于自身包名解析的标识，迁完后由 todo 8 同步改；本 todo 不动避免双重责任）。
  Parallelization: Wave 1 | Blocked by: 2 | Blocks: 4, 8, 9, 5, 6, 7
  References: 7 个 SDK 内部交叉 import 文件如上; 51 个 core 文件 = `cmd/` 45 + `internal/{controller,dirs,state,tx}/` 6; `daedalus/core/go.mod:1`; `daedalus/core/internal/version/version.go:13`（不在本 todo 范围；review Oracle A 修正路径：原 plan 误写为 `daedalus/core/version/version.go`，实际在 `internal/` 子树下）
  Acceptance criteria (agent-executable): `grep -rE 'github.com/daedalus-os/daedalus/core/internal/(audit|blueprint|i18n|objectmodel|pathguard|pkgquery|plugin|policy|shellpolicy|sysinfo|version)' daedalus/core daedalus-sdk 2>/dev/null | wc -l` 应为 0（旧 import 全部清空，包括 core 与 daedalus-sdk 两个子树）；`grep -rE 'github.com/Daedalusys/daedalus-sdk' daedalus/core daedalus-sdk 2>/dev/null | wc -l` 应 ≥ 58（51 core + 7 SDK-internal 至少 58 处新 import）；`cat daedalus/core/go.mod | grep -E "replace.*daedalus-sdk"` 应有 1 行
  QA scenarios:
    - happy: 51 个 core 文件 + 7 个 SDK 内部交叉 import 全部更新到新路径；replace 指令就位；`cd daedalus/core && go mod tidy && go test ./...` exit 0 且 `cd ../daedalus-sdk && go test ./...` exit 0
    - failure: 任一 import 漏改 → `go build ./...` 报 unresolved import；或 SDK 内部交叉 import 漏改 → `cd daedalus-sdk && go test ./...` 报 cannot find module → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a1/task-3.log`
  Commit: Y | `refactor(core+sdk): switch SDK package imports to daedalus-sdk module (incl. SDK-internal cross-imports)`

- [x] 4. 76 脚本路径更新 + 3 点漂移测试 + justfile/sync 脚本路径同步 + **DevRelPath 迁移**（review Oracle Issue 1 修，**execution blocker**：原 plan 未处理 `policy.go:46` 的 `DevRelPath = "daedalus/files/system/opt/daedalus/shared/policy.toml"` 生产常量在 SDK 仓内失效问题）
  What to do / Must NOT do: 改 `daedalus/files/scripts/76-daedalus-plugin-gen.sh` 的 import/path 引用（**只改路径**，不重写 7 阶段校验逻辑）；改 `daedalus/files/scripts/70-daedalus-mcp-servers.sh` 任何对 `daedalus/core/internal/...` 的引用；改 `daedalus/core/internal/policy/policy_test.go` 的 policy.toml 路径断言（如有指向 SDK 包内部）；改 `daedalus/core/internal/objectmodel/objectmodel_test.go` 同上；`justfile` 的 `plugin-pack` 仍指向 `daedalus/plugin/<cap>/`（todo 7 改）；`scripts/sync-daedalus.sh` plugin 腿仍指向 `daedalus/plugin/`（todo 7 改）。**不**改漂移测试逻辑（3 点钉死保持原样）；**不**改 policy.toml 本身（已是单一事实源）。
  
  **DevRelPath 迁移**（review Oracle Issue 1 + Momus Issue 3 修；这是执行阻断 bug，不修则 `policy.ResolvePath()` 在 SDK 仓内 `go test` 永远回退到 `Default()` 而非真实 policy.toml）：将 `DevRelPath` 常量从 `daedalus/core/internal/policy/policy.go:46` 改为可配置的回溯路径优先级。**顺序纠正（Momus 实证：Go test binary 的 CWD = package 目录，`testdata/policy.toml` 在 level 1 就命中，先于 walk-up 到的 cross-repo 候选）**：
  1. **首选（post-split 生效）**：跨仓平级候选 `daedalus-core/files/system/opt/daedalus/shared/policy.toml`（3 仓平级 clone 时从 SDK 仓 walk-up 命中；**注意**：mono-repo 阶段此候选不存在——那时目录还是 `daedalus/core/` 不是 `daedalus-core/`，所以此候选在 todo 12 rename 前是 dead path，靠 testdata 兜底）
  2. **次选（mono-repo 与 SDK 独立 CI 生效）**：`testdata/policy.toml`（SDK 仓自带 fixture；Go test binary CWD = package dir，`testdata/` 在 level 1 命中——**实际优先级高于 cross-repo**，因为 walk-up 从 CWD 开始）
  3. **fallback**：通过 `DAEDALUS_POLICY_PATH` env 显式注入（已有，test 与 systemd drop-in 用）
  
  在 `daedalus-sdk/policy/policy.go`（todo 2 迁过去的）写**完整 ResolvePath 循环体**（review Oracle Issue 8 修——原 plan 只给 var 声明不给循环体，executor 要自己发明）：
  ```go
  var DevRelPaths = []string{
      "daedalus-core/files/system/opt/daedalus/shared/policy.toml", // 跨仓平级 clone 时命中
      "testdata/policy.toml",                                       // SDK 仓自带 fixture（CWD 级命中）
  }
  
  func ResolvePath() (string, error) {
      if p := os.Getenv(EnvPolicyPath); p != "" {
          return p, nil
      }
      if st, err := os.Stat(ProductionPath); err == nil && !st.IsDir() {
          return ProductionPath, nil
      }
      if wd, err := os.Getwd(); err == nil {
          // 嵌套循环：每层 dir × 每个 relPath 候选
          for dir := wd; ; {
              for _, rel := range DevRelPaths {
                  cand := filepath.Join(dir, rel)
                  if st, err := os.Stat(cand); err == nil && !st.IsDir() {
                      return cand, nil
                  }
              }
              parent := filepath.Dir(dir)
              if parent == dir {
                  break
              }
              dir = parent
          }
      }
      return "", ErrNotFound
  }
  ```
  **testdata drift 同步（review Oracle Issue 3 修；SDK 的 3 点漂移测试只校验 testdata ↔ `policy.Default()` ↔ objectmodel，会漏掉生产 policy.toml）**：
  - `daedalus-sdk/policy/testdata/policy.toml` 是 `daedalus-core/files/system/opt/daedalus/shared/policy.toml` 的**完整生产一致副本**（不是缩水 fixture）
  - 加同步脚本 `daedalus-core/scripts/sync-policy-testdata.sh`：`cp daedalus-core/files/system/opt/daedalus/shared/policy.toml daedalus-sdk/policy/testdata/policy.toml`（在 todo 4 内写脚本；CI 与本地 dev 跑）
  - 加 CI 守门（review Oracle Issue 3）：`daedalus-sdk/.github/workflows/test.yml` 加一步 `diff daedalus-core/files/system/opt/daedalus/shared/policy.toml daedalus-sdk/policy/testdata/policy.toml` 不一致即 fail（todo 14 配 CI 时落实；本 todo 先把脚本与 testdata 落位）
  
  更新 `policy_test.go` 加 `TestResolvePath_CrossRepoLayout` 覆盖两个候选都命中的场景（含 CWD-level testdata 优先 + walk-up cross-repo 命中的两种布局）。验收：
  - `cd daedalus-core && go test ./...` exit 0（原 daedalus-core go.mod 仍用旧 `daedalus/core/...` 路径 OK）
  - `cd daedalus-sdk && go test ./policy/... ./objectmodel/...` exit 0（review Oracle Issue 1 修：从 SDK 仓跑测试也能命中 policy.toml）
  - `grep -E 'daedalus-os/daedalus/core/internal/(audit|blueprint|i18n|objectmodel|pathguard|pkgquery|plugin|policy|shellpolicy|sysinfo|version)' daedalus/files/scripts/*.sh | wc -l` 应为 0
  Parallelization: Wave 1 | Blocked by: 3 | Blocks: 8, 9, 12, 18
  References: `daedalus/files/scripts/76-daedalus-plugin-gen.sh:1-246`；`daedalus/files/scripts/70-daedalus-mcp-servers.sh`；`daedalus/core/internal/policy/policy_test.go`；`daedalus/core/internal/objectmodel/objectmodel_test.go`；`daedalus/core/internal/policy/policy.go:43-46`（DevRelPath 常量）+ `policy.go:125-146`（ResolvePath 循环体基线）；`daedalus-sdk/policy/policy.go`（todo 2 迁过去的，需更新 ResolvePath()）；`daedalus/core/files/system/opt/daedalus/shared/policy.toml`（生产真值）
  Acceptance criteria (agent-executable): `cd daedalus-core && go test ./...` exit 0；`cd daedalus-sdk && go test ./policy/... ./objectmodel/...` exit 0（review Oracle Issue 1 验收：从 SDK 仓跑测试也通）；`grep -E 'daedalus-os/daedalus/core/internal/(audit|blueprint|i18n|objectmodel|pathguard|pkgquery|plugin|policy|shellpolicy|sysinfo|version)' daedalus/files/scripts/*.sh | wc -l` 应为 0；`test -f daedalus-sdk/policy/testdata/policy.toml`（testdata fixture 就位）+ `diff daedalus/core/files/system/opt/daedalus/shared/policy.toml daedalus-sdk/policy/testdata/policy.toml` exit 0（testdata = 生产一致副本，review Oracle Issue 3）
  QA scenarios:
    - happy: 3 点漂移测试全 pass；`policy.ResolvePath()` 在 mono-repo（testdata 命中）与 post-split（cross-repo 命中）两种布局至少命中一个；76 脚本 dry-run (`DAEDALUS_PLUGIN_GEN_ROOT=/tmp/xxx bash 76-daedalus-plugin-gen.sh`) exit 0；`sync-policy-testdata.sh` 跑一次后 `diff` exit 0
    - failure: 漂移测试 fail（policy.toml ↔ `policy.Default()` ↔ objectmodel 常量 任一不一致）→ exit 1 + 具体不一致项；或 SDK 仓 `go test ./policy/...` fail（DevRelPath 迁移漏改 / 循环体错）→ exit 1；或 testdata 与生产 policy.toml 不一致（sync 未跑）→ diff exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a1/task-4.log`
  Commit: Y | `chore(scripts): update 76 + 70 scripts for SDK module layout + DevRelPath cross-repo migration + testdata sync`

- [x] 5. 创建 `daedalus-plugins/` monorepo 骨架 + 6 个子目录 go.mod
  What to do / Must NOT do: 在主仓根新建 `daedalus-plugins/` 目录（与 `daedalus-core/`、`daedalus-sdk/` 同级），写 `daedalus-plugins/go.work` 含 6 个 module 引用占位；建 6 个子目录 `fs/ shell/ pkg/ sysinfo/ service/ blueprint/`，每子目录写 `go.mod`（module = `github.com/Daedalusys/daedalus-plugins/<cap>`，go 1.25.0，依赖 SDK 仓 `require github.com/Daedalusys/daedalus-sdk v0.0.0` + `replace github.com/Daedalusys/daedalus-sdk => ../../daedalus-sdk`）；**不**复制任何插件源码（todo 6 负责）；**不**新建 `blueprints/` 数据目录（todo 6 整体迁）。
  Parallelization: Wave 2 | Blocked by: 3 | Blocks: 6, 7
  References: `daedalus/core/cmd/daedalus-{fs,shell,pkg,sysinfo,service,blueprint}/` 6 个 cmd 目录；`daedalus/plugin/{fs,shell,pkg,sysinfo,service,blueprint}/` 6 个 plugin 源目录
  Acceptance criteria (agent-executable): `test -d daedalus-plugins && for c in fs shell pkg sysinfo service blueprint; do head -1 daedalus-plugins/$c/go.mod | grep -q "module github.com/Daedalusys/daedalus-plugins/$c"; done`
  QA scenarios:
    - happy: 6 个子目录 + go.mod 全部就位；`go.work` 文件含 6 个 use 指令
    - failure: 任一子目录缺 go.mod 或 module 路径错 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a2/task-5.log`
  Commit: Y | `chore(plugins): scaffold daedalus-plugins monorepo with 6 sub-modules`

- [x] 6. 物理迁移 6 个 Go 能力插件源（manifest + cmd/ + i18n/ + tests/）
  What to do / Must NOT do: 对 6 个能力插件逐个 `git mv`：
  - `daedalus/plugin/fs/` → `daedalus-plugins/fs/`
  - `daedalus/plugin/shell/` → `daedalus-plugins/shell/`
  - `daedalus/plugin/pkg/` → `daedalus-plugins/pkg/`
  - `daedalus/plugin/sysinfo/` → `daedalus-plugins/sysinfo/`
  - `daedalus/plugin/service/` → `daedalus-plugins/service/`
  - `daedalus/plugin/blueprint/` → `daedalus-plugins/blueprint/`
  每插件源内容：`daedalus.plugin.json`（manifest）+ `cmd/daedalus-<cap>/`（Go 源码，从 `daedalus/core/cmd/daedalus-<cap>/` 迁）+ `i18n/{en_US,zh_CN}.json`（如有）+ `cmd/daedalus-<cap>/*_test.go`；**不**改 `cmd/*.go` 的 import 路径（review Oracle I 修；todo 3 已改好，git mv 后 import 不变，重复改是 no-op）；**不**迁 copilot 插件（留主仓）；**不**迁 `blueprints/` 数据形态（保留 `//go:embed` 原状，但 rsync 路径指向新位置）。
  Parallelization: Wave 2 | Blocked by: 5 | Blocks: 7
  References: `daedalus/core/cmd/daedalus-fs/main.go` + `daedalus/plugin/fs/daedalus.plugin.json`；类比 6 套；`daedalus/plugin/blueprint/blueprints/` 数据保留
  Acceptance criteria (agent-executable): `for c in fs shell pkg sysinfo service blueprint; do test -f daedalus-plugins/$c/daedalus.plugin.json && test -d daedalus-plugins/$c/cmd; done`；`test -d daedalus/plugin/copilot`（copilot 仍在主仓）；`cd daedalus-plugins/fs && go build -trimpath -o /tmp/daedalus-fs . && /tmp/daedalus-fs --help 2>&1 | head`（daedalus-fs 可执行）
  QA scenarios:
    - happy: 6 个插件源完整迁出；每插件的 cmd 仍能编译；copilot 仍留主仓
    - failure: 6 个插件任一文件漏迁或 cmd 编译失败 → exit 1 + 编译错串
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a2/task-6.log`
  Commit: Y | `refactor(plugins): relocate 6 capability plugin sources to plugins monorepo`

- [x] 7. 改写 `justfile` 的 `plugin-pack` 指向新源路径 + 76 脚本 + sync 脚本 + blueprint-embed 路径
  What to do / Must NOT do: 改 `justfile` 的 `plugin-pack` recipe 的循环源路径 `daedalus/plugin/${cap}` → `daedalus-plugins/${cap}`；改 `scripts/sync-daedalus.sh` plugin 腿的源路径；改 `daedalus/files/scripts/76-daedalus-plugin-gen.sh` 路径引用（CAPS 变量与 `$PLUGINS` 路径可能也需改，但本 todo 保留根 `daedalus/` 仓结构指向 `daedalus/files/system/opt/daedalus/plugins/`，即安装态路径不变，只动 source 路径）；**修正 blueprint-embed 路径**（review Issue C）：`blueprint-embed` recipe 的 rsync 源/目标都必须改：
  - 源: `daedalus-plugins/blueprint/blueprints/`（不再是 `daedalus/plugin/blueprint/blueprints/`，因为 blueprint 插件已迁 monorepo）
  - 目标: `daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/`（**不再是** `daedalus/core/cmd/daedalus-blueprint/blueprints/`，因为 `daedalus-blueprint` cmd 已随 todo 6 迁出到 plugins monorepo；todo 12 物理拆仓后路径变 `daedalus-core/cmd/...`，但 blueprint cmd 不在 core 仓，所以 todo 12 不动它）
  
  `//go:embed all:blueprints/*` 在 `daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints_embed.go` 仍指向 cmd 同目录的 `blueprints/` 子目录（与原方案同构，embed 数据必须紧邻 cmd）。**也加新仓 `.gitignore`**（review Oracle E 修；todo 6 迁出后，`daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/` 是 plugins 仓的新目录，由 `just blueprint-embed` 周期性 rsync 写入，必须在 plugins 仓根 `.gitignore` 加 `blueprint/cmd/daedalus-blueprint/blueprints/` 一行；原 `daedalus/core/cmd/daedalus-blueprint/blueprints/` 的 gitignore 行保留在 core 仓直到 todo 17 清理）。**不**重写 76 脚本的 7 阶段校验逻辑；**不**动 copilot 路径（`scripts/pack-copilot-plugin.sh` 与 `just copilot-plugin` recipe 保留 `daedalus/plugin/copilot/` 引用）；**不**改 `blueprints/` 数据形态（6 蓝图 manifest.json + schema.json + template.tmpl + pre_check.sh + post_check.sh + README.md 原样保留）。
  Parallelization: Wave 2 | Blocked by: 6 | Blocks: 8, 9, 12, 18
  References: `justfile:65-141` (plugin-pack recipe)；`justfile:250-255` (blueprint-embed recipe)；`scripts/sync-daedalus.sh:1-200`；`daedalus/files/scripts/76-daedalus-plugin-gen.sh:53` (CAPS 变量)；`daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints_embed.go:28` (`//go:embed all:blueprints/*`)
  Acceptance criteria (agent-executable): `grep -E "daedalus-plugins" justfile` 应有 ≥ 8 行（plugin-pack 循环 6 + blueprint-embed 2 源/目标）；`grep -E "daedalus-plugins" scripts/sync-daedalus.sh` 应有 ≥ 6 行；`just plugin-pack 2>&1 | tail -5` 成功打印"plugin-pack: 6 个能力插件 ... 已安装"；`just blueprint-embed 2>&1` 成功打印 rsync 完成（6 蓝图子目录 + 36 文件就位）
  QA scenarios:
    - happy: `just plugin-pack` 跑通（前提：todo 1-6 已完成），6 插件 zip 生成 + 6 安装态目录就位 + /usr/local/bin 6 二进制就位；`just go-build` 不报 `blueprints/` embed 错误
    - failure: 76 脚本任一校验失败（manifest 不存在 / 单元缺 DynamicUser / tools drift）→ exit 1 + 错串；或 `blueprint-embed` rsync 目标不存在 → 默默写到错位置，embed 失败 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a2/task-7.log`
  Commit: Y | `chore(build): point justfile + sync + 76 script to plugins monorepo (incl. blueprint-embed path fix)`

- [x] 8. 60 个文件（59 .go + 1 go.mod）import 路径全量 rename：`daedalus-os` → `Daedalusys`
  What to do / Must NOT do: 用 `sed -i` 或 Go AST 工具（如 `gofmt -r`）在所有 `.go` 与 `go.mod` 文件中：
  - `github.com/daedalus-os/daedalus/core` → `github.com/Daedalusys/daedalus-core`（含 cmd + internal/4 核心包 + tests，共 51 个 core 文件）
  - `github.com/daedalus-os/daedalus/core/internal/<X>` 已在 todo 3 改 → `github.com/Daedalusys/daedalus-sdk/<X>`（不再二次改）
  - `daedalus/core/go.mod` module 声明 → `github.com/Daedalusys/daedalus-core`
  - `daedalus-sdk/go.mod` module 声明 → `github.com/Daedalusys/daedalus-sdk`（todo 1 已设；本 todo 校验）
  - `daedalus-plugins/<cap>/go.mod` module 声明 → `github.com/Daedalusys/daedalus-plugins/<cap>`（todo 5 已设；本 todo 校验）
  - **也**改 `daedalus/core/internal/version/version.go:13` 的字符串常量 `const ModulePath = "github.com/daedalus-os/daedalus/core"` → `const ModulePath = "github.com/Daedalusys/daedalus-core"`（review Oracle A 修；原 plan 路径错写为 `daedalus/core/version/version.go` 实际在 `internal/` 子树；review Issue A 补；todo 3 留白由本 todo 收口）
  
  改完跑 `cd daedalus-core && go mod tidy && go test ./...`；**不**改任何 `.md` 文档（todo 9 改）；**不**改 shell 脚本（todo 9 改）；**不**改 `daedalus-os/...` 字符串在注释中的（**强制**：注释语言为中文，零英文 `daedalus-os` 引用）。
  Parallelization: Wave 3 | Blocked by: 4, 7 | Blocks: 12
  References: 60 文件 grep 计数 = 51 core + 7 SDK-internal + 1 version.go + 1 go.mod（review Issue B 修正；原 plan 误计 49）；`daedalus/core/go.mod:1`；`daedalus-sdk/go.mod:1`；`daedalus-plugins/<cap>/go.mod:1`；`daedalus/core/internal/version/version.go:13`（oracle A 修正路径）
  Acceptance criteria (agent-executable): `grep -rE 'github.com/daedalus-os' --include='*.go' --include='go.mod' . | wc -l` 应为 0；`grep -rE 'github.com/Daedalusys' --include='*.go' --include='go.mod' . | wc -l` 应 ≥ 60；`cd daedalus-core && go mod tidy && go test ./...` exit 0；`cd daedalus-sdk && go test ./...` exit 0；`for c in fs shell pkg sysinfo service blueprint; do (cd daedalus-plugins/$c && go build -trimpath -o /tmp/test-$c ./cmd/daedalus-$c) && (cd daedalus-plugins/$c && go test ./...); done` exit 0
  QA scenarios:
    - happy: 全部 60 个文件 import 路径更新到新前缀；3 仓 (core/sdk/plugins) 全部 `go build` + `go test` 通过；`version.ModulePath` 字符串也改完
    - failure: 任一 import 漏改 → `go build` 报 unresolved import；或 `go mod tidy` 找不到 module；或 `version.ModulePath` 漏改 → 任何使用 `version.ModulePath` 的代码报旧路径 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a3/task-8.log`
  Commit: Y | `refactor: rename 60 file import paths to github.com/Daedalusys/... (incl. version.ModulePath)`

- [x] 9. build 脚本 / containerfile / CI / docs 字符串引用 rename + copilot dev 路径修复 + tests/deno + i18n 脚本 + justfile.demo 路径修复
  What to do / Must NOT do: 在所有非 Go 文件中（`*.sh` `Containerfile` `.github/workflows/*.yml` `*.md` `*.toml` `justfile` `daedalus-dev.toml*` `*.ts`）做字符串替换：
  - `github.com/daedalus-os/daedalus` → `github.com/Daedalusys/daedalus-{core,sdk,plugins}`（按上下文区分 module）
  - `git@github.com:Daedalusys/Daedalusys.git` 保留（实际 remote 已是）
  - 注释中的"daedalus-os"全部清掉（中文注释强制）
  
  涉及文件：`Containerfile`、`scripts/sync-daedalus.sh`、`daedalus/files/scripts/*.sh`、`daedalus/files/system/usr/lib/systemd/system/daedalus-*.service`（若有）、`daedalus/files/system/usr/local/bin/daedalus` wrapper、`.github/workflows/build-daedalus.yml`、`AGENTS.md`、`VISION.md`、`README.md`、`daedalus-dev.toml.example`、`justfile`、`scripts/justfile.demo`、`scripts/copilot-prep.sh`、`scripts/pack-copilot-plugin.sh`
  
  **也改 4 个 copilot Deno 源文件**（review Issue D 修；原 plan 误判"零改动"——这些 `.ts` 包含 13 个硬编码 `daedalus/core/bin/...` 字符串，dir 改名后会指向不存在路径导致 dev 回退链断）：
  - `daedalus/plugin/copilot/audit.ts:74,75,85` —— `daedalus/core/bin/daedalus-audit` → `daedalus-core/bin/daedalus-audit`（以及 `../daedalus/core/bin/...` → `../daedalus-core/bin/...`）
  - `daedalus/plugin/copilot/exec.ts:110,111,122,157,158,169` —— `daedalus/core/bin/daedalus-{shell,tx}` → `daedalus-core/bin/daedalus-{shell,tx}`
  - `daedalus/plugin/copilot/main.ts:200,201` —— `daedalus/core/bin/daedalus-tx` → `daedalus-core/bin/daedalus-tx`
  - `daedalus/plugin/copilot/policy.ts:8` —— 注释中 `daedalus/core/internal/shellpolicy` → `daedalus-core/internal/shellpolicy`（注释为中文本体，英文 token 改路径）
  
  涉及 comment 行（`audit.ts:61` `exec.ts:92,137` `main.ts:174`）也同步改；**不**改 i18n locale 文件内容（zh_CN / en_US）；**不**改 `daedalus/files/system/opt/daedalus/plugins/` 任何安装态（构建产物）。
  
  **也改全部 10 个 Deno 测试文件的路径**（review Momus #2 + Oracle Issue 1/2 修；这些测试在每 wave 跑 `deno test --allow-all tests/deno/` 时执行，包含 todo 12 rename 后失效的硬编码路径；**原 plan 只列 2 个文件是最大遗漏——实际有 10 个文件 21 处 import + 6 处字符串**）。按路径机制分三类，**三种机制不同，executor 必须区分**：
  
  **类别 A：ES module import（10 个文件 21 处 `../../daedalus/plugin/copilot/*.ts`）** —— post-split copilot 在 `daedalus-core/plugin/copilot/`，从 `daedalus-core/tests/deno/` 到 copilot 是 `../../plugin/copilot/`（**注意**：`daedalus/` 段随 rename 消失，不是 `../../../daedalus-core/plugin/copilot/`——copilot 在 core 仓**内部**，不是兄弟仓；momus 的 `../../../` 只对 SDK 兄弟仓成立，对 copilot 不成立）。改法：全局 `../../daedalus/plugin/copilot/` → `../../plugin/copilot/`：
  - `tests/deno/audit.test.ts:7` (`audit.ts`)
  - `tests/deno/exec.test.ts:7` (`exec.ts`)
  - `tests/deno/i18n.test.ts:21` (`i18n.ts`)
  - `tests/deno/llm.test.ts:3` (`llm.ts`)
  - `tests/deno/llm_error.test.ts:23` (`llm.ts`)
  - `tests/deno/main.test.ts:2-5,8` (`main.ts` + `exec.ts` + `i18n.ts` + `policy.ts`)
  - `tests/deno/policy.test.ts:2,17` (`policy.ts`)
  - `tests/deno/risk-classifier.test.ts:7,8` (`policy.ts`)
  - `tests/deno/shellpolicy_contract.test.ts:15` (`policy.ts`)
  - `tests/deno/tx_aggregate.test.ts:13-16,403,424` (`main.ts` + `audit.ts` + `i18n.ts` + `exec.ts` + i18n URL + main URL)
  
  **类别 B：URL-relative 读文件（shellpolicy_contract.test.ts）** —— 该测试用 `new URL("相对路径", repoRoot)`，`repoRoot = new URL("../../", import.meta.url)` 指向**仓库根**（post-split = `daedalus-core/`）。三类引用：
  - `shellpolicy_contract.test.ts:20` 读 `daedalus/core/internal/shellpolicy/shellpolicy.go` → **`../../../daedalus-sdk/shellpolicy/shellpolicy.go`**（review Momus 实证修正：`daedalus-sdk/` 是**兄弟仓**，从 `daedalus-core/tests/deno/` 出发 `../../../` 才到 `daedalusys/` 平级目录；`../../` 只到 `daedalus-core/`，找不到 SDK；momus 用 Deno 2.9.5 实测：`../../daedalus-sdk` → NOT FOUND，`../../../daedalus-sdk` → EXISTS）
  - `shellpolicy_contract.test.ts:24` 读 `daedalus/files/system/opt/daedalus/shared/policy.toml` → **`../../../daedalus-core/files/system/opt/daedalus/shared/policy.toml`**（review Oracle Issue 2 补：`daedalus/files/` rename 到 `daedalus-core/files/`，且从 `daedalus-core/` 根出发是 `../../`，但这里 `repoRoot` 已指向 `daedalus-core/`，所以相对 `repoRoot` 是 `files/system/...`——**修正**：`new URL("files/system/opt/daedalus/shared/policy.toml", repoRoot)`，因为 `repoRoot` = `daedalus-core/` 根，`daedalus/files/` → `daedalus-core/files/` 在 `repoRoot` 之下，无 `..` 前缀）
  - `shellpolicy_contract.test.ts:97` 读 `daedalus/plugin/copilot/policy.ts` → **`plugin/copilot/policy.ts`**（相对 `repoRoot` = `daedalus-core/`，`daedalus/plugin/` → `daedalus-core/plugin/`，`daedalus/` 段消失；review Oracle Issue 2 补）
  
  **类别 C：CWD-relative `Deno.statSync` 字面字符串（exec.test.ts）** —— CI 从 `daedalus-core/` 根跑 `deno test tests/deno/`，CWD = `daedalus-core/`，字面路径直接相对 CWD（**不是**相对 `tests/deno/`）：
  - `exec.test.ts:344,351-352,714,723-724` —— `"daedalus/core/bin/daedalus-shell|tx"` → **`"daedalus-core/bin/daedalus-shell|tx"`**（一个段，CWD = `daedalus-core/`）；`"../daedalus/core/bin/daedalus-shell|tx"` → **`"../daedalus-core/bin/daedalus-shell|tx"`**（review Momus 实证修正：CWD-relative 用**单段** `daedalus-core/bin/...`，**不是** `../../daedalus-core/bin/...`——`../../` 会从 `daedalus-core/` 跑到 `daedalusys/` 再找 `daedalus-core/`，错）
  
  **CI workspace 布局要求**（review Oracle Issue 4 + Momus 实证）：`daedalus-core` 的 CI workflow 必须 `actions/checkout` `Daedalusys/daedalus-sdk` 到 `../daedalus-sdk`（平级兄弟），否则类别 B 的 `../../../daedalus-sdk/...` 解析失败；该 checkout 步骤在 todo 14 已修。
  
  **验收 grep 必须扩到全部 3 类**（review Oracle Issue 1）：`grep -rE 'daedalus/(core|plugin|files)' tests/deno/*.test.ts | wc -l` 应为 0（覆盖 `daedalus/core/` `daedalus/plugin/` `daedalus/files/` 三种前缀；原 plan 的 `grep -E 'daedalus/core/'` 漏了 `daedalus/plugin/copilot/` import 与 `daedalus/files/` policy.toml 路径）。
  
  **也改 `scripts/plugin-i18n-sync.sh`**（review Oracle C 修；该脚本的硬编码路径会断）：
  - line 58: `$ROOT/daedalus/core/` → `$ROOT/daedalus-core/`（扫描根）
  - line 63: `$ROOT/daedalus/core/internal/i18n/locales` → `$ROOT/daedalus-sdk/i18n/locales`（locale 目录）
  - line 69: 同上
  
  **也改 `scripts/justfile.demo`**（review Oracle D 修；该 demo 含 dev 路径硬编码）：
  - line 24: `cd daedalus/core` → `cd daedalus-core`（go-build-demo 路径）
  - line 27: `daedalus/files/system/usr/local/bin` → `daedalus-core/files/system/usr/local/bin`
  - line 67: `DAEDALUS_STATE_PATH="$PWD/daedalus/files/system/var/lib/daedalus/state.jsonl"` → `DAEDALUS_STATE_PATH="$PWD/daedalus-core/files/system/var/lib/daedalus/state.jsonl"`
  - line 68: `DAEDALUS_TX_DIR="$PWD/daedalus/files/system/var/lib/daedalus/tx"` → `DAEDALUS_TX_DIR="$PWD/daedalus-core/files/system/var/lib/daedalus/tx"`
  
  **也收口 regex**（review Oracle B 修；`grep -rE 'daedalus-os' ...` 误匹配镜像 tag `daedalus-os:latest`、构建产物 `daedalus-os.oci.tar` / `daedalus-os-qcow2-*`、image 命名 `localhost/daedalus-os:latest`）：
  - 验收 grep 改为 `grep -rE 'github.com/daedalus-os' --include='*.sh' --include='Containerfile' --include='*.yml' --include='*.md' --include='*.toml' --include='*.ts' .` 专指 Go module 引用
  - 镜像 tag / 构建产物名 / `localhost/daedalus-os` 字串保留（不是 Go module path）
  - Acceptance 的 `wc -l == 0` 仅对 `github.com/daedalus-os` 模式生效
  Parallelization: Wave 3 | Blocked by: 4, 7 | Blocks: 12
  References: 60 文件 grep 计数 `daedalus-os/daedalus`（49 .go + 1 go.mod 由 todo 8 改；本 todo 改全部非 Go 文件 + 4 个 copilot .ts + 全部 10 个 tests/deno .ts + 2 个 helper 脚本）；`Containerfile`、`scripts/sync-daedalus.sh`、`.github/workflows/build-daedalus.yml`、各 `*.md`；`daedalus/plugin/copilot/{audit,exec,main,policy}.ts` 13 个字符串行 + 1 注释行（review Issue D 验证清单）；`tests/deno/` 10 个测试文件 21 处 import + 6 处字符串（review Momus #2 实证 + Oracle Issue 1/2 清单）；`scripts/plugin-i18n-sync.sh` line 58/63/69（review Oracle C）；`scripts/justfile.demo` line 24/27/67-68（review Oracle D）
  Acceptance criteria (agent-executable): `grep -rE 'github.com/daedalus-os' --include='*.sh' --include='Containerfile' --include='*.yml' --include='*.md' --include='*.toml' --include='*.ts' --include='go.mod' . | wc -l` 应为 0（注释与字符串清空，包括 Go module 引用 + 4 个 copilot .ts + 全部 10 个 tests/deno .ts；review Oracle B 修订 regex 缩窄到 `github.com/daedalus-os` 模式，避免误匹配 `daedalus-os:latest` image tag 与 `daedalus-os.oci.tar` 构建产物）；`bash -n scripts/sync-daedalus.sh` exit 0；`bash -n daedalus/files/scripts/76-daedalus-plugin-gen.sh` exit 0；`bash -n scripts/plugin-i18n-sync.sh` exit 0；`grep -E 'daedalus/core/bin' daedalus/plugin/copilot/{audit,exec,main}.ts | wc -l` 应为 0；`grep -rE 'daedalus/(core|plugin|files)' tests/deno/*.test.ts | wc -l` 应为 0（review Oracle Issue 1 修正：覆盖全部 3 类前缀，原 plan 只查 `daedalus/core/` 漏了 `daedalus/plugin/` import）
  QA scenarios:
    - happy: 全部非 Go 文件 + 4 个 copilot Deno 文件 + 全部 10 个 tests/deno 测试文件 + 2 个 helper 脚本无 `github.com/daedalus-os` 残留；13 个 `daedalus/core/bin/...` copilot 字符串全改 `daedalus-core/bin/...`；21 处 `../../daedalus/plugin/copilot/` import 全改 `../../plugin/copilot/`；shellpolicy_contract 的 SDK 引用改 `../../../daedalus-sdk/...` + core files 引用改相对 `repoRoot`；exec.test.ts 的 CWD-relative 改 `daedalus-core/bin/...`；`scripts/plugin-i18n-sync.sh` 与 `scripts/justfile.demo` 路径全改；shell 脚本语法 OK；AGENTS.md/VISION.md/README.md 中无 `github.com/daedalus-os` 残留
    - failure: 任一脚本语法错（sed 误伤引号）→ `bash -n` 报 syntax error；或 copilot 路径漏改 → dev 模式断链（v3 构建机发现，本机无 deno 跑不全）→ exit 1；或任一 Deno 测试路径漏改 → `deno test --allow-all tests/deno/` 在 Wave 5+ 失败（`../../daedalus/plugin/copilot/` import 找不到 → module not found）→ exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a3/task-9.log`
  Commit: Y | `chore: rename non-Go references from daedalus-os to Daedalusys (copilot + 10 tests/deno + i18n + justfile.demo)`

- [x] 10. 改 `daedalus-sdk/internal/plugin/manifest.go` schema（C1 升级）
  What to do / Must NOT do: 改 `daedalus-sdk/internal/plugin/manifest.go`（todo 2 迁过来的 internal/plugin/manifest.go；注意 SDK 仓没有 `internal/` 限制，包路径 = `daedalus-sdk/plugin/manifest.go`）：
  - `Runtime` 字段从 `string` 改结构化对象：`type Runtime struct { Name string \`json:"name"\`; Version string \`json:"version,omitempty"\` }`（`Name` 枚举 `deno`/`native`/`controller`，`Version` 可选为 runtime 版本号）
  - 加 `APIVersion string \`json:"api_version"\``（semver，必填）
  - 加 `License string \`json:"license"\``（SPDX 标识，必填）
  - 加 `Maintainer string \`json:"maintainer"\``（email 格式，必填）
  - 保留 `ID/Name/Version/Type/Executable/Entrypoint/Permissions/Tools/Resources/I18n/Checksums` 11 个现有字段
  - 写新字段的 validate 函数（`APIVersion` semver 校验、`License` SPDX 格式校验、`Maintainer` email 格式校验）
  - **改既有 `manifest_test.go`（review Oracle Issue 4 修；原 plan 只加 4 个新测试，但 `manifest_test.go` 的 `validManifest()` 助手与 ~10 个既有用例用 `Runtime: RuntimeNative` 字符串赋值，Runtime 改 struct 后**编译失败**）**：
    - `daedalus-sdk/plugin/manifest_test.go:17` 的 `Runtime: RuntimeNative` → `Runtime: Runtime{Name: RuntimeNative}`
    - `manifest_test.go:38` 的 `m.Runtime = RuntimeDeno` → `m.Runtime = Runtime{Name: RuntimeDeno}`
    - `manifest_test.go:79,126,128-131` 其他 Runtime 赋值同样改 struct
    - `validManifest()` 助手加 3 个必填新字段：`APIVersion: "0.1.0"`、`License: "Apache-2.0"`、`Maintainer: "team@daedalusys.io"`
    - 既有"合法清单"用例（`TestValidate_AcceptsLegalManifests`）因新必填字段加入 `validManifest()` 而自动通过；**若某用例显式省略新字段测试"可选"语义，需确认新字段是必填而非可选**
  - 显式要求 `UnmarshalJSON` 实现（review Oracle Issue 4）：`Manifest.UnmarshalJSON` 接受 `"runtime": "deno"` 老形态（字符串）→ `Runtime{Name: "deno"}`；写时（Marshal）为新对象形态。**这是硬性要求**，不是叙述性提及——6 个插件 manifest 的旧 `"runtime": "native"` 在 todo 11 改之前，校验器必须能读
  - 改 `daedalus-sdk/internal/plugin/manifest_resources_test.go` 加新字段测试
  - **不**删 `runtime` 字符串字段的兼容；**不**改其他 11 个 SDK 包。
  Parallelization: Wave 4 | Blocked by: 4, 7 | Blocks: 11
  References: `daedalus/core/internal/plugin/manifest.go`（迁到 SDK 后的路径 = `daedalus-sdk/plugin/manifest.go`）；`daedalus/core/internal/plugin/manifest_test.go:17,38,79,126,128-131`（validManifest 助手 + 既有用例，review Oracle Issue 4 清单）；`daedalus/core/internal/plugin/manifest_resources_test.go`
  Acceptance criteria (agent-executable): `cd daedalus-sdk && go test ./plugin/...` exit 0（**含既有 manifest_test.go 编译通过**，review Oracle Issue 4 验收：改 Runtime struct 后 `validManifest()` 与全部既有用例不编译失败）；新增测试 `TestManifestAPIVersion`、`TestManifestLicense`、`TestManifestMaintainer`、`TestManifestRuntimeStruct` 4 项全 pass；`grep -E "APIVersion|License|Maintainer|Runtime" daedalus-sdk/plugin/manifest.go | wc -l` ≥ 8；`grep -c "Runtime: RuntimeNative\|Runtime{Name:" daedalus-sdk/plugin/manifest_test.go` ≥ 1（旧字符串赋值已清）
  QA scenarios:
    - happy: 4 个新测试全 pass；既有 `TestValidate_AcceptsLegalManifests` 全 pass（validManifest 已含新字段）；老 manifest（`runtime: "deno"` 字符串）经 `UnmarshalJSON` 仍能反序列化为 `Runtime{Name: "deno"}`
    - failure: 任一字段校验失败（`api_version` 非 semver / `license` 非 SPDX / `maintainer` 非 email）→ 测试报 expected vs got → exit 1；**或 `manifest_test.go` 编译失败（Runtime 字符串赋值未改 struct）→ `go test ./plugin/...` 报 compile error → exit 1**
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a4/task-10.log`
  Commit: Y | `feat(sdk/plugin): add api_version/license/maintainer + structured runtime per C1 (incl. existing manifest_test.go migration)`

- [x] 11. 6 插件 manifest 改写 + i18n 同步 + 校验测试
  What to do / Must NOT do: 改 6 个插件 manifest（`daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint}/daedalus.plugin.json`）：
  - 加 `"api_version": "0.1.0"`
  - 加 `"license": "Apache-2.0"`
  - 加 `"maintainer": "team@daedalusys.io"`
  - 改 `"runtime": "native"` → `"runtime": { "name": "native" }`
  - 改 `"runtime": "deno"` → `"runtime": { "name": "deno" }`（仅 copilot；本 todo 不动 copilot）
  - 保留 `id/name/version/type/executable/entrypoint/permissions/tools/resources/i18n` 9 个现有字段原值
  - 跑 `daedalus/core/bin/daedalus-plugin-pack -in daedalus-plugins/<cap> -out /tmp/<cap>.plugin.zip` 校验新 schema 可被 Pack 接受
  - **不**改 i18n 目录（i18n 字段在 manifest 中是声明，与新字段无关）；**不**改 copilot manifest（留主仓，单独 todo 不在范围）；**不**改 6 个插件的 `bin/daedalus-<cap>` 二进制（构建产物）。
  Parallelization: Wave 4 | Blocked by: 10 | Blocks: 12
  References: `daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint}/daedalus.plugin.json` 6 文件；`daedalus-sdk/plugin/manifest.go` 新 schema（todo 10 产出）
  Acceptance criteria (agent-executable): `for c in fs shell pkg sysinfo service blueprint; do daedalus/core/bin/daedalus-plugin-pack -in daedalus-plugins/$c -out /tmp/$c.zip 2>&1 | grep -q "checksums injected"; done`；`cat daedalus-plugins/fs/daedalus.plugin.json | jq .api_version,.license,.maintainer,.runtime.name` 应输出 4 行非空
  QA scenarios:
    - happy: 6 插件 manifest 全部含 4 个新字段；Pack 工具接受新 schema 并注入 checksums
    - failure: 4 新字段任一缺失或 runtime 仍为字符串 → Pack 报 `manifest validation failed: missing api_version` → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a4/task-11.log`
  Commit: Y | `feat(plugins): rewrite 6 manifests to C1 schema (api_version/license/maintainer/runtime struct)`

- [x] 12. **物理目录改名** `daedalus/core/` → `daedalus-core/` + 创建 3 个 GitHub 仓 + 推代码 + branch protection
  What to do / Must NOT do: **第一步（review Momus #3 修；原 plan 把目录改名放到 todo 17 与 todo 12 拆仓矛盾；momus 进一步指出 `git mv daedalus/core daedalus-core` 不产生嵌套 `core/`）**：在主仓根执行 `git mv daedalus/core daedalus-core`（**注意**：这会把 `daedalus/core/{cmd,go.mod,go.sum,internal,Makefile}/` 平铺到 `daedalus-core/{cmd,go.mod,go.sum,internal,Makefile}/`——**没有** `daedalus-core/core/` 嵌套；momus 验证：实际 `daedalus/core/` 内容是 `cmd/ go.mod go.sum internal/ Makefile` 直接在根，不是嵌套结构）+ `git mv daedalus/plugin daedalus-core/plugin`（仅迁 `copilot/` 与 `README.md`，6 能力已在 todo 6 迁 monorepo） + `git mv daedalus/files daedalus-core/files` —— 三次 git mv 是 atomic 改名，所有 git 历史保留。改完**主仓根**布局：`daedalus-core/{cmd,go.mod,go.sum,internal,Makefile,plugin/copilot,plugin/README.md,files}/` + 旁路 `daedalus-sdk/` `daedalus-plugins/`（仍在主仓根，等第二步 git push 出仓）+ 仓根其他文件 `Containerfile` `justfile` `scripts/` `tests/` `AGENTS.md` `VISION.md` `README.md` `daedalus-dev.toml.example` `.github/workflows/` `examples/`。**主仓**整体改名为 `daedalus-core`（原 `daedalusys/Daedalusys`）。**第二步** GitHub 仓建：
  
  **第二步**：用 `gh repo create` 创建 3 仓（前提：todo 1-11 已完成 = 主仓代码已就位可拆）：
  - `gh repo create Daedalusys/daedalus-core --public --description "Daedalus image build + 5 core runtime (host/audit/tx/smoke/plugin-pack) + copilot"`
  - `gh repo create Daedalusys/daedalus-sdk --public --description "Daedalus SDK: 11 security core packages (audit/policy/pathguard/shellpolicy/version/plugin/objectmodel/i18n/pkgquery/sysinfo/blueprint) + 5 Provider/Slot placeholders"`
  - `gh repo create Daedalusys/daedalus-plugins --public --description "Daedalus plugins monorepo: 6 Go capability plugins (fs/shell/pkg/sysinfo/service/blueprint)"`
  
  物理拆分：
  - 新仓 `git init` + 加 remote + `git push` + 配置 `main` 分支保护（要求 PR + 1 review + CI pass）
  - **不**改源码逻辑（仅物理迁移）；**不**改 Go module 路径（todo 8 已改完 `replace ../daedalus-sdk` 在阶段 1 期间；阶段 2 改 `replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk` 或在 3 仓根目录设 `go.work` 替代）；**不**触发 CI（CI 在 todo 14 配置）。
  
  **失败路径**（review Issue H 强约束）：`gh` 未认证或网络断时，本 todo 降级为"仅完成物理改名 + 准备 push 脚本"，把 `gh repo create` 步骤移到 V3 构建机合并后跑；**不**让"无 gh"成为 todo 12 整体 fail 的原因。
  
  **部分失败回滚清单**（review Oracle J 补；`gh repo create` 部分成功后无文档化恢复路径，遗留空仓在 GitHub 上）：
  - 若 `daedalus-core` 已建但 `daedalus-sdk` 失败：用户显式 `gh repo delete Daedalusys/daedalus-core` 后重试；本机不自动删（破坏性 + 用户确认门）
  - 若 3 仓全建但某仓 push 失败：单仓 `git push -f origin main`（先确认 force-push 安全：仓仅本地 main，无他人 push）
  - 若 push 后发现代码错乱：3 仓全 `gh repo delete --confirm` + 重新 todo 12（**用户必介入**：删除有数据丢失风险）
  - 失败恢复路径必须用户在场，worker 不自动回滚（决策点）
  Parallelization: Wave 5 | Blocked by: 4, 7, 8, 9, 11 | Blocks: 13, 14, 15, 18
  References: 主仓根目录结构（rename 前 = `daedalus/{core,plugin,files}/`，rename 后 = `daedalus-core/{core,plugin,files}/`）；3 仓 description 字段参考主仓 README
  Acceptance criteria (agent-executable): `test -d daedalus-core && test -d daedalus-core/cmd && test -d daedalus-core/internal && test -d daedalus-core/files && test -d daedalus-core/plugin/copilot && test -f daedalus-core/go.mod && ! test -d daedalus/core`（物理改名完成，**平铺布局**：daedalus-core 根下是 cmd/internal/files/plugin/copilot 等，**不**嵌套 daedalus-core/core/；review Momus #3 修）；`gh repo view Daedalusys/daedalus-core --json name | jq -r .name` = "daedalus-core"（V3 跑，本机降级为 dry-run）；类比 sdk / plugins
  QA scenarios:
    - happy: 物理改名完成 + 3 仓创建 + branch protection 启用 + 首次 push 成功（V3 跑全链，本机跑"改名 + 准备 push 脚本"）
    - failure: 物理改名残留 `daedalus/core/` → acceptance fail；`gh` CLI 未登录或权限不足 → 报 authentication required → 改用 SSH key 推送 / 退化为 V3 后跑 / 用户介入
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a5/task-12.log`
  Commit: Y (主仓 1 commit 改名 + 3 仓各 1 commit 首推) | `chore(repos): rename daedalus/core to daedalus-core, create Daedalusys/daedalus-{core,sdk,plugins} GitHub repos`

- [x] 13. `go.work` 本地 dev 桥 + `.gitignore` 模式 + 兄弟仓布局守门
  What to do / Must NOT do: 在主仓 (`daedalus-core`) 根目录写 `go.work`：
  ```
  go 1.25.0
  
  use (
      .
      ../daedalus-sdk
      ../daedalus-plugins/fs
      ../daedalus-plugins/shell
      ../daedalus-plugins/pkg
      ../daedalus-plugins/sysinfo
      ../daedalus-plugins/service
      ../daedalus-plugins/blueprint
  )
  ```
  加 `.gitignore` 忽略：`daedalus-sdk/` `daedalus-plugins/`（前提：contributor clone 全 3 仓到同级目录）；保留主仓 git 跟踪的文件层级（`daedalus-core/` 等）。在 `daedalus-core/go.mod` 改 `replace` 指令：`replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk`（相对路径，**注意**：与阶段 1 的 `=> ../daedalus-sdk` 相同；3 仓物理拆后仓平级，`go.work` 优先于 `replace`）；6 个 `daedalus-plugins/<cap>/go.mod` 同款 `replace github.com/Daedalusys/daedalus-sdk => ../../daedalus-sdk`。写 README 段到 `daedalus-core/README.md` "本地开发"段，说明 3 仓 clone 顺序与 go.work 路径约定。**不**把 `go.work` 入主仓 git（`.gitignore` 模式）；**不**改 module 路径（todo 8 已改）。
  
  **也更新 `.gitignore` 制品模式**（review Momus #5 修；原 plan 只增 go.work/sibling 模式，未动既有 `daedalus/core/bin/` 制品模式——todo 12 改名后这些模式不再匹配，新制品 `daedalus-core/bin/` 变成 untracked 风险被 commit）：
  - 改 `.gitignore` 既有行：`daedalus/core/bin/` → `daedalus-core/bin/`（`go build` 产物）
  - 改：`daedalus/plugin/*/bin/` → `daedalus-plugins/*/bin/`（plugin 制品）
  - 改：`daedalus/files/system/...` 模式 → `daedalus-core/files/system/...`（rootfs 镜像树制品）
  - 改：`daedalus/core/cmd/daedalus-blueprint/blueprints/` → `daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/`（blueprint embed 暂存；review Oracle E 补：todo 6 后 `daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/` 也要加 `.gitignore` 行，因为 rsync 会污染）
  - 写脚本 `daedalus-core/scripts/verify-dev-layout.sh`：检查 `../daedalus-sdk` 与 `../daedalus-plugins` 兄弟目录存在 + 各仓含 `go.mod` + 各仓 module 路径匹配 `github.com/Daedalusys/...`；不通过即 exit 1 + 缺哪个写哪个
  - 在 `daedalus-core/justfile` 加 `just verify-dev-layout` recipe 调该脚本
  - 在 `daedalus-core/.github/workflows/test.yml` 加 dev-layout 守门 job（不在 PR 阻断主流程，仅 `continue-on-error: true` + 标 warn）
  - 在 `daedalus-core/README.md` "本地开发"段明文写："**前置**：3 仓以平级目录形态 clone：`mkdir -p ~/work/daedalusys && cd ~/work/daedalusys && git clone <core> && git clone <sdk> && git clone <plugins>`；仓名必须为 `daedalus-core` / `daedalus-sdk` / `daedalus-plugins`（与 `go.work` 路径对应）"
  - **也加 3 个 `go.work.example` 模板**（review Oracle Issue 5 修；原 plan 只在 `daedalus-core` 放 `go.work`（gitignored），单仓 clone 的 plugin 作者拿不到 SDK）：
    - `daedalus-core/go.work.example`：3 仓平级 clone 时把该文件 `cp go.work.example go.work` 即生效（已有 todo 13 描述）
    - **`daedalus-sdk/go.work.example`**（新增）：单仓 clone SDK 时，模板内容为 `use ( . )`（仅 SDK 自己），让 SDK 仓 `go test` 跑通；无需 sibling plugin 仓
    - **`daedalus-plugins/<cap>/go.work.example`**（6 个新增）：单仓 clone 任一 plugin 时，模板内容为：
      ```
      go 1.25.0
      use (
          .
          ../../daedalus-sdk
      )
      ```
      让单 plugin 仓 `go build` 跑通；该文件 `cp` 为 `go.work` 即生效
  - 所有 8 个 `go.work.example` 模板在 3 仓各 PR 中 commit 进 git（与 `go.work` 不同——后者 gitignored）
  Parallelization: Wave 5 | Blocked by: 12 | Blocks: 14, 15
  References: 主仓根 `.gitignore`；`daedalus-core/go.mod` replace 指令；`daedalus-core/README.md`
  Acceptance criteria (agent-executable): `test -f go.work && grep -E "^use" go.work | wc -l` ≥ 1；`grep -E "daedalus-sdk|daedalus-plugins" .gitignore` ≥ 2；`test -f daedalus-core/scripts/verify-dev-layout.sh` + 该脚本可执行 + `bash daedalus-core/scripts/verify-dev-layout.sh` exit 0（前提：3 仓平级 clone）；`grep "兄弟仓\|平级\|siblings" daedalus-core/README.md` ≥ 1；本地 3 仓平级 clone 后 `cd daedalus-core && go build ./...` exit 0
  QA scenarios:
    - happy: contributor clone 3 仓平级（如 `~/work/daedalusys/{daedalus-core,daedalus-sdk,daedalus-plugins}/`）+ 写 `go.work` + 跑 `just verify-dev-layout` exit 0 + `go build` 全过
    - failure: `go.work` 路径错（`../daedalus-sdk` vs `./daedalus-sdk`）→ `go build` 报 cannot find module；或兄弟仓 layout 错（仓改名 / 移动）→ `verify-dev-layout.sh` 报缺哪个 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a5/task-13.log`
  Commit: Y | `chore(dev): add go.work local dev bridge + verify-dev-layout guard for 3-repo layout`

- [x] 14. 3 仓各自 CI 配置（build/test/lint + 漂移测 + 跨仓集成 + SDK release pipeline + cross-repo checkout）
  What to do / Must NOT do: 写 4 个仓的 `.github/workflows/*.yml`（**SDK 仓新增 release pipeline**；review Oracle Issue 2 修——原 plan 漏了 `daedalus-sdk/.github/workflows/release.yml`，第三方无法 `go get github.com/Daedalusys/daedalus-sdk@vX.Y.Z`）：
  - `daedalus-core/.github/workflows/build.yml`：跑 `just go-test` + `deno test --allow-all tests/deno/` + `bash tests/deno/i18n_keys.test.sh` + 触发镜像构建 `just build`（仅在 `daedalus-plugins` 有新 release tag 时）；**第一步必须 `actions/checkout` 把 SDK 仓以 sibling 路径 check out 到 `../daedalus-sdk`（review Oracle Issue 3 修）**：`uses: actions/checkout@v4` + `repository: Daedalusys/daedalus-sdk` + `path: ../daedalus-sdk` + **`ref: ${{ github.event.inputs.sdk_version }}`（review Oracle Issue 7 修——pinned-tag 策略：SDK 版本从 `daedalus-core/go.mod` 的 `require github.com/Daedalusys/daedalus-sdk vX.Y.Z` 读取，CI 用 `sed -n 's/.*daedalus-sdk v\([0-9.]*\).*/\1/p' go.mod` 提取，传给 `ref: v$SDK_VERSION`；不写死字面量占位符）**；否则 `replace github.com/Daedalusys/daedalus-sdk => ../daedalus-sdk` 在 CI 中找不到模块
  - **plugin 下载步骤跨引用（review Oracle Issue 5 修——原 plan 把 plugin zip 下载拆到 todo 15，但 workflow 文件是 todo 14 写的，executor 会漏集成）**：`daedalus-core/.github/workflows/build.yml` 的镜像构建 job **内联** plugin zip 下载步骤（`gh release download --repo Daedalusys/daedalus-plugins --pattern '*.plugin.zip' --dir /tmp/plugins-release/`）→ `daedalus-plugin-pack -verify --keep` 解压到镜像树安装态；todo 15 只写 `fetch-plugins.sh` 脚本与 `just build` 前置调用，不重复描述 workflow 步骤
  - `daedalus-sdk/.github/workflows/test.yml`：跑 `go vet ./...` + `go test ./...`（含 3 点漂移测试 + 跨仓 layout 漂移守门：拒绝 module 路径含 `daedalus-os`）+ **testdata drift 守门（review Oracle Issue 3 落地）**：`diff daedalus-core/files/system/opt/daedalus/shared/policy.toml daedalus-sdk/policy/testdata/policy.toml` 不一致即 fail（注意：SDK 仓独立 CI 没有 `daedalus-core` 兄弟仓，此守门放 core 仓 build.yml 或 plugin 仓——见下）
  - **testdata drift 守门落位（review Oracle Issue 3 修正）**：SDK 仓独立 CI **没有** `daedalus-core` 兄弟仓，`diff` 无法跑；改放 `daedalus-core/.github/workflows/build.yml`（core 仓有 SDK 兄弟 checkout + 生产 policy.toml），加一步 `diff daedalus-core/files/system/opt/daedalus/shared/policy.toml ../daedalus-sdk/policy/testdata/policy.toml` 不一致即 fail
  - `daedalus-sdk/.github/workflows/release.yml`（**新增 review Oracle Issue 2 + Issue 6 修**）：tag-driven（`on: push: tags: ['v*']`）+ `go test ./...` + **`goreleaser`**（review Oracle Issue 6 定版：SLSA L2 + provenance + SBOM 用 `goreleaser` 原生支持，不用 `gh release create` 手工拼）+ 上传 semver 化的 source tarball + `provenance` attestations（SLSA build level 2 目标）
  - `daedalus-plugins/.github/workflows/test.yml`：6 个 sub-module 独立 `go build -trimpath -o bin/ ./cmd/...` + `go test ./...` + i18n 同步校验 + **第一步必须 `actions/checkout` 把 SDK 仓以 sibling 路径 check out 到 `../daedalus-sdk`（review Momus Issue 2 修正：`../../daedalus-sdk` 会解析到 checkout root 上级的兄弟仓本身，不满足 plugin module 的 `replace ... => ../../daedalus-sdk` 相对链；正确的是 `path: ../daedalus-sdk` 指向 workspace 兄弟，`go build` 从 `daedalus-plugins/fs/` 跑时 `../../daedalus-sdk` 才解析到 workspace 的 `daedalus-sdk`）**
  
  跨仓集成：`daedalus-core` CI 在 `daedalus-plugins` 新 release tag 时拉 zip（`gh release download --repo Daedalusys/daedalus-plugins --pattern '*.plugin.zip' --dir /tmp/plugins-release/`）+ 集成到镜像。**不**复刻 `daedalus-plugins` 镜像构建（仅 core 仓跑 `just build`）；**不**用同一个 workflow file 跨仓（每个仓独立 file）。
  Parallelization: Wave 5 | Blocked by: 12, 13 | Blocks: 15, 18
  References: `.github/workflows/build-daedalus.yml`（主仓现有 CI 作基线）；3 仓各自 go.mod；review Oracle Issue 2-3, 5-7；review Momus Issue 2（plugins CI checkout 路径实证）
  Acceptance criteria (agent-executable): 4 个仓各有一个 `.github/workflows/*.yml`（core 2 个：test + build；sdk 2 个：test + release；plugins 1 个：test + release 已在 todo 15）；`act -j test`（本地 act 工具）能 dry-run core 仓的 test job 跑通；或 push 到分支后看到 CI 状态绿（前提：todo 12-13 已完成 = 仓已建可推）；`grep -E "actions/checkout" daedalus-plugins/.github/workflows/test.yml` 的 `path:` 为 `../daedalus-sdk`（不是 `../../daedalus-sdk`，review Momus Issue 2 验收）
  QA scenarios:
    - happy: 4 仓 CI 全部跑通（`just go-test` + `deno test` + 漂移测 + 6 插件 build + SDK release + testdata drift 守门）
    - failure: 任一仓的 test job 失败（漂移测试 fail / deno 缺 / go vet 错 / cross-repo checkout 错 / testdata drift）→ CI 报红 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a5/task-14.log`
  Commit: Y (4 仓各 1 commit) | `ci: per-repo workflow (test/lint/build + cross-repo plugin release fetch + SDK release pipeline)`

- [x] 15. 跨仓 release 流水线（plugins → core CI 拉 zip）+ 本地等价兜底
  What to do / Must NOT do: **CI workflow 配置**（V3 构建机合并后跑；本机无 `gh` 无网络，本 todo 本机降级为"CI workflow 文件就位 + 本地等价全链跑通"，review Issue H 强约束）：
  - `daedalus-plugins/.github/workflows/release.yml`：触发 = 推送 `v*` tag 或手动 dispatch；步骤 = 6 个 sub-module 各跑 `go build -trimpath -o bin/ ./cmd/...` + `daedalus-plugin-pack -in . -out bin/<id>.plugin.zip` + 6 zip 上传到 GitHub release
  - `daedalus-core` CI 拉 zip：镜像构建 job 之前 `gh release download` 或 `curl -L https://github.com/Daedalusys/daedalus-plugins/releases/latest/download/daedalus.<cap>.plugin.zip` 拉 6 个 zip 到 `/tmp/plugins/` + `daedalus-plugin-pack -verify --keep` 解压到镜像树安装态
  
  写脚本 `daedalus-core/scripts/fetch-plugins.sh` 落实上述流程；改 `justfile` 的 `build` recipe 在 `just build` 前调 `fetch-plugins.sh`；**不**改 `plugin-pack` recipe（仍按主仓 source 路径，本地 dev 用）；**不**改 plugin zips 的内容格式（todo 11 已升级 C1 schema，zip 内 manifest 含 api_version/license/maintainer）。
  
  **本地等价兜底**（executor 环境无 `gh` / 无网络，acceptance 不依赖远端）：
  - 在 `daedalus-core/scripts/fetch-plugins.sh` 接受 `--local-zip-dir <dir>` 参数，目录内已有 6 zip 时直接 `daedalus-plugin-pack -verify` 解压，跳过 `gh release download`
  - 写一个本地集成脚本 `daedalus-core/scripts/local-cross-repo-test.sh`：
    1. 跑 `for c in fs shell pkg sysinfo service blueprint; do (cd daedalus-plugins/$c && go build -trimpath -o /tmp/test-zips/bin/daedalus-$c ./cmd/daedalus-$c) && (cd daedalus-plugins/$c && daedalus-plugin-pack -in . -out /tmp/test-zips/bin/daedalus.$c.plugin.zip); done`（本地生成 6 zip）
    2. 调 `daedalus-core/scripts/fetch-plugins.sh --local-zip-dir /tmp/test-zips/bin` 解压到镜像树
    3. 跑 `daedalus-host -dir <dest> list` 期望 6 插件全 ok（含 checksums）
    4. 跑 `daedalus-plugin-pack -verify` 任一 zip 报 `checksum mismatch` 则 exit 1
  - 本任务 acceptance 改为跑 `local-cross-repo-test.sh` exit 0，不依赖 GitHub release 真存在
  - 把 `gh release create` / `curl -L` 推到 GitHub release 的步骤显式标"V3 构建机合并后跑"，本机零网络零 gh 不报失败
  
  **向后兼容与废弃说明**（review Oracle G 补；原 plan 无 deprecation 计划——`github.com/daedalus-os/daedalus/core` 是 breaking change，老消费者无降级路径）：
  - 在 `daedalus-core/go.mod` 加 `retract [v0.1.0, v0.2.0)` 段（如果 v0.1.0 存在）+ 写 v0.2.0 release notes 显式标注 breaking change：模块路径从 `github.com/daedalus-os/daedalus/core` 改为 `github.com/Daedalusys/daedalus-core`，并提供迁移步骤（`go.mod` replace + import 全局 rename + `daedalus-{shell,fs,pkg,sysinfo,service,blueprint}` 路径不变）
  - 老镜像消费者（v0.1.0 release）：在 AGENTS.md / release notes 明文"v0.1.0 不可升级到 v0.2.0，固定在 v0.1.0 + 不动 go.mod"——无中间兼容层
  - **不**写 deprecated stub @ 旧 module path（review Oracle G 提议但否决：维持单一事实源原则，不留占位字符串——C5 已拍板不留占位）
  Parallelization: Wave 6 | Blocked by: 12, 13, 14 | Blocks: 18
  References: `daedalus-core/justfile:46-47`（build recipe）；`daedalus-core/scripts/sync-daedalus.sh`（参照现有 shell 风格）；`daedalus-plugins` 6 个 sub-module go.mod；`daedalus/core/cmd/daedalus-plugin-pack/main.go`（`daedalus-plugin-pack -verify` CLI）
  Acceptance criteria (agent-executable): `daedalus-core/scripts/fetch-plugins.sh --help` exit 0 + 打印 `--local-zip-dir` 选项；`bash daedalus-core/scripts/local-cross-repo-test.sh` exit 0（本地生成 6 zip + fetch 验证 + checksum 校验全过）；CI workflow 文件 `daedalus-plugins/.github/workflows/release.yml` 与 `daedalus-core/.github/workflows/build.yml` 内容就位（V3 跑实际发布）
  QA scenarios:
    - happy: 本机跑 `local-cross-repo-test.sh` 全过（6 zip 本地生成 + 6 zip verify + 6 插件在镜像树安装态）；CI workflow 文件就位待 V3 跑
    - failure: 任一 zip sha256 校验失败 → `daedalus-plugin-pack -verify` 报 `checksum mismatch` → `local-cross-repo-test.sh` exit 1 + 错串
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a6/task-15.log`
  Commit: Y (2 仓各 1 commit) | `ci(release): cross-repo plugin release fetch (zip from daedalus-plugins) + local fallback`

- [x] 16. SDK 仓 5 个占位目录 + README（C3）
  What to do / Must NOT do: 在 `daedalus-sdk` 仓建 5 个空目录：
  - `secretprovider/` + `README.md`（指向 #33 KWallet + #34 systemd-creds 抽象）
  - `memoryprovider/` + `README.md`（指向 #29 持久记忆/知识图谱抽象）
  - `modelprovider/` + `README.md`（指向 #31 提示词缓存 / 模型 provider 抽象）
  - `agentprovider/` + `README.md`（指向未来 agent provider 抽象）
  - `transportprovider/` + `README.md`（指向 MCP stdio/HTTP/A2A 抽象）
  
  每 README 模板：
  ```
  # <Provider 名称> 占位
  
  本目录为 #42 Provider/Slot 抽象的占位（issue #46 C3 拍板）。
  
  ## 状态
  - 当前：空目录 + README，仅声明 slot 名
  - 目标：等 #42 落地时填实 contract 与实现
  
  ## 参考
  - issue #46：[Architecture] daedalus-sdk 抽出 + 内置插件独立仓
  - issue #42：[Architecture] Provider / Slot 架构
  - 相关议题（按需）：#33 / #34 / #29 / #31
  ```
  
  **不**写 Go 代码（`secretprovider.go` 之类不创建）；**不**暴露 import 路径（不在 `daedalus-sdk` 主 package 引用这些空目录）；**不**改 SDK go.mod（占位目录不进 module 依赖）。
  Parallelization: Wave 6 | Blocked by: 12 | Blocks: 18
  References: SDK 仓根目录；#42 Provider/Slot 抽象定义
  Acceptance criteria (agent-executable): `cd daedalus-sdk && for d in secretprovider memoryprovider modelprovider agentprovider transportprovider; do test -f $d/README.md; done`；`for d in secretprovider memoryprovider modelprovider agentprovider transportprovider; do ! ls $d/*.go 2>/dev/null; done`（无 Go 文件）；`cd daedalus-sdk && go test ./...` exit 0（占位不影响 build）
  QA scenarios:
    - happy: 5 占位目录 + 5 README 就位；SDK 仓 `go test ./...` 仍 exit 0
    - failure: 误写了 `secretprovider.go` → `go build` 报 empty package 或 import 循环 → exit 1
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a6/task-16.log`
  Commit: Y | `docs(sdk): add 5 Provider/Slot placeholder dirs (C3)`

- [x] 17. AGENTS.md / VISION.md / 镜像脚本改用 core/sdk/plugin/files 四层
  What to do / Must NOT do: 改主仓根 `AGENTS.md` "OVERVIEW" 段 + "STRUCTURE" 段 + "CODE MAP" 段：
  - "三层结构" → "四层结构（决策 23/24 + 25）"
  - `daedalus/core/` → `daedalus-core/`（注意**目录名也改**：`daedalus/core/` 物理改名为 `daedalus-core/` 以与仓名对齐）
  - `daedalus/plugin/` → `daedalus-core/plugin/`（copilot 留主仓 → 路径 `daedalus-core/plugin/copilot/`）
  - `daedalus/files/` → `daedalus-core/files/`
  - `daedalus-sdk/` `daedalus-plugins/` 已是新仓根目录
  
  改 `VISION.md`：§1-§11 中所有 `daedalus/core/` 引用同步改 `daedalus-core/`；§5 parity map 的"Daedalus 现状"列更新到新 module 路径（`github.com/Daedalusys/daedalus-core` / `daedalus-sdk` / `daedalus-plugins/<cap>`）；§10 路线图 P3+ 行加注释说明 SDK 仓与 plugins 仓的演进位置。
  
  改 `daedalus/files/scripts/*.sh`（70/75/76 等）的注释中 `daedalus/core/internal/` 引用（实际路径不变 = `daedalus-core/internal/` 因为目录物理改名）
  
  改 `daedalus/plugin/README.md`（copilot 之外的 6 插件说明 → 移到 `daedalus-plugins/README.md`，主仓 `daedalus/plugin/README.md` 缩为 copilot + 三层结构说明）
  
  写新 `daedalus-sdk/README.md`（11 个包索引 + Provider/Slot 占位说明）—— **也含威胁模型段**（review Oracle Issue 8 修；SDK 公开面增量需文档化）：
  - "Security surface / threat model" 段：说明 `internal/policy`、`internal/shellpolicy`、`internal/pathguard`、`internal/audit` 迁出后变为顶级 SDK 包，**公开可被 import**；攻击者可绕过 policy 加载直接构造 `policy.Default()` 实例
  - 列出 SDK 仓的补偿控制（**不是** SDK 代码内补偿，是**运行时**侧由 core 仓守住）：
    - `systemd DynamicUser=yes` 隔离进程命名空间
    - `ReadOnlyPaths=/opt/daedalus/shared/policy.toml` 限制运行时写
    - Landlock/seccomp drop-in 限制 syscall
  - 明确"SDK 路线 = 公开 contract，否则 SDK 不可用"的 trade-off 立场（与 scope 段一致）
  - 列下游使用方**必须**通过 systemd drop-in 守住运行时的清单
  
  写新 `daedalus-plugins/README.md`（6 插件索引 + 跨仓 release 流程）
  
  **不**改 §5 parity map 的"故意无"行（决策内容不变）；**不**改 `daedalus/files/system/opt/daedalus/plugins/` 任何内容（安装态）；**不**改 §4 术语表（plugin `type` vs resource `kind` 两轴保持原状）。
  Parallelization: Wave 7 | Blocked by: 4, 7, 8, 9, 11 | Blocks: 18
  References: `AGENTS.md:1-461`（review Oracle F 修：实际 461 行不是 441）；`VISION.md:1-441`；`daedalus/plugin/README.md`；`daedalus/files/scripts/*.sh`
  Acceptance criteria (agent-executable): `grep -rE 'daedalus/core/' AGENTS.md VISION.md daedalus-core/files/scripts/ daedalus-core/plugin/README.md 2>/dev/null | wc -l` 应为 0（旧路径全清）；`grep -rE 'daedalus-core/' AGENTS.md VISION.md | wc -l` ≥ 10（新路径覆盖）；`test -f daedalus-sdk/README.md && test -f daedalus-plugins/README.md`
  QA scenarios:
    - happy: 3 仓 README + AGENTS.md + VISION.md 全部改用四层措辞
    - failure: 旧 `daedalus/core/` 路径残留 → 用户找不到新代码 → 文档阅读报错
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a7/task-17.log`
  Commit: Y (3 仓各 1 commit) | `docs: align AGENTS.md + VISION.md to 4-layer core/sdk/plugin/files model`

- [x] 18. 镜像构建 + 零残留断言（V3 构建机补跑，阶段 7 收口）
  What to do / Must NOT do: 跑 `just build`（V3 构建机，**本机 CPU 不支持**，见 AGENTS.md NOTES todo 18）：
  - podman build 出 `localhost/daedalus-os:latest`
  - 跑 `just verify-image` 断言 `/opt` 内无 `*.py` `*.pyc` `__pycache__` `*.test.ts` `go.mod` `vendor`
  - 跑 `podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-host list` 期望 **7 插件**全 ok（**copilot + 6 能力 = 7**；review Issue F 修 —— 原 plan 写"5 插件"是 stale，`76-daedalus-plugin-gen.sh:53` 的 `CAPS` 已是 6 能力，justfile `plugin-pack` 循环也跑 6，镜像内必然是 7）
  - 跑 `podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl` 期望哈希链验证 pass
  - 跑 `podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-tx --help` 期望 out-of-band CLI 可用
  
  本机等价断言（不退化为零断言）：
  - `find daedalus-core/files/system 2>/dev/null \( -name "*.py" -o -name "*.test.ts" -o -name "__pycache__" -o -name "go.mod" \) | wc -l` == 0
  - `cd daedalus-core && go test ./... && deno test --allow-all tests/deno/ && bash tests/deno/i18n_keys.test.sh` 全 pass
  - `cd daedalus-sdk && go test ./...` 全 pass
  - `for c in fs shell pkg sysinfo service blueprint; do (cd daedalus-plugins/$c && go build -trimpath -o /tmp/daedalus-$c ./cmd/daedalus-$c); done` 全 pass
  - `cd daedalus-core && just plugin-pack 2>&1 | tail -3` 期望打印"plugin-pack: 6 个能力插件 ... 已安装"
  - `bash daedalus-core/scripts/local-cross-repo-test.sh` exit 0（todo 15 落地的本地等价）
  
  **不**在本机做真实镜像构建（CPU 限制）；**不**修改 build recipe（仅跑断言）；**不**触发 CI（CI 在 todo 14 已就位）。
  Parallelization: Wave 7 | Blocked by: 12, 13, 14, 15, 16, 17 | Blocks: -
  References: `daedalus-core/justfile:46-47`（build recipe）；`daedalus-core/justfile:60-63`（verify-image recipe）；AGENTS.md NOTES 段（v3 构建机限制）；`daedalus/files/scripts/76-daedalus-plugin-gen.sh:53`（CAPS = 6 能力）；`daedalus-core/scripts/local-cross-repo-test.sh`（todo 15 落地）
  Acceptance criteria (agent-executable): 本机 6 项断言全 pass（含 7 插件期望 + 跨仓本地集成）；V3 构建机补跑（合并后跑，**本 plan 验收以本机断言为准**）
  QA scenarios:
    - happy: 本机 6 项断言全 pass；V3 构建机合并后跑（不在本 plan 阻塞范围）
    - failure: 任一本机断言 fail（漂移测 / deno 测 / 6 插件 build / i18n 门禁 / 7 插件 list / 跨仓本地集成）→ exit 1 + 具体错串
    - Evidence: `.omo/evidence/ulw/<session>/sdk-extraction-and-plugin-monorepo/<goalId>/a7/task-18.log`
  Commit: N (验证步骤, 不产生新 commit)

## Final verification wave
> Runs in parallel after ALL todos. ALL must APPROVE. Surface results and wait for the user's explicit okay before declaring complete.
- [x] F1. Plan compliance audit
- [x] F2. Code quality review
- [x] F3. Real manual QA
- [x] F4. Scope fidelity

## Commit strategy
- **Wave 1 (SDK 抽离, 4 commits)**: 1=SDK init, 2=11 包迁移, 3=import 重写, 4=76 脚本+漂移
- **Wave 2 (Plugins monorepo, 3 commits)**: 5=monorepo init, 6=6 插件迁, 7=justfile+76+sync
- **Wave 3 (Import rename, 2 commits)**: 8=Go import rename, 9=非 Go 引用 rename
- **Wave 4 (Manifest C1, 2 commits)**: 10=schema, 11=6 manifest 改写
- **Wave 5 (3 仓 + go.work, 5 commits)**: 12=3 仓建 (3 仓各 1), 13=go.work, 14=3 仓 CI (3 仓各 1)
- **Wave 6 (Cross-repo + 占位, 2 commits)**: 15=跨仓 release (2 仓各 1), 16=5 占位
- **Wave 7 (文档 + 终验, 3 commits)**: 17=3 仓 README + AGENTS + VISION, 18=验证步骤 (no commit)

总 commit 数 ≈ 21，主仓分支 `sdk-extraction-and-plugin-monorepo`，3 仓各自分支，PR 序列逐 wave 提。6 个 Wave 各为 1 个 PR 标题（`Wave 1: SDK 抽离` 等），单个 PR 内的 commit 按上述编号。

## Success criteria
- 3 仓（`daedalus-core` / `daedalus-sdk` / `daedalus-plugins`）在 `Daedalusys` org 下已建，主分支含首版代码
- 11 SDK 包从 `daedalus-core/internal/` 完全迁出，旧路径残留为 0（grep 验证）
- 6 Go 能力插件从 `daedalus-core/plugin/` 完全迁出，旧路径残留为 0
- **60 文件 (59 .go + 1 go.mod) + 所有非 Go 引用**全部 rename 到 `github.com/Daedalusys/...`，`daedalus-os/...` 残留为 0
- 5 SDK 占位目录 + 5 README 就位
- 6 插件 manifest 全部按 C1 schema（`api_version` / `license` / `maintainer` / `runtime` 结构化）
- `go.work` 本地 dev 桥就位，contributor 3 仓平级 clone + `go build` 跑通
- 3 仓 CI 各自跑通：core 仓 `just go-test` + `deno test` + i18n 门禁；sdk 仓 `go test ./...`（含 3 点漂移测试）；plugins 仓 6 sub-module 各自 `go build` + `go test`
- 跨仓 release 流水线：plugins 推 tag → release 出 6 zip → core CI 拉 zip → 镜像构建用新 zip
- 镜像零残留断言保留（V3 构建机补跑）
- 全部 18 todos 跑过，4 final verification (F1-F4) 全 APPROVE
- 用户在 worker 跑完后给 "approved" 才能宣告 plan 完成
