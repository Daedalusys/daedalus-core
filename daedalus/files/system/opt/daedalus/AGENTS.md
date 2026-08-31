# opt/daedalus — 插件安装态运行时根（Go 宿主 + Deno copilot 命令顾问插件）

**Runtime root:** `/opt/daedalus` in the image. Source root: `daedalus/files/system/opt/daedalus/`.

## OVERVIEW

todo 11 三层目录迁移后，本目录只保留**插件安装态** `plugins/`（构建产物，
由 `just plugin-pack` 与 `./scripts/pack-copilot-plugin.sh` 经 Pack→Verify 生成，manifest 含 checksums）
与**运行时策略单一事实源** `shared/policy.toml`（todo 12，Go 服务器经 internal/policy 启动时读取）。
Daedalus Copilot（command advisor / 命令顾问，非 agent：生成命令 + L0/L1/L2 风险标注，
仅 L0 可经沙箱执行，其余仅展示由用户手动执行）的 **Deno 源码态**已迁出镜像树，住进 `daedalus/plugin/copilot/`；
镜像树内的旧 `deno/` 子树（todo 5 前的能力服务器与 copilot 源目录）已整体删除。
四个 OS 能力服务器（fs / shell / pkg / sysinfo）均以**插件内二进制**
（`plugins/daedalus.<cap>/bin/daedalus-<cap>`，由 `daedalus/core/` 构建的 Go 静态二进制）
安装并由 systemd 直接执行；宿主 `daedalus-host` 安装于 `/usr/local/bin/`；
哈希链审计 CLI（`daedalus-audit`，Go）源码在 `daedalus/core/cmd/daedalus-audit/`，
copilot 运行期依赖的 `daedalus-audit` 与 `daedalus-shell` 两个二进制由 `just plugin-pack`
安装到 `/usr/local/bin/`（task 21；即 audit.ts/exec.ts 的生产默认路径，
与 copilot wrapper `--allow-run` 放行一致；镜像内端到端验证为 v3 构建机项）。

## STRUCTURE

```
opt/daedalus/
├── plugins/                 # 宿主 daedalus-host 的发现/校验根（构建产物，勿手改）
│   ├── daedalus.copilot/    # Copilot 插件安装态（5 个 .ts + manifest 带 checksums）
│   ├── daedalus.fs/         # 能力插件安装态（manifest + bin/daedalus-fs）
│   ├── daedalus.shell/
│   ├── daedalus.pkg/
│   └── daedalus.sysinfo/
└── shared/
    └── policy.toml          # 安全策略单一事实源（[shell]/[fs]/[audit]，Go internal/policy 运行时读取）
```

源码侧对应关系（三层结构，决策 23/24）：

| 层 | 位置 | 内容 |
|----|------|------|
| 代码逻辑 | `daedalus/core/` | Go 模块（host/audit/能力服务器/打包器） |
| 预置插件 | `daedalus/plugin/` | copilot 源码 + 5 个 manifest + 能力 bin/ |
| 镜像构建 | `daedalus/files/` | rootfs 落位：本目录（安装态）+ systemd + wrapper + scripts |

## WHERE TO LOOK

| Task | Location |
|------|----------|
| 运行时策略单一事实源（强制执行值） | 本目录 `shared/policy.toml` + `daedalus/core/internal/policy/`（Go 严格加载, 损坏 fail-closed） |
| Shell 白名单 / 路径规则（权威实现） | `daedalus/core/internal/shellpolicy/`（Go） |
| Copilot 侧白名单（冻结副本） | `daedalus/plugin/copilot/policy.ts` —— 改 Go 侧时必须同步 |
| Copilot CLI 源码 / 单元测试 | `daedalus/plugin/copilot/`（源码）+ 仓库根 `tests/deno/`（测试, todo 14 迁出镜像树） |
| 审计哈希链算法 | `daedalus/core/internal/audit/`（Go，与 Python 金样字节级兼容） |
| 能力服务器行为（fs/shell/pkg/sysinfo） | `daedalus/core/cmd/daedalus-<cap>/`（Go） |
| 插件格式说明 | `daedalus/plugin/README.md` + `daedalus/core/internal/plugin/` |
| 单元 ExecStart 渲染/自校验（构建期） | `daedalus/files/scripts/76-daedalus-plugin-gen.sh`（manifest + policy.toml → unit, tools 交叉核对） |
| 沙箱 / systemd 集成 | `daedalus/files/system/usr/lib/systemd/system/daedalus-*.service`（ExecStart 指向插件内二进制） |

## CONVENTIONS

- **单一事实源 = Go**：策略与审计的行为规格以 `daedalus/core` 为准；
  `policy.ts` 中的 15 命令 / 9 前缀 / 5 blocked 常量是有意保留的冻结副本，
  使 copilot 进程内校验与 `daedalus-shell` 二进制零偏离。
- **白名单变更走三点链**：`shared/policy.toml`（运行时强制执行）↔ `policy.Default()`
  （缺失兜底）↔ shellpolicy/pathguard 常量（兼容默认），三处同步改，漂移由防漂移测试拒绝。
- **宿主非父进程（决策 16）**：`daedalus-host` 只安装/发现/校验并**打印**启动命令，
  绝不 spawn；systemd 按 `76-daedalus-plugin-gen.sh` 渲染的 ExecStart 直接执行能力服务器。
- **Copilot 绝不直接执行命令**：一律 spawn `daedalus-shell`（stdio JSON-RPC，
  MCP `initialize` → `notifications/initialized` → `tools/call shell_exec`）。
- **审计一律经 `daedalus-audit` CLI**：禁止 Deno 文件系统 API 直写审计文件；
  环境变量 `DAEDALUS_AUDIT_BIN` 可覆盖二进制路径（默认 `/usr/local/bin/daedalus-audit`）。
- **安装态 = 构建产物**：本目录下任何文件不得手改；变更走
  `just plugin-pack` / `./scripts/pack-copilot-plugin.sh` 重新 Pack→Verify。
- **中文注释强制**（见仓库根 CONVENTIONS）。

## ANTI-PATTERNS

- **NEVER** 在本目录恢复 copilot 源码（`deno/` 已废弃，源码只住 `daedalus/plugin/copilot/`）。
- **NEVER** 在 copilot 内用 `sh -c` / 拼接 shell 字符串执行命令。
- **NEVER** 单独修改 `policy.ts` 冻结副本而不动 `internal/shellpolicy`（反之亦然）。
- **NEVER** 改动审计哈希链载荷顺序：`timestamp + identity + tool + args_str + outcome + prev_hash`。
- **NEVER** 把 Python/Deno 能力服务器实现加回来——Go 二进制是唯一实现。

## COMMANDS

```bash
# Copilot 单元测试（Deno,源码在 daedalus/plugin/copilot/、测试在仓库根 tests/deno/,todo 14 迁出;从仓库根运行）
deno test --allow-all tests/deno/
# Go 工作区测试（能力服务器 + 审计 + 策略 + 宿主 + 打包器）
cd daedalus/core && go test ./...
# 重新生成 copilot 安装态（打包源 = daedalus/plugin/copilot/）
./scripts/pack-copilot-plugin.sh
```

## 源码树内调试（dev-install 替代形态 — 0 安装）

> 本段由 plan `daedalus-dev-mode`（todo 2）增量追加：记录如何不安装、不触碰镜像安装态 `/opt/daedalus/plugins`，直接用宿主二进制对插件做检视/校验/打印启动命令。以下命令均已实际执行验证。

### 用途与已验证用法

`daedalus-host` 支持用 `-dir` 旗标指向任意插件目录（优先级：`-dir` 旗标 > `DAEDALUS_PLUGIN_DIR` 环境变量 > 镜像默认 `/opt/daedalus/plugins`），五个子命令 `list` / `inspect` / `verify` / `run-plugin` / `render-unit` 全部适用。两侧形态：

- 镜像内（安装态）：宿主在 `/usr/local/bin/daedalus-host`，插件在 `/opt/daedalus/plugins`，直接 `daedalus-host list` 即可（期望 5 插件全 ok）。
- 仓库侧（源码树，0 安装）：源码侧 `daedalus/plugin/<短名>/`（`fs`、`shell`、`pkg`、`sysinfo`、`copilot`）的目录名与 manifest id（`daedalus.fs` 等）不一致，且源码 manifest 不含 checksums。因此直接 `-dir daedalus/plugin` 时 `list` 能运行但全部标 degraded，`inspect`/`verify`/`run-plugin`/`render-unit` 会被 id 一致性防线拒绝。完整可用的源码树形态是先打包再解包（打包会注入 checksums），从仓库根执行：

```bash
# 1) 解包构建产物（zip 内 checksums 与内容自洽；改源码后需重跑 just plugin-pack 刷新 zip）
plugdir=$(mktemp -d)
for p in fs shell pkg sysinfo copilot; do
  daedalus/core/bin/daedalus-plugin-pack -verify "daedalus/core/bin/daedalus.$p.plugin.zip" --keep "$plugdir/daedalus.$p"
done
# 2) 五个子命令全部可用（以 daedalus.fs 为例，其余插件同理）
daedalus/core/bin/daedalus-host -dir "$plugdir" list                  # 5 插件全 ok
daedalus/core/bin/daedalus-host -dir "$plugdir" inspect daedalus.fs   # manifest 详情 + 完整性
daedalus/core/bin/daedalus-host -dir "$plugdir" verify daedalus.fs    # sha256 校验
daedalus/core/bin/daedalus-host -dir "$plugdir" run-plugin daedalus.fs   # 仅打印启动命令
daedalus/core/bin/daedalus-host -dir "$plugdir" render-unit daedalus.fs  # 仅输出 systemd 片段
```

### 与 `just dev-install` 的关系

- 只需检视 manifest、校验完整性或打印启动命令：用上面的 0 安装形态，不落任何东西到系统路径。
- 需要一套可运行的安装（binaries + 解包插件，例如装到 `~/.local` 免 sudo）：用 `just dev-install <prefix>`；装好后可用 `just host-list` 一键列插件。这两个 recipe 由同一 plan（`daedalus-dev-mode`，todo 1）提供，以 `justfile` 实际内容为准。

### 安全约束（恒成立，与运行形态无关）

- 宿主绝不 spawn，也绝不是任何 MCP 服务器的父进程；`run-plugin` 只把构造好的启动命令**打印**到 stdout，真正执行者是 systemd（按 ExecStart）或用户自己。
- `render-unit` 只输出 `[Service]` + `ExecStart=` 单元片段文本，不落盘、不启停任何单元。
- degraded 插件（sha256 不匹配、id 与目录名不一致、manifest 损坏）会被 `run-plugin`/`render-unit` 拒绝（退出码 1），不产出启动命令。
- 宿主每次子命令执行都会经 `daedalus-audit` 写一条 `host_*` 哈希链审计条目（`host_list` / `host_inspect` / `host_verify` / `host_run_plugin` / `host_render_unit`），写失败静默忽略（尽力而为）。
