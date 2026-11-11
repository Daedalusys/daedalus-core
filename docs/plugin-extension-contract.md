# Plugin Extension Contract:扩展点是协议,不是运行时

**状态**: 提案,待裁决。**实现落点**: `daedalus-sdk/plugin/manifest.go`(schema)、
`cmd/daedalus-host/start.go`(`buildStartTokens`)、`files/scripts/76-daedalus-plugin-gen.sh`(单元渲染)、
`files/system/usr/local/bin/daedalus`(wrapper)。本文只写契约语义,不复述实现。

## 一句话

插件与宿主之间的契约应该是一条**线协议**,不是一个**启动命令**。当前 manifest 用
`runtime` + `entrypoint` 描述"怎么拼 argv",把运行时方言写进了宿主逻辑,于是"换运行时"
与"换安全边界"变成了同一个动作。

## 问题:三个字段编码了两个维度

| 字段 | 声称编码 | 实际编码 | 谁消费 |
|---|---|---|---|
| `type` | 语义角色 | 语义角色 | 只有 `inspect` 打印 |
| `runtime` | 运行时 | **启动方言** | `buildStartTokens` 的 `switch` |
| `entrypoint` | 启动参数 | **运行时方言的具体旗标** | 原样拼进 argv |

三处症状,都不是风格问题:

1. **沙箱写在运行时方言里。** `plugin/copilot/daedalus.plugin.json:12-16` 的
   `--allow-env` / `--allow-net` / `--allow-read=…` 是 Deno 的旗标语法,却由宿主拼装
   (`cmd/daedalus-host/start.go` 的 `buildStartTokens`)。宿主不懂 Deno,却要替 Deno 声明权限。
   而 copilot 没有 systemd 单元,这串旗标就是它**唯一的**沙箱(`files/system/usr/local/bin/daedalus:4-9` 自述)。
   换成 Node,`--allow-read=` 一类的旗标全部不存在,沙箱随之消失。
2. **`Runtime.Version` 是装饰性的。** 结构里定义了版本字段(`daedalus-sdk/plugin/manifest.go:74-79`),
   注释写着"如 Deno 2.1.4",但 `buildStartTokens` 从不读它。manifest 写什么版本都不影响启动命令。
   一个不生效的字段比没有这个字段更危险:**它让人以为已经钉住了。**
3. **`type` 是装饰性的。** `TypeController` 的注释自述"校验放行即全部语义,宿主 type-agnostic"。
   于是三种进程形态(常驻服务 / 交互 CLI / 调和服务)在宿主看来没有区别,全靠 `runtime` 的
   `switch` 间接区分。

`entrypoint` 在实践中已是 Deno 专属:`buildStartTokens` 的 `native` 分支把它当作
`executable` 之后的普通参数(`return append([]string{absExec}, entry...)`),
而 13 个能力插件(`daedalus-plugins` 的 `origin/main`)全部 `runtime=native` 且 `entrypoint` 为空 —— 这条路径从未被走过。
也就是说,**这套旗标抽象只在 1 个插件上有实际内容**。

## 参照:Kubernetes 的扩展点

K8s 有四类扩展方式,没有一类询问扩展"你是什么运行时":

| 扩展点 | 契约 | 发现方式 | 语言自由从哪来 |
|---|---|---|---|
| CRD + controller | API server 的 REST/watch(HTTP + JSON) | `kubectl apply` 一个 CRD | controller 只是 apiserver 的客户端 |
| Admission webhook | HTTPS POST `AdmissionReview` | 注册 URL + `failurePolicy` / `timeoutSeconds` / `sideEffects` | 契约是版本化 JSON schema |
| Aggregation(`APIService`) | 自带 apiserver + discovery | 注册 `APIService` 对象 | 契约是 K8s API 约定 |
| kubelet 插件(CRI / CSI / CNI) | UDS + 版本化 gRPC;CNI 更简单:可执行文件 + stdin JSON + 退出码 | 固定 socket 路径或 bin 目录 | 契约是 protobuf / JSON |

三条可迁移的结论:

- **契约是线协议,语言自由是副产品。** 所以不需要 `runtime` 字段。若让 CRI 插件声明"我是 JVM 应用",
  kubelet 就得懂 JVM 参数 —— 荒谬,所以不做。
- **发现是显式的。** `APIService` 对象、registration socket 路径、bin 目录。不是"扫目录读一个字段"。
- **版本协商是硬要求。** `v1` / `v1beta1` 在 K8s 里是协议的一部分,不是装饰。

还有一条容易被忽略:**K8s 核心组件不给扩展当进程管理者**。controller 跑在用户自己的
Deployment 里;CRI 由 systemd 启动,kubelet 只连 socket。唯一由 kubelet `exec` 的是 CNI,
而它的契约恰好是"一个可执行文件 + stdin JSON + 退出码"——没有运行时知识。

## 裁决

四条,按顺序落地:

1. **删除 `runtime` 与 `entrypoint`。** `executable` 是唯一启动入口,宿主 `exec` 它,
   不解释它。shebang、解释器旗标、环境前缀都属于插件包内部,不进 manifest。
2. **`type` 升为 load-bearing。** 它决定进程形态,不再只是打印值:
   - `capability` → 常驻 MCP 服务,渲染 systemd 单元(`76-daedalus-plugin-gen.sh` 现状);
   - `copilot` → 交互 CLI,前台进程,进 `systemd-run --scope`(见 #117);
   - `controller` → 调和服务,进程内 `ReconcileFunc` 注册(`internal/controller/runtime.Registry`),不 `exec`。
3. **`api_version` 参与协商。** 当前只校验格式(`manifest.go:58`)。宿主遇到不认识的
   major 版本必须 fail-closed 拒绝启动,与 K8s 的 `v1` / `v1beta1` 同语义。
4. **controller 型的接入方式是数据 + 注册,不是启动参数。** 与 CRD + controller 同形:
   对象模型声明在 manifest 的 `resources`,调和服务在装配构造期注册。

### 新形状

改动前:

```json
{ "type": "copilot", "runtime": "deno", "executable": "main.ts",
  "entrypoint": ["--allow-env", "--allow-net", "--allow-read=/opt/daedalus,…"] }
```

改动后:

```json
{ "type": "copilot", "executable": "bin/daedalus-copilot", "api_version": "0.2.0" }
```

`bin/daedalus-copilot` 是插件包内的入口(chmod +x,进 checksums),内容类似:

```sh
#!/bin/sh
exec /usr/local/bin/deno run --allow-env --allow-net --allow-read=… "$(dirname "$0")/../main.ts" "$@"
```

宿主看到的就是"一个可执行文件"。它不需要知道那是 Deno。

### 沙箱归位

`--allow-*` 从 manifest 移到两处,各司其职:

- **OS 层**:`systemd-run --scope` + `DynamicUser` / `ProtectSystem=strict` /
  `SystemCallFilter` / `IPAddressAllow`(见 #117)。这是权威边界,运行时换掉也还在。
- **插件包内**:入口脚本里的解释器旗标。降为 defense-in-depth,由 checksums 覆盖。

### Deno 的位置

裁决后,Deno 变成插件包内部的实现选择,不再是宿主契约的一部分。这样两件事被拆开:

- **生命周期**:Deno LTS 通道止于 2027-01-31,无后继公告(#116)。这是版本节奏问题。
- **安全边界**:由 OS 层承担,与运行时无关。

于是迁移到 Node 26 的费用从"重写安全边界"降为"重写插件内部",可以在生命周期需要时
按普通重构排期,而不是一次安全事件。

## 兼容与迁移

三阶段,每阶段独立可回滚:

| 阶段 | 内容 | 判据 |
|---|---|---|
| 1 | 沙箱下沉到 OS 层(#117),manifest 不动 | 删掉 `--allow-*` 后 copilot 仍启动、探针仍 `DENIED` |
| 2 | schema 换形:删 `runtime` / `entrypoint`,`type` 生效,`api_version` 协商 | 14 个 manifest(13 能力 + copilot)通过新校验;`buildStartTokens` 的 `switch` 消失 |
| 3 | 运行时迁移(视生命周期决定) | 只改插件包内部,宿主零改动 |

阶段 2 是破坏性 schema 变更,`api_version` 从 `0.1.0` 升到 `0.2.0`。13 个能力插件
(`daedalus-plugins`)本来就是 `native` 且不写 `entrypoint`,删字段对它们是空操作;
只有 copilot 需要新增入口脚本。

## 不做的事

- **不做 user-scoped 动态安装。** `~/.local/share/daedalus/plugins` 目前零实现;在契约稳定前引入
  动态安装,等于把尚未收敛的抽象暴露给第三方。
- **不做远程 controller 协议。** `type: controller` 先只支持进程内注册,等有第二个实现再谈线协议。
- **不引入第二策略源。** 权限仍归 `policy.toml` 与 OS 层;manifest 只声明需求,不声明强制。

## 开放问题

- 入口脚本是 shell(依赖 `/bin/sh` 存在)还是静态 wrapper 二进制?前者简单,后者不引入
  解释器依赖,但多一个构建产物。
- `type` 决定进程形态后,`copilot` 这个枚举名是否该换成形态名(如 `interactive`)?
  现名是产品角色,不是进程形态,两者混在同一个字段里。
- 阶段 2 与阶段 1 的先后是否必须?当前判断是必须:先证明 OS 层能独立兜住,再删运行时旗标,
  否则中间态会出现"两个边界都不完整"。
