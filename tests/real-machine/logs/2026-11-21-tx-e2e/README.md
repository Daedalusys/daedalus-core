# tx 通道真机 E2E(2026-11-21)

在 `Masterwet_Server`(x86_64、systemd 259、有活着的用户会话、sudo 免密)上跑的端到端验收。

## 怎么复现

```bash
# 目标机上:准备二进制(用**已发布**的插件制品 + 从 core 源码构的 CLI)
gh release download v0.6.0 --repo Daedalusys/daedalus-plugins --pattern 'daedalus.tx.plugin.zip'
unzip -o daedalus.tx.plugin.zip -d txpkg
# core 源码里构建 CLI(需 SDK v0.2.0)
go build -o bin/daedalus-tx ./cmd/daedalus-tx
cp txpkg/bin/daedalus-tx-mcp bin/ && sudo install -m 0755 bin/daedalus-tx /usr/local/bin/daedalus-tx

# 跑验收(用户须在 daedalus-signers 组里,签发条子要该组的目录写权限)
sg daedalus-signers -c "E2E_CLIENT=$PWD/tx-e2e-client.py \
  bash tests/mcp/tx-e2e.sh --bin-dir $PWD/bin \
  --policy $PWD/policy.toml --evidence $PWD/EVIDENCE.md"
```

## 这一轮实测出的事实

1. **三档都真跑通了**:auto 档单元真的起来;confirm 档无条子被拒、有条子放行;human 档未批准不执行、批准后按同一 argv 执行。
2. **`max_uses` 的账本路径是真门**:账本落到 `$HOME/.local/share/daedalus/tx` 时,插件按 fail-closed **拒绝执行**(`grant_usage_failed`) —— 这条防线有效,但也暴露了 `/var/lib/daedalus/tx` 此前没人建,现已在 tmpfiles 里补上。
3. **`sudo` 签不出 root 条子**:`sudo -i daedalus-tx grant add` 的属主仍是调用者(`SUDO_USER` 优先)。root 条子必须来自**没有 `SUDO_USER` 的 root 进程**(实测用 `sudo env -u SUDO_USER …` 等价复现)。
4. **条子按 `grant_id` 显式引用**:第二档的 `tx_apply` 需要把 `grant_id` 传进来(不是自动扫目录挑一张)。人签完条子后要把 id 告诉 agent —— 这一条当时没有工具能"列出可用条子",是已知缺口。

## 第二轮(v2):越权修复后的复跑(32/32 通过)

证据:`EVIDENCE-v2.md`(同一台机器、同一套脚本,新增 13 条断言)。

这一轮修的是第一轮暴露的越权:**普通用户拿 root 签的条子重启了自己域里的单元**
(当时唯一挡住它的是账本打不开这件无关故障)。修法 = 条子绑定使用者 + 账本按人分文件
+ 待办 id 带执行者 + 新增只读工具 `tx_grants`。

复现命令(与第一轮相同,二进制换成 v2 构建):

```bash
# 目标机上
scp -r tx-e2e-v2 udefine-dev@Masterwet_Server:~/
sudo install -m 0755 ~/tx-e2e-v2/bin/daedalus-tx /usr/local/bin/daedalus-tx
bash ~/tx-e2e-v2/run.sh          # 内部: sg daedalus-signers -c "bash tx-e2e.sh --bin-dir … --policy …"
```

新增断言与实测结果:

| 断言 | 结果 |
| --- | --- |
| root `grant add --for <本用户>` 成功,文件属主仍是 root,`for_uid` = 本用户 uid | ✅ |
| root **不写** `--for` ⇒ 退出码 2,错误串「root 签发必须写明给谁」 | ✅ |
| root 给本用户签的条子真能用(`outcome=applied`) | ✅ |
| root 给**另一个用户**(uid 967)签的条子,本用户拿去用 ⇒ `grant_for_other_user`,detail 点明两个 uid,单元没动 | ✅ |
| `tx_grants` 列出本用户可用条子、**不列**别人的;`executor.uid` = 本用户 | ✅ |
| 第二个用户以自己身份跑 `tx_grants`:执行者是自己,看得到给他签的那张,看不到本用户的 | ✅ |
| 两个用户对同一意图的待办 id 不同(`p-0e693a34e02f8589` vs `p-5087acfd07aad07f`) | ✅ |

第一轮记的两条缺口现状:
- **缺口 1(没有「列出可用条子」的工具)**:已修 —— `tx_grants` + 拒绝体回带 `available_grants`。
- **缺口 3(多用户共用同一账本文件)**:已修 —— 账本改为 `usage-<uid>.jsonl`,每人一本;
  真机上两个用户各自可用,谁也不会因为打不开别人的账本而永久 fail-closed。

## 已知未覆盖

- 真 guest(QEMU/ISO)内的同一套验收 —— 本轮是在真机上做的,不是镜像启动的 guest。
- 第二用户用 `sudo -u` 临时造(nologin、无家目录):它验证的是**绑定判定与账本分文件**,
  不是「第二个用户真有一条 user bus 去跑 systemctl --user」。后者要 `loginctl enable-linger`
  + `user@<uid>.service`,属部署面,不在本脚本内。

## 第三轮(v3):第三档归属 + 第二个真用户会话(76/76 通过)

后续这一轮把上面最后一条的缺口补上了:**第二个用户改成有家目录、有 login shell、
由 linger 起 user manager 的真用户**,两个用户各自在自己的 user 域里起同名单元并各自
执行。同时验了第三档的三处收紧:待办按人分目录、批准记录写请求者(权威 = 待办文件的
stat 属主)、一份批准只能用一次。

- 证据与复现命令:[`../2026-11-22-tx-e2e/README.md`](../2026-11-22-tx-e2e/README.md)、
  [`EVIDENCE-v3.md`](../2026-11-22-tx-e2e/EVIDENCE-v3.md)
- 结果:通过 76,失败 0 —— 本文件记的 32 条一条没少,本轮新增 44 条(9 条断言 + 归属/
  一次性/伪造归属的额外断言)
- 复现入口不变:同一个 `tests/mcp/tx-e2e.sh`,只是脚本现在自己建/删第二个真用户与
  `/run/daedalus/consumed`

