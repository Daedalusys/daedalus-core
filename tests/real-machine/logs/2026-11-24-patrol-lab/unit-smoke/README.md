# 单元沙箱冒烟（真实 systemd + root）

`../RESULTS.md` §2.2 说清了端到端验收跑在普通用户下，`StateDirectory=` /
`LoadCredential=` / `ProtectSystem=strict` / `BindReadOnlyPaths=` / `ExecStartPre` 链都没被
覆盖。这份证据补上这一段：在同一个靶场 guest（AlmaLinux bootc，systemd 252）上用 root
起一个真实单元的影子单元 —— 单元段与
`files/system/usr/lib/systemd/system/daedalus-patrol.service` 逐行一致，只把 `ExecStart`
换成探针（镜像里没有 opencode）。

复制到 guest 执行：

```sh
./gssh.sh 9601 'sudo -n bash -s' < harness.sh
```

`output.txt` 是原样输出。三个场景：

| 场景 | 前提 | 期望 | 实测 |
| --- | --- | --- | --- |
| A | 宿主**没有** `/root/.config/opencode`（bind 源缺失，单元注释承认这是合法部署：用内联 provider 凭据） | 单元成功；不因 `chmod` ENOENT 起不来 | `Result=success`，`home` 0700 |
| B | 宿主有配置目录 + `auth.json`；数据目录里有上一轮残留 | 配置只读可见；`auth.json` 复制为 0600；残留被清 | 见下 |
| C | 复制目标不可写 / 源文件缺失 | 前者让单元失败，后者静默通过 | `exit 1` / `exit 0` |

## B 的实测（`output.txt` 第二段）

- `$HOME/.config/opencode/opencode.json` 0644 可见、内容为宿主原文（模式来自宿主源文件，
  只读挂载不改模式）—— 配置是只读挂载，沙箱内 `touch` 写宿主配置被拒（`host_cfg_rw=no`）。
- `$HOME/.local/share/opencode/auth.json` 0600，来自 `ExecStartPre` 的复制。
- 上轮残留的 `log/stale.log` 不存在 —— 数据目录每轮 `rm -rf` 重建。
- `findmnt` 显示目标挂的是 `/dev/vda4[/ostree/.../.config/opencode]`、`ro`，与
  `BindReadOnlyPaths=-` 的 `-` 前缀语义一致（源缺失时整条跳过，不建目标点）。

## 为什么权限是 0700/0600

`UMask=0077` 在单元内生效（探针打印 `umask=0077`）：客户端写的 `log/`、`storage.db` 分别
是 0700 / 0600，与轮次目录的 `0700` / `0600` 一致。`StateDirectory=` 本身是 0755，但 `home`
由 `ExecStartPre=/bin/install -d -m 0700` 显式收紧 —— 这一条不能省：bind 目标点由 systemd
以 0755 建（不受单元 umask 影响），而宿主配置里可能有内联 `apiKey`。

`LoadCredential=` 的两个文件在 `$CREDENTIALS_DIRECTORY` 里是 `0400`，内容可读。

## 这份证据**不**覆盖

- 真实二进制在单元里跑（`ExecStart=/usr/local/bin/daedalus-patrol run`）—— 探针只验证
  沙箱与 `ExecStartPre` 链，业务逻辑由 `../RESULTS.md` 与 `../matrix/` 覆盖。
- 镜像内安装与 `daedalus-patrol.timer` 调度。
- 多节点。
