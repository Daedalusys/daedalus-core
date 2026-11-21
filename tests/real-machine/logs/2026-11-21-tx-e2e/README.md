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
4. **条子按 `grant_id` 显式引用**:第二档的 `tx_apply` 需要把 `grant_id` 传进来(不是自动扫目录挑一张)。人签完条子后要把 id 告诉 agent —— 这一条目前没有工具能"列出可用条子",是已知缺口。

## 已知未覆盖

- 真 guest(QEMU/ISO)内的同一套验收 —— 本轮是在真机上做的,不是镜像启动的 guest。
- 多用户同机共用一个账本文件的情形(第一个创建者拥有 0644 账本,别人只能读)。
