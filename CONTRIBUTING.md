# Contributing to Daedalus

感谢你对 Daedalus 的关注。本文档是**三仓共用的贡献指南公共部分**；各仓特有约束见各自 `CONTRIBUTING.md` 的「本仓特有约束」段。

## Prerequisites

- **Go** 1.22+ (推荐 1.23+)
- **Deno** 1.x (仅 copilot 插件开发需要)
- **Podman** / **Docker** (镜像构建)
- **just** (命令运行器,类 make)
- **git** + **jj** (版本控制,推荐 jj)

## Three-Repo Layout

Daedalus 由三个平级仓库组成，本地开发必须以**平级目录**形态 clone：

```
~/work/daedalusys/
├── daedalus-core/      # 镜像编排 + 5 core runtime + copilot 源码
├── daedalus-sdk/       # 11 个安全核心包 + Provider/Slot 契约
└── daedalus-plugins/   # 9 个 Go 能力插件 monorepo
```

**仓名必须为 `daedalus-core` / `daedalus-sdk` / `daedalus-plugins`**（与 `go.work` 路径对应）。

### go.work Bridge

三仓经 `go.work` 桥接依赖关系：

```bash
cd ~/work/daedalusys/daedalus-core
cp go.work.example go.work
just verify-dev-layout   # 守门：兄弟仓就位 + go.mod module 路径匹配
go build ./...           # 经 go.work 解析 SDK 与插件模块
```

- `go.work` 引用 `../daedalus-sdk` 与 `../daedalus-plugins/{fs,shell,...}`
- 单仓 clone（无兄弟仓）时：各仓自带 `go.work.example`，`cp` 为 `go.work` 即生效

## Code Style

### Go

- **格式化**: `gofmt -l .` 必须零输出
- **静态检查**: `go vet ./...` 必须通过
- **命名**: 遵循 Go 标准命名惯例（导出大写，非导出小写）
- **错误处理**: 使用哨兵错误（`errors.Is` / `errors.As`），`%w` 包装保留哨兵
- **注释语言**: **必须使用中文**（见下方「注释规范」）

### TypeScript / Deno

- **格式化**: `deno fmt` 必须零差异
- **静态检查**: `deno lint` 必须通过
- **运行时**: 仅 copilot 插件使用 Deno，其余能力插件全部 Go 实现

## 注释规范（强制）

本项目所有源代码、配置文件、构建脚本中的注释**必须使用中文**。包括但不限于：

| 文件类型 | 注释格式 |
|----------|----------|
| Go 源码 | `//` 与 `/* */` 注释，godoc 注释 |
| TypeScript / Deno | `//` 与 `/* */` 注释 |
| Shell / Bash | `#` 注释 |
| systemd unit | `#` 注释 |
| justfile / Makefile | `#` 注释 |
| Containerfile | `#` 注释 |
| YAML / JSON / TOML | 注释字段（含 `policy.toml`、`daedalus.plugin.json`） |

**不视为注释的内容**（保留英文）：标识符、字符串字面量、API 协议字段（如 JSON 键、HTTP 头、协议名）、系统命令、URL、日志中可被外部解析的 token。

## Testing

### Go Tests

```bash
# 全仓测试
go test ./...

# 带覆盖率
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# 金样向量重放（audit 包）
go test -run TestGolden ./audit/...
```

### Deno Tests（仅 copilot）

```bash
deno test --allow-all tests/deno/
```

### 三仓联合测试

```bash
# 从 core 仓根执行（经 go.work 桥接）
just test    # 全量：本仓 + SDK + plugins + Deno

# 或手动
just go-test  # 仅 Go
just test     # Go + Deno
```

## Build

```bash
# 同步 + 构建镜像
just build

# 仅同步 vendor 树
just sync

# 构建 Go 二进制
just go-build

# 打包插件
just plugin-pack

# 验证镜像
just verify-image

# 开发模式构建（demo build-tag）
just go-build-demo
```

## Commit Convention

- **语言**: 提交信息使用**中文**
- **格式**: `<type>(<scope>): <description>`
- **type**: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`
- **scope**: 可选，标明影响的模块（如 `audit`, `plugin/copilot`, `pathguard`）
- **禁止**: 提交信息中不写 `todo N` / `决策 N` / `oracle review` 等进度信息

## Pull Request Process

1. **Fork** 对应仓库（或在组织内创建分支）
2. **本地测试** 通过：`go test ./...` + `go vet ./...`
3. **提交** 遵循上述 commit convention
4. **PR 标题** 清晰描述变更内容
5. **PR 说明** 包含：
   - 变更动机
   - 变更内容摘要
   - 测试方法
   - 影响范围（是否跨仓）

### 跨仓变更

如果变更影响多个仓库：

1. 先在**影响面最大的仓库**开 issue
2. 各仓分别开 PR，互相 cross-link
3. 说明依赖关系（如「SDK PR #X 先合并后，本 PR 才能通过」）

## Issue Filing

请在**新仓**开 issue。本 issue tracker **仅服务本仓代码**：

- 跨仓问题（如同时影响 SDK 与 plugins）请先开在本仓，影响面大者会在评论里 cross-link 到其他仓
- 老仓 `Daedalusys/Daedalusys` 已于 2026-09-21 archived，历史 issue 保留可读；新 issue 一律开在本仓

## Security

- **不要**在代码中硬编码 API 密钥、密码或证书
- **不要**提交包含敏感信息的文件
- **安全漏洞**请通过 GitHub Security Advisories 私下报告，不要开公开 issue

## Architecture References

- [VISION.md](VISION.md) — 系统愿景与设计层总览
- [AGENTS.md](AGENTS.md) — AI 操作知识库（含反模式清单）
- [ARCHITECTURE.md](ARCHITECTURE.md) — 中等粒度架构说明
