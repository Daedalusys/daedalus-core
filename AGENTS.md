# Daedalus: AI-Native Operating System Knowledge Base

**Generated:** 2026-09-21
**Repo:** `github.com/Daedalusys/daedalus-core` (Go module root + image build)
**Siblings:** `../daedalus-sdk/` (11 安全核心包)、`../daedalus-plugins/` (6 Go 能力插件 monorepo)

## OVERVIEW
Immutable, atomic, AI-native desktop OS on AlmaLinux Bootc (KDE variant). Adds a Model Context Protocol (MCP) capability-middleware layer with three security boundaries (model/capability, enforcement/sandboxing, evidence/verification), tamper-evident audit logging, systemd credential isolation, and atomic rollback.

**本仓职责**: 镜像编排 (`Containerfile` + `files/` 构建树) + 5 个 core runtime 二进制 (`cmd/daedalus-{host,audit,tx,smoke,plugin-pack}`) + copilot 插件源码 (`plugin/copilot/`) + 契约包 (`internal/{controller,tx}`)。**SDK 与 6 个 Go 能力插件已迁出独立仓**(见兄弟仓)。

> 愿景与设计层总览见 [VISION.md](VISION.md);本文件是操作知识库,两层互补不重叠。

## STRUCTURE
```
Daedalusys/                    # 本仓 = daedalus-core (镜像编排 + 5 个 core runtime + copilot 源码)
├── Containerfile              # ACTIVE root build recipe (2-stage, bootc lint; COPY 白名单 = base_image/files/{system,scripts} + *.pub)
├── justfile                   # Unified orchestrator (sync/build/test/go-build/go-test/plugin-pack/copilot-plugin/verify-image/iso/qemu)
├── go.work                    # 三仓平级 dev 桥 (use .  ../daedalus-sdk  ../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint,dupe})
├── scripts/                   # 仓库级辅助脚本 (镜像外)
│   ├── sync-daedalus.sh           # Syncs tracked files/ source tree into base_image vendor tree
│   ├── pack-copilot-plugin.sh     # Copilot Pack→Verify 安装态生成
│   ├── verify-dev-layout.sh       # 三仓平级布局守门 (兄弟仓就位 + go.mod module 路径匹配)
│   ├── plugin-i18n-sync.sh        # 双向校验 i18n 声明 ↔ locale 文件
│   ├── fetch-plugins.sh           # 三仓平级 clone 后校验兄弟仓
│   └── justfile.demo              # demo/dev recipe (go-build-demo/dev-copilot),经 justfile `import` 合入
├── cmd/                       # ★ 5 个 core runtime 二进制入口
│   ├── daedalus-host/          #   插件 list/inspect/verify/run-plugin/render-unit (决策 16:非父进程,零 spawn)
│   ├── daedalus-audit/         #   哈希链审计 CLI (--identity/--tool/--args/--outcome/--log-path, verify 子命令)
│   ├── daedalus-tx/            #   事务通道 CLI (begin→propose→apply→rollback; service/package 适配器)
│   ├── daedalus-smoke/         #   镜像内端到端 smoke (v3 构建机补跑; todo 18)
│   └── daedalus-plugin-pack/   #   zip 打包器 (checksums 注入 + manifest 规范化自摘要 + zip-slip 防线)
├── internal/                  # ★ 契约包 (核心 runtime 共享;其余安全包已迁 daedalus-sdk)
│   ├── controller/             #   controller 共享包 (决策 25 契约缝锁定)
│   └── tx/                     #   tx 共享包 (D-1 裁决的 ctx 侧路透传;适配器注册表)
├── plugin/                    # ★ 官方预置插件层 (源码侧; 仅 copilot 留本仓)
│   ├── copilot/                #   Deno 源码 + daedalus.plugin.json (命令顾问 CLI)
│   └── fs/ shell/ pkg/ sysinfo/ service/ blueprint/ dupe/   # ★ 迁移期残留占位 (主源在 ../daedalus-plugins/<cap>/)
├── daedalus-plugins/          # ★ 迁移期残留: go.work + blueprints 复制产物 (//go:embed 用;主源在 ../daedalus-plugins/blueprint/)
├── files/                     # 镜像构建层: rootfs 落位 (构建产物,非源码)
│   ├── scripts/                # Daedalus build steps (60-ai-middleware, 65-ai-safety, 70-daedalus-mcp-servers, 75-daedalus-copilot, 76-daedalus-plugin-gen)
│   └── system/                 # Rootfs-mirror overlay
│       ├── opt/daedalus/plugins/      # 插件安装态 (构建产物: daedalus.copilot + 6 能力,含 checksums manifest;勿手改)
│       ├── opt/daedalus/shared/policy.toml  # 安全策略单一事实源 (shell/fs/audit/objectmodel/blueprints 节)
│       ├── usr/lib/systemd/system/   # daedalus-*.service + .service.d drop-ins (landlock/credentials)
│       ├── usr/local/bin/            # daedalus CLI wrapper + daedalus-{host,audit,shell} 二进制
│       └── etc/credstore/            # systemd LoadCredential placeholders
├── examples/                  # mcp_client_config.json (4 Go 能力服务器条目)
├── tests/deno/                # 镜像外 Deno 测试: copilot 5 组 .test.ts + shellpolicy_contract.test.ts (Go↔Deno 跨语言契约)
├── base_image/                # VENDORED AlmaLinux/atomic-desktop fork (gitignored, own .git) — sync-daedalus.sh 落位目标
└── .github/workflows/         # build-daedalus.yml (runs `just build` from repo root + go-test/test 门 + 镜像零残留断言)
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Go MCP 能力服务器实现 | `../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint,dupe}/cmd/` | go-sdk stdio 服务器;唯一实现 (Python/Deno 双实现已删除) |
| 主机运行时共享 SDK 包 | `../daedalus-sdk/{pathguard,shellpolicy,pkgquery,sysinfo,policy,audit,plugin,objectmodel,i18n,blueprint,version,state,dirs}/` | 公开 contract 仓;威胁面见 `../daedalus-sdk/AGENTS.md` |
| Modify Daedalus Copilot CLI | `plugin/copilot/` (源码) + `files/system/opt/daedalus/plugins/daedalus.copilot/` (镜像安装态) | policy, audit, llm, exec, main orchestration;安装态是构建产物勿手改 |
| core runtime 二进制 | `cmd/daedalus-{host,audit,tx,smoke,plugin-pack}/` | 宿主 / 审计 / 事务 / smoke / 打包器 |
| 契约包(仅 host/tx 共享) | `internal/{controller,tx}/` | 决策 25 契约缝锁定;其余安全包已迁 SDK |
| 插件格式 / 宿主 | `cmd/daedalus-{host,plugin-pack}/` + `../daedalus-sdk/plugin/` + `../daedalus-plugins/<cap>/daedalus.plugin.json` | manifest schema + zip 打包 + sha256 + zip-slip 防护;宿主仅安装/发现/校验(非父进程) |
| 安全白名单 / 策略 | `files/system/opt/daedalus/shared/policy.toml` + `../daedalus-sdk/policy/` | 单一事实源,Go 运行时读取;ALLOW_COMMANDS env 整体 REPLACE;损坏或缺失均 fail-closed 拒启(回退 Default 需 development opt-in) |
| Copilot 侧冻结副本 | `plugin/copilot/policy.ts` | 与 `../daedalus-sdk/shellpolicy` 同步义务;契约由 `tests/deno/shellpolicy_contract.test.ts` 钉 |
| Audit hash chain | `../daedalus-sdk/audit/` + `cmd/daedalus-audit/` | genesis `0`*64, syscall.Flock, sha256 链, Python 金样字节级兼容 (`../daedalus-sdk/audit/testdata/golden.jsonl`) |
| 事务通道(service/package) | `cmd/daedalus-tx/` + `internal/tx/` | begin→propose→apply→rollback;service/package 适配器;euid 守门;sidecar 落盘必捕获 |
| 期望视图投影(sdk#4) | `internal/desiredview/` + `docs/desired-state-projection.md` | journal→Current Desired View 纯函数全量 replay;applied 唯一提交点;封闭映射 fail-closed;`[ownership]` 归 P4 落 policy |
| Add systemd sandbox rule | `files/system/usr/lib/systemd/system/daedalus-*.service.d/*.conf` | landlock.conf = seccomp/network;credentials.conf = LoadCredential |
| Reorder / add image build step | `files/scripts/NN-name.sh` | 60-ai-middleware, 65-ai-safety, 70-daedalus-mcp-servers, 75-daedalus-copilot, 76-daedalus-plugin-gen |
| Add a new plugin/server | `../daedalus-plugins/<id>/` (manifest + bin) → `just plugin-pack` → `76-daedalus-plugin-gen.sh` 渲染 systemd ExecStart | 宿主 `run-plugin`/`render-unit` 消费 manifest; sync via `just sync` |
| 蓝图数据(参数化模板) | `../daedalus-plugins/blueprint/blueprints/<id>/` | 6 蓝图 × 6 文件;`just blueprint-embed` rsync 到 `cmd/daedalus-blueprint/blueprints/` 供 `//go:embed` |
| CI / image build | `just build` / `.github/workflows/build-daedalus.yml` | repo root context, runs `just build` (sync + podman build) + `just go-test`/`just test` + 零残留断言 |

## CODE MAP
CodeGraph indexes `tests/` and tracked root files (`base_image/` is gitignored). Source paths under `cmd/` `internal/` `plugin/` `files/` (本仓), `../daedalus-sdk/` (SDK 包) 与 `../daedalus-plugins/` (6 能力插件) are authoritative.

| Symbol / Component | Type | Location | Role |
|--------------------|------|----------|------|
| `daedalus` CLI | wrapper script | `files/system/usr/local/bin/daedalus` | 询问宿主构造启动命令 (不自己声明 Deno 权限), `$HOME` 占位展开后 exec |
| `daedalus-host` | Go binary | `cmd/daedalus-host/` (镜像态 `usr/local/bin/daedalus-host`) | 插件 list/inspect/verify/run-plugin/render-unit;**非父进程,零 spawn**;`paths.go` / `paths_demo.go` build-tag 互斥(dev 路径重写仅 demo 编译期常驻) |
| `daedalus-audit` | Go binary | `cmd/daedalus-audit/` (镜像态 `usr/local/bin/`) | 哈希链审计 CLI;所有审计写入唯一入口 |
| `daedalus-tx` | Go binary | `cmd/daedalus-tx/` | 事务 CLI;`begin`→`propose`→`apply`→`rollback`;service/package 适配器;user-scope-only (service) / root-only (package) euid 守门 |
| `daedalus-smoke` | Go binary | `cmd/daedalus-smoke/` | 镜像内端到端 smoke;v3 构建机补跑 |
| `daedalus-plugin-pack` | Go binary | `cmd/daedalus-plugin-pack/` | zip 打包器;checksums 注入 + manifest 规范化自摘要 + zip-slip 防线 |
| `controller` | Go pkg | `internal/controller/` | 决策 25 契约缝锁定包;host/tx 共享 |
| `tx` | Go pkg | `internal/tx/` | 事务通道共享包;`tx.Step` 六键线上契约;D-1 裁决 ctx 侧路透传;`tx.List()` 回放序(created_at↑,id↑) |
| `desiredview` | Go pkg | `internal/desiredview/` | 期望视图投影(纯函数 replay,零副作用);`Entry.source_tx` 为 P4 generation 前置 |
| `plugin` | Go pkg | `../daedalus-sdk/plugin/` | manifest schema 校验 + Pack/Extract/Verify/VerifyDir |
| `policy` | Go pkg | `../daedalus-sdk/policy/` | policy.toml 严格加载 (ErrNotFound 哨兵 / LoadOrDefault / ALLOW_COMMANDS REPLACE) |
| `shellpolicy` | Go pkg | `../daedalus-sdk/shellpolicy/` | 15 命令 / 4 bin 目录 / 路径规则权威实现 (CLEAN_ENV, 30s, rc 126/124) |
| `pathguard` | Go pkg | `../daedalus-sdk/pathguard/` | fs 路径校验 (ALLOWED_DIRS 前缀边界, 空字节, realpath) |
| `audit` | Go pkg | `../daedalus-sdk/audit/` | 哈希链审计库, Python 金样字节级兼容 (三种序列化模式) |
| `objectmodel` | Go pkg | `../daedalus-sdk/objectmodel/` | 对象模型 schema 单一事实源;Kind 封闭枚举 7 类 + spec/status 信封 `Object` |
| `cmd/daedalus-{fs,shell,pkg,sysinfo,service,blueprint,dupe}` | Go binaries | `../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint,dupe}/cmd/` | 6 个 MCP stdio 能力服务器 (镜像态 = 插件内 `bin/`) |
| `policy.toml` | TOML | `files/system/opt/daedalus/shared/policy.toml` | 安全策略单一事实源 ([shell]/[fs]/[audit]/[objectmodel]/[blueprints]) |
| `daedalus.plugin.json` | manifest | `plugin/copilot/` + `../daedalus-plugins/<cap>/` | 插件声明 (id/type/runtime/executable/entrypoint/permissions/tools/resources/i18n) |
| `policy.ts` | TS module | `plugin/copilot/policy.ts` | Proposal schema validation & frozen shellpolicy copy |
| `audit.ts` | TS module | `plugin/copilot/audit.ts` | Hash-chained audit logging via daedalus-audit Go CLI |
| `llm.ts` | TS module | `plugin/copilot/llm.ts` | OpenAI & Anthropic cloud LLM adapters with revision support |
| `exec.ts` | TS module | `plugin/copilot/exec.ts` | JSON-RPC bridge spawning daedalus-shell Go MCP server with 40s watchdog |
| `main.ts` | TS entry | `plugin/copilot/main.ts` | Command advisor (命令顾问) flow: L0 y/n sandboxed execution, L1/L2 display-only + manual-execution hint, opt-in interactive loop, edit/revise, REPL |
| `76-daedalus-plugin-gen.sh` | build script | `files/scripts/76-daedalus-plugin-gen.sh` | 构建期从 manifest+policy.toml 渲染/自校验 systemd ExecStart + tools 交叉核对 + `resources[].kind` ⊆ `[objectmodel].enabled_kinds` |
| `75-daedalus-copilot.sh` | build script | `files/scripts/75-daedalus-copilot.sh` | Sets directory and executable permissions for Copilot |

---

## 1. Architecture Overview & Three Security Boundaries

Traditional systems grant AI agents or LLM clients unrestricted shell access, posing high risks of accidental corruption, prompt injection exploitation, or privilege escalation. Daedalus replaces raw shell access with structured, typed, and isolated capability servers across three distinct security layers:

```
+-------------------------------------------------------------------------+
|                  LLM / AI Agent Client / Daedalus Copilot                   |
|         (e.g., Claude Desktop, VS Code Agent, `daedalus` CLI)               |
+-------------------------------------------------------------------------+
                                    │
                         JSON-RPC via stdio (MCP)
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 1: Capability & Model Interface (Model <-> Capability Boundary)   │
│  - Standard Model Context Protocol (MCP)                                │
│  - Strict JSON schemas & typed parameters                               │
│  - Declarative read-only annotations vs mutating operations             │
│  - Zero raw shell string interpolation                                  │
│  - Copilot proposal validation & opt-in human confirmation              │
│  - plugin manifest 声明请求能力 ⊆ policy.toml 强制执行值                 │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Enforcement & Sandboxing Boundary (Enforcement Boundary)       │
│  - Go 静态二进制能力服务器 (CGO_ENABLED=0), systemd 直接执行             │
│  - Deno 细粒度权限仅用于 copilot 插件 (manifest entrypoint 旗标)         │
│  - systemd Hardening (DynamicUser=yes, ProtectSystem=strict)            │
│  - Landlock LSM (Kernel-level path-scoped access restriction)           │
│  - Seccomp System Call Filters (@system-service ~@privileged)           │
│  - policy.toml 单一事实源 (Go ../daedalus-sdk/policy 运行时读取, fail-closed)│
│  - systemd LoadCredential= (Secrets never stored in container image)    │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 3: Evidence & Verification Boundary (Evidence Boundary)           │
│  - Cryptographic append-only audit trail (/var/log/daedalus/audit.jsonl)    │
│  - SHA-256 hash chaining + file locking (Go syscall.Flock)              │
│  - Plugin sha256 checksums + manifest 规范化自摘要 (宿主 verify)         │
│  - AlmaLinux bootc immutable rootfs (composefs)                         │
│  - Atomic OS updates and one-click rollback (`bootc rollback`)          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Object Model

> 本段由 plan `aios-object-model-alignment` W7/T31 增量追加 (决策 25 落地;完整决策与执行模型细节见 `.omo/plans/aios-object-model-alignment.md`,此处不重复)。

- **单一事实源**: 对象模型 schema 只有 `../daedalus-sdk/objectmodel/` 一处定义。`Resource` 是 manifest 声明类型,三字段元组 `kind` + `name` + `desired_state` (JSON 键 `kind`/`name`/`desired_state`),绝不掺入 systemctl 字段;`ServiceState` 是查询/状态载荷共用类型 (`kind`/`name`/`desired_state`/`properties`/`conditions`,Properties 键为 systemctl 属性名原文、Conditions 是其上的派生语义层,两层并存不互替)。信封 `Object`(`api_version`/`kind`/`metadata{name,labels,annotations,generation}`/`spec`/`status{observed_generation,conditions,properties}`)同在 `objectmodel/envelope.go`,两类载荷都投影进它;`internal/controller` 的同名类型是它的**别名**,不得出现第二份形状。labels/annotations/generation 目前只有读侧 API(`MatchLabels`/`GetCondition`/`UpsertCondition`)无填充方,填充归调和循环;uid/resourceVersion/ownerRef/finalizers 未落地(出现真实消费者再谈),id 也未进入 v1 声明模式。
- **资源种类**: `Kind` 封闭枚举共 7 类 (service / package / container / capability / task / transaction / policy),v1 仅 `service` 与 `package` 有 provider;其余五类是保留枚举位,校验器接受、策略网关 fail-closed 拒绝。**新增资源种类必须先在 `../daedalus-sdk/objectmodel/` 加 Kind 常量 + `kindRegistry` 登记 + 校验分支**,再经 `policy.toml [objectmodel].enabled_kinds` 放行(三点漂移测试钉死)。
- **package kind (plan `daedalus-pkg-kind` 增量追加)**: `package` 自此有 provider — 只读观测走 `../daedalus-plugins/pkg/` 的 `dnf_query`/`dnf_list_installed` (`../daedalus-sdk/pkgquery`),状态变更一律经 `daedalus-tx` 的 `package.set` 适配器(事务通道, begin→propose→apply→rollback);`desired_state` 为 `present`/`absent`/`latest` 三值冻结三元组,映射 dnf `install`/`remove`/`upgrade` (`packageSetVerbs`,表外值拒绝);适配器 euid==0 守门(错串 `package.set requires root (euid=0); re-run via sudo`,与 `service.set` 的 user-scope-only 强制镜像对称 — package 强制 root,service 拒绝 root 单元);回滚依托 `dnf history undo`,Apply 以"捕获 dnf history id + sidecar 落盘 (`<txID>-<step.Index>.dnf_history_id`,tx-id 经 D-1 裁决的 ctx 侧路透传给适配器,`tx.Step` 六键线上契约不动)"为**成功必要条件** — 捕获或落盘任一失败都整体判 Apply 失败,杜绝"Apply 成功但 Rollback 不可用" (fail-closed 哲学);Rollback 读侧 sidecar 缺失/不可读→严格 error,不兜底、绝不猜 history id,history 已清理时 `present`/`latest` best-effort `remove` 兜底、`absent` 严格 error 且 dnf 物理零调用(不可逆)。
- **transactable**: 不是所有资源都事务化。事务性资源(目前 service / package)的状态变更走 `daedalus-tx` (begin→propose→apply→rollback) 通道;未启用 / 无 tx 适配器的非事务性资源被策略网关与适配器注册表拒绝,连 begin→propose→apply 序列都无法发起。v1 执行模型: `daedalus-tx` 是用户态调用的 CLI,无 systemd 单元(决策 25)。
- **状态字段**: `ActiveState`/`SubState` 等观测态不在声明 schema 里,由 `../daedalus-sdk/state/` 包提供 — `state.jsonl` 追加式观测缓存,行形 `StateEntry{kind, name, observed_at, payload}`,payload 为序列化的 `objectmodel.ServiceState`。state 是派生缓存,与哈希链审计(证据层)分离;v1 状态记忆按上下文隔离 (DynamicUser 命名空间,决策 25 补充条款)。
- **安装态 vs 源码侧**: 镜像安装态 `files/system/opt/daedalus/plugins/daedalus.*/daedalus.plugin.json` 的 `resources` 字段(含 `desired_state` 补全与 checksums 注入)是 `just plugin-pack` 的构建期产物;源码侧仅 `../daedalus-plugins/service/` 与 `../daedalus-plugins/pkg/` 声明 `resources`,其余 4 个既有能力与 copilot manifest 暂不写。`76-daedalus-plugin-gen.sh` 构建期交叉核对 `resources[].kind` ⊆ `[objectmodel].enabled_kinds`,漂移即拒构建。

---

## 2. OS Capability MCP Servers & Copilot CLI

Daedalus implements OS capability servers adhering to the Model Context Protocol specification. All servers are **Go static binaries** (`../daedalus-plugins/<cap>/cmd/daedalus-<cap>/`, official `modelcontextprotocol/go-sdk` stdio servers), packaged as `daedalus-plugin` (type=capability, runtime=native) under `/opt/daedalus/plugins/daedalus.<cap>/bin/`. The systemd units' ExecStart is rendered and self-verified at build time from the plugin manifests + `policy.toml` (`76-daedalus-plugin-gen.sh`), preserving per-service DynamicUser/Landlock drop-ins.

### 1. Filesystem Server (`daedalus.fs` plugin / `cmd/daedalus-fs`)
- **Purpose**: Safe, path-scoped file inspection and modifications.
- **Allowed Directory Model**: paths validated by `../daedalus-sdk/pathguard` against `policy.toml [fs].allowed_dirs` (e.g., `/home`, `/tmp`, `/var/log`).
- **Path Traversal Protection**: Rejects relative paths, null bytes, `..`, and realpath results escaping the allowlist (prefix-boundary check prevents `/home` matching `/home2`).
- **Tools**: `read_file`, `write_file`, `list_dir`, `move_file`.

### 2. Shell Server (`daedalus.shell` plugin / `cmd/daedalus-shell`)
- **Purpose**: Controlled execution of safe, read-only and diagnostic system commands.
- **Argv Whitelisting**: 15-command `allowed_commands`, 4 binary dirs (`/usr/bin /bin /usr/sbin /sbin`), path-like arg rules (`../daedalus-sdk/shellpolicy`, policy.toml `[shell]` section; `ALLOW_COMMANDS` env = whole-set REPLACE).
- **No Raw Shell Interpretation**: Direct `os/exec` argv execution without `/bin/sh` or `/bin/bash` wrapper.
- **Execution Limits**: 30s timeout, CLEAN_ENV, returncode 126 (denied) / 124 (timeout).
- **Tools**: `shell_exec`.

### 3. Package Management Server (`daedalus.pkg` plugin / `cmd/daedalus-pkg`)
- **Purpose**: Read-only package inspection (`../daedalus-sdk/pkgquery`);regex pattern guards against injection.
- **Read-Only Inspection**: `rpm -q --info` with `dnf repoquery --info` fallback.
- **Tools**: `dnf_query`, `dnf_list_installed`.

### 4. System Information Server (`daedalus.sysinfo` plugin / `cmd/daedalus-sysinfo`)
- **Purpose**: Read-only OS and environment state discovery (`../daedalus-sdk/sysinfo`: `/etc/os-release`, `/proc/cpuinfo`, `/proc/meminfo`, `ip -j addr`).
- **Tools**: `os_release`, `hardware_info`, `network_status`.

### 4b. Service Server (`daedalus.service` plugin / `cmd/daedalus-service`)
- **Purpose**: systemd 单元只读状态查询(状态变更一律经 `daedalus-tx` `service.set`,不允许本插件直接改状态)。
- **Tools**: `service.query`, `service.list`;观测结果走 `../daedalus-sdk/state/` 追加缓存。
- **Kind 声明**: manifest `resources: [{ kind: "service", name: "*" }]` — 唯一声明 `service` kind 的官方插件。

### 4c. Blueprint Server (`daedalus.blueprint` plugin / `cmd/daedalus-blueprint`)
- **Purpose**: 可复用的参数化配置模板(蓝图)渲染与安全应用:nginx 站点/反代、postgres 库/用户、redis ACL、haproxy 后端。数据经 `//go:embed` 编入二进制(`../daedalus-plugins/blueprint/cmd/daedalus-blueprint/blueprints/`,构建期由 `just blueprint-embed` 从 `../daedalus-plugins/blueprint/blueprints/` 复制,源码侧为唯一事实源),渲染走 Go `text/template`,schema 校验经 `../daedalus-sdk/blueprint` + `jsonschema-go` 预编译。
- **6 Tools**: `blueprint_list` / `blueprint_inspect`(只读)/ `blueprint_render`(渲染预览 + 确认令牌)/ `blueprint_apply` / `blueprint_status` / `blueprint_remove`(写型,经确认令牌消费)。
- **执行模型 v1-direct**: render 产生单次有效 confirm_token(15 分钟过期,成功即消费),apply 以令牌为前提,渲染内容经 daedalus-tx `blueprint.write` 适配器落盘(三段生命周期快照/写/回滚)。secret 引用只接受 `secret://` 形态,明文 fail-closed 拒绝(v1 恒返 `ErrSecretSourceUnavailable`,KWallet/credstore 接入位预留)。
- **6 蓝图**: `nginx-vhost` / `nginx-reverse-proxy` / `postgres-db` / `postgres-user` / `redis-acl` / `haproxy-backend`(每目录 manifest.json + schema.json + template.tmpl + pre_check.sh + post_check.sh + README.md)。
- **Policy**: `policy.toml [blueprints]` 节(输出目录白名单 `output_dirs` / post_check 命令白名单 `post_check_commands` / reload 服务白名单 `reload_services` / secret 来源 `secret_sources`);post_check 白名单经 `../daedalus-sdk/shellpolicy` 的 `RegisterBlueprintsPostCheckSource` 钩子注入(严格子集,独立于主白名单,与 policy.toml 三点漂移测试钉死)。
- **Systemd**: `daedalus-blueprint.service` 参照 shell/fs 模式(DynamicUser/Landlock/seccomp drop-in),`ReadWritePaths` 放行 output_dirs 落盘;ExecStart 由 76 脚本 render-unit 渲染自校验。构建期打包走暂存目录排除 `blueprints/` 数据(zip 只含 manifest + bin/,蓝图数据不重复进 rootfs,plan §4 line 121)。

### 5. Daedalus Copilot CLI (`daedalus` / plugin `daedalus.copilot`, runtime=deno)
- **Purpose**: Command advisor (命令顾问, 非 agent): translates user intent into a proposed shell command with a risk label (L0 safe / L1 cautious / L2 danger, judged by a local static classifier — the LLM never self-labels risk). L0 (safe + within the 15-command allowlist) may run in the `daedalus-shell` sandbox after y/n confirmation; L1/L2 are display-only with a manual-execution hint. Out-of-allowlist commands are never auto-executed.
- **Risk-Tiered Flow**: Proposals are validated against the schema and re-checked against the 15-command allowlist plus path rules before execution. L0 asks y/n then runs sandboxed; L1/L2 never enter the execution channel. `-i/--interactive` displays the proposal and prompts for `[y]es / [e]dit / [n]o (feedback) / [q]uit`. `-v/--verbose` shows the translated command, `--dry-run` shows without executing, `-y/--yes` is a backward-compat alias, and `-V` prints the version. Every decision still lands in the hash-chained audit log.
- **Zero Raw Execution**: The Copilot process never executes shell commands directly. It spawns the sandboxed `daedalus-shell` Go MCP server over stdio JSON-RPC.
- **Plugin Host Launch**: Copilot is installed as plugin `daedalus.copilot` (runtime=deno) under `/opt/daedalus/plugins/`; source lives in `plugin/copilot/`. The `/usr/local/bin/daedalus` wrapper asks `daedalus-host run-plugin daedalus.copilot` to construct (not spawn) the launch command from the manifest, expands `$HOME` placeholders, then execs it:
  ```sh
  argv=$(/usr/local/bin/daedalus-host run-plugin daedalus.copilot -dir /opt/daedalus/plugins -- "$@")
  eval "set -- $argv"; # $HOME token expansion; then
  exec "$@"   # = deno run --allow-env --allow-net --allow-read=... --allow-write=... --allow-run=... main.ts
  ```
- **Configuration**: Resolves keys/models via CLI flags > Environment (`DAEDALUS_LLM_API_KEY`, etc.) > User config (`~/.config/daedalus/copilot.json`).

---

## 3. Plugin Format (`daedalus-plugin`) & Host

- **Manifest** `daedalus.plugin.json`: `id` (`^[a-z0-9]+(\.[a-z0-9]+)*$`), `name`, `version`, `type` (`copilot`|`capability`), `runtime` (`native`|`deno`), `executable` (相对路径,需可执行位), `entrypoint` (宿主生成启动命令旗标; deno runtime 由宿主自动前置 `deno run`, manifest 不写 `run`), `permissions` (声明式), `tools` (能力插件暴露的 MCP 工具), `resources` (service / package 插件声明, `{ kind, name }`), `i18n` (locale 数组), `checksums` (Pack 注入的逐条目 sha256 + manifest 规范化自摘要)。
- **Packing**: `daedalus-plugin-pack -in <dir> -out x.zip` 可复现打包(Fixed 1980-01-01 timestamps,字典序); `-verify zip --keep <dest>` 解压即校验。zip-slip 九道防线(`..` 段/绝对路径/符号链接/O_EXCL|O_NOFOLLOW/重复条目/zip-bomb LimitReader 等)。
- **Host** (`daedalus-host`): `list` / `inspect` / `verify` / `run-plugin <id>` (仅打印启动命令) / `render-unit <id>` (生成 systemd ExecStart 供构建期消费)。**宿主不是 MCP 服务器的父进程** — systemd 直接执行渲染出的 ExecStart,保留每服务沙箱 drop-ins; degraded 插件被 run-plugin/render-unit 拒绝。所有宿主操作写 `host_*` 审计条目。
- **Build-time install only**: 插件随镜像内建;无运行时联网下载安装。

---

## 4. Sandboxing & Defense-in-Depth Hardening

### systemd Service Sandboxing Profiles
All Daedalus service profiles (`/usr/lib/systemd/system/daedalus-*.service`) enforce zero-privilege defaults:
- `DynamicUser=yes`, `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp=yes`.
- `NoNewPrivileges=yes`, `RestrictSUIDSGID=yes`, `ProtectKernelModules=yes`, `ProtectKernelTunables=yes`, `ProtectControlGroups=yes`.
- Resource constraints: `CPUQuota=50%`, `MemoryMax=256M`, `IOWeight=100`.

### Linux Landlock & Seccomp
- **Seccomp (`SystemCallFilter`)**: Whitelists only safe system service calls (`@system-service`), explicitly denying `@privileged`, `@resources`, and `@obsolete`.
- **Memory Protection**: `MemoryDenyWriteExecute=yes`.

### Deno Runtime Permissions (copilot only)
Only the copilot plugin runs on Deno; its permission flags are manifest entrypoint constants (read `/opt/daedalus/plugins/daedalus.copilot`, audit/shell/deno binaries under `/usr/local/bin`, `$HOME` config/state paths), constructed and printed by the host, exec'd by the wrapper.

### Policy Single Source of Truth
`/opt/daedalus/shared/policy.toml` is the enforced runtime policy. Go servers load it at startup via `../daedalus-sdk/policy`: missing → fail-closed refusal to start (生产默认;回退 built-in `Default()` 需 `DAEDALUS_POLICY_MODE=development` 显式 opt-in,回退值与 constants 逐项一致、drift-tested); corrupted → fail-closed refusal to start. `76-daedalus-plugin-gen.sh` performs a build-time `DAEDALUS_POLICY_PATH` handshake check against the installed binaries and rejects unit/manifest drift.

---

## 5. Tamper-Evident Audit Logging

Every MCP tool invocation, Copilot translation, security rejection, confirmation, user edit, and host operation is logged to `/var/log/daedalus/audit.jsonl` (with unprivileged fallback to `$HOME/.local/share/daedalus/audit.jsonl`) using a cryptographic hash chain. Implementation: `../daedalus-sdk/audit/` (Go), exposed as the `daedalus-audit` CLI (`--identity/--tool/--args/--outcome/--log-path`, `verify` subcommand). All writers (servers, host, copilot `audit.ts` via `DAEDALUS_AUDIT_BIN`) go through this CLI — direct file writes are forbidden.

### Hash Chaining Specification
- `timestamp`, `identity`, `tool`, `args`, `policy_version`, `outcome`, `prev_hash`, `entry_hash`.
- Genesis hash is `0` * 64.
- `entry_hash = SHA256(timestamp + identity + tool + args_str + outcome + prev_hash)` — implementation in `../daedalus-sdk/audit/hashtx.go`; golden vectors pin every field order.
- `args_str` serialization is byte-identical to the deleted Python reference (`sort_keys` + `(",",":")` + ensure_ascii `\uXXXX` escaping, incl. microsecond==0 isoformat quirk) — pinned by golden vectors in `../daedalus-sdk/audit/testdata/golden.jsonl`.
- Concurrency: exclusive flock on append (Go `syscall.Flock`, LOCK_UN before Close via defer LIFO).

---

## 6. Secrets Isolation (`systemd LoadCredential`)

Daedalus strictly forbids hardcoding API tokens, private keys, or passwords inside container images.
- System services receive secrets at runtime via `LoadCredential=daedalus_token:/etc/credstore/daedalus_token`.
- User-level tools (Copilot) read credentials from environment variables or secure user configuration (`~/.config/daedalus/copilot.json`).

---

## 7. Atomic OS Foundation & Rollback Guarantee

- **Atomic Image Construction (`bootc`)**: Root filesystem built as OCI container image (`Containerfile`), validated with `bootc container lint`.
- **Composefs & Read-Only Immutability**: `/usr` is mounted as read-only composefs; state limited to `/etc` and `/var`.
- **One-Click Rollback**: `bootc rollback` restores previous booted deployment atomically.

---

## CONVENTIONS
- **注释语言规范(强制)**:本项目所有源代码、配置文件、构建脚本中的注释**必须使用中文**。包括但不限于:
  - Go 源码(`//` 与 `/* */`)注释与 godoc 注释
  - TypeScript / Deno 源码(`//` 与 `/* */`)注释
  - Shell / Bash 脚本(`#`)注释
  - systemd unit 文件(`#`)注释
  - justfile / Makefile 文件(`#`)注释
  - Docker / Containerfile(`#`)注释
  - YAML / JSON / TOML 配置文件中的注释字段(含 `policy.toml`、`daedalus.plugin.json` 的注释字段)
  - 现有代码文件中残留的英文注释必须翻译为中文,新提交也必须遵守此规则
  - 标识符、字符串字面量、API 协议字段(如 JSON 键、HTTP 头、协议名)、系统命令、URL、日志中可被外部解析的 token 等**不视为注释**,保留英文以保证互操作性
- **代码文件清单**:本项目涉及的主要代码文件包括:
  - **Go (core)**: `cmd/daedalus-{audit,host,plugin-pack,smoke,tx}/` + `internal/{controller,tx}/` + `../daedalus-sdk/{pathguard,shellpolicy,pkgquery,sysinfo,policy,audit,plugin,objectmodel,i18n,blueprint,version,state,dirs}/` + `../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint,dupe}/cmd/`
  - **TS / Deno (copilot 源码)**: `plugin/copilot/{policy,audit,exec,llm,main}.ts` + `daedalus.plugin.json`
  - **TS 测试 (镜像外)**: `tests/deno/{policy,audit,exec,llm,main}.test.ts` + `tests/deno/shellpolicy_contract.test.ts`
  - **Shell scripts**: `files/scripts/{60-ai-middleware,65-ai-safety,70-daedalus-mcp-servers,75-daedalus-copilot,76-daedalus-plugin-gen}.sh`, `usr/local/bin/daedalus` wrapper, `scripts/sync-daedalus.sh`, `scripts/pack-copilot-plugin.sh`, `scripts/justfile.demo`, `scripts/copilot-prep.sh`
  - **systemd units**: `daedalus-audit.service`, `daedalus-env.service`, `daedalus-{fs,shell,pkg,sysinfo,service,blueprint,dupe}.service`, and each capability's `.service.d/{landlock,credentials}.conf` drop-ins
  - **Policy / manifests**: `files/system/opt/daedalus/shared/policy.toml`, `plugin/copilot/daedalus.plugin.json`, `../daedalus-plugins/<cap>/daedalus.plugin.json`
- **三仓平级布局(决策 23/24 + 25 + 迁移至 issue-migration-to-3-repos)**: 本仓 = 代码逻辑层 + copilot 插件源码 + 镜像构建落位 + 5 个 core runtime; `../daedalus-sdk/` = SDK 公开仓(11 安全核心包 + 5 Provider/Slot 占位 + state/dirs); `../daedalus-plugins/` = 插件仓(6 Go 能力插件 monorepo)。镜像内 `opt/daedalus/plugins/` 是**构建产物(安装态)**, `plugin/` 与 `../daedalus-plugins/<cap>/` 是**源码侧定义** — 两者绝不同步混用。**三仓须平级 clone**(仓名必须为 `daedalus-core` / `daedalus-sdk` / `daedalus-plugins`,与本仓 `go.work` 路径对应),由 `just verify-dev-layout` 守门。
- **插件命名语义(目录名 ≠ 系统组件,是"能力提供者")**: 插件目录名/id(`shell/`、`daedalus.shell` 等)是**稳定技术标识符** — 被 systemd 单元、CI、import 路径、copilot 硬编码引用锁定,永不改; manifest `name` 是**人类可读显示名**,按"XX 能力"语义命名,只影响展示层。`shell` = 受控命令执行能力(不是 shell 解释器); `service` = 只读服务状态查询能力(不是 systemd 服务,状态变更走 `daedalus-tx`); `pkg` = dnf/rpm 只读包查询能力; `fs` = 路径作用域文件读写能力。完整对照表见 `../daedalus-plugins/README.md`「命名语义」段。
- **Vendor tree = `base_image/`**: Vendored upstream fork (gitignored). Updated from `files/` (及 `plugin/` → `base_image/plugin/`, 在 COPY 白名单外) via `scripts/sync-daedalus.sh` before container builds.
- **Go 依赖策略**: 仅入库 `go.mod` + `go.sum`(版本与完整性锁); `vendor/` 不入库,构建期 `go build` 自动从 module proxy 下载到 `GOMODCACHE`,首次构建需联网; `go mod verify` 校验 go.sum 完整性。`go.work` 不入库,模板 `go.work.example` 入库 — clone 后 `cp go.work.example go.work`。
- **镜像安装态策略**: `files/system/opt/daedalus/plugins/daedalus.*/bin/` 与 `files/system/usr/local/bin/daedalus-{audit,host,shell}` 是 `just plugin-pack` 的 Go 编译产物副本,均**不入库**(同 `cmd/../bin/` 性质);开发者 clone 后必须 `just plugin-pack` 才能 build,与 `just sync` 一起完成 vendor 树重建。
- **Justfile workflow**: Use `just sync`, `just build`, `just test`, `just plugin-pack`, `just verify-image`, `just iso`, `just qemu`, `just verify-dev-layout` for all lifecycle actions.
- **Build step order**: Numbered scripts in `files/scripts/` run in `sort --sort=human-numeric` order (`10-base` → … → `60-ai-middleware` → `65-ai-safety` → `70-daedalus-mcp-servers` → `75-daedalus-copilot` → `76-daedalus-plugin-gen` → `91-image-info` → `cleanup.sh`)。
- **Go 唯一实现(取代旧 Python+Deno parity 条款)**: fs/shell/pkg/sysinfo/service/blueprint 与审计仅有 Go 实现(能力服务器在 `../daedalus-plugins/<cap>/`,审计库在 `../daedalus-sdk/audit/`),不得恢复 Python/Deno 服务器。Copilot 的 `policy.ts` 内联 15 命令/9 前缀/5 blocked **冻结副本**,与 `../daedalus-sdk/shellpolicy` 存在**双向同步义务**(改一侧必改另一侧);该契约由 `tests/deno/shellpolicy_contract.test.ts`(ALLOW_COMMANDS REPLACE 语义 Go↔Deno 一致)与 Go 侧钉子测试共同钉住。policy.toml ↔ `policy.Default()` ↔ shellpolicy/pathguard 常量的三点防漂移链见 `../daedalus-sdk/policy` 测试。
- **测试布局**: Deno 测试住仓库根 `tests/deno/`(镜像外),相对导入指向 `plugin/copilot/` 源码;Go 测试随包住本仓 `cmd/`、`internal/` 及兄弟仓各包。镜像 rootfs 内零测试文件。
- **Single Containerfile**: `Containerfile` at repository root is the sole build entry point. All `*.daedalus` aliases have been removed.
- **i18n 多语言(强制)**: 所有插件的 UI 字符串必须经 `i18n.ts` 的 `t(key, ...args)` 走,不在源码里硬编码。locale 文件在 `<plugin>/i18n/<locale>.json`(POSIX 下划线命名,`en_US` / `zh_CN` / `ja_JP` / `ko_KR` 等); manifest 声明 `"i18n": ["en_US", "zh_CN"]` 数组形式,en_US 必定位兜底。声明 ↔ 实物用 `scripts/plugin-i18n-sync.sh` 双向校验,CI 走严模式(exit 1 拒漂移),开发者加新 locale 走 `--autofix` 自动改写 manifest。locale 探测: `LC_ALL` > `LANG` > `en_US`,支持精确匹配 → 语言级回退(精确 `zh_CN` → 语言级 `zh` → 兜底 `en_US`)。命名: `<key>` 风格 + `{0}` `{1}` printf 占位符。Go 侧 MCP server 的字符串翻译在 P1 单独做(共享同一份 JSON 文件,经 `embed.FS` 嵌入二进制)。
- **本仓库边界**: 本仓装的是 daedalus 运行时(本仓 Go 静态二进制 + `cmd/` 5 个 core runtime) + 官方自带 copilot 插件(`plugin/copilot/`) + 6 个能力插件(主源在 `../daedalus-plugins/<cap>/`,本仓只持 `bin/` 构建产物) + 这些官方插件的运维工具(`scripts/plugin-i18n-sync.sh` 等)。**插件开发脚手架**(生成新插件骨架、`daedalus-plugin-scaffold new` 之类)属另一个仓库,本仓库不实现; **外部作者的插件**各自维护在各自仓库,通过 `daedalus-host` 加载(后续 plan)。
- **禁止注释引用计划编号**: `todo N` / `决策 N` / `oracle review` / `round-N` 等进度信息写 commit message 或 `.omo/plans/`,不进源码注释。
- **注释只写 why,不写 what**: 代码可自解释处不加注释。
- **单文件注释密度软上限 ~15%**: 后续可接 CI 门禁。
- **跨仓/跨语言对齐注释不写精确行号**: `py:43-53` 这类行号会腐烂,只写行为语义。
- **文件头 ≤8 行**: 一句 what + 关键 invariant + 指回 README/AGENTS 的链接。

## ANTI-PATTERNS (THIS PROJECT)
- **NEVER** `shell=True` / `bash -c` / `sh -c` in subprocess.
- **NEVER** run a command outside the 15-entry `allowed_commands`, nor from a binary dir other than `/usr/bin /bin /usr/sbin /sbin`.
- **NEVER** accept relative paths, null bytes, or paths resolving outside allowlist.
- **NEVER** hardcode secrets in the image — credentials flow via `LoadCredential` or user-scoped config.
- **NEVER** directly execute arbitrary shell commands inside Copilot (`daedalus`) — always route through the sandboxed `daedalus-shell` MCP bridge.
- **NEVER** modify/insert/delete audit log lines or break the hash chain; NEVER write the audit file directly — always via the `daedalus-audit` CLI.
- **NEVER** let `daedalus-host` spawn or become the parent process of any MCP server (决策 16): the host only installs/discovers/verifies and *prints* the launch command; systemd executes it. Never add exec/spawn to `run-plugin`/`render-unit`.
- **NEVER** introduce a second policy source of truth: whitelist changes go through `shared/policy.toml` (runtime) with `policy.Default()` and the `shellpolicy`/`pathguard` constants updated in lockstep (drift tests fail otherwise); units must not carry a drifted `Environment=ALLOW_COMMANDS=`.
- **NEVER** leak source into the image rootfs: 本仓 `cmd/` `internal/` `plugin/` 源码与任何 `*.test.ts`/`*.py`/`__pycache__`/`vendor` must never be rsync/COPY'd into `/opt` — only build products(`plugins/` 安装态, `usr/local/bin` binaries, `shared/policy.toml`) land there(`just verify-image` asserts this)。
- **NEVER** hand-edit `files/system/opt/daedalus/plugins/`(构建产物)— regenerate via `just plugin-pack` / `./scripts/pack-copilot-plugin.sh`。
- **NEVER** modify/commit changes to `base_image/` directly; modify 本仓 `files/` `plugin/` 与兄弟仓 SDK/plugins 源码,跑 `just sync` / `./scripts/sync-daedalus.sh`。
- **禁止绕开 daedalus-tx 直接改 service 状态**: 所有 service 状态变更必须经 `daedalus-tx service.set` 通道,绕过即破坏审计链。
- **禁止在 main.ts 写硬编码 UI 字符串**: 所有 i18n 字符串必须经 `t(key, ...args)`,新加 key 必须同时写 `i18n/{en_US,zh_CN}.json` + manifest `i18n` 数组。
- **禁止把镜像内安装态 `files/system/opt/daedalus/plugins/` 手改**: 必须 `just plugin-pack` / `./scripts/pack-copilot-plugin.sh` 重新生成。
- **禁止跨仓 SDK 自给**: 本仓不实现 `policy` `shellpolicy` `pathguard` `audit` 等包 — 全在 `../daedalus-sdk/`,需要时 `import` 而非内联。
- **禁止独立 release 本仓**: 镜像即发布物,本仓 tag 由镜像版本驱动,不独立发 Go module 版本。

## COMMANDS
```bash
# Justfile orchestration (Recommended)
just sync                  # Sync files/{,plugin} into base_image/ vendor tree
just build                 # sync + podman build Daedalus image (需 x86-64-v3 构建机;本机 BLOCKED, 见 NOTES)
just test                  # 全量测试: 本仓 cmd/ + internal/ + ../daedalus-sdk/ + ../daedalus-plugins/<cap>/ + deno test --allow-all tests/deno/
just go-build              # CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o cmd/../bin/ ./cmd/... (5 个 core runtime 二进制)
just go-test               # 本仓 cmd/ + internal/ + ../daedalus-sdk/ + ../daedalus-plugins/<cap>/ 全量 go test
just go-build-demo         # 同 go-build + -tags demo,触发 cmd/daedalus-host/{paths.go,paths_demo.go} build-tag 互斥路径重写
just dev-copilot *args     # 0 安装端到端跑 copilot:rebuild demo 二进制 → 解包 5 插件到临时目录 → eval 拼好的 argv
just plugin-pack           # 构建 Go 二进制 → 同步到 ../daedalus-plugins/<cap>/bin/ → 打 6 zip (checksums 注入) → 解压到 files/system/opt/daedalus/plugins/ + host/audit/shell 进 usr/local/bin
just copilot-plugin        # 打包 copilot 插件安装态 (./scripts/pack-copilot-plugin.sh)
just blueprint-embed       # rsync ../daedalus-plugins/blueprint/blueprints/* → cmd/.../blueprints/ (//go:embed 源)
just i18n-sync [plugin-dir]            # 双向校验,严格,不一致 exit 1(CI 守门用)
just i18n-sync-autofix [plugin-dir]    # 以 i18n/ 目录为准,改写 manifest 的 i18n 字段
just verify-dev-layout     # 三仓平级布局守门 (兄弟仓就位 + go.mod module 路径匹配)
just verify-image          # 镜像零残留断言 (find /opt 无 *.py/*.pyc/__pycache__/*.test.ts/go.mod/vendor;需已构建镜像)
just iso                   # Build bootable ISO via bootc-image-builder
just qemu                  # Build qcow2 and test-boot in QEMU

# Go 直接命令 (本仓 cmd/ + internal/)
go build ./... && go vet ./... && go test ./...

# 兄弟仓测试 (走 go.work 桥接)
cd ../daedalus-sdk && go test ./...
cd ../daedalus-plugins && go test ./...

# Deno 测试(源码在 plugin/copilot/,测试在仓库根 tests/deno/;相对导入跨目录指向源码)
deno test --allow-all tests/deno/
deno test --allow-all tests/deno/policy.test.ts
deno test --allow-all tests/deno/audit.test.ts
deno test --allow-all tests/deno/llm.test.ts
deno test --allow-all tests/deno/exec.test.ts
deno test --allow-all tests/deno/main.test.ts
deno test --allow-all tests/deno/shellpolicy_contract.test.ts  # Go↔Deno 跨语言契约(替代旧 py parity)

# 审计链验证 (金样向量重放)
go run ./cmd/daedalus-audit verify --log-path ../daedalus-sdk/audit/testdata/golden.jsonl

# Validate image manually (v3 构建机)
bootc container lint
podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-host list   # 期望 7 插件 (copilot + 6 能力)
podman run --rm localhost/daedalus-os:latest cat /usr/lib/os-release
```

## NOTES
- 本仓(`daedalus-core`)、`../daedalus-sdk/`、`../daedalus-plugins/` 平级 clone 是 dev 桥的唯一拓扑;`go.work` 不入库,模板 `go.work.example` 入库 — clone 后 `cp go.work.example go.work`,跑 `just verify-dev-layout` 守门。
- 模板除 `use` 全部 cap 外,还对 SDK 的 `v0.0.0` 与 `v0.0.0-00010101000000-000000000000` 各钉一行 `replace => ../daedalus-sdk`:workspace 级 replace 覆盖各 cap `go.mod` 里面向插件仓 CI 的 `../daedalus-sdk`,否则 `just go-test` 撞 "conflicting replacements"。本地跑插件测试走该 workspace(`go test github.com/Daedalusys/daedalus-plugins/<cap>/...`),不需要符号链接桥。
- `scripts/sync-daedalus.sh` copies safely: systemd leg uses targeted stale-delete (only `daedalus-*`, upstream assets untouched); plugin leg uses `--delete --delete-excluded` (vendor mirrors source exactly); `base_image/plugin/` sits outside the Containerfile COPY whitelist so it never reaches rootfs.
- `base_image/` carries upstream dormant CI targeting upstream repos — do NOT mistake for Daedalus CI.
- Deno install (`65-ai-safety.sh`) pulls from `https://deno.land/install.sh` to `/usr/local/bin/deno` — used **only** by the copilot plugin now.
- **构建机限制 (todo 5/10/13 记录)**: `just build` 在本机不可执行 — `almalinux-bootc:10` 要求 x86-64-v3 而本机 CPU 不支持; 镜像内断言(`just verify-image`, in-image `daedalus-host list`, bootc lint, 76 脚本真实执行, copilot wrapper 全链路)在 v3 构建机/CI 上补跑(plan todo 18)。本机等价断言: `find files/system base_image/files/system \( -name "*.py" -o -name "*.test.ts" -o -name "__pycache__" -o -name "go.mod" \) | wc -l` == 0。
- 镜像内 `/usr/local/bin/daedalus-{audit,shell}`(audit.ts/exec.ts 的生产默认路径)已由 `just plugin-pack` 构建期安装(task 21 修复断链缺口; 与 wrapper `--allow-run` 放行路径一致); `daedalus-host list` 等镜像内端到端断言仍在 v3 构建机补跑(todo 18)。copilot 内向上回溯 dev 路径的回退链仅作开发态兜底。
- **mid-migration 残留说明**: `daedalus-plugins/blueprint/{cmd,bin,blueprints}/`(本仓下)是迁移期产物 — cmd 与 bin 仅供历史回溯,主源在 `../daedalus-plugins/blueprint/`; `daedalus-plugins/go.work` 是迁移桥,迁移完成后会清空。`plugin/{fs,shell,pkg,sysinfo,service}/` 5 个子目录是占位 README,主源在 `../daedalus-plugins/<cap>/`。

## 源码树内调试(dev-install 替代形态 — 0 安装)

> 本段由 plan `daedalus-dev-mode`(todo 2)增量追加: 记录如何不安装、不触碰镜像安装态 `/opt/daedalus/plugins`,直接用宿主二进制对插件做检视/校验/打印启动命令。以下命令均已实际执行验证。

### 用途与已验证用法

`daedalus-host` 支持用 `-dir` 旗标指向任意插件目录(优先级: `-dir` 旗标 > `DAEDALUS_PLUGIN_DIR` 环境变量 > 镜像默认 `/opt/daedalus/plugins`),五个子命令 `list` / `inspect` / `verify` / `run-plugin` / `render-unit` 全部适用。两侧形态:

- 镜像内(安装态): 宿主在 `/usr/local/bin/daedalus-host`,插件在 `/opt/daedalus/plugins`,直接 `daedalus-host list` 即可(期望 7 插件全 ok)。
- 仓库侧(源码树,0 安装): 源码侧 `../daedalus-plugins/<cap>/` 的目录名与 manifest id(`daedalus.<cap>` 等)一致,但源码 manifest 不含 checksums。因此直接 `-dir ../daedalus-plugins` 时 `list` 能运行但全部标 degraded,`inspect`/`verify`/`run-plugin`/`render-unit` 会被 id 一致性防线拒绝。完整可用的源码树形态是先打包再解包(打包会注入 checksums),从仓库根执行:

```bash
# 1) 解包构建产物(zip 内 checksums 与内容自洽;改源码后需重跑 just plugin-pack 刷新 zip)
plugdir=$(mktemp -d)
for p in fs shell pkg sysinfo service blueprint; do
  cmd/daedalus-plugin-pack -in "../daedalus-plugins/$p" -out "$plugdir/daedalus.$p.zip"
  cmd/daedalus-plugin-pack -verify "$plugdir/daedalus.$p.zip" --keep "$plugdir/daedalus.$p"
done
# 2) 五个子命令全部可用(以 daedalus.fs 为例,其余插件同理)
cmd/daedalus-host -dir "$plugdir" list                   # 6 插件全 ok
cmd/daedalus-host -dir "$plugdir" inspect daedalus.fs    # manifest 详情 + 完整性
cmd/daedalus-host -dir "$plugdir" verify daedalus.fs     # sha256 校验
cmd/daedalus-host -dir "$plugdir" run-plugin daedalus.fs   # 仅打印启动命令
cmd/daedalus-host -dir "$plugdir" render-unit daedalus.fs  # 仅输出 systemd 片段
```

### 与 `just dev-install` 的关系

- 只需检视 manifest、校验完整性或打印启动命令: 用上面的 0 安装形态,不落任何东西到系统路径。
- 需要一套可运行的安装(binaries + 解包插件,例如装到 `~/.local` 免 sudo): 用 `just dev-install <prefix>`; 装好后可用 `just host-list` 一键列插件。这两个 recipe 由同一 plan(`daedalus-dev-mode`,todo 1)提供,以 `justfile` 实际内容为准。

### 安全约束(恒成立,与运行形态无关)

- 宿主绝不 spawn,也绝不是任何 MCP 服务器的父进程;`run-plugin` 只把构造好的启动命令**打印**到 stdout,真正执行者是 systemd(按 ExecStart)或用户自己。
- `render-unit` 只输出 `[Service]` + `ExecStart=` 单元片段文本,不落盘、不启停任何单元。
- degraded 插件(sha256 不匹配、id 与目录名不一致、manifest 损坏)会被 `run-plugin`/`render-unit` 拒绝(退出码 1),不产出启动命令。
- 宿主每次子命令执行都会经 `daedalus-audit` 写一条 `host_*` 哈希链审计条目(`host_list` / `host_inspect` / `host_verify` / `host_run_plugin` / `host_render_unit`),写失败静默忽略(尽力而为)。

## Demo 构建模式(计划 todo 3 增量)

> 解决"无 KVM 镜像也能端到端跑 copilot CLI"的需求: 补一个 build-tag-gated 路径重写层,让 `-tags demo` 编译的宿主二进制能从本地 `daedalus-dev.toml` 读 dev 路径,把 entrypoint 字符串里写死的 `/opt/daedalus` 与 `/usr/local/bin` 重写到 dev 树对应位置,deno 二进制走 `$PATH` 或显式指定。**生产构建(默认 tag)行为完全不变** — 没有 dev 配置读代码,没有运行时 flag,编译期常量折叠消掉重写分支。

### 设计要点

- **编译期门控**: `cmd/daedalus-host/{paths.go,paths_demo.go}` 通过 `//go:build !demo` / `//go:build demo` 互斥编译;`paths.go` 只放 prod 字面量(`var denoBinary = "/usr/local/bin/deno"`、`var devPrefix = ""`、`const devMode = false`),`paths_demo.go` 才接 `init()` + `loadDevPaths()`。发行版二进制里**不存在**任何读 dev 配置的代码路径,零攻击面。
- **fail-closed**: `paths_demo.go` 的 `init()` 在 `loadDevPaths()` 失败时 `os.Exit(2)` 拒启,绝不静默退化为 prod 行为;`go test` 运行期间 `testing.Testing()` 为真则跳过强校验,让单测直接覆写包级变量。
- **重写范围**: `buildStartTokens` 在 `devMode==true`(编译期 const 折叠)时对每个 entrypoint token 做 `strings.ReplaceAll`: `/opt/daedalus` → `devPrefix+"/opt/daedalus"`、`/usr/local/bin` → `devPrefix+"/usr/local/bin"`。`/home` `/var/log` `/tmp` `/proc` `/etc/...` `$HOME/...` 等普通路径**不受改写**(不是镜像根)。`denoBinary` 与 `devPrefix` 来自 `daedalus-dev.toml`。
- **现有 prod 断言零回归**: `cmd/daedalus-host/main_test.go:TestRunPlugin_Deno` 在两种 tag 下都通过 — prod 模式 `denoBinary = "/usr/local/bin/deno"`;demo 模式 `init()` 因 `testing.Testing()` 跳过强校验,`denoBinary` 保留 demo 文件的 `var denoBinary = "/usr/local/bin/deno"` 缺省值。`buildStartTokens` 在 `devPrefix==""` 时重写是恒等替换,输出与 prod 逐字节一致。

### 配置文件

`daedalus-dev.toml`(仓库根,`.gitignore`,仅 `daedalus-dev.toml.example` 入库):

```toml
# dev 树里"镜像布局"的根目录;entrypoint 里的 /opt/daedalus 改写为
# {prefix}/opt/daedalus,/usr/local/bin 同理。从仓库根跑就用默认。
prefix = "./files/system"

# Deno 二进制绝对路径;留空走 $PATH (exec.LookPath)。
# deno = "/home/user/.asdf/installs/deno/2.1.4/bin/deno"
```

解析优先级: `$DAEDALUS_DEV_PATHS`(必须绝对路径)> 自 cwd 向上找 `daedalus-dev.toml` > 报错 exit 2。

### 已验证用法(端到端跑通)

```bash
# 一次性: cp 模板
cp daedalus-dev.toml.example daedalus-dev.toml

# 一次性: 确保 plugin zip 已就位(之前跑过 just plugin-pack / just copilot-plugin)
# cmd/daedalus-plugin-pack 输出的 <plugdir>/daedalus.{fs,shell,pkg,sysinfo,service,blueprint,dupe}.plugin.zip 必须存在

# 1) 构建 demo 二进制(覆盖 cmd/daedalus-host)
just go-build-demo

# 2) 单独验证 argv 重写
plugdir=$(mktemp -d)
for p in fs shell pkg sysinfo service blueprint; do
  cmd/daedalus-plugin-pack -in "../daedalus-plugins/$p" -out "$plugdir/daedalus.$p.zip"
  cmd/daedalus-plugin-pack -verify "$plugdir/daedalus.$p.zip" --keep "$plugdir/daedalus.$p"
done
cmd/daedalus-host -dir "$plugdir" run-plugin daedalus.copilot -- "show os version"
#   期望输出:
#     <deno> run --allow-env --allow-net
#     '--allow-read=./files/system/opt/daedalus,/home,/var/log,...'
#     '--allow-write=/var/log/daedalus,/tmp,$HOME/.local/share/daedalus'
#     --allow-run=./files/system/usr/local/bin/deno,
#                  ./files/system/usr/local/bin/daedalus-audit,
#                  ./files/system/usr/local/bin/daedalus-shell
#     $plugdir/daedalus.copilot/main.ts 'show os version'
# 注意: /home /var/log /tmp /proc /etc/... $HOME/... 不被改写(不是镜像根)

# 3) 端到端跑 copilot(包含 LLM 调用或 --dry-run)
just dev-copilot --dry-run "show os version"
#   等价于: rebuild demo 二进制 → 安装 daedalus-{audit,shell} 到镜像布局位
#           files/system/usr/local/bin/(deno --allow-run 旗标放行的路径)
#         → 解包 6 插件 → 拼 argv → export DAEDALUS_AUDIT_BIN/SHELL_BIN
#           与布局位同步 → eval 执行
#   无 LLM key 也能跑(走 --dry-run);有 key 自动翻译自然语言指令
#   镜像布局位已 .gitignore,不会污染版本控制

# 4) prod 模式输出对照(同一命令,just go-build 切回 prod)
just go-build
cmd/daedalus-host -dir "$plugdir" run-plugin daedalus.copilot -- "x"
#   期望输出: /usr/local/bin/deno run ... --allow-read=/opt/daedalus,...
#   镜像路径字面量,与 demo 模式对比一目了然
```

### 与其他 dev 路径的关系

| 路径 | recipe | 行为 | 何时用 |
|------|--------|------|--------|
| 镜像内(生产) | (无,systemd 跑) | 全部路径硬编码 | `just build` 出来的 podman/ISO |
| 装到 `~/.local` | `just dev-install <prefix>` | 镜像路径(装出镜像布局的目录结构) | 装一套可用安装到前缀 |
| 检视 / 打印启动命令 | 0 安装(`mktemp -d` + `daedalus-host -dir`) | 镜像路径(仅 capability server 可跑,copilot 路径错误) | 只想 inspect/verify/render-unit |
| **Demo 跑 copilot** | `just go-build-demo` + `just dev-copilot` | **dev 路径重写**(path-aware) | **dev 树里端到端跑 copilot** |

### 安全性质(与运行时 flag 方案的对比)

- 发行版二进制: 根本没有读 `daedalus-dev.toml` 的代码,攻击者无法通过 env、文件、CLI flag 让 prod 二进制走 dev 路径。
- demo 二进制误进生产环境: 缺 `daedalus-dev.toml` 即 exit 2,不产出任何 argv;即便配置存在也只影响本机 dev 用户的 argv 构造,不影响镜像内任何东西。
- 与运行时 flag 方案相比: build tag 让"dev 能力"在 prod 二进制里**字面上不存在**,不是"加了 flag 但默认关闭"。