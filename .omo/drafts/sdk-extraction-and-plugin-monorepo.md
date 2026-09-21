---
slug: sdk-extraction-and-plugin-monorepo
status: plan_finalized
intent: clear
review_required: true
plan_path: .omo/plans/sdk-extraction-and-plugin-monorepo.md
plan_sha256: 0be71e9a7f3d03e006e2711496e921dedb538fc8a0c93f6a2141731b1f84afad
review_round_id: rr-20260920-05
pending-action: plan finalized (user terminated review loop)
approach: 两阶段合并为单 PR 系列 = 阶段 1 在同仓内做物理布局（SDK 抽离 + 6 插件迁 monorepo），阶段 2 拆 3 仓。Go 跨仓开发用 go.work 桥，发布用 GitHub release zip（C2 拍板），5 个空目录占位（C3 拍板），Import 路径立刻对齐实际（C5），manifest 字段 C1 升级已决。
review:
  momus:
    status: approved
    workspace_root: /var/lofibass_ssd/code/Daedalus/Daedalusys
    runtime_home: /home/lofibass/.config/opencode
    target: .omo/plans/sdk-extraction-and-plugin-monorepo.md
    round_id: rr-20260920-03
    plan_sha256: 6cd18112b72b39be77203b6809a39e19caf96ee97235c25c16e61a604fdff973
    launch_id: launch-momus-20260920-03
    session: ses_f42166787ffebdLPeY8fd76IvW
    background_task_id: bg_61399cdd
    result: OKAY (round 3, sha 6cd18112)
  independent:
    status: terminated_by_user
    workspace_root: /var/lofibass_ssd/code/Daedalus/Daedalusys
    runtime_home: /home/lofibass/.config/opencode
    target: .omo/plans/sdk-extraction-and-plugin-monorepo.md
    round_id: rr-20260920-05
    plan_sha256: 0be71e9a7f3d03e006e2711496e921dedb538fc8a0c93f6a2141731b1f84afad
    launch_id: launch-oracle-20260920-05
    session: ses_f41d96814ffeaf2rzNnsxGk5Bi
    background_task_id: bg_f542607b
    result: cancelled by user "不用等oracle了，直接开始写计划" (2026-09-20)
---

# Round 1 review summary (rr-20260920-01)

Both reviewers (momus + oracle) returned CHANGES_REQUESTED with 5+6 issues respectively. Resolved 8 fixes (overlap on issues 1, 2, 4):

| # | Reviewer | Issue | Fix |
|---|----------|-------|-----|
| 1 | momus + oracle | SDK cross-imports (7 files) not handled | Expanded todo 3 to include 7 SDK-internal imports + updated acceptance grep |
| 2 | oracle | `blueprint-embed` destination path wrong | Fixed todo 7: target = `daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/` |
| 3 | oracle | Copilot Deno dev paths broken (13 strings in 4 .ts files) | Expanded todo 9 to include 4 copilot files + 14 string replacements |
| 4 | momus + oracle | "49 files" count wrong (actual 60) | Updated all 5 occurrences: 49 → 60 (59 .go + 1 go.mod) |
| 5 | momus | todo 2/3 import scope overlap | Resolved via todo 3 expansion (Issue 1) |
| 6 | momus | todo 15 lacks executor fallback (no gh/network) | Added `local-cross-repo-test.sh` + `--local-zip-dir` flag to `fetch-plugins.sh` |
| 7 | momus | todo 12 vs 17 directory rename conflict | Moved physical rename to todo 12; todo 17 only docs |
| 8 | oracle | "5 插件" assertion wrong (actual 7) | Fixed todo 18: 5 → 7 plugins (copilot + 6 caps) |
| 9 | oracle | go.work + replace paths assume fixed sibling layout | Added `verify-dev-layout.sh` + CI guard + README section |
| 10 | (consistency) | Plan's execution strategy + dependency matrix | Updated to reflect new file counts + new verify-dev-layout step |

## Round 2 review summary (rr-20260920-02)

Both reviewers (momus + oracle) returned CHANGES_REQUESTED again with 5+10 issues respectively. Resolved all 15 in round 3:

| # | Reviewer | Issue | Fix location in plan |
|---|----------|-------|----------------------|
| 1 | momus | Plugin build commands use `go build .` (should be `./cmd/daedalus-$c`) | todo 8, todo 15, todo 18, verification table (line 77) all use `./cmd/daedalus-$c` |
| 2 | momus | `tests/deno/{shellpolicy_contract,exec}.test.ts` hardcoded paths break Deno test in every wave | todo 9 expanded: 5 Deno test files + 6 hardcoded path updates |
| 3 | momus | todo 12 nested `daedalus-core/core/` contradicts `git mv` semantics (mv flattens, no nested) | todo 12: layout = `daedalus-core/{cmd,go.mod,go.sum,internal,Makefile,plugin,files}/` (no nested core/); acceptance = `test -d daedalus-core/cmd` not `core` |
| 4 | momus | todo 11 references phantom `daedalus-sdk/bin/daedalus-plugin-pack` | todo 11: `daedalus/core/bin/daedalus-plugin-pack` (real path, Wave 4 before rename) |
| 5 | momus | `.gitignore` artifact patterns stale after rename | todo 13: 4 lines updated (`daedalus/core/bin/` → `daedalus-core/bin/` etc.) + new line for plugins repo blueprint embed |
| 6 | oracle | `version/version.go` wrong path (not under `internal/`) | todo 3 + todo 8: path corrected to `daedalus/core/internal/version/version.go:13` |
| 7 | oracle | Acceptance regex `daedalus-os` too broad (matches image tag) | todo 9: regex narrowed to `github.com/daedalus-os`; image tag preserved |
| 8 | oracle | `scripts/plugin-i18n-sync.sh` hardcoded paths | todo 9: 3 paths updated (line 58, 63, 69) |
| 9 | oracle | `scripts/justfile.demo` hardcoded dev paths | todo 9: 4 paths updated (line 24, 27, 67, 68) |
| 10 | oracle | `blueprints/` gitignore not migrated to plugins repo | todo 7: new line in plugins repo `.gitignore` |
| 11 | oracle | Counting errors (51 vs 38, 7 vs 11, 13+1 vs 16, 441 vs 461) | todo 3 corrected, todo 8 corrected, todo 17 references updated (AGENTS.md 1-461, VISION.md 1-441 legitimate) |
| 12 | oracle | No backwards-compat / deprecation plan | todo 15: `retract [v0.1.0, v0.2.0)` + v0.2.0 release notes; no deprecated stub (C5 原则) |
| 13 | oracle | Security surface expansion undocumented (internal/ → top-level SDK packages) | scope: trade-off 显式承认; threat model 增量 documented |
| 14 | oracle | Redundant import-rename step in todo 6 | todo 6: step removed (no-op given todo 3 already did it) |
| 15 | oracle | No failure-recovery for partial `gh repo create` | todo 12: rollback checklist with 3 scenarios; user confirmation gate |

**Plan size**: 551 lines / 66,106 bytes (was 514 / 57,854 in round 2; +37 lines = 15 fixes' content)

# Draft: sdk-extraction-and-plugin-monorepo

## Components (topology ledger)
| id | outcome (one line) | status | evidence path |
|----|--------------------|--------|---------------|
| SDK-1 | daedalus-sdk 仓建立 + 11 个安全核心包从 core/internal/ 迁出 + 独立 go.mod | active | daedalus/core/go.mod:1, daedalus/core/internal/{audit,blueprint,i18n,objectmodel,pathguard,pkgquery,plugin,policy,shellpolicy,sysinfo,version} |
| Plugins-1 | daedalus-plugins monorepo 建立 + 6 个 Go 能力插件迁出 + 各独立 go.mod | active | daedalus/core/cmd/daedalus-{fs,shell,pkg,sysinfo,service,blueprint}/, daedalus/plugin/{fs,shell,pkg,sysinfo,service,blueprint}/ |
| Core-1 | 主仓收缩为 image 编排 + 5 个 core runtime (host/audit/tx/smoke/plugin-pack) + copilot | active | daedalus/core/cmd/daedalus-{host,audit,tx,smoke,plugin-pack}/, daedalus/plugin/copilot/ |
| Cross-1 | go.work 本地 dev 桥 + 跨仓 release 流水线 (zip) | active | B7a/B7b 默认, C2 拍板 |
| Docs-1 | AGENTS.md / VISION.md / 镜像脚本改用 core/sdk/plugin/files 四层 | active | AGENTS.md:1, VISION.md:1, daedalus/files/scripts/*.sh |
| Schema-1 | manifest 字段升级 C1: runtime 改结构化对象 + api_version/license/maintainer 三字段 | active | daedalus/core/internal/plugin/manifest.go, daedalus/plugin/*/daedalus.plugin.json |
| Empty-1 | SDK 仓预留 5 个空目录 (secretprovider/memoryprovider/modelprovider/agentprovider/transportprovider) | active | C3 拍板, #42 Provider/Slot 抽象对齐 |

## Open assumptions (announced defaults)
| assumption | adopted default | rationale | reversible? |
|------------|-----------------|-----------|-------------|
| copilot 插件(daedalus.copilot)位置 | **留主仓** (issue #46 未列入 6 迁插件列表) | Deno runtime 与 Go 插件形态不同;issue 明确 6 个 Go 插件;等 #41 拍板后再决定 | yes (后续 plan 可迁) |
| 3 仓命名 | `daedalus-core` / `daedalus-sdk` / `daedalus-plugins` | 与 issue 表述 + 用户"core, plugin, sdk"对齐 | yes (rename) |
| Go module 路径 | `github.com/Daedalusys/daedalus-core` / `github.com/Daedalusys/daedalus-sdk` / `github.com/Daedalusys/daedalus-plugins/<cap>` | 用户 C5 "一切以实际为准", 与实际 remote `git@github.com:Daedalusys/Daedalusys.git` 一致 | no (一旦发布难改) |
| GitHub org | **不重命名**, 沿用 `Daedalusys` | issue C5 倾向: 短期改代码, 远期再讨论 org rename | yes (org rename) |
| Plugins 形态 | **monorepo** (6 子目录, 各独立 go.mod), 非 6 独立仓 | issue 默认: 单一 repo 维护更易, release zip 统一 | no (release zip 已固定) |
| 跨仓 release 载体 | **GitHub release zip** (B7b + C2 一致) | 与 C2 拍板一致, 阶段 1+2 统一 | no (已 C2 拍板) |
| 5 个 SDK 占位目录 | **空目录 + README.md**, 不带 Go 代码, 不暴露 import | 等 #42 落地时填实 provider, 占位即 shape, 不预写实现 | yes (易改/易删) |
| i18n / deno 测试 / 镜像脚本 | **保留在主仓** (daedalus-core), 不随拆 | copilot 留主仓, 测试跟着 copilot; 镜像脚本本就是主仓产物 | yes (随 copilot 走) |
| 6 插件 source 形态 | **整体迁出** (manifest + bin/ + i18n) | 安装态是构建产物, source = manifest + bin/, 重新跑 `just plugin-pack` 即重生成 | no (release 已发) |
| 阶段 1+2 合并 | **单 PR 系列** (6 PR, 一波一个), 不留中间态 | 中间态 = 漂移风险, 一波到 3 仓 | no (commit 历史固定) |
| V3 构建机 todo 18 镜像断言 | **合并后跑** (本机 CPU 不支持, 见 AGENTS.md NOTES) | 已知限制, 不在本期阻塞 | yes (后续跑) |
| `blueprints/` 数据 | **保留 plugin/blueprint 形态** (//go:embed 复制到 core 仓同位置) | 已是构建期复制, 迁仓后从 daedalus-plugins 拉到 core 仓, 同形态 | yes (易改) |

## Findings (cited - path:lines)
- **F1** `daedalus/core/go.mod:1` 当前 module = `github.com/daedalus-os/daedalus/core`, go 1.25.0, 依赖 `modelcontextprotocol/go-sdk v1.7.0` + `BurntSushi/toml v1.6.0`
- **F2** `daedalus/core/internal/` 15 个子目录, 11 个目标迁 SDK: `audit/`, `blueprint/`, `i18n/`, `objectmodel/`, `pathguard/`, `pkgquery/`, `plugin/`, `policy/`, `shellpolicy/`, `sysinfo/`, `version/`; **留在 core (4 个)**: `controller/` (决策 25 契约缝), `dirs/` (被 daedalus-host 专用), `state/` (被 daedalus-service 专用), `tx/` (被 daedalus-tx 专用)
- **F3** `daedalus/core/cmd/` 11 个二进制: **迁 plugins (6)**: daedalus-{fs,shell,pkg,sysinfo,service,blueprint}; **留 core (5)**: daedalus-{host,audit,tx,smoke,plugin-pack}
- **F4** `daedalus/plugin/` 7 个子目录: **迁 monorepo (6)**: fs/shell/pkg/sysinfo/service/blueprint; **留主仓 (1)**: copilot
- **F5** grep 计数: 49 个 Go 文件 import `github.com/daedalus-os/daedalus/core/internal/...`; 49 个 Go 文件含 `daedalus-os/daedalus` 字符串
- **F6** `daedalus/files/system/opt/daedalus/shared/policy.toml:1-80` 5 节: `[shell]/[fs]/[audit]/[objectmodel]/[blueprints]`
- **F7** `daedalus/files/scripts/76-daedalus-plugin-gen.sh:1-246` 246 行, 7 阶段校验 (完整性/ExecStart 渲染/tools 核对/沙箱语义/幂等/策略消费/可读性)
- **F8** `justfile:65-141` `plugin-pack` recipe: 6 能力循环 (fs/shell/pkg/sysinfo/service/blueprint) + /usr/local/bin 装 host/audit/shell/service/tx
- **F9** `daedalus/plugin/copilot/daedalus.plugin.json:6` `runtime: "deno"` (字符串, 需 C1 升级为结构化对象)
- **F10** `daedalus/plugin/service/daedalus.plugin.json:9-11` 唯一在源码侧声明 `resources` 的官方清单: `[{ "kind": "service", "name": "*" }]`
- **F11** `daedalus/plugin/blueprint/daedalus.plugin.json:8` 6 工具: `blueprint_list/inspect/render/apply/status/remove`; `blueprints/` 6 蓝图数据已 `//go:embed` 进二进制
- **F12** `daedalus/core/internal/objectmodel/objectmodel.go` 7 类 Kind 封闭枚举 (service/package/container/capability/task/transaction/policy), v1 仅 service 有 provider
- **F13** `daedalus/core/internal/policy/policy.go:164` `LoadOrDefault()` 现状: `ErrNotFound` → `Default()`; 仅"损坏/含未知键"才拒绝启动 (与 #45 漂移项 2 关联)
- **F14** `scripts/sync-daedalus.sh:1-200` sync 脚本 2 leg: systemd 腿 targeted stale-delete (只动 `daedalus-*`), plugin 腿 `--delete --delete-excluded` (vendor 镜像 source)
- **F15** `daedalus/core/internal/plugin/manifest.go` 单一事实源: manifest schema 校验, 当前字段: `id/name/version/type/runtime/executable/entrypoint/permissions/tools/resources/i18n/checksums`

## Decisions (with rationale)
- **D1 (C2, 用户拍板)**: 阶段 1+2 = GitHub release zip 分发; 远期自研轻量化 store 与 Control Center 同位 (留作 #6 Blueprint UI 演化). 理由: zip = 已有工具链零成本; OCI 提前一倍工时.
- **D2 (C3, 用户拍板)**: SDK 仓预留 5 个空目录 (secretprovider/memoryprovider/modelprovider/agentprovider/transportprovider), 各带 README 占位文档. 理由: 与 #42 Provider/Slot 抽象对齐, 先占位, 等 #42 实现时填实.
- **D3 (C4, 用户拍板)**: AGENTS.md / VISION.md / 引用文档改用 core/sdk/plugin/files 四层; 不沿用 #41 Base/Slot/App Store 抽象措辞. 理由: 四层 = 物理事实, Base/Slot/App Store = 抽象模型; 两套不互斥但表述分裂会让 AGENTS.md 漂移.
- **D4 (C5, 用户拍板)**: import 路径立刻改 = `github.com/Daedalusys/daedalus-{core,sdk,plugins}`, 不留 `daedalus-os/...` 占位. 理由: 一切以实际为准, 占位字符串 = 待坏债务; org rename 与 import rename 是两件独立事.
- **D5 (B3, issue 默认, 7 天未反对)**: `daedalus-service` 插件加入本计划 (不 carve out). 理由: 已是 6 能力之一, 迁出时机与 fs/shell/pkg/sysinfo/blueprint 完全同构, 无独立技术债.
- **D6 (B7a, issue 默认, 7 天未反对)**: `go.work` 用作本地 dev 桥, `.gitignore` 模式. 理由: 单仓时代 go.work 不需要, 跨仓后 contributor clone 全 3 仓需 go.work 桥接; `.gitignore` 模式避免污染每个仓.
- **D7 (B7b, issue 默认, 7 天未反对)**: 跨仓 release 走 GitHub release zip. 理由: 与 C2 一致, 统一发布载体.
- **D8 (C1, issue 已决 2026-09-17)**: manifest 字段升级: `runtime` 从 string 改结构化对象 + `api_version` / `license` / `maintainer` 三字段. 理由: 协议演进, deno/native 二元化是 v1 现状, v1.5+ 计划加 controller 等; license/maintainer 为打包元数据必经字段.

## Scope IN
- 3 仓物理拆 (`daedalus-core` / `daedalus-sdk` / `daedalus-plugins`)
- 11 SDK 包从 `daedalus/core/internal/` 迁 `daedalus-sdk/`, 独立 go.mod
- 6 Go 能力插件从 `daedalus/plugin/` 迁 `daedalus-plugins/<cap>/`, 各独立 go.mod
- 全部 import 路径 rename: `github.com/daedalus-os/...` → `github.com/Daedalusys/...`
- 5 个 SDK 占位目录 + README
- manifest schema C1 升级 + 6 插件 manifest 改写
- `go.work` 本地 dev 桥
- 跨仓 release 流水线 (zip)
- AGENTS.md / VISION.md / 镜像构建脚本 (`76-daedalus-plugin-gen.sh` + `*-*.sh`) 改用 core/sdk/plugin/files 四层措辞
- `justfile` / `scripts/sync-daedalus.sh` 指向新路径
- 3 仓各自 CI: core 跑 `just build` 镜像构建, sdk 跑 go vet + go test + 漂移测试, plugins 跑 6 sub-module 独立 build
- 3 点漂移测试保留: policy.toml ↔ `policy.Default()` ↔ objectmodel 常量
- 镜像零残留断言保留

## Scope OUT (Must NOT have)
- 不实现 5 个 Provider/Slot 的实际 provider (仅占位)
- 不引入 OCI App Store / 任何 registry
- 不重命名 GitHub org (`Daedalusys` 保留)
- 不动 copilot 插件 (`daedalus.copilot` 留主仓, 仍是 `daedalus-plugin-copilot` 包)
- 不实现自研 store / Control Center
- 不分离 plugin/blueprint 的 `blueprints/` 数据 (已 embed 进二进制, 保留原状)
- 不动 v3 构建机 todo 18 镜像端到端断言 (合并后跑, 已知 CPU 限制)
- 不实现 plugin runtime hot swap / 运行时联网安装 (留 P4)
- 不做 Python / Deno 能力服务器恢复 (Go 唯一实现, ANTI-PATTERNS 守门)
- 不实现新的 Provider 替换 (留 #42 + #33/#34)
- 不重写 76 脚本逻辑 (仅改路径引用, 不改 7 阶段校验)

## Open questions
(空 - 用户已对 4 决策 (C2/C3/C4/C5) + 4 默认 (B3/B7a/B7b/C1) + 11 次级默认全部表态)

## Approval gate
status: approved
approved_by: loficore
approved_at: 2026-09-20
approval_message: "都OK,进 plan 写完整文件"

## Review state
status: review_in_flight
phase: review_round_initialized
plan_path: .omo/plans/sdk-extraction-and-plugin-monorepo.md
plan_sha256: 0be71e9a7f3d03e006e2711496e921dedb538fc8a0c93f6a2141731b1f84afad
review_round_id: rr-20260920-05
momus_launch_id: launch-momus-20260920-05
oracle_launch_id: launch-oracle-20260920-05
plan_line_count: 651
plan_byte_count: 82845
workspace_root: /var/lofibass_ssd/code/Daedalus/Daedalusys
runtime_home: /home/lofibass/.config/opencode
pending-action: review .omo/plans/sdk-extraction-and-plugin-monorepo.md
next-event: momus terminal verdict (in_flight → approved | changes_requested | inconclusive) AND oracle terminal verdict
prev_round: rr-20260920-04 (BOTH CHANGES_REQUESTED; momus 3 issues + oracle 9 issues = 12; all fixed in round 5)
prev_round_summary: |
  Round 4 → 5 fix count: 12 issues fixed (3 momus + 9 oracle)
  - Momus Issue 1 (Deno path depth): todo 9 now distinguishes 3 path mechanisms — A) ES module import `../../daedalus/plugin/copilot/` → `../../plugin/copilot/` (copilot in core repo); B) URL-relative reads → `../../../daedalus-sdk/` for SDK sibling + relative-to-repoRoot for core files; C) CWD-relative statSync → single-segment `daedalus-core/bin/` (momus empirically verified with Deno 2.9.5)
  - Momus Issue 2 (plugins CI checkout): todo 14 fixed `../../daedalus-sdk` → `../daedalus-sdk`
  - Momus Issue 3 (DevRelPaths ordering): todo 4 fixed — testdata actually hits first (CWD = package dir); cross-repo candidate only post-split; full ResolvePath loop body shown
  - Oracle Issue 1 (Deno import coverage): todo 9 now enumerates ALL 10 test files + 21 import sites + 3 path mechanism categories
  - Oracle Issue 2 (shellpolicy_contract extra lines): todo 9 adds lines 24 + 97
  - Oracle Issue 3 (testdata drift): todo 4 adds sync-policy-testdata.sh + CI diff gate (moved to core build.yml since SDK standalone CI has no sibling)
  - Oracle Issue 4 (manifest_test.go compile): todo 10 now migrates validManifest() + Runtime struct in ~10 existing test cases + explicit UnmarshalJSON
  - Oracle Issue 5 (plugin download step split): todo 14 now inlines the plugin download step in core build.yml workflow
  - Oracle Issue 6 (goreleaser vs gh): todo 14 picks goreleaser (SLSA L2 + provenance + SBOM)
  - Oracle Issue 7 (pinned-tag): todo 14 reads SDK version from go.mod require + passes to ref
  - Oracle Issue 8 (ResolvePath loop body): todo 4 shows full nested loop body
  - Oracle Issue 9 (narrative vs code order): todo 4 corrects narrative to match code order

  Total fixed across 4 review rounds: 44 issues
  Plan size: 651 lines / 82,845 bytes
