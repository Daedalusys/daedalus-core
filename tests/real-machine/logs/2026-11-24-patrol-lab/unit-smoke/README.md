# 单元沙箱冒烟（真实 systemd + root）

`../RESULTS.md` §2.2 说清了端到端验收跑在普通用户下，`StateDirectory=` /
`LoadCredential=` / `ProtectSystem=strict` / `BindReadOnlyPaths=` / `ExecStartPre` 链都没被
覆盖。这份证据补上这一段：在同一个靶场 guest（AlmaLinux bootc，systemd 252）上用 root
起一个影子单元。它的 `[Unit]` 与 `[Service]` 段与
`files/system/usr/lib/systemd/system/daedalus-patrol.service` 一致，**只有 `ExecStart`
一行不同**（换成探针，因为靶场镜像里既没有 opencode 也没有 `daedalus-patrol` 二进制）。

复制到 guest 执行：

```sh
./gssh.sh 9601 'sudo -n bash -s' < harness.sh
```

`output.txt` 是原样输出（每段只含该次 invocation 的日志，不靠 `--since`，它只精确到秒）。

| 场景 | 前提 | 期望 | 实测 |
| --- | --- | --- | --- |
| A | 宿主**没有** `/root/.config/opencode`（bind 源缺失；单元注释承认这是合法部署：用内联 provider 凭据） | 单元成功；`home` 被建出来并收紧到 0700，不因 ENOENT 起不来 | `Result=success`，`home` 0700，bind 目标点不存在 |
| B | 宿主有配置目录 + `auth.json`；数据目录里有上一轮残留 | 配置只读可见；`auth.json` 0600；残留被清 | 见下 |
| C | 复制片段：目标不可写 / 源文件缺失 | 前者非 0，后者 0 | `1` / `0` |

## A：`install -d` 这一行不能省

`BindReadOnlyPaths=-` 的 `-` 前缀让「源缺失」整条跳过，systemd 也就不建目标点 ——
`home` 不会被它顺手建出来。旧写法 `chmod 700 .../home` 在这里 ENOENT，单元每轮起不来；
`install -d -m 0700` 建目录并设模式，两种情况都成立。A 段 `ls: cannot access ...` 是
目标点不存在的直接证据。

## B：`output.txt` 第二段

- `--- mount table ---`：目标挂的是 `/dev/vda4[/ostree/.../.config/opencode]`、`ro`。
- `$HOME/.config/opencode/opencode.json` 0644 可见、内容为宿主原文（模式来自宿主源文件，
  只读挂载不改模式）；`bind_rw=no` 与 `hostpath_rw=no` 说明沙箱内改不了它。
- `$HOME/.local/share/opencode/auth.json` 0600，来自 `ExecStartPre` 的复制。
- `log/stale.log` 不存在 —— 数据目录每轮 `rm -rf` 重建（B 段跑前预置过这个文件）。

## 为什么权限是 0700 / 0600

`UMask=0077` 在单元内生效（探针打印 `umask=0077`）：客户端写的 `log/`、`storage.db` 分别
是 0700 / 0600，与轮次目录的 `0700` / `0600` 一致。`StateDirectory=` 本身是 0755，`home`
由 `ExecStartPre=/bin/install -d -m 0700` 显式收紧 —— bind 目标点由 systemd 以 0755 建
（不受单元 umask 影响），而宿主配置里可能有内联 `apiKey`。

`LoadCredential=` 的两个文件在 `$CREDENTIALS_DIRECTORY` 里是 `0400`，内容可读。

## C 只到片段级

`cp_failure_exit=1` / `cp_source_missing_exit=0` 验的是 `ExecStartPre` 里那段 shell 的
返回码语义；目标用 `/proc/daedalus-patrol/auth.json`（ENOENT）代替 ENOSPC/EACCES。
**没有**真的让单元失败 —— 制造写失败要动文件系统，靶场不做。单元级失败路径只由代码
阅读保证：`exit 1` 会让 `ExecStartPre` 失败，oneshot 单元随之 `Result=exit-code`。

## 这份证据**不**覆盖

- 真实二进制在单元里跑（`ExecStart=/usr/local/bin/daedalus-patrol run`）—— 探针只验证
  沙箱与 `ExecStartPre` 链，业务逻辑由 `../RESULTS.md` 与 `../matrix/` 覆盖。
- 镜像内安装与 `daedalus-patrol.timer` 调度。
- 多节点。
