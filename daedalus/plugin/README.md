# daedalus/plugin — 官方预置插件定义层(源码态)

三层目录结构(决策 23/24)中的**插件层**:每个官方预置插件一个子目录,
内含清单 `daedalus.plugin.json` 与其运行所需的全部源码/二进制。
本目录是**源码态与打包输入**;镜像内的权威形态是安装态
`daedalus/files/system/opt/daedalus/plugins/<id>/`(构建产物,经 Pack→Verify 生成,
manifest 带逐条目 sha256 checksums)。本目录内容绝不直接进镜像 rootfs。

## 目录布局

```
plugin/
├── copilot/            # type=copilot, runtime=deno —— Daedalus Copilot CLI
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

- **copilot**: `./pack-copilot-plugin.sh` —— 暂存 5 个 `.ts`(排除 `.test.ts`)+ 清单
  → Pack 注入 checksums → `-verify --keep` 解压安装态(解压即完整校验,摘要不符拒绝安装)。
- **4 能力插件**: `just plugin-pack` —— 构建 Go 二进制拷入各自 `bin/` → 同一 Pack→Verify 流程;
  顺带安装宿主与 copilot 运行期依赖的审计/执行二进制到
  `daedalus/files/system/usr/local/bin/daedalus-{host,audit,shell}`（task 21）。
- 安装态入库后经 `just sync`(rsync `files/system/` leg)进镜像 `/opt/daedalus/plugins/`;
  `just sync` 另有保守 leg 把本目录同步到 `base_image/plugin/` 仅作构建上下文,不进镜像。
- 运行期消费方: 宿主 `daedalus-host list/verify/run-plugin`;systemd 单元由
  `76-daedalus-plugin-gen.sh` 经 `render-unit` 按 manifest 渲染。

## 约定(强制)

- 注释一律中文(仓库根 CONVENTIONS)。
- 本目录**只进构建上下文**:源码/二进制绝不 rsync 进镜像 rootfs `/opt`。
- 未打包清单(无 checksums)在安装根下会被宿主判 degraded——这是零放宽设计,勿绕过。
- 改 `copilot/policy.ts` 冻结副本必须同步 `daedalus/core/internal/shellpolicy`(反之亦然)。
