# Diva-OS: AI-Native Operating System Architecture

Diva-OS is an immutable, atomic, and AI-native desktop operating system distribution built on top of AlmaLinux Bootc (KDE variant). It introduces a structured AI capability middleware layer based on the Model Context Protocol (MCP), enforcing rigorous kernel- and runtime-level security boundaries, tamper-evident audit logging, credential isolation, and atomic rollback guarantees.

---

## 1. Architecture Overview & Three Security Boundaries

Traditional systems grant AI agents or LLM clients unrestricted shell access, posing high risks of accidental corruption, prompt injection exploitation, or privilege escalation. Diva-OS replaces raw shell access with structured, typed, and isolated capability servers across three distinct security layers:

```
+-------------------------------------------------------------------------+
|                         LLM / AI Agent Client                          |
|         (e.g., Claude Desktop, VS Code Agent, Headless MCP Client)      |
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
│  - Cryptographic append-only audit trail (/var/log/diva/audit.jsonl)    │
│  - SHA-256 hash chaining + file locking (fcntl.flock)                   │
│  - AlmaLinux bootc immutable rootfs (composefs)                         │
│  - Atomic OS updates and one-click rollback (`bootc rollback`)          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 2. OS Capability MCP Servers

Diva-OS implements OS capability servers adhering to the Model Context Protocol specification. In development/testing, Python SDK implementations (`/opt/diva/servers/*.py`) provide rapid prototyping; in production, Deno runtime servers (`/opt/diva/deno/*.ts`) provide high performance and fine-grained runtime permission sandboxing.

The capability servers communicate via standard JSON-RPC over `stdio` and expose discrete tools:

### 1. Filesystem Server (`diva-fs` / `fs_server.ts`)
- **Purpose**: Safe, path-scoped file inspection and modifications.
- **Allowed Directory Model**: All paths are resolved against strict allowlists (e.g., `/home`, `/tmp`, `/var/log`).
- **Path Traversal Protection**: Rejects directory traversal attempts (`..`) and unauthorized root/system file modifications (`/etc`, `/usr`, `/boot`).
- **Tools**:
  - `read_file`: Reads file content within allowed directory boundaries.
  - `write_file`: Writes content to allowed writable paths.
  - `list_directory`: Lists directory entries with metadata.
  - `get_file_info`: Returns file size, modification timestamp, permissions, and file type.

### 2. Shell Server (`diva-shell` / `shell_server.ts`)
- **Purpose**: Controlled execution of safe, read-only and diagnostic system commands.
- **Argv Whitelisting**: Strict executable and argument pattern allowlists (e.g., `git`, `ip`, `df`, `free`, `uptime`, `ls`, `cat`, `grep`, `systemctl status`).
- **No Raw Shell Interpretation**: Direct process execution without `/bin/sh` or `/bin/bash` wrapper to eliminate command chaining (`&&`, `;`, `|`, `$(...)`) and prompt injection vulnerabilities.
- **Execution Limits**: Mandatory timeout limits (default 15s) and bounded stdout/stderr buffer limits to prevent resource exhaustion and hanging processes.
- **Tools**:
  - `run_command`: Executes a whitelisted command with argv array and timeout.

### 3. Package Management Server (`diva-pkg` / `pkg_server.py`)
- **Purpose**: Safe inspection and controlled lifecycle queries of packages.
- **Read-Only Inspection**: Wraps `rpm` and `dnf` queries.
- **Tools**:
  - `list_installed_packages`: Queries installed RPM packages and versions.
  - `search_package`: Searches package repositories for matching packages.
  - `package_info`: Returns summary, description, repository, and dependency details for a package.

### 4. System Information Server (`diva-sysinfo` / `sysinfo_server.py`)
- **Purpose**: Read-only OS and environment state discovery.
- **Isolation**: Read-only access to `/etc/os-release`, `/proc/cpuinfo`, `/proc/meminfo`, and hardware discovery interfaces.
- **Tools**:
  - `get_os_release`: Parses `/etc/os-release` and returns distribution metadata.
  - `get_hardware_info`: Returns CPU, RAM, and storage overview.
  - `get_network_info`: Returns active network interfaces and IP configurations.

---

## 3. Sandboxing & Defense-in-Depth Hardening

Diva-OS establishes multiple layers of process isolation and access restriction:

### Deno Runtime Fine-Grained Permissions
Production capability servers run on Deno with explicit permission flags, eliminating ambient system access:
```bash
deno run \
  --allow-read=/home,/var/log,/tmp \
  --allow-write=/home,/tmp \
  /opt/diva/deno/fs_server.ts
```
Any attempt to access ungranted resources (such as outbound network sockets or unauthorized filesystem paths) triggers an immediate runtime exception.

### systemd Service Sandboxing Profiles
All Diva service profiles (`/usr/lib/systemd/system/diva-*.service`) enforce zero-privilege defaults:
- `DynamicUser=yes`: Generates an ephemeral UID/GID allocated at start time, deleted on stop.
- `ProtectSystem=strict`: Mounts the entire `/usr`, `/boot`, `/etc`, and root hierarchy as read-only.
- `ProtectHome=read-only` or isolated via `PrivateTmp=yes`.
- `NoNewPrivileges=yes`: Disallows privilege escalation via setuid/setgid binaries.
- `RestrictSUIDSGID=yes`, `ProtectKernelModules=yes`, `ProtectKernelTunables=yes`, `ProtectControlGroups=yes`.
- Resource constraints: `CPUQuota=50%`, `MemoryMax=256M`, `IOWeight=100`.

### Linux Landlock & Seccomp
- **Seccomp (`SystemCallFilter`)**: Whitelists only safe system service calls (`@system-service`), explicitly denying `@privileged`, `@resources`, and `@obsolete`.
- **Memory Protection**: `MemoryDenyWriteExecute=yes` blocks runtime generation of executable code pages.
- **Network Family Lockdown**: Restricts socket families (`AF_UNIX AF_INET AF_INET6`) and restricts loopback interfaces (`RestrictNetworkInterfaces=lo`) for local capability endpoints.

---

## 4. Tamper-Evident Audit Logging

Every MCP tool invocation, parameter payload, execution outcome, and security rejection is logged to `/var/log/diva/audit.jsonl` using a cryptographic hash chain.

### Hash Chaining Specification
Each log entry is represented as a structured JSON object containing:
- `timestamp`: UTC ISO-8601 timestamp.
- `identity`: Identifier of caller (e.g., `agent-client`, `user-session`).
- `tool`: Invoked tool name.
- `args`: Canonical JSON serialization of tool arguments.
- `policy_version`: Policy version identifier.
- `outcome`: Call result (`success`, `denied`, `error`).
- `prev_hash`: SHA-256 hash of the preceding line (Genesis hash is `0` * 64).
- `entry_hash`: SHA-256 computed across the current payload + `prev_hash`:
  $$\text{entry\_hash} = \text{SHA256}(\text{timestamp} + \text{identity} + \text{tool} + \text{args\_str} + \text{outcome} + \text{prev\_hash})$$

```json
{
  "timestamp": "2026-08-26T12:00:00.000Z",
  "identity": "agent-default",
  "tool": "read_file",
  "args": {"path": "/home/user/document.txt"},
  "policy_version": "1.0",
  "outcome": "success",
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000000",
  "entry_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}
```

### Concurrency & Tamper Resistance
- **Concurrency Control**: Append operations acquire an exclusive file lock (`fcntl.flock(LOCK_EX)`) to guarantee atomic sequential hash continuity.
- **Service Initialization**: `diva-audit.service` initializes `/var/log/diva` permissions on boot.
- **Verification**: Any modification, insertion, or deletion of log lines breaks the cryptographic hash chain and is immediately detectable.

---

## 5. Secrets Isolation (`systemd LoadCredential`)

Diva-OS strictly forbids hardcoding API tokens, private keys, or passwords inside container images.

- **Credential Injection**: Secret credentials (e.g., LLM provider API tokens or MCP auth tokens) are managed using systemd's credential framework (`LoadCredential=diva_token:/etc/credstore/diva_token`).
- **Encrypted & Isolated Memory**: At runtime, systemd mounts credentials into a secure, service-private ramfs (`$CREDENTIALS_DIRECTORY/diva_token`), accessible only by the specific dynamic service user.
- **Image Portability**: Base container images contain only metadata definitions and fallback placeholders, keeping the underlying image clean and portable across environments.

---

## 6. Atomic OS Foundation & Rollback Guarantee

Diva-OS is constructed using container-native operating system principles (AlmaLinux Bootc):

### 1. Atomic Image Construction (`bootc`)
The operating system root filesystem is packaged and delivered as an OCI container image (`Containerfile` / `Containerfile.diva`). Build stages compile system scripts, install packages into `/usr`, configure systemd services, and execute `bootc container lint` to validate image integrity prior to deployment.

### 2. Composefs & Read-Only Immutability
At runtime, the operating system mounts `/usr` as a read-only composefs filesystem. Mutable state is confined strictly to `/etc` (configuration) and `/var` (persistent data and logs).

### 3. One-Click Rollback Mechanism
If a configuration change or autonomous task leads to an unstable system state:
```bash
# View deployment history and status
bootc status

# Roll back atomically to previous booted deployment
bootc rollback

# Reboot into the verified clean deployment
systemctl reboot
```
The rollback mechanism switches GRUB boot loader targets to the previous immutable deployment root, restoring exact system state without leaving orphaned packages or lingering file modifications.
