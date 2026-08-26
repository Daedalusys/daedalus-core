# opt/daedalus — MCP Capability Servers & Audit

**Runtime root:** `/opt/daedalus` in the image. Source root: `base_image/files/system/opt/daedalus/`. All behavior changes must land in BOTH the Python and Deno implementations (parity is tested).

## OVERVIEW
Four MCP capability servers (fs, shell, pkg, sysinfo) in two parallel implementations — `servers/*.py` (Python reference, `/opt/daedalus/venv`) and `deno/*.ts` (Deno production, path-scoped `--allow-*` flags) — plus the hash-chained audit logger `audit-log.py`.

## STRUCTURE
```
opt/daedalus/
├── audit-log.py        # sha256 chain, fcntl.flock LOCK_EX, genesis "0"*64, CLI
├── servers/            # Python reference impls (FastMCP)
│   ├── fs_server.py    # read/write/list/move — ALLOWED_DIRS /home /var/log /tmp
│   ├── shell_server.py # shell_exec — 15-cmd ALLOW_COMMANDS, CLEAN_ENV, 30s timeout
│   ├── pkg_server.py   # dnf_query/dnf_list_installed — read-only, PACKAGE_PATTERN regex
│   └── sysinfo_server.py # os_release/hardware_info/network_status — read-only /proc
└── deno/               # production impls (import.meta.main, JSON-RPC stdio)
    ├── fs_server.ts    # same allowlist, runServer/handleJsonRpcMessage
    ├── shell_server.ts # validateCommand/validatePath/validateArg, recordAudit
    └── shell_server.test.ts  # deno test
```

## WHERE TO LOOK
| Task | File |
|------|------|
| Change fs allowlist / path rules | `servers/fs_server.py` + `deno/fs_server.ts` (ALLOWED_DIRS, prefix boundary) |
| Change shell allowlist | `servers/shell_server.py` + `deno/shell_server.ts` (ALLOW_COMMANDS, ALLOWED_PATH_PREFIXES, BLOCKED_PATHS) |
| Audit chain / hashing | `audit-log.py` (`compute_entry_hash`, `log_audit`, `GENESIS_HASH`) |
| Tool schemas / annotations | per-server tool decorators (`readOnlyHint`/`destructiveHint`) |

## CONVENTIONS
- **Parity is law**: fs and shell each exist as `.py` + `.ts`. Keep behavior identical; tests assert allowlist-subset parity (`test_python_deno_behavior_parity`).
- **Validation order** (shell): command basename ∈ ALLOW_COMMANDS → binary dir ∈ `/usr/bin /bin /usr/sbin /sbin` → each arg: null-byte check, `--flag=/path` value checked, path-like args via `validatePath` against prefix allowlist + BLOCKED_PATHS. Rejection → returncode `126` + audit `allowed=false` (not a throw).
- **Paths**: absolute only, no null bytes, prefix boundary `/home/` (rejects `/home2`), symlink-resolved (`realpath`) before allowlist check.
- **Env**: spawn with minimal `CLEAN_ENV` (PATH + C.UTF-8), never inherit ambient env.
- **Deno flags** live in the systemd unit AND `examples/mcp_client_config.json` — keep in sync: fs `--allow-read=/home,/var/log,/tmp --allow-write=/home,/tmp`; shell `--allow-run=<15 bins>` `--allow-read=<prefixes>` `--allow-write=/var/log/daedalus`.

## ANTI-PATTERNS
- **NEVER** `shell=True` / `bash -c` / `sh -c` — `asyncio.create_subprocess_exec` (py), `new Deno.Command` argv (ts). `shell_server.py:6`.
- **NEVER** execute outside ALLOW_COMMANDS, or from a binary dir other than the four above. returncode `126`.
- **NEVER** accept relative paths / null bytes / out-of-allowlist paths (incl. `/etc/shadow`, `/root`, traversal `..`).
- **NEVER** add mutating tools to pkg/sysinfo — read-only (`rpm -q`/`dnf repoquery`, `/proc`, `/etc/os-release`).
- **NEVER** reorder the audit hash payload: `timestamp + identity + tool + args_str + outcome + prev_hash`.
- **NEVER** write audit lines except via `log_audit` (append + LOCK_EX); log is `chattr +a` at boot.

## COMMANDS
```bash
# Python integration tests (from repo root)
python3 -m pytest tests/
# Deno shell unit test
deno test --allow-all daedalus/files/system/opt/daedalus/deno/shell_server.test.ts
```
