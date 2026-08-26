# Daedalus: AI-Native Operating System Knowledge Base

**Generated:** 2026-08-27
**Commit:** a4f76c2
**Branch:** (detached HEAD)

## OVERVIEW
Immutable, atomic, AI-native desktop OS on AlmaLinux Bootc (KDE variant). Adds a Model Context Protocol (MCP) capability-middleware layer with three security boundaries (model/capability, enforcement/sandboxing, evidence/verification), tamper-evident audit logging, systemd credential isolation, and atomic rollback. Includes the `daedalus` AI Copilot CLI for natural language command assistance with strict allowlist enforcement, opt-in interactive confirmation (default auto-execute), and cryptographic audit trails. Stack: bootc/OSTree + systemd units + Deno TS MCP servers (production) + Python MCP servers (reference) + Just orchestration.

## STRUCTURE
```
Daedalus/
├── Containerfile            # ACTIVE root build recipe (2-stage, bootc lint)
├── justfile                 # Unified orchestrator (just sync, build, test, iso, qemu)
├── sync-daedalus.sh             # Syncs tracked daedalus/ source tree into base_image vendor tree
├── daedalus/                    # ★ TRACKED SOURCE ROOT for all Daedalus-owned files
│   └── files/
│       ├── scripts/         # Daedalusbuild steps (60, 65, 70, 75-daedalus-copilot.sh)
│       └── system/          # Rootfs-mirror overlay
│           ├── opt/daedalus/    # MCP servers (deno/prod + servers/py ref + copilot/ + audit-log.py)
│           ├── usr/lib/systemd/system/   # daedalus-*.service + .service.d drop-ins
│           ├── usr/local/bin/            # daedalus CLI wrapper script
│           └── etc/credstore/            # systemd LoadCredential placeholders
├── base_image/              # VENDORED AlmaLinux/atomic-desktop fork (gitignored, own .git)
│   └── files/               # Target for sync-daedalus.sh before container build
├── examples/                # mcp_client_config.json (6 server entries)
├── tests/                   # test_mcp_integration.py, test_copilot_integration.py
└── .github/workflows/       # build-daedalus.yml (runs `just build` from repo root)
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Modify filesystem MCP behavior | `daedalus/files/system/opt/daedalus/servers/fs_server.py` + `deno/fs_server.ts` | BOTH impls must change (parity tested) |
| Modify shell MCP allowlist | `.../servers/shell_server.py` + `.../deno/shell_server.ts` | 15-command ALLOW_COMMANDS, CLEAN_ENV |
| Modify Daedalus Copilot CLI | `daedalus/files/system/opt/daedalus/deno/copilot/` + `.../usr/local/bin/daedalus` | policy, audit, llm, exec, main orchestration |
| Add systemd sandbox rule | `daedalus/files/system/usr/lib/systemd/system/daedalus-*.service.d/*.conf` | landlock.conf = seccomp/network; credentials.conf = LoadCredential |
| Reorder / add image build step | `daedalus/files/scripts/NN-name.sh` | 60-ai-middleware, 65-ai-safety, 70-daedalus-mcp-servers, 75-daedalus-copilot |
| Add a new MCP server | `opt/daedalus/servers/` (py) or `opt/daedalus/deno/` (ts) + new daedalus-*.service + drop-ins | sync via `just sync` / `./sync-daedalus.sh` |
| Audit hash chain | `daedalus/files/system/opt/daedalus/audit-log.py` | genesis `0`*64, fcntl.flock, sha256 chain |
| CI / image build | `just build` / `.github/workflows/build-daedalus.yml` | repo root context, runs `just build` (sync + podman build) |

## CODE MAP
CodeGraph indexes `tests/` and tracked root files (`base_image/` is gitignored). Source paths in `daedalus/files/` are authoritative.

| Symbol / Component | Type | Location | Role |
|--------------------|------|----------|------|
| `daedalus` CLI | wrapper script | `daedalus/files/system/usr/local/bin/daedalus` | Sandboxed Deno entry point for Copilot |
| `policy.ts` | TS module | `.../copilot/policy.ts` | Proposal schema validation & daedalus-shell validator re-exports |
| `audit.ts` | TS module | `.../copilot/audit.ts` | Hash-chained audit logging via audit-log.py CLI |
| `llm.ts` | TS module | `.../copilot/llm.ts` | OpenAI & Anthropic cloud LLM adapters with revision support |
| `exec.ts` | TS module | `.../copilot/exec.ts` | JSON-RPC bridge spawning daedalus-shell MCP server with 40s watchdog |
| `main.ts` | TS entry | `.../copilot/main.ts` | Copilot auto-execute default, opt-in interactive loop, edit/revise, REPL |
| `75-daedalus-copilot.sh` | build script | `daedalus/files/scripts/75-daedalus-copilot.sh` | Sets directory and executable permissions for Copilot |
| MCP tools (read_file/write_file/list_dir/move_file) | py server | `.../servers/fs_server.py` | fs capability |
| MCP tools (shell_exec) | py server | `.../servers/shell_server.py` | shell capability |
| MCP tools (dnf_query/dnf_list_installed) | py server | `.../servers/pkg_server.py` | read-only pkg |
| MCP tools (os_release/hardware_info/network_status) | py server | `.../servers/sysinfo_server.py` | read-only sysinfo |
| `log_audit` / `compute_entry_hash` | py lib | `.../opt/daedalus/audit-log.py` | hash-chained audit + CLI |
| `runServer` / `handleJsonRpcMessage` | ts server | `.../deno/fs_server.ts` | Deno fs (import.meta.main) |
| `runServer` / `handleMessage` / `validateCommand` | ts server | `.../deno/shell_server.ts` | Deno shell |

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
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Enforcement & Sandboxing Boundary (Enforcement Boundary)       │
│  - Deno Runtime Granular Permissions (--allow-read, --allow-write)      │
│  - systemd Hardening (DynamicUser=yes, ProtectSystem=strict)            │
│  - Landlock LSM (Kernel-level path-scoped access restriction)           │
│  - Seccomp System Call Filters (@system-service ~@privileged)           │
│  - systemd LoadCredential= (Secrets never stored in container image)    │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 3: Evidence & Verification Boundary (Evidence Boundary)           │
│  - Cryptographic append-only audit trail (/var/log/daedalus/audit.jsonl)    │
│  - SHA-256 hash chaining + file locking (fcntl.flock)                   │
│  - AlmaLinux bootc immutable rootfs (composefs)                         │
│  - Atomic OS updates and one-click rollback (`bootc rollback`)          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 2. OS Capability MCP Servers & Copilot CLI

Daedalus implements OS capability servers adhering to the Model Context Protocol specification. In development/testing, Python SDK implementations (`/opt/daedalus/servers/*.py`) provide rapid prototyping; in production, Deno runtime servers (`/opt/daedalus/deno/*.ts`) provide high performance and fine-grained runtime permission sandboxing.

### 1. Filesystem Server (`daedalus-fs` / `fs_server.ts`)
- **Purpose**: Safe, path-scoped file inspection and modifications.
- **Allowed Directory Model**: All paths are resolved against strict allowlists (e.g., `/home`, `/tmp`, `/var/log`).
- **Path Traversal Protection**: Rejects directory traversal attempts (`..`) and unauthorized root/system file modifications (`/etc`, `/usr`, `/boot`).
- **Tools**: `read_file`, `write_file`, `list_directory`, `get_file_info`.

### 2. Shell Server (`daedalus-shell` / `shell_server.ts`)
- **Purpose**: Controlled execution of safe, read-only and diagnostic system commands.
- **Argv Whitelisting**: Strict executable and argument pattern allowlists (15 commands: `git`, `ip`, `df`, `free`, `uptime`, `ls`, `cat`, `grep`, `systemctl status`, etc.).
- **No Raw Shell Interpretation**: Direct process execution without `/bin/sh` or `/bin/bash` wrapper.
- **Execution Limits**: 15-30s timeout limits and bounded stdout/stderr buffer limits.
- **Tools**: `run_command` / `shell_exec`.

### 3. Package Management Server (`daedalus-pkg` / `pkg_server.py`)
- **Purpose**: Safe inspection and controlled lifecycle queries of packages.
- **Read-Only Inspection**: Wraps `rpm` and `dnf` queries.
- **Tools**: `list_installed_packages`, `search_package`, `package_info`.

### 4. System Information Server (`daedalus-sysinfo` / `sysinfo_server.py`)
- **Purpose**: Read-only OS and environment state discovery (`/etc/os-release`, `/proc/cpuinfo`, `/proc/meminfo`).
- **Tools**: `get_os_release`, `get_hardware_info`, `get_network_info`.

### 5. Daedalus Copilot CLI (`daedalus` / `copilot/main.ts`)
- **Purpose**: Natural language terminal companion that translates user intent into allowlisted system commands.
- **Auto-Execute Default**: Translated commands execute immediately after schema validation and re-validation against the 15-command allowlist plus path rules, with no y/n prompt. Human-in-the-loop confirmation is opt-in via `-i/--interactive`, which displays the proposal and prompts for `[y]es / [e]dit / [n]o (feedback) / [q]uit`. `-v/--verbose` shows the translated command before executing, `--dry-run` shows without executing, `-y/--yes` is a backward-compat alias for the default, and `-V` prints the version. Every decision still lands in the hash-chained audit log.
- **Zero Raw Execution**: The Copilot process never executes shell commands directly. It spawns the sandboxed `daedalus-shell` Deno MCP server over stdio JSON-RPC.
- **Strict Sandbox Flags**: Runs via `/usr/local/bin/daedalus` wrapper:
  ```sh
  exec /usr/local/bin/deno run \
    --allow-env \
    --allow-net \
    --allow-read=/opt/daedalus,/home,/var/log,/tmp,/proc,/sys,/etc/os-release,/usr/lib/os-release,/etc/fedora-release,/etc/almalinux-release,"$HOME"/.config/daedalus \
    --allow-write=/var/log/daedalus,/tmp,"$HOME"/.local/share/daedalus \
    --allow-run=/usr/local/bin/deno,/opt/daedalus/venv/bin/python \
    /opt/daedalus/deno/copilot/main.ts "$@"
  ```
- **Configuration**: Resolves keys/models via CLI flags > Environment (`DAEDALUS_LLM_API_KEY`, etc.) > User config (`~/.config/daedalus/copilot.json`).

---

## 3. Sandboxing & Defense-in-Depth Hardening

Daedalus establishes multiple layers of process isolation and access restriction:

### Deno Runtime Fine-Grained Permissions
Production capability servers run on Deno with explicit permission flags:
```bash
deno run \
  --allow-read=/home,/var/log,/tmp \
  --allow-write=/home,/tmp \
  /opt/daedalus/deno/fs_server.ts
```

### systemd Service Sandboxing Profiles
All Daedalusservice profiles (`/usr/lib/systemd/system/daedalus-*.service`) enforce zero-privilege defaults:
- `DynamicUser=yes`, `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp=yes`.
- `NoNewPrivileges=yes`, `RestrictSUIDSGID=yes`, `ProtectKernelModules=yes`, `ProtectKernelTunables=yes`, `ProtectControlGroups=yes`.
- Resource constraints: `CPUQuota=50%`, `MemoryMax=256M`, `IOWeight=100`.

### Linux Landlock & Seccomp
- **Seccomp (`SystemCallFilter`)**: Whitelists only safe system service calls (`@system-service`), explicitly denying `@privileged`, `@resources`, and `@obsolete`.
- **Memory Protection**: `MemoryDenyWriteExecute=yes`.

---

## 4. Tamper-Evident Audit Logging

Every MCP tool invocation, Copilot translation, security rejection, confirmation, and user edit is logged to `/var/log/daedalus/audit.jsonl` (with unprivileged fallback to `$HOME/.local/share/daedalus/audit.jsonl`) using a cryptographic hash chain.

### Hash Chaining Specification
- `timestamp`, `identity`, `tool`, `args`, `policy_version`, `outcome`, `prev_hash`, `entry_hash`.
- Genesis hash is `0` * 64.
- $\text{entry\_hash} = \text{SHA256}(\text{timestamp} + \text{identity} + \text{tool} + \text{args\_str} + \text{outcome} + \text{prev\_hash})$
- Concurrency: `fcntl.flock(LOCK_EX)` on append.

---

## 5. Secrets Isolation (`systemd LoadCredential`)

Daedalus strictly forbids hardcoding API tokens, private keys, or passwords inside container images.
- System services receive secrets at runtime via `LoadCredential=daedalus_token:/etc/credstore/daedalus_token`.
- User-level tools (Copilot) read credentials from environment variables or secure user configuration (`~/.config/daedalus/copilot.json`).

---

## 6. Atomic OS Foundation & Rollback Guarantee

- **Atomic Image Construction (`bootc`)**: Root filesystem built as OCI container image (`Containerfile`), validated with `bootc container lint`.
- **Composefs & Read-Only Immutability**: `/usr` is mounted as read-only composefs; state limited to `/etc` and `/var`.
- **One-Click Rollback**: `bootc rollback` restores previous booted deployment atomically.

---

## CONVENTIONS
- **注释语言规范（强制）**：本项目所有源代码、配置文件、构建脚本中的注释**必须使用中文**。包括但不限于：
  - TypeScript / Deno 源码（`//` 与 `/* */`）注释
  - Python 源码（`#` 与 `"""docstring"""`）注释
  - Shell / Bash 脚本（`#`）注释
  - systemd unit 文件（`#`）注释
  - justfile / Makefile 文件（`#`）注释
  - Docker / Containerfile（`#`）注释
  - YAML / JSON / TOML 配置文件中的注释字段
  - docstring 与函数/方法/类的 JSDoc / Python docstring
  - 现有代码文件中残留的英文注释必须翻译为中文，新提交也必须遵守此规则
  - 标识符、字符串字面量、API 协议字段（如 JSON 键、HTTP 头、协议名）、系统命令、URL、日志中可被外部解析的 token 等**不视为注释**，保留英文以保证互操作性
- **代码文件清单**：本项目涉及的主要代码文件包括：
  - **TS / Deno (copilot)**: policy.ts, audit.ts, exec.ts, llm.ts, main.ts, shell_server.ts, fs_server.ts (and their .test.ts)
  - **Python (servers + tests)**: servers/{fs,shell,pkg,sysinfo}_server.py, audit-log.py, tests/{test_mcp,test_copilot}_integration.py
  - **Shell scripts**: daedalus/files/scripts/60-ai-middleware.sh, 65-ai-safety.sh, 70-daedalus-mcp-servers.sh, 75-daedalus-copilot.sh, /usr/local/bin/daedalus wrapper, sync-daedalus.sh
  - **systemd units**: daedalus-audit.service, daedalus-env.service, daedalus-fs.service, daedalus-fs-deno.service, daedalus-pkg.service, daedalus-shell.service, daedalus-shell-deno.service, daedalus-sysinfo.service, and all `.service.d/*.conf` drop-ins
- **Source tree = `daedalus/files/`**: All Daedalus-owned scripts, services, servers, and wrappers are tracked under `daedalus/files/`.
- **Vendor tree = `base_image/files/`**: Vendored upstream fork (gitignored). Updated from `daedalus/files/` via `sync-daedalus.sh` before container builds.
- **Justfile workflow**: Use `just sync`, `just build`, `just test`, `just iso`, `just qemu` for all lifecycle actions.
- **Build step order**: Numbered scripts in `files/scripts/` run in `sort --sort=human-numeric` order (`10-base` → `60-ai-middleware` → `65-ai-safety` → `70-daedalus-mcp-servers` → `75-daedalus-copilot` → `91-image-info` → `cleanup.sh`).
- **Python + Deno parity**: Behavior changes to fs/shell servers must land in BOTH `servers/*.py` and `deno/*.ts`.
- **Single Containerfile**: `Containerfile` at repository root is the sole build entry point. All `*.daedalus` aliases have been removed.

## ANTI-PATTERNS (THIS PROJECT)
- **NEVER** `shell=True` / `bash -c` / `sh -c` in subprocess.
- **NEVER** run a command outside the 15-entry `ALLOW_COMMANDS`, nor from a binary dir other than `/usr/bin /bin /usr/sbin /sbin`.
- **NEVER** accept relative paths, null bytes, or paths resolving outside allowlist.
- **NEVER** hardcode secrets in the image — credentials flow via `LoadCredential` or user-scoped config.
- **NEVER** directly execute arbitrary shell commands inside Copilot (`daedalus`) — always route through the sandboxed `daedalus-shell` MCP bridge.
- **NEVER** modify/insert/delete audit log lines or break the hash chain.
- **NEVER** commit changes to `base_image/` directly; modify `daedalus/files/` and run `just sync` / `./sync-daedalus.sh`.

## COMMANDS
```bash
# Justfile orchestration (Recommended)
just sync                  # Sync daedalus/files into base_image/files
just build                 # Sync + build Daedalus image (podman build)
just test                  # Run test suite (Deno + pytest)
just iso                   # Build bootable ISO via bootc-image-builder
just qemu                  # Build qcow2 and test-boot in QEMU

# Test MCP and Copilot integration (Python)
python3 -m pytest tests/
python3 -m unittest tests/test_mcp_integration.py

# Deno unit tests
deno test --allow-all daedalus/files/system/opt/daedalus/deno/
deno test --allow-all daedalus/files/system/opt/daedalus/deno/shell_server.test.ts
deno test --allow-all daedalus/files/system/opt/daedalus/deno/copilot/policy.test.ts
deno test --allow-all daedalus/files/system/opt/daedalus/deno/copilot/audit.test.ts
deno test --allow-all daedalus/files/system/opt/daedalus/deno/copilot/llm.test.ts
deno test --allow-all daedalus/files/system/opt/daedalus/deno/copilot/exec.test.ts
deno test --allow-all daedalus/files/system/opt/daedalus/deno/copilot/main.test.ts

# Validate image manually
bootc container lint
podman run --rm localhost/daedalus-os:latest cat /usr/lib/os-release
podman run --rm localhost/daedalus-os:latest bootc --version
```

## NOTES
- `daedalus/files/` is the authoritative source directory tracked by git. `base_image/` is gitignored vendor.
- `sync-daedalus.sh` copies files safely without deleting upstream vendor assets in shared directories (like systemd).
- `base_image/` carries upstream dormant CI targeting upstream repos — do NOT mistake for DaedalusCI.
- Deno install (`65-ai-safety.sh`) pulls from `https://deno.land/install.sh` to `/usr/local/bin/deno`.
