# Daedalus: AI-Native Operating System Knowledge Base

**Generated:** 2026-08-29
**Commit:** 0a40f96
**Branch:** (detached HEAD)

## OVERVIEW
Immutable, atomic, AI-native desktop OS on AlmaLinux Bootc (KDE variant). Adds a Model Context Protocol (MCP) capability-middleware layer with three security boundaries (model/capability, enforcement/sandboxing, evidence/verification), tamper-evident audit logging, systemd credential isolation, and atomic rollback. OS capability servers (fs/shell/pkg/sysinfo), the hash-chained audit CLI, the plugin host (`daedalus-host`) and the plugin packer (`daedalus-plugin-pack`) are **Go static binaries** (single implementation; the former Python/Deno dual implementations are removed). A VSIX-like `daedalus-plugin` format (manifest `daedalus.plugin.json` + zip + sha256 checksums) makes every server and the Deno-based `daedalus` AI Copilot CLI an installable, discoverable, verifiable plugin. Security policy lives in one `policy.toml` single source of truth consumed at runtime by the Go servers. Stack: bootc/OSTree + systemd units + Go static binaries + Deno copilot plugin + Just orchestration.

## STRUCTURE
```
Daedalus/
├── Containerfile            # ACTIVE root build recipe (2-stage, bootc lint; COPY 白名单 = base_image/files/{system,scripts} + *.pub)
├── justfile                 # Unified orchestrator (sync/build/test/go-build/go-test/plugin-pack/copilot-plugin/verify-image/iso/qemu)
├── sync-daedalus.sh         # Syncs tracked daedalus/ source tree into base_image vendor tree (targeted stale-delete on systemd leg; --delete-excluded on plugin leg)
├── pack-copilot-plugin.sh   # Copilot Pack→Verify 安装态生成 (源 = daedalus/plugin/copilot/)
├── daedalus/                    # ★ TRACKED SOURCE ROOT for all Daedalus-owned files (三层结构, 决策 23/24)
│   ├── core/                #   代码逻辑层: Go 模块根 (module github.com/daedalus-os/daedalus/core; 镜像外, 仅 go.mod + go.sum 入库, vendor/ 与 bin/ 均不入库)
│   │   ├── cmd/             #     daedalus-{fs,shell,pkg,sysinfo,audit,host,plugin-pack,smoke} 二进制入口
│   │   └── internal/        #     pathguard shellpolicy pkgquery sysinfo policy audit plugin version 共享包
│   ├── plugin/              #   官方预置插件层 (源码侧): copilot/ (Deno 源码 + daedalus.plugin.json) + {fs,shell,pkg,sysinfo}/ (manifest + bin/)
│   └── files/               #   镜像构建层: rootfs 落位 (构建产物, 非源码)
│       ├── scripts/         # Daedalus build steps (60-ai-middleware, 65-ai-safety, 70-daedalus-mcp-servers, 75-daedalus-copilot, 76-daedalus-plugin-gen)
│       └── system/          # Rootfs-mirror overlay
│           ├── opt/daedalus/plugins/  # 插件安装态 (构建产物: daedalus.copilot + 4 能力, 含 checksums manifest; 勿手改)
│           ├── opt/daedalus/shared/policy.toml  # 安全策略单一事实源 (shell/fs/audit 节, Go 运行时读取)
│           ├── usr/lib/systemd/system/   # daedalus-*.service + .service.d drop-ins
│           ├── usr/local/bin/            # daedalus CLI wrapper + daedalus-host/audit/shell 二进制 (task 21 接线)
│           └── etc/credstore/            # systemd LoadCredential placeholders
├── base_image/              # VENDORED AlmaLinux/atomic-desktop fork (gitignored, own .git)
│   └── files/               # Target for sync-daedalus.sh before container build (base_image/plugin/ 在 COPY 白名单之外)
├── examples/                # mcp_client_config.json (4 Go 能力服务器条目, 指向插件内二进制)
├── tests/deno/              # 镜像外 Deno 测试: copilot 5 组 .test.ts + shellpolicy_contract.test.ts (Go↔Deno 跨语言契约)
└── .github/workflows/       # build-daedalus.yml (runs `just build` from repo root + go-test/test 门 + 镜像零残留断言)
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Go MCP 能力服务器实现 | `daedalus/core/cmd/daedalus-{fs,shell,pkg,sysinfo}/` + `daedalus/core/internal/{pathguard,shellpolicy,pkgquery,sysinfo}/` | go-sdk stdio 服务器; 唯一实现 (Python/Deno 双实现已删除) |
| Modify Daedalus Copilot CLI | `daedalus/plugin/copilot/` (源码) + `daedalus/files/system/opt/daedalus/plugins/daedalus.copilot/` (镜像安装态) | policy, audit, llm, exec, main orchestration; 安装态是构建产物勿手改 |
| 插件格式 / 宿主 | `daedalus/core/cmd/daedalus-{host,plugin-pack}/` + `daedalus/core/internal/plugin/` + `daedalus/plugin/*/daedalus.plugin.json` + `daedalus/plugin/README.md` | manifest schema + zip 打包 + sha256 + zip-slip 防护; 宿主仅安装/发现/校验 (非父进程) |
| 安全白名单 / 策略 | `daedalus/files/system/opt/daedalus/shared/policy.toml` + `daedalus/core/internal/policy/` | 单一事实源, Go 运行时读取; ALLOW_COMMANDS env 整体 REPLACE; 损坏 fail-closed 拒启 |
| Copilot 侧冻结副本 | `daedalus/plugin/copilot/policy.ts` | 与 `internal/shellpolicy` 同步义务; 契约由 `tests/deno/shellpolicy_contract.test.ts` 钉 |
| Audit hash chain | `daedalus/core/internal/audit/` + `daedalus/core/cmd/daedalus-audit/` | genesis `0`*64, syscall.Flock, sha256 链, Python 金样字节级兼容 (`internal/audit/testdata/golden.jsonl`) |
| Add systemd sandbox rule | `daedalus/files/system/usr/lib/systemd/system/daedalus-*.service.d/*.conf` | landlock.conf = seccomp/network; credentials.conf = LoadCredential |
| Reorder / add image build step | `daedalus/files/scripts/NN-name.sh` | 60-ai-middleware, 65-ai-safety, 70-daedalus-mcp-servers, 75-daedalus-copilot, 76-daedalus-plugin-gen |
| Add a new plugin/server | `daedalus/plugin/<id>/` (manifest + bin) → `just plugin-pack` → `76-daedalus-plugin-gen.sh` 渲染 systemd ExecStart | 宿主 `run-plugin`/`render-unit` 消费 manifest; sync via `just sync` |
| CI / image build | `just build` / `.github/workflows/build-daedalus.yml` | repo root context, runs `just build` (sync + podman build) + `just go-test`/`just test` + 零残留断言 |

## CODE MAP
CodeGraph indexes `tests/` and tracked root files (`base_image/` is gitignored). Source paths under `daedalus/{core,plugin,files}/` are authoritative.

| Symbol / Component | Type | Location | Role |
|--------------------|------|----------|------|
| `daedalus` CLI | wrapper script | `daedalus/files/system/usr/local/bin/daedalus` | 询问宿主构造启动命令 (不自己声明 Deno 权限), `$HOME` 占位展开后 exec |
| `daedalus-host` | Go binary | `daedalus/core/cmd/daedalus-host/` (镜像态 `usr/local/bin/daedalus-host`) | 插件 list/inspect/verify/run-plugin/render-unit; **非父进程, 零 spawn** |
| `daedalus-{audit,shell}` 安装态 | Go binaries | `daedalus/core/cmd/daedalus-{audit,shell}/` (镜像态 `usr/local/bin/`, `just plugin-pack` 安装, task 21) | copilot audit.ts/exec.ts 生产默认路径; 插件内 `bin/daedalus-shell` 副本归 systemd ExecStart, 两态互不替代 |
| `daedalus-plugin-pack` | Go binary | `daedalus/core/cmd/daedalus-plugin-pack/` | zip 打包器: checksums 注入 + manifest 规范化自摘要 + zip-slip 防线 |
| `internal/plugin` | Go pkg | `daedalus/core/internal/plugin/` | manifest schema 校验 + Pack/Extract/Verify/VerifyDir |
| `internal/policy` | Go pkg | `daedalus/core/internal/policy/` | policy.toml 严格加载 (ErrNotFound 哨兵 / LoadOrDefault / ALLOW_COMMANDS REPLACE) |
| `internal/shellpolicy` | Go pkg | `daedalus/core/internal/shellpolicy/` | 15 命令 / 4 bin 目录 / 路径规则权威实现 (CLEAN_ENV, 30s, rc 126/124) |
| `internal/pathguard` | Go pkg | `daedalus/core/internal/pathguard/` | fs 路径校验 (ALLOWED_DIRS 前缀边界, 空字节, realpath) |
| `internal/audit` | Go pkg | `daedalus/core/internal/audit/` | 哈希链审计库, Python 金样字节级兼容 (三种序列化模式) |
| `cmd/daedalus-{fs,shell,pkg,sysinfo}` | Go binaries | `daedalus/core/cmd/...` | 4 个 MCP stdio 能力服务器 (镜像态 = 插件内 `bin/`) |
| `policy.toml` | TOML | `daedalus/files/system/opt/daedalus/shared/policy.toml` | 安全策略单一事实源 ([shell]/[fs]/[audit]) |
| `daedalus.plugin.json` | manifest | `daedalus/plugin/{copilot,fs,shell,pkg,sysinfo}/` | 插件声明 (id/type/runtime/executable/entrypoint/permissions/tools) |
| `policy.ts` | TS module | `daedalus/plugin/copilot/policy.ts` | Proposal schema validation & frozen shellpolicy copy |
| `audit.ts` | TS module | `daedalus/plugin/copilot/audit.ts` | Hash-chained audit logging via daedalus-audit Go CLI |
| `llm.ts` | TS module | `daedalus/plugin/copilot/llm.ts` | OpenAI & Anthropic cloud LLM adapters with revision support |
| `exec.ts` | TS module | `daedalus/plugin/copilot/exec.ts` | JSON-RPC bridge spawning daedalus-shell Go MCP server with 40s watchdog |
| `main.ts` | TS entry | `daedalus/plugin/copilot/main.ts` | Copilot auto-execute default, opt-in interactive loop, edit/revise, REPL |
| `76-daedalus-plugin-gen.sh` | build script | `daedalus/files/scripts/76-daedalus-plugin-gen.sh` | 构建期从 manifest+policy.toml 渲染/自校验 systemd ExecStart + tools 交叉核对 |
| `75-daedalus-copilot.sh` | build script | `daedalus/files/scripts/75-daedalus-copilot.sh` | Sets directory and executable permissions for Copilot |

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
│  - policy.toml 单一事实源 (Go internal/policy 运行时读取, fail-closed)   │
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

## 2. OS Capability MCP Servers & Copilot CLI

Daedalus implements OS capability servers adhering to the Model Context Protocol specification. All servers are **Go static binaries** (`daedalus/core/cmd/daedalus-<cap>/`, official `modelcontextprotocol/go-sdk` stdio servers), packaged as `daedalus-plugin` (type=capability, runtime=native) under `/opt/daedalus/plugins/daedalus.<cap>/bin/`. The systemd units' ExecStart is rendered and self-verified at build time from the plugin manifests + `policy.toml` (`76-daedalus-plugin-gen.sh`), preserving per-service DynamicUser/Landlock drop-ins.

### 1. Filesystem Server (`daedalus.fs` plugin / `cmd/daedalus-fs`)
- **Purpose**: Safe, path-scoped file inspection and modifications.
- **Allowed Directory Model**: paths validated by `internal/pathguard` against `policy.toml [fs].allowed_dirs` (e.g., `/home`, `/tmp`, `/var/log`).
- **Path Traversal Protection**: Rejects relative paths, null bytes, `..`, and realpath results escaping the allowlist (prefix-boundary check prevents `/home` matching `/home2`).
- **Tools**: `read_file`, `write_file`, `list_dir`, `move_file`.

### 2. Shell Server (`daedalus.shell` plugin / `cmd/daedalus-shell`)
- **Purpose**: Controlled execution of safe, read-only and diagnostic system commands.
- **Argv Whitelisting**: 15-command `allowed_commands`, 4 binary dirs (`/usr/bin /bin /usr/sbin /sbin`), path-like arg rules (`internal/shellpolicy`, policy.toml `[shell]` section; `ALLOW_COMMANDS` env = whole-set REPLACE).
- **No Raw Shell Interpretation**: Direct `os/exec` argv execution without `/bin/sh` or `/bin/bash` wrapper.
- **Execution Limits**: 30s timeout, CLEAN_ENV, returncode 126 (denied) / 124 (timeout).
- **Tools**: `shell_exec`.

### 3. Package Management Server (`daedalus.pkg` plugin / `cmd/daedalus-pkg`)
- **Purpose**: Read-only package inspection (`internal/pkgquery`); regex pattern guards against injection.
- **Read-Only Inspection**: `rpm -q --info` with `dnf repoquery --info` fallback.
- **Tools**: `dnf_query`, `dnf_list_installed`.

### 4. System Information Server (`daedalus.sysinfo` plugin / `cmd/daedalus-sysinfo`)
- **Purpose**: Read-only OS and environment state discovery (`internal/sysinfo`: `/etc/os-release`, `/proc/cpuinfo`, `/proc/meminfo`, `ip -j addr`).
- **Tools**: `os_release`, `hardware_info`, `network_status`.

### 5. Daedalus Copilot CLI (`daedalus` / plugin `daedalus.copilot`, runtime=deno)
- **Purpose**: Natural language terminal companion that translates user intent into allowlisted system commands.
- **Auto-Execute Default**: Translated commands execute immediately after schema validation and re-validation against the 15-command allowlist plus path rules, with no y/n prompt. Human-in-the-loop confirmation is opt-in via `-i/--interactive`, which displays the proposal and prompts for `[y]es / [e]dit / [n]o (feedback) / [q]uit`. `-v/--verbose` shows the translated command before executing, `--dry-run` shows without executing, `-y/--yes` is a backward-compat alias for the default, and `-V` prints the version. Every decision still lands in the hash-chained audit log.
- **Zero Raw Execution**: The Copilot process never executes shell commands directly. It spawns the sandboxed `daedalus-shell` Go MCP server over stdio JSON-RPC.
- **Plugin Host Launch**: Copilot is installed as plugin `daedalus.copilot` (runtime=deno) under `/opt/daedalus/plugins/`; source lives in `daedalus/plugin/copilot/`. The `/usr/local/bin/daedalus` wrapper asks `daedalus-host run-plugin daedalus.copilot` to construct (not spawn) the launch command from the manifest, expands `$HOME` placeholders, then execs it:
  ```sh
  argv=$(/usr/local/bin/daedalus-host run-plugin daedalus.copilot -dir /opt/daedalus/plugins -- "$@")
  eval "set -- $argv"; # $HOME token expansion; then
  exec "$@"   # = deno run --allow-env --allow-net --allow-read=... --allow-write=... --allow-run=... main.ts
  ```
- **Configuration**: Resolves keys/models via CLI flags > Environment (`DAEDALUS_LLM_API_KEY`, etc.) > User config (`~/.config/daedalus/copilot.json`).

---

## 3. Plugin Format (`daedalus-plugin`) & Host

- **Manifest** `daedalus.plugin.json`: `id` (`^[a-z0-9]+(\.[a-z0-9]+)*$`), `name`, `version`, `type` (`copilot`|`capability`), `runtime` (`native`|`deno`), `executable` (相对路径, 需可执行位), `entrypoint` (宿主生成启动命令旗标; deno runtime 由宿主自动前置 `deno run`, manifest 不写 `run`), `permissions` (声明式), `tools` (能力插件暴露的 MCP 工具), `checksums` (Pack 注入的逐条目 sha256 + manifest 规范化自摘要)。
- **Packing**: `daedalus-plugin-pack -in <dir> -out x.zip` 可复现打包 (Fixed 1980-01-01 timestamps, 字典序); `-verify zip --keep <dest>` 解压即校验。zip-slip 九道防线 (`..` 段/绝对路径/符号链接/O_EXCL|O_NOFOLLOW/重复条目/zip-bomb LimitReader 等)。
- **Host** (`daedalus-host`): `list` / `inspect` / `verify` / `run-plugin <id>` (仅打印启动命令) / `render-unit <id>` (生成 systemd ExecStart 供构建期消费)。**宿主不是 MCP 服务器的父进程**——systemd 直接执行渲染出的 ExecStart, 保留每服务沙箱 drop-ins; degraded 插件被 run-plugin/render-unit 拒绝。所有宿主操作写 `host_*` 审计条目。
- **Build-time install only**: 插件随镜像内建; 无运行时联网下载安装。

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
`/opt/daedalus/shared/policy.toml` is the enforced runtime policy. Go servers load it at startup via `internal/policy`: missing → built-in `Default()` fallback (identical to constants, drift-tested); corrupted → fail-closed refusal to start. `76-daedalus-plugin-gen.sh` performs a build-time `DAEDALUS_POLICY_PATH` handshake check against the installed binaries and rejects unit/manifest drift.

---

## 5. Tamper-Evident Audit Logging

Every MCP tool invocation, Copilot translation, security rejection, confirmation, user edit, and host operation is logged to `/var/log/daedalus/audit.jsonl` (with unprivileged fallback to `$HOME/.local/share/daedalus/audit.jsonl`) using a cryptographic hash chain. Implementation: `daedalus/core/internal/audit/` (Go), exposed as the `daedalus-audit` CLI (`--identity/--tool/--args/--outcome/--log-path`, `verify` subcommand). All writers (servers, host, copilot `audit.ts` via `DAEDALUS_AUDIT_BIN`) go through this CLI — direct file writes are forbidden.

### Hash Chaining Specification
- `timestamp`, `identity`, `tool`, `args`, `policy_version`, `outcome`, `prev_hash`, `entry_hash`.
- Genesis hash is `0` * 64.
- $\text{entry\_hash} = \text{SHA256}(\text{timestamp} + \text{identity} + \text{tool} + \text{args\_str} + \text{outcome} + \text{prev\_hash})$
- `args_str` serialization is byte-identical to the deleted Python reference (`sort_keys` + `(",",":")` + ensure_ascii `\uXXXX` escaping, incl. microsecond==0 isoformat quirk) — pinned by golden vectors in `daedalus/core/internal/audit/testdata/golden.jsonl`.
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
- **注释语言规范（强制）**：本项目所有源代码、配置文件、构建脚本中的注释**必须使用中文**。包括但不限于：
  - Go 源码（`//` 与 `/* */`）注释与 godoc 注释
  - TypeScript / Deno 源码（`//` 与 `/* */`）注释
  - Shell / Bash 脚本（`#`）注释
  - systemd unit 文件（`#`）注释
  - justfile / Makefile 文件（`#`）注释
  - Docker / Containerfile（`#`）注释
  - YAML / JSON / TOML 配置文件中的注释字段（含 `policy.toml`、`daedalus.plugin.json` 的注释字段）
  - 现有代码文件中残留的英文注释必须翻译为中文，新提交也必须遵守此规则
  - 标识符、字符串字面量、API 协议字段（如 JSON 键、HTTP 头、协议名）、系统命令、URL、日志中可被外部解析的 token 等**不视为注释**，保留英文以保证互操作性
- **代码文件清单**：本项目涉及的主要代码文件包括：
  - **Go (core)**: `daedalus/core/cmd/daedalus-{fs,shell,pkg,sysinfo,audit,host,plugin-pack,smoke}/` + `daedalus/core/internal/{pathguard,shellpolicy,pkgquery,sysinfo,policy,audit,plugin,version}/`
  - **TS / Deno (copilot 源码)**: `daedalus/plugin/copilot/{policy,audit,exec,llm,main}.ts` + `daedalus.plugin.json`
  - **TS 测试 (镜像外)**: `tests/deno/{policy,audit,exec,llm,main}.test.ts` + `tests/deno/shellpolicy_contract.test.ts`
  - **Shell scripts**: `daedalus/files/scripts/{60-ai-middleware,65-ai-safety,70-daedalus-mcp-servers,75-daedalus-copilot,76-daedalus-plugin-gen}.sh`, `usr/local/bin/daedalus` wrapper, `sync-daedalus.sh`, `pack-copilot-plugin.sh`
  - **systemd units**: `daedalus-audit.service`, `daedalus-env.service`, `daedalus-{fs,shell,pkg,sysinfo}.service`, and each capability's `.service.d/{landlock,credentials}.conf` drop-ins
  - **Policy / manifests**: `daedalus/files/system/opt/daedalus/shared/policy.toml`, `daedalus/plugin/*/daedalus.plugin.json`
- **三层结构 (决策 23/24)**: `daedalus/core/` = Go 代码逻辑层 (镜像外); `daedalus/plugin/` = 官方预置插件源码侧定义; `daedalus/files/` = 镜像构建落位 (rootfs 镜像树 + 构建脚本)。镜像内 `opt/daedalus/plugins/` 是**构建产物 (安装态)**, `daedalus/plugin/` 是**源码侧定义**——两者绝不同步混用。
- **Vendor tree = `base_image/`**: Vendored upstream fork (gitignored). Updated from `daedalus/files/` (及 `daedalus/plugin/` → `base_image/plugin/`, 在 COPY 白名单外) via `sync-daedalus.sh` before container builds.
- **Go 依赖策略**: 仅入库 `daedalus/core/go.mod` + `go.sum`(版本与完整性锁);`daedalus/core/vendor/` 不入库,构建期 `go build` 自动从 module proxy 下载到 `GOMODCACHE`,首次构建需联网;`make verify` 仍可执行 `go mod verify` 校验 go.sum 完整性。
- **镜像安装态策略**: `daedalus/files/system/opt/daedalus/plugins/daedalus.*/bin/` 与 `daedalus/files/system/usr/local/bin/daedalus-{audit,host,shell}` 是 `just plugin-pack` 的 Go 编译产物副本,均**不入库**(同 `daedalus/core/bin/` 性质);开发者 clone 后必须 `just plugin-pack` 才能 build,与 `just sync` 一起完成 vendor 树重建。
- **Justfile workflow**: Use `just sync`, `just build`, `just test`, `just plugin-pack`, `just verify-image`, `just iso`, `just qemu` for all lifecycle actions.
- **Build step order**: Numbered scripts in `daedalus/files/scripts/` run in `sort --sort=human-numeric` order (`10-base` → … → `60-ai-middleware` → `65-ai-safety` → `70-daedalus-mcp-servers` → `75-daedalus-copilot` → `76-daedalus-plugin-gen` → `91-image-info` → `cleanup.sh`).
- **Go 唯一实现 (取代旧 Python+Deno parity 条款)**: fs/shell/pkg/sysinfo 与审计仅有 `daedalus/core` 的 Go 实现, 不得恢复 Python/Deno 服务器。Copilot 的 `policy.ts` 内联 15 命令/9 前缀/5 blocked **冻结副本**, 与 `internal/shellpolicy` 存在**双向同步义务** (改一侧必改另一侧); 该契约由 `tests/deno/shellpolicy_contract.test.ts` (ALLOW_COMMANDS REPLACE 语义 Go↔Deno 一致) 与 Go 侧钉子测试共同钉住。policy.toml ↔ `policy.Default()` ↔ shellpolicy/pathguard 常量的三点防漂移链见 `daedalus/core/internal/policy` 测试。
- **测试布局**: Deno 测试住仓库根 `tests/deno/` (镜像外), 相对导入指向 `daedalus/plugin/copilot/` 源码; Go 测试随包住 `daedalus/core/`。镜像 rootfs 内零测试文件。
- **Single Containerfile**: `Containerfile` at repository root is the sole build entry point. All `*.daedalus` aliases have been removed.

## ANTI-PATTERNS (THIS PROJECT)
- **NEVER** `shell=True` / `bash -c` / `sh -c` in subprocess.
- **NEVER** run a command outside the 15-entry `allowed_commands`, nor from a binary dir other than `/usr/bin /bin /usr/sbin /sbin`.
- **NEVER** accept relative paths, null bytes, or paths resolving outside allowlist.
- **NEVER** hardcode secrets in the image — credentials flow via `LoadCredential` or user-scoped config.
- **NEVER** directly execute arbitrary shell commands inside Copilot (`daedalus`) — always route through the sandboxed `daedalus-shell` MCP bridge.
- **NEVER** modify/insert/delete audit log lines or break the hash chain; NEVER write the audit file directly — always via the `daedalus-audit` CLI.
- **NEVER** let `daedalus-host` spawn or become the parent process of any MCP server (决策 16): the host only installs/discovers/verifies and *prints* the launch command; systemd executes it. Never add exec/spawn to `run-plugin`/`render-unit`.
- **NEVER** introduce a second policy source of truth: whitelist changes go through `shared/policy.toml` (runtime) with `policy.Default()` and the `shellpolicy`/`pathguard` constants updated in lockstep (drift tests fail otherwise); units must not carry a drifted `Environment=ALLOW_COMMANDS=`.
- **NEVER** leak source into the image rootfs: `daedalus/core/` (Go 源码 + 构建期下载到 GOMODCACHE 的模块) and `daedalus/plugin/` sources and any `*.test.ts`/`*.py`/`__pycache__`/`vendor` must never be rsync/COPY'd into `/opt` — only build products (`plugins/` 安装态, `usr/local/bin` binaries, `shared/policy.toml`) land there (`just verify-image` asserts this).
- **NEVER** hand-edit `daedalus/files/system/opt/daedalus/plugins/` (构建产物) — regenerate via `just plugin-pack` / `./pack-copilot-plugin.sh`.
- **NEVER** modify/commit changes to `base_image/` directly; modify `daedalus/{core,plugin,files}/` and run `just sync` / `./sync-daedalus.sh`.

## COMMANDS
```bash
# Justfile orchestration (Recommended)
just sync                  # Sync daedalus/{files,plugin} into base_image/ vendor tree
just build                 # sync + podman build Daedalus image (需 x86-64-v3 构建机; 本机 BLOCKED, 见 NOTES)
just test                  # 全量测试: cd daedalus/core && go test ./... + deno test --allow-all tests/deno/
just go-build              # CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -o bin/ ./cmd/... (全部 Go 二进制;依赖首次构建需联网从 module proxy 下载到 GOMODCACHE)
just go-test               # cd daedalus/core && go test ./...
just plugin-pack           # 构建 Go 二进制 → 打包 4 能力插件 → 安装态落 files/system/opt/daedalus/plugins/ + host/audit/shell 进 usr/local/bin
just copilot-plugin        # 打包 copilot 插件安装态 (./pack-copilot-plugin.sh)
just verify-image          # 镜像零残留断言 (find /opt 无 *.py/*.pyc/__pycache__/*.test.ts/go.mod/vendor; 需已构建镜像)
just iso                   # Build bootable ISO via bootc-image-builder
just qemu                  # Build qcow2 and test-boot in QEMU

# Go 直接命令
cd daedalus/core && go build ./... && go vet ./... && go test ./...

# Deno 测试(源码在 daedalus/plugin/copilot/,测试在仓库根 tests/deno/;相对导入跨目录指向源码)
deno test --allow-all tests/deno/
deno test --allow-all tests/deno/policy.test.ts
deno test --allow-all tests/deno/audit.test.ts
deno test --allow-all tests/deno/llm.test.ts
deno test --allow-all tests/deno/exec.test.ts
deno test --allow-all tests/deno/main.test.ts
deno test --allow-all tests/deno/shellpolicy_contract.test.ts  # Go↔Deno 跨语言契约(替代旧 py parity)

# 审计链验证 (金样向量重放)
cd daedalus/core && go run ./cmd/daedalus-audit verify --log-path internal/audit/testdata/golden.jsonl

# Validate image manually (v3 构建机)
bootc container lint
podman run --rm localhost/daedalus-os:latest /usr/local/bin/daedalus-host list   # 期望 5 插件 (copilot + 4 能力)
podman run --rm localhost/daedalus-os:latest cat /usr/lib/os-release
```

## NOTES
- `daedalus/{core,plugin,files}/` are the authoritative source directories tracked by git. `base_image/` is gitignored vendor.
- `sync-daedalus.sh` copies safely: systemd leg uses targeted stale-delete (only `daedalus-*`, upstream assets untouched); plugin leg uses `--delete --delete-excluded` (vendor mirrors source exactly); `base_image/plugin/` sits outside the Containerfile COPY whitelist so it never reaches rootfs.
- `base_image/` carries upstream dormant CI targeting upstream repos — do NOT mistake for Daedalus CI.
- Deno install (`65-ai-safety.sh`) pulls from `https://deno.land/install.sh` to `/usr/local/bin/deno` — used **only** by the copilot plugin now.
- **构建机限制 (todo 5/10/13 记录)**: `just build` 在本机不可执行——`almalinux-bootc:10` 要求 x86-64-v3 而本机 CPU 不支持; 镜像内断言 (`just verify-image`, in-image `daedalus-host list`, bootc lint, 76 脚本真实执行, copilot wrapper 全链路) 在 v3 构建机/CI 上补跑 (plan todo 18)。本机等价断言: `find daedalus/files/system base_image/files/system \( -name "*.py" -o -name "*.test.ts" -o -name "__pycache__" -o -name "go.mod" \) | wc -l` == 0。
- 镜像内 `/usr/local/bin/daedalus-{audit,shell}` (audit.ts/exec.ts 的生产默认路径) 已由 `just plugin-pack` 构建期安装 (task 21 修复断链缺口; 与 wrapper `--allow-run` 放行路径一致); `daedalus-host list` 等镜像内端到端断言仍在 v3 构建机补跑 (todo 18)。copilot 内向上回溯 dev 路径的回退链仅作开发态兜底。
