# tx 通道真机 E2E(v3:第三档归属 + 第二个真用户会话)

2026-11-22 在 `Masterwet_Server` 上跑的一轮端到端验收。证据原文见
[`EVIDENCE-v3.md`](EVIDENCE-v3.md):**通过 76,失败 0**,其中上一轮 32 条一条没少、
本轮新增 44 条。

## 这一轮验的是什么

第三档(待办 + 批准)此前有三处「靠碰巧成立」的机制,这一轮把它们变成事实:

1. **待办按人分目录**:`/run/daedalus/pending/<uid>/`(0700,属主本人),根目录仍是
   1777。别人的待办在文件系统层就读不到,也抢建不了同名文件卡住落位。
2. **批准记录带请求者**:`daedalus-tx approve` 在批准当场从**待办文件的 stat 属主**
   取请求者,写进 `requested_by_uid` / `requested_by`;插件消费时显式核对
   `requested_by_uid == 执行者 uid`,不符即 `approval_for_other_user`。
3. **批准一次性**:`/run/daedalus/consumed/<uid>/<id>` 用 `O_EXCL` 占位,
   占住即烧掉 —— 第二次用同一份批准得到 `approval_consumed`。
4. **第二个用户是真用户**:`useradd -m -s /bin/bash` + `loginctl enable-linger` +
   `systemctl start user@<uid>.service`,MCP 客户端与 `systemctl --user` 都带
   `XDG_RUNTIME_DIR` / `DBUS_SESSION_BUS_ADDRESS` / `HOME`。上一轮那个 nologin +
   `sudo -u` 的临时用户只验了绑定判定,验不到「两个用户各有自己一条 user bus」。

## 怎么复现

```bash
# 1) 本机:两个工作树各构一个二进制(core CLI 与 MCP 插件)
cd /home/lofibass/orca/workspaces/daedalus-core/tx-core-v3 && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o /mnt/cache/tmp/opencode/tx-e2e-v3/bin/daedalus-tx ./cmd/daedalus-tx
cd <plugins 工作树>/tx && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o /mnt/cache/tmp/opencode/tx-e2e-v3/bin/daedalus-tx-mcp ./cmd/daedalus-tx-mcp

# 2) 同一个 stage 目录里放齐:bin/ + tests/mcp/{tx-e2e.sh,tx-e2e-client.py}
#    + files/system/opt/daedalus/shared/policy.toml + 一个 run.sh
cat > /mnt/cache/tmp/opencode/tx-e2e-v3/run.sh <<'EOF'
#!/bin/bash
cd "$HOME/tx-e2e-v3" || exit 1
export E2E_CLIENT="$PWD/tx-e2e-client.py"
sg daedalus-signers -c "bash tx-e2e.sh --bin-dir $PWD/bin --policy $PWD/policy.toml --evidence $PWD/EVIDENCE.md"
EOF
chmod +x /mnt/cache/tmp/opencode/tx-e2e-v3/run.sh

# 3) 落到目标机并跑(调用者须在 daedalus-signers 组里:签发条子要该组目录写权限)
scp -r /mnt/cache/tmp/opencode/tx-e2e-v3 Masterwet_Server:~/
ssh Masterwet_Server 'sudo install -m 0755 ~/tx-e2e-v3/bin/daedalus-tx /usr/local/bin/daedalus-tx \
  && bash ~/tx-e2e-v3/run.sh'
```

脚本自己准备并清理:`/run/daedalus/{grants,pending,approvals,consumed}` 的权限、档位表
`/etc/daedalus/agent-autonomy.toml`、两个用户域里的测试单元、第二个用户(不存在才建,
跑完连家目录删掉)。既有用户一律不动。

## 本轮 9 条断言与实测

| # | 断言 | 证据里的 check 名 | 结果 |
| --- | --- | --- | --- |
| 1 | root `grant add --for 第二用户` 成功、`for_uid` 是第二用户 uid;第二用户拿它执行 ⇒ `applied` | `场景2g:root 给第二用户签条子成功` / `条子 for_uid=第二用户 uid` / `条子属主=root` / `执行前第二用户域里的单元没在跑` / `第二用户用给他的条子执行` / `第二用户域里的单元真的起来了` | ✅×6 |
| 2 | 第一用户拿同一张条子 ⇒ `grant_for_other_user` | `场景2g:同一张条子本用户用不了` | ✅ |
| 3 | 第二用户的待办落 `/run/daedalus/pending/<uid2>/`,第一用户落自己的目录 | `场景3c:第二用户待办落在自己的 uid 子目录` / `本用户待办落在自己的 uid 子目录` / `待办不再落在待办根目录` / `两个目录 0700 且属主=本人` | ✅×5 |
| 4 | root `daedalus-tx pending list` 同时看到两份,各自 `requested_by` 正确 | `场景3c:root pending list 退出码 0` / `清单里有本用户的待办` / `清单里有第二用户的待办` / `清单里{本用户,第二用户}待办的 requested_by` 与 `requested_by_uid` | ✅×6 |
| 5 | 批准记录里 `requested_by_uid` = 请求者 uid | `场景3c:批准记录 requested_by_uid=第二用户` / `requested_by=第二用户账号名` | ✅×2 |
| 6 | 同一份批准第二次使用 ⇒ `approval_consumed`;参数变了 ⇒ `approval_mismatch`,两者可区分 | `场景3:消费标记已落盘` / `同一份批准第二次使用被拒` / `场景3d:参数变了 ⇒ approval_mismatch` / `指纹不符时不执行` / `按真实指纹重批后可用` | ✅×5 |
| 7 | `tx_pending` 只返回自己的待办 | `场景3c:{第二用户,本用户} tx_pending 里有/不含…` | ✅×4 |
| 8 | 用量账本每人一本 | `场景6:两本账本不是同一个文件` / `{本用户,第二用户}的账本存在` / 各自属主 / 第二用户账本记的是他自己的条子 | ✅×6 |
| 9 | 上一轮的 9 条(第一档/第二档/第三档/危险名单/拒绝可区分,含 2c/2d/2e/2f/3b)仍然全绿 | 上一轮 32 条全部保留(逐条比对,一条不少) | ✅×32 |

额外补的三组(不在 9 条里,但都是本轮语义的直接后果):

| 断言 | check 名 | 结果 |
| --- | --- | --- |
| 第二用户用「批给他的」那份批准执行成功(正向路径不被误伤) | `场景3e:第二用户用被批准的待办执行` / `第二用户域里的单元真的动了` | ✅×2 |
| 落在受害者 id 上的伪造待办被批准后,受害者本人执行 ⇒ `approval_for_other_user` | `场景3f:root 看到的请求者是第二用户` / `批给别人的批准受害者用不了` / `被拒后单元没动` | ✅×3 |
| 第二个用户真有自己一条 user bus | `准备:第二用户的 user bus 可用` / `第二用户 Linger=yes` | ✅×2 |

## 这一轮实测出的事实

1. **`approval_mismatch` 的真实触发路径是「人批准了被改写过的 argv」**,不是
   「批完再改文件」。插件消费批准时按**请求**重算 argv 并与批准记录里的指纹比对,
   它根本不看待办文件内容;所以先批准再改写待办文件,执行仍然会成功(这也符合
   设计:批准绑定的是人看过的那条 argv)。要让"参数变了"这条断言诚实,必须让
   **人先看到被改写的那一份并批准它**(`pending show` 给出的是改写后的指纹),
   agent 再按真实意图执行才会撞指纹。脚本第一版把顺序写反了,当场变红,已改正。
2. **第二用户的会话连「探测」都要换身份**:`/run/user/<uid>` 是 0700,调用者
   (`udefine-dev`)连 stat 那个 bus socket 都会被拒。脚本第一版在调用者身份下探
   `[ -S /run/user/<uid>/bus ]`,于是永远探不到自己刚起的会话 —— 必须
   `sudo -u <user> test -S …`。
3. **`/run/daedalus/consumed` 必须由部署建出**(1777 root:root)。它缺失时第三档
   在"占住"这一步 fail-closed,表现为"批准有效但执行不了";生产由 tmpfiles 建,
   E2E 显式建一份。
4. **批准记录的请求者核对照常挡住伪造归属**:第二用户在自己目录里放一份 id 等于
   第一用户待办 id 的文件(内容完全相同、指纹相同),root 批准后,第一用户本人执行
   得到 `approval_for_other_user` 而不是放行 —— 这道门不是靠 id 派生碰巧兜住的。
5. **同一 id 出现在两个用户目录时 root 的 `approve` 整份拒掉**(「重复即伪造」)。
   所以上面那条伪造实验先把受害者目录里那份(已消费的)移走,否则验不到请求者核对。

## 缺口关闭情况

| 上一轮记的缺口 | 现状 |
| --- | --- |
| 第二用户是 `sudo -u` 临时造的 nologin 用户,验不到「第二个真 user bus」 | **已关闭**:本轮第二个用户有家目录、login shell、linger 起的 user manager,两个用户各自在自己的 user 域里起同名单元并各自执行 |
| 待办目录 1777 ⇒ 任何本地用户都能读别人的待办 | **已关闭**:待办落 `<root>/<uid>/`(0700),只在文件系统层隔离;`tx_pending` 也只列自己那本 |
| 「这份批准是给我这份请求的」没有显式判定 | **已关闭**:批准记录写 `requested_by_uid`(权威 = 待办文件 stat 属主),消费侧显式核对 |
| 一份批准在窗口内可反复用 | **已关闭**:`O_EXCL` 消费标记,第二次用得到 `approval_consumed` |

## 仍然没覆盖 / 仍然存在的取舍

- **消费标记可自截断**:标记归 agent 自己所有,挡得住意外重放(循环、重试、重复提交),
  挡不住恶意重放。与 `max_uses` 同性质 —— 速率限制,不是安全边界。
- **per-user 目录被抢占**:别人抢建 `/run/daedalus/pending/<uid>/` 并自己拥有,该用户的
  第三档就不可用,直到管理员删掉目录(错误文案给 `sudo rm -rf` 处置步骤)。需要本地
  shell 权限,且攻击者自己得不到任何东西。
- **真 guest(QEMU/ISO)内的同一套验收**:本轮仍在真机上做的,不是镜像启动的 guest。
