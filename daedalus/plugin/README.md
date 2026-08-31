# daedalus/plugin — 官方预置插件定义层(源码态)

三层目录结构(决策 23/24)中的**插件层**:每个官方预置插件一个子目录,
内含清单 `daedalus.plugin.json` 与其运行所需的全部源码/二进制。
本目录是**源码态与打包输入**;镜像内的权威形态是安装态
`daedalus/files/system/opt/daedalus/plugins/<id>/`(构建产物,经 Pack→Verify 生成,
manifest 带逐条目 sha256 checksums)。本目录内容绝不直接进镜像 rootfs。

## 产品定位:command advisor(命令顾问)

copilot 插件(`daedalus.copilot`)的产品定位是 **command advisor / 命令顾问**:
理解自然语言意图 → 翻译成白名单内的 shell 命令并**生成命令** → 逐条**风险标注** →
由**用户手动执行**。不是 agent:插件自身从不替用户执行命令。

**风险分级**(本地静态 classifier 判定,LLM 不参与自标注,防 prompt injection):

| 级别 | 语义 | 处理 |
|------|------|------|
| `L0` / safe | 安全且在 15 命令白名单内 | 可经 `daedalus-shell` 沙箱执行(y/n 确认后) |
| `L1` / cautious | 谨慎(有副作用/需人工判断) | **仅展示**命令 + 风险理由,提示用户手动执行 |
| `L2` / danger | 危险(破坏性/不可逆) | **仅展示**命令 + 🚨 风险理由,提示用户手动执行 |

即:L1/L2 命令架构上不进执行通道,只有 L0(安全 + 白名单内)可在用户确认后
经 `daedalus-shell` 沙箱执行;白名单外命令一律不自动执行。

## 目录布局

```
plugin/
├── copilot/            # type=copilot, runtime=deno —— Daedalus 命令顾问 CLI (command advisor)
│   ├── daedalus.plugin.json      # 清单(entrypoint 携带 deno run 权限旗标)
│   ├── {main,policy,audit,exec,llm}.ts        # 源码(5 个,打包进插件)
│   └── {main,policy,audit,exec,llm}.test.ts   # deno test 单元测试(不打包)
├── fs/                 # type=capability, runtime=native —— 文件系统能力
│   ├── daedalus.plugin.json
│   └── bin/daedalus-fs             # 由 just plugin-pack 从 daedalus/core 构建拷入
├── shell/              # 同上:shell_exec(15 命令白名单,权威在 core/internal/shellpolicy)
├── pkg/                # 同上:dnf/rpm 只读查询
└── sysinfo/            # 同上:os-release/hardware/network 只读探测
```

## 清单格式(`daedalus.plugin.json`)

schema 与校验的单一事实源: `daedalus/core/internal/plugin/manifest.go`。

| 字段 | 必填 | 说明 |
|------|------|------|
| `id` | ✔ | 文法 `^[a-z0-9]+(\.[a-z0-9]+)*$`(无连字符),目录名 = id(`daedalus.copilot`) |
| `name` / `version` | ✔ | 展示名;version 为 semver 2.0.0 文法 |
| `type` | ✔ | 枚举 `copilot` / `capability` |
| `runtime` | ✔ | 枚举 `native`(Go 静态二进制直接 exec)/ `deno`(宿主拼 `deno run <entrypoint> <executable>`) |
| `executable` | ✔ | 包内相对路径(native 如 `bin/daedalus-shell`;deno 如 `main.ts`),打包时必须有可执行位 |
| `entrypoint` | 可选 | deno runtime 的权限旗标列表;**不要写 `run`**(宿主已自动前置 `deno run`);`$HOME` 占位由 wrapper 在 argv 层展开 |
| `permissions` | 可选 | `{read,write,run}` 路径白名单,与 entrypoint 旗标逐项一致 |
| `tools` | capability 必填 | 声明的 MCP 工具名;76-daedalus-plugin-gen.sh 构建期与二进制 stdio `tools/list` 交叉核对,漂移即拒绝 |
| `checksums` | 安装态必填 | 由 `daedalus-plugin-pack` 注入(逐条目 sha256 + manifest 规范化自摘要);源码态清单**不得手填** |

## 打包与安装流水线

- **copilot**: `./scripts/pack-copilot-plugin.sh` —— 暂存 5 个 `.ts`(排除 `.test.ts`)+ 清单
  → Pack 注入 checksums → `-verify --keep` 解压安装态(解压即完整校验,摘要不符拒绝安装)。
  命令顾问(command advisor)插件:L0 之外的风险级别仅展示、由用户手动执行。
- **4 能力插件**: `just plugin-pack` —— 构建 Go 二进制拷入各自 `bin/` → 同一 Pack→Verify 流程;
  顺带安装宿主与 copilot 运行期依赖的审计/执行二进制到
  `daedalus/files/system/usr/local/bin/daedalus-{host,audit,shell}`（task 21）。
- 安装态入库后经 `just sync`(rsync `files/system/` leg)进镜像 `/opt/daedalus/plugins/`;
  `just sync` 另有保守 leg 把本目录同步到 `base_image/plugin/` 仅作构建上下文,不进镜像。
- 运行期消费方: 宿主 `daedalus-host list/verify/run-plugin`;systemd 单元由
  `76-daedalus-plugin-gen.sh` 经 `render-unit` 按 manifest 渲染。

## i18n 多语言支持(强制约定)

所有插件的 UI 字符串必须经 `i18n.ts` 的 `t(key, ...args)` 走,不在源码里硬编码。

- **locale 文件**:放在插件目录 `i18n/<locale>.json` 下(POSIX 下划线命名,
  `en_US` / `zh_CN` / `ja_JP` 等;如 `i18n/en_US.json`、`i18n/zh_CN.json`)。
- **manifest 声明**:`daedalus.plugin.json` 加 `"i18n": ["en_US", "zh_CN"]` 数组字段
  (数组形式,第一个是默认 locale)。
- **en_US 必定位兜底**:任何插件都不能省;en_US 不在(声明或文件任一侧缺失且无法兜底)
  即校验失败。
- **声明 ↔ 文件严格一致**:manifest 声明的 locale 集合必须与 `i18n/` 目录实物完全一致;
  漂移 CI 拒(严模式 exit 1)。开发者加新 locale 后用
  `scripts/plugin-i18n-sync.sh --autofix`(即 `just i18n-sync-autofix`)一键同步 manifest。
- **命名约定**:key 用点分风格(如 `confirm.prompt` / `verbose.preview`),翻译文本里
  用 `{0}` `{1}` 占位符(printf 风格,由 `t(key, ...args)` 依序填充)。
- **locale 探测顺序**:env `LC_ALL` > `LANG` > `en_US`。
- **文件级 fallback 链**:精确匹配(如 `zh_CN`)→ 语言级回退(如 `zh`)→ 兜底 `en_US`。
- **工具**:`just i18n-sync`(CI 守门,严模式)/ `just i18n-sync-autofix`(开发,
  以 `i18n/` 目录实物为准改写 manifest)。打包侧 `scripts/pack-copilot-plugin.sh`
  会把 `i18n/` 目录一并打进 zip。

## 约定(强制)

- 注释一律中文(仓库根 CONVENTIONS)。
- 本目录**只进构建上下文**:源码/二进制绝不 rsync 进镜像 rootfs `/opt`。
- 未打包清单(无 checksums)在安装根下会被宿主判 degraded——这是零放宽设计,勿绕过。
- 改 `copilot/policy.ts` 冻结副本必须同步 `daedalus/core/internal/shellpolicy`(反之亦然)。
