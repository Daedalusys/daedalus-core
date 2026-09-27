# Architecture

> 文档分工:README.md 管上手,AGENTS.md 管操作,VISION.md 管愿景与设计论证,本文管系统结构。
> 设计动机、决策取舍与路线图见 [VISION.md](VISION.md);命令速查与操作约定见 [AGENTS.md](AGENTS.md)。
> 全局架构图:[docs/architecture.svg](docs/architecture.svg),由 `python3 scripts/arch-diagram.py` 生成。
> 改架构改脚本重跑,禁止另行维护 drawio 或手绘第二事实源。

## Overview

Daedalus 是一台 immutable、atomic、AI-native 的桌面操作系统底座,跑在 AlmaLinux Bootc(KDE 变体)之上。
它不把裸 shell 交给 AI agent,而是给 agent 类型化资源、有边界的通道与可验证的保证。
核心命题一句话:平台首推路径 = 类型化资源的标准操作 = 有日志事务 = 哈希链审计 = 可回滚;
保证附着于通道,其余通道在硬边界内存在,平台不背书(VISION.md §1)。
系统按三个问题拆成三个面:声明面 Spec 回答「系统应该是什么样」,执行面 Tx 回答「怎么改、怎么恢复」,
观测面 Observe 回答「系统现在是什么样」;审计与策略是两道横切,分别管「操作是否可追溯」与「通道是否被允许」。
物理形态上它是三仓体系:core 编排镜像与运行时骨架,plugins 供给受治理的 OS 能力,sdk 持有全部公开契约;
三仓平级 clone,`go.work` 桥接,`just verify-dev-layout` 守门。

## Three-Repo Topology

| 仓 | 角色 | 内容 |
| --- | --- | --- |
| `daedalus-core`(本仓) | 镜像编排 + 运行时骨架 | `Containerfile`/`justfile` 构建编排;5 个 core runtime 二进制(`cmd/`);契约缝(`internal/`);copilot 插件源码(`plugin/copilot/`) |
| `daedalus-plugins` | 能力供给 | 9 个 Go `type=capability` 插件 monorepo:fs / shell / pkg / sysinfo / service / blueprint / dupe / trace / proc(前 6 个有镜像安装态) |
| `daedalus-sdk` | 公开 contract 仓 | 对象模型 `objectmodel`、策略 `policy`、审计 `audit`、观测缓存 `state`、插件格式 `plugin`、白名单 `shellpolicy`/`pathguard`,以及 provider/slot 契约缝 |

依赖与边界:

- `daedalus-core` 与 `daedalus-plugins` 都 `import` `daedalus-sdk` 的契约包,自身不内联第二份实现;
  契约形状的唯一事实源在 SDK,core 的 `internal/controller` 只是它的别名。
- 插件的源码在 plugins 仓,镜像安装态(`/opt/daedalus/plugins`)是构建产物;
  源码侧与安装态绝不混用,安装态由 `fetch-plugins` / `copilot-plugin` 再生。
- `type` 与 `kind` 互为正交:`type`(`copilot` / `capability`)是打包维度,`kind` 是对象模型的
  封闭枚举(7 类),两轴各答各的问题,同名属 documented 巧合(VISION.md §4)。
- 三仓须平级且仓名固定,`just verify-dev-layout` 守门;插件随镜像构建期内建,
  运行时零联网安装。

制品链(全部构建期内完成):

1. `just plugin-pack` 编译 core 与能力二进制,打包 zip 并逐条目注入 sha256。
2. `-verify` 解压即校验,落到镜像树的安装态目录。
3. `just sync` 把 `files/` 与 `plugin/` 同步进 `base_image/` vendor 树。
4. `just build` 出 OCI 镜像,即最终发布物;本仓 tag 由镜像版本驱动。

## Core Components (daedalus-core)

### Runtime Binaries

`cmd/` 下 5 个 Go 静态二进制(`CGO_ENABLED=0`),构成镜像内的运行时骨架:

| 二进制 | 职责 |
| --- | --- |
| `daedalus-host` | 插件宿主:`list` / `inspect` / `verify` / `run-plugin` / `render-unit`;只打印启动命令,绝不 spawn、绝不做 MCP 父进程(决策 16),执行者是 systemd |
| `daedalus-audit` | 哈希链审计 CLI(`--identity/--tool/--args/--outcome/--log-path` + `verify` 子命令);内部落 SDK `audit.LogAudit`,是给 copilot 这类跨进程写入方用的进程边界封装,不是唯一写入口 |
| `daedalus-tx` | 事务通道 CLI:`begin`→`propose`→`apply`→`rollback`,service/package 适配器,带 euid 守门与回滚 sidecar |
| `daedalus-smoke` | 镜像内端到端 smoke 断言,构建机补跑 |
| `daedalus-plugin-pack` | 插件 zip 打包器:checksums 注入 + manifest 规范化自摘要 + zip-slip 九道防线 |

关键约束:

- 宿主零 spawn(决策 16):`run-plugin` / `render-unit` 只构造并**打印**启动命令,
  绝不成为任何 MCP 服务器的父进程;真正的执行者是 systemd 与 wrapper 的 `exec`。
- degraded 插件(校验和不符、id 与目录名不一致、manifest 损坏)被 `run-plugin` / `render-unit` 拒绝。
- 每次宿主子命令写一条 `host_*` 审计条目,审计写失败静默忽略(尽力而为,不阻塞主流程)。
- 路径常量按 build tag 互斥:生产字面量零 dev 代码,`-tags demo` 才编入 dev 路径重写。

### Copilot CLI

`plugin/copilot/`(TypeScript/Deno,插件 id `daedalus.copilot`)是 plugin-based 的命令顾问(命令顾问,非 agent):把自然语言意图翻译成带风险标签的提议命令。风险由本地静态分类器给出 L0/L1/L2,LLM 不自标注:

| 级别 | 语义 | 行为 |
| --- | --- | --- |
| L0 | safe,安全且在白名单内 | 用户 y/n 确认后进入 `daedalus-shell` 沙箱执行,或进入事务五步 |
| L1 | cautious,需留心 | 仅展示,附手动执行提示 |
| L2 | danger,高危 | 仅展示,附手动执行提示 |

Copilot 自身从不直接执行 shell,执行模型如下:

- 零裸执行:经 JSON-RPC stdio 桥到沙箱化的 `daedalus-shell` Go MCP 服务器,桥上带 40s watchdog。
- 双重校验:提议先过 schema 校验,执行前再对照 15 命令白名单与路径规则;白名单外的命令永不自动执行。
- 全程留痕:翻译、确认、编辑、拒绝每次决策都经 `daedalus-audit` 落哈希链。
- 启动链是宿主零 spawn 模式:`/usr/local/bin/daedalus` wrapper 向 `daedalus-host run-plugin`
  要启动命令,展开 `$HOME` 占位符后 `exec`。
- `policy.ts` 是 `../daedalus-sdk/shellpolicy` 的冻结副本,存在双向同步义务,
  由 `tests/deno/shellpolicy_contract.test.ts` 钉住 Go↔Deno 语义一致。
- UI 字符串一律经 `i18n.ts` 的 `t(key, ...args)`,`en_US` 兜底,manifest `i18n` 声明与 locale 文件双向校验。

### Contract Packages (internal/)

`internal/` 是 core 运行时共享的契约缝,形状的唯一事实源在 `daedalus-sdk`,此处的同名类型是别名,不得出现第二份形状:

- `internal/controller/`:类型定义与七个标准动词 `query` / `list` / `set` / `delete` / `apply` /
  `rollback` / `status` 的常量表(动词 token 的唯一规范,文档与代码冲突时以代码为准)。
  同时钉了 `ReconcileFunc`、`AdmissionFunc` 等契约缝:签名先落码,运行时零调用,接入时不改协议。
- `internal/tx/`:事务状态机 `begin`→`propose`→`apply`→`rollback`,适配器注册表与 `tx.Step`
  六键线上契约。journal 全程落盘,`tx.List()` 按 `created_at↑, id↑` 回放;
  只有事务性资源(现役 service / package)可发起序列,其余被策略网关与注册表拒绝。
- `internal/desiredview/`:期望态投影。journal → Current Desired View 纯函数全量 replay,零副作用;
  `applied` 是唯一提交点,封闭映射 fail-closed;`Entry.source_tx` 是后续 generation 比较的前置。

## Security Model

三层边界架构(VISION.md §1「硬边界,软偏好」的结构化落点;三层完整图见 AGENTS.md §1):

| 层 | 边界 | 主要机制 |
| --- | --- | --- |
| Layer 1 | Image build-time | 可复现构建、基础镜像 digest 钉死、插件 sha256 校验与 manifest 自摘要、镜像签名(90 阶段)、`bootc container lint` |
| Layer 2 | Runtime sandboxing | systemd `DynamicUser`、Landlock LSM 路径约束、seccomp 白名单(收 `@system-service`、拒 `@privileged`)、`LoadCredential` 凭证隔离、`policy.toml` fail-closed 策略网关、Deno 细粒度权限(仅 copilot) |
| Layer 3 | Audit trail | `/var/log/daedalus/audit.jsonl` SHA-256 哈希链(genesis 为 `0`*64,`syscall.Flock` 串行追加),唯一合规写入口是 SDK `audit.LogAudit`,`verify` 子命令可回放校验,金样向量钉字节兼容 |

分层细节:

- **Layer 1 · 构建期**:插件 `daedalus.plugin.json` 声明的能力须 ⊆ `policy.toml` 强制值,
  `76-daedalus-plugin-gen.sh` 构建期渲染 systemd ExecStart 并自校验,漂移即拒构建;
  镜像 rootfs 零源码残留,`just verify-image` 断言。
- **Layer 2 · 运行时**:systemd 单元统一 `DynamicUser=yes` + `ProtectSystem=strict`,
  Landlock 做路径级访问约束,seccomp 收 `@system-service`、拒 `@privileged` / `@resources`,
  `LoadCredential=` 让密钥永不进镜像;能力读写边界由 policy 分级强制,而非依赖「进程只读」假设。
- **Layer 3 · 证据**:genesis 为 `0`*64,`entry_hash = SHA256(timestamp+identity+tool+args_str+outcome+prev_hash)`,
  `syscall.Flock` 串行追加,金样向量钉字节级兼容;唯一合规写入口是 SDK `audit.LogAudit`
  —— 能力服务器与宿主/事务在进程内直调它,copilot 经 `daedalus-audit` CLI 跨进程桥接;
  审计文件禁直写。
- **策略单点**:`files/system/opt/daedalus/shared/policy.toml` 是唯一事实源,缺失或损坏一律拒启;
  回退内置 `Default()` 需 `DAEDALUS_POLICY_MODE=development` 显式 opt-in,并有漂移测试守门。

## Data Flow: A Transaction

一次变更的完整走位,四个阶段串成主链:

```
Spec(资源声明) → Tx(事务五步) → Observe(state 缓存) → Audit(哈希链)
```

1. **Spec(声明面)**:以 `Resource{kind, name, desired_state}` 表达期望态,
   动词收敛为 `set` / `delete`。`delete` 写的是 `absent` 哨兵,是可声明、可回滚的普通期望,
   不是执行破坏性命令。
2. **Tx(执行面)**:`daedalus-tx` 走 `begin`→`propose`→`apply`(`rollback` 兜底),
   每步带 before/after 快照与逆序回滚计划并落 journal;适配器执行真实副作用,
   现役 `service.set`(user-scope-only)与 `package.set`(要求 euid=0)。
   package 的 Apply 以捕获 dnf history id 并落 sidecar 为成功必要条件,fail-closed。
3. **Observe(观测面)**:只读查询(`service.query` / `service.list` 等)写 `state.jsonl`
   追加式缓存,行形 `StateEntry{kind, name, observed_at, payload}`;派生缓存与证据层分离。
   `desiredview` 再从 journal replay 出当前期望视图,读观测与读证据因此解耦。
4. **Audit(横切证据)**:事务每步经 SDK `audit.LogAudit` 在进程内盖 TxID + TxStep 二级链
   (`cmd/daedalus-tx/stamp.go`),审计文件禁直写,单笔变更因此可解释、可回放、可回滚。

双层回滚兜底:事务级 before/after 快照与逆序计划管「单次变更」,bootc 部署级原子回滚管「整个部署」,
两个粒度各自可退(VISION.md §7④)。

## Image Build Process

底层不可变 OS 生成链是 `buildah → dnf --installroot → ostree → bootc`,本仓的 `Containerfile` 在钉死 digest 的 `almalinux-bootc:10` 基础镜像上做多 stage 增量编排。关键步骤:

| 步骤 | 入口 | 作用 |
| --- | --- | --- |
| 拉插件 | `just fetch-plugins` | 从 plugins 仓 release 拉 `*.plugin.zip`,`-verify` 解压即校验(fail-closed) |
| 再生 copilot 安装态 | `just copilot-plugin` | 从 `plugin/copilot/` 源码重打安装态,防入库产物与源码漂移 |
| 打包 | `just plugin-pack` | 构建 5 个 core runtime 与 9 个能力二进制 → zip 注入 checksums → 校验解压到镜像树 |
| 布局守门 | `just verify-dev-layout` | 校验三仓平级就位与各仓 `go.mod` module 路径匹配 |
| 同步 vendor | `just sync` | `scripts/sync-daedalus.sh` 把 `files/` 与 `plugin/` 落位到 `base_image/` vendor 树 |
| 构建镜像 | `just build` | 依赖前三步,`podman build` 按 `Containerfile` 分 stage 跑编号脚本,末段 `bootc container lint` |

`Containerfile` 的 stage 划分:

- `FROM scratch AS ctx` 携带 `base_image/files/{system,scripts}` 与签名公钥,只供构建期 bind mount,
  不进最终镜像;COPY 白名单即这几类。
- `build.sh --init` 第一个 stage 拷贝 system files(sentinel 防重),`--finalize` 末段才跑 cleanup。
- 编号脚本按 stage 白名单执行:`10-50` 上游基础、`64` dnf 镜像源、`60`/`63` 目录结构与状态、
  `65` AI 安全基础、`70`/`70a`/`75` MCP 服务与 copilot、`76` 插件生成与 systemd 渲染、
  `77-79` xrdp / 开发账号 / sddm、`90`/`91` 签名与 image info;漏列的脚本不会执行。
  (`60` 之后的 Daedalus 脚本在本仓 `files/scripts/`;`10-50`/`64`/`77-79`/`90`/`91` 属
  gitignored 的 `base_image/` vendor 树,不在本仓校对范围内。)
- 阶段间状态靠共享 rootfs 传递;改脚本后需 `--no-cache` 重跑对应 stage
  (脚本经 bind mount 进容器,不进镜像层缓存 key)。
- 收尾 `RUN bootc container lint` 校验镜像合规,产出 `localhost/daedalus-os:latest`。

## Directory Structure

| 目录 | 用途 |
| --- | --- |
| `cmd/` | 5 个 core runtime 二进制入口(host / audit / tx / smoke / plugin-pack) |
| `internal/` | 契约包:`controller`、`tx`、`desiredview`,host 与 tx 共享,契约缝锁定 |
| `plugin/copilot/` | Copilot CLI 源码(TypeScript/Deno)+ `daedalus.plugin.json` + i18n;镜像安装态是它的构建产物,勿手改 |
| `files/` | 镜像 overlay:`system/`(systemd 单元与 drop-in、`policy.toml`、插件安装态、credstore)与 `scripts/`(编号构建步骤) |
| `base_image/` | Vendored AlmaLinux 镜像树(gitignored,自带 `.git`),`just sync` 的落位目标,禁止直接改 |
| `scripts/` | 镜像外构建工具:`sync-daedalus.sh`、`fetch-plugins.sh`、`verify-dev-layout.sh`、`pack-copilot-plugin.sh`、`arch-diagram.py` |
| `tests/` | `tests/deno/`(copilot 测试与 Go↔Deno 跨语言契约)与 `tests/integration/`(tx roundtrip) |
| `docs/` | 架构图 `architecture.svg`/`png` 与设计裁决(如 `desired-state-projection.md`) |

源码(`cmd/`、`internal/`、`plugin/`)永不进镜像 rootfs,`/opt` 只允许构建产物落地,`just verify-image` 对此做零残留断言。

## See Also

- [VISION.md](VISION.md):宣言、问题域、三面架构、k8s-parity、分歧与开放问题(更深的设计论证)。
- [AGENTS.md](AGENTS.md):组件清单、命令、约定与反模式(操作知识库)。
- [docs/architecture.svg](docs/architecture.svg):一张图总览,改架构改 `scripts/arch-diagram.py` 重跑。
- `../daedalus-sdk/docs/provider-slot.md`:provider/slot 契约与双层 Extension 模型细节。
