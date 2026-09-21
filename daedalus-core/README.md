# daedalus-core

Daedalus 核心仓：镜像编排（`files/` 构建树 + 根 `Containerfile`）+ 5 个 core runtime
（`cmd/daedalus-{host,audit,tx,smoke,plugin-pack}`）+ copilot 插件源码（`plugin/copilot/`）。

与 `daedalus-sdk`（11 个安全核心包）、`daedalus-plugins`（6 个 Go 能力插件 monorepo）
构成 3 仓体系；本地开发以**平级目录**形态共存，经 `go.work` 桥接。

## 本地开发

**前置**：3 仓以平级目录形态 clone（`go.work` 本地 dev 桥依赖兄弟仓路径）：

```bash
mkdir -p ~/work/daedalusys && cd ~/work/daedalusys
git clone <core> && git clone <sdk> && git clone <plugins>
```

仓名必须为 `daedalus-core` / `daedalus-sdk` / `daedalus-plugins`（与 `go.work` 路径对应）。

clone 完成后，在 `daedalus-core/` 根执行：

```bash
cp go.work.example go.work   # 3 仓平级 dev 桥（go.work 不入库，模板入库）
just verify-dev-layout       # 守门：兄弟仓就位 + 各仓 go.mod module 路径匹配
go build ./...               # 经 go.work 解析 SDK 与 6 插件模块
```

- `go.work` 引用 `../daedalus-sdk` 与 `../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint}`，
  优先于各 go.mod 的 `replace` 指令（`daedalus-core/go.mod` 的 replace 为 `=> ../daedalus-sdk`）。
- 单仓 clone（无兄弟仓）时：SDK 仓自带 `daedalus-sdk/go.work.example`（`use ( . )`），
  各插件仓自带 `daedalus-plugins/<cap>/go.work.example`（`use ( . ../../daedalus-sdk )`），
  各自 `cp` 为 `go.work` 即生效。