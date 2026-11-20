# daedalus-core

Daedalus 核心仓：镜像编排（`files/` 构建树 + 根 `Containerfile`）+ 5 个 core runtime
（`cmd/daedalus-{host,audit,tx,smoke,plugin-pack}`）+ copilot 插件源码（`plugin/copilot/`）。

与 `daedalus-sdk`（11 个安全核心包）、`daedalus-plugins`（13 个 Go 能力插件 monorepo）
构成 3 仓体系；本地开发以**平级目录**形态共存，经 `go.work` 桥接。

## 发布物：先试试

发布时间是 **alpha**：给你一台 QEMU/KVM 能跑的试用镜像，不是生产系统。
上手步骤、试用账号、以及「哪些事在 immutable 节点上做不到」都在
[docs/quickstart.md](docs/quickstart.md)。

| Release 资产 | 是什么 |
|---|---|
| `qcow2-<ver>.qcow2` | QEMU/KVM 试用镜像（带文档化的 alpha 试用账号，口令公开） |
| `iso-verify-<ver>.iso` | **CI 验证夹具，不是安装器**：开机自动装到 `/dev/vda`（会清盘），见 [#191](https://github.com/Daedalusys/daedalus-core/issues/191) |

「第一次跑起来」的验收探针是 `tests/real-machine/probe-first-run.sh`；最近一次真机实跑
的证据（含它抓到的 3 个缺陷）在
[tests/real-machine/logs/2026-11-19-first-run/EVIDENCE.md](tests/real-machine/logs/2026-11-19-first-run/EVIDENCE.md)。


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
go build ./...               # 经 go.work 解析 SDK 与 13 插件模块
```

- `go.work` 引用 `../daedalus-sdk` 与 `../daedalus-plugins/{fs,shell,pkg,sysinfo,service,blueprint,dupe,diskclean,organize,trace,proc,osstate,snapshot}`，
  优先于各 go.mod 的 `replace` 指令（`daedalus-core/go.mod` 的 replace 为 `=> ../daedalus-sdk`）。
- 单仓 clone（无兄弟仓）时：SDK 仓自带 `daedalus-sdk/go.work.example`（`use ( . )`），
  各插件仓自带 `daedalus-plugins/<cap>/go.work.example`（`use ( . ../../daedalus-sdk )`），
  各自 `cp` 为 `go.work` 即生效。
## Where to file issues

请在新仓开 issue。本 issue tracker **仅服务本仓代码**：
- 跨仓问题（如同时影响 SDK 与 plugins）请先开在本仓，影响面大者会在评论里 cross-link 到其他仓。
- 老仓 `Daedalusys/Daedalusys` 已于 2026-09-21 archived，历史 issue 保留可读；新 issue 一律开在本仓。
# CI trigger for headless verification (2026-10-11T04:57:27Z)
