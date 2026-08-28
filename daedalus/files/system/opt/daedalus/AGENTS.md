# opt/daedalus — 插件安装态运行时根（Go 宿主 + Deno copilot 插件）

**Runtime root:** `/opt/daedalus` in the image. Source root: `daedalus/files/system/opt/daedalus/`.

## OVERVIEW

todo 11 三层目录迁移后，本目录只保留**插件安装态** `plugins/`（构建产物，
由 `just plugin-pack` 与 `./pack-copilot-plugin.sh` 经 Pack→Verify 生成，manifest 含 checksums）
与**运行时策略单一事实源** `shared/policy.toml`（todo 12，Go 服务器经 internal/policy 启动时读取）。
Daedalus Copilot 的 **Deno 源码态**已迁出镜像树，住进 `daedalus/plugin/copilot/`；
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
  `just plugin-pack` / `./pack-copilot-plugin.sh` 重新 Pack→Verify。
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
./pack-copilot-plugin.sh
```
