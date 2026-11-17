> **归档说明(2026-11-17)**:本文件写于 2026-10-11,其中「镜像无法构建」的结论**已过期**(daedalus-plugins v0.2.0 之后镜像三次 job 全绿,qcow2/ISO 已进 Release)。本目录的 `headless-test.sh` 已被 `tests/real-machine/` 下的探针套件取代,保留作历史对照。
> 现行事实源:`tests/real-machine/`、`tests/fleet/EVIDENCE/`、`.github/workflows/build.yml`。

# Daedalus latest-image deployment — findings

## TL;DR

The latest image **cannot be built** from `daedalus-core@main` because the image build
fails at stage 76 (`76-daedalus-plugin-gen.sh`). Root cause: cross-repo integration
break — core's `policy.toml` has 3 schema sections (`[ownership]`, `[confirmation]`,
`[diskclean]`) that the prebuilt `daedalus-plugins` release `v0.1.1` (2026-09-22)
does not recognize. The plugins SDK's `policy.Load` is fail-closed on unknown TOML
keys, so the plugin binaries refuse to start, and the build-time `tools/list`
handshake times out.

This blocks issue `#36` (P4 controller verification) end-to-end until either:

1. A new `daedalus-plugins` release is cut (the release workflow already pins the
   latest SDK — the only missing step is `workflow_dispatch` with `tag=v0.2.0`), **or**
2. Core's `policy.toml` is pruned to the schema the prebuilt plugins understand.

I did not push a branch / cut a release — those are shared-repo side effects that need
your OK. See *Recommended actions* below.

## Evidence

### 1. CI build failure (workflow_dispatch run 38095670347)

```
Build & Publish Daedalus Image (kde)  Build Daedalus Container Image  …

76-daedalus-plugin-gen: policy.toml 预检通过(15 项 shell 白名单,
DynamicUser 可读,objectmodel 启用 kinds:package service )

verify: daedalus.fs verification passed (all sha256 matched)

76-daedalus-plugin-gen: 错误: daedalus.fs: 二进制未应答 tools/list
(启动失败、30s 超时,或 DAEDALUS_POLICY_PATH 指向的策略被 Go 解析器拒绝):
/opt/daedalus/plugins/daedalus.fs/bin/daedalus-fs

Error: building at STEP "RUN …/build.sh 76": while running runtime:
exit status 1
```

### 2. Local reproduction (definitive — I ran the prebuilt binary locally)

Downloaded `daedalus.fs.plugin.zip` from `Daedalusys/daedalus-plugins@v0.1.1` and ran
its MCP `tools/list` handshake with two policies:

**(A) Core's current `files/system/opt/daedalus/shared/policy.toml`** (with all 3
sections):
```
daedalus-fs server error: policy load failed, refusing to start:
policy: …policy.toml 含未知键(fail-closed,拒绝拼写错误/第二事实源):
confirmation, confirmation.max_pending_tokens, confirmation.ttl_seconds,
diskclean, diskclean.allowed_write_dirs, diskclean.old_kernel_dir_pattern,
ownership, ownership.by_kind, ownership.by_kind.service, ownership.default
```

**(B) Same file minus `[ownership]`** (still has `[confirmation]` + `[diskclean]`):
```
daedalus-fs server error: policy load failed, refusing to start:
policy: …policy-noown.toml 含未知键(fail-closed,拒绝拼写错误/第二事实源):
confirmation, confirmation.max_pending_tokens, confirmation.ttl_seconds,
diskclean, diskclean.allowed_write_dirs, diskclean.old_kernel_dir_pattern
```

So `v0.1.1`'s SDK predates **all three** sections — only the workaround that prunes
`[confirmation]` + `[diskclean]` + `[ownership]` would let the prebuilt plugins
parse the policy. That's a non-starter for production.

### 3. Why no one noticed yet

- Last successful image build was `a13696bbe…` on **2026-09-26** (predates `#35`).
- Last push to main was run `38064421829` — `workflow_dispatch` for image build is
  gated to `feat/*` push / tag / manual dispatch; the auto push to main only runs the
  `test` job, which uses the in-tree SDK (same source as the policy) so the drift is
  invisible locally.
- A new `daedalus-plugins` release hasn't been cut since `v0.1.1` (2026-09-22), so no
  CI surface rebuilds the plugins against the new SDK.

### 4. Target machine capabilities (`Mastwet_Server` → `undef-dev`)

- Fedora Linux 44 KDE, kernel 6.19.10, systemd 259.
- podman 5.8.1 (rootless), `/dev/kvm` present, **no qemu installed**, **sudo password
  required** (no NOPASSWD).
- 475 GB disk free, 15 GB RAM.
- Connectivity to `ghcr.io` / `quay.io` works (`401` from a probe = reachable).
- Image pull: ~3:39 min for 7.21 GB (old image), same expected for the new one.

### 5. Headless runtime check (against the old image `a13696bbe`)

Container: `podman run --privileged --systemd=always --network=host
--cgroupns=host --tmpfs /run --tmpfs /tmp …`. systemd reached `degraded` after 40s.
Failed units are the usual suspects for a rootless container with no real
hardware (NM-wait-online, iscsid, auditd, mcelog, chronyd, etc.) — not relevant to
the P4 work.

| Key | Result | Detail |
|-----|--------|--------|
| A1 | (false-positive — symlink path resolves to itself because target missing) | dangling symlink, no unit file |
| A2 | FAIL | unit file `daedalus-host-controller.service` not in image (pre-P4) |
| B1 | FAIL | controller not in image |
| B2 | SKIP | no `daedalus-host.service` to compare (pre-P4) |
| B3 | FAIL | controller not loaded |
| D1 | FAIL | `daedalus-host: unknown subcommand drift` (subcommand added in P4) |
| D2 | FAIL | `daedalus-host: unknown subcommand ownership` (subcommand added in P4) |
| E1 | PASS | `daedalus-audit verify` returns `verified 0 entries; hash chain intact` |
| E2 | NOTE | `/var/log/daedalus/audit.jsonl` exists, 0 entries |
| F1 | (false-positive — `daedalus-smoke` not in `$PATH` in container) | n/a |

The "P4+" classification in my harness was a false positive (used `readlink -f` on a
non-existent path, which echoes the argument). The old image is genuinely pre-P4.

## Recommended actions (pick one)

**Option A — Cut a new `daedalus-plugins` release, then rebuild the image.** This is
the correct, non-invasive fix.

1. `gh workflow run release.yml --repo Daedalusys/daedalus-plugins
   -f tag=v0.2.0`  (workflow checks out latest SDK → binaries know about
   `[ownership]` / `[confirmation]` / `[diskclean]`).
2. After release completes: `gh workflow run build.yml --ref main -f VARIANT=kde
   -f PUSH=true`  (now succeeds because new plugins parse the policy).
3. `podman pull ghcr.io/daedalusys/daedalus-os:latest` on `Mastwet_Server`.
4. Re-run `headless-test.sh` for full issue `#36` coverage.

I'd be touching the public release list (`v0.2.0` tag). Requires your OK.

**Option B — Build the image locally on `Mastwet_Server` with a pruned
`policy.toml`.** Faster (no cross-repo coordination), but ships an artifact that
silently drops `[confirmation]` / `[diskclean]` protections until a proper plugins
release happens. Not recommended for any "real" deployment.

**Option C — Stay on the 2026-09-26 image.** Already done (above). Validates the
headless harness but doesn't exercise the P4 controller at all.

## Environment constraints on `Mastwet_Server` (for future runs)

- No passwordless sudo ⇒ rootless podman only. `--privileged --systemd=always`
  gives us systemd-as-PID-1 and the controller; `/usr` is read-only under the
  rootless overlay, so `dnf install` inside the container **will not work** for
  the C3 path of issue `#36`. If you want a clean C3 run, we need either NOPASSWD
  sudo on the target or to install qemu + boot the qcow2 under KVM (`/dev/kvm` is
  there, ready).
- Image pull over WAN takes ~4 min for 7 GB; plan accordingly.

## Files for follow-up

- `~/code/Daedalus/deploy-test/headless-test.sh` — headless harness (issue `#36`
  coverage; auto-skips controller items when the image is pre-P4).
- This report.