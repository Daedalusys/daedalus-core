# 真机证据 —— `service.set` 的 `enabled` 维度回滚(#195)

**构建**:`ghcr.io/daedalusys/daedalus-os:9814ed738dfee6f884a291bf3b37b779d395ccc4`
**CI run**:`44576858616`(main)· 腿:`Deploy Verify (guest probes A3/A4/A5/A6/A7/A12/copilot/A2)`
**宿主**:self-hosted KVM runner(`[self-hosted, linux, x64, daedalus-kvm]`,x86-64-v3 + `/dev/kvm` 可写)
**原始输出**:`probe-first-run.log`(artifact `deploy-verify-guest-probes-evidence` 里的同名文件,逐字)

## 判据与结果

这一轮验的是**「自称可回滚、实际没回滚」**那条假成功:`desired_state=enabled` 改的是
`~/.config/systemd/user/*.wants/` 里的符号链接,而运行态(ActiveState)根本没变。
旧实现的回滚只按运行态发动词 → 单元原本 inactive 时发的是 `stop`(空转)→ 符号链接留在原地,
事务却照样报 `rolled_back`。

探针 T3b 因此从「已知缺陷、记 NOTE 不判红」改成**硬判据**,判据是
「回滚前后 `is-enabled` 完全一致」,不硬编码起始值:

```
== T3b · enabled 维度的回滚(#195) ==
      is-enabled(回滚前): disabled
      is-enabled(apply 后): enabled
      is-enabled(rollback 后): disabled
PASS T3b enabled 维度回滚成立(回滚前后都是 disabled)
```

## 同一份日志里 T0–T5 全绿

```
PASS T0 发布镜像带可登录账号: daedalus:x:1000:1000::/var/home/daedalus:/bin/bash
PASS T1 系统身份可读(kernel=6.12.0-273.el10.x86_64,bootc 有 booted 部署)
PASS T2 14 个插件全部 ok(实测 ok=14 degraded=0)
PASS T2b daedalus-smoke 通过
PASS T3 daedalus-probe.service started → rollback 回 inactive,事务闭环成立
PASS T3b enabled 维度回滚成立(回滚前后都是 disabled)
PASS T4 审计链 intact(verified 2 entries; hash chain intact)
PASS T5 package.set 在 immutable 节点上 propose 即清晰 fail-closed
```

## 这次构建里其它探针也全绿

同一个 artifact 里 11 份探针日志,**零 FAIL**:
A2(bootc install+upgrade,含宿主块设备前后逐字节一致)、A3(systemd 沙箱:seccomp/landlock)、
A4(审计:truncate 被拒 / 轮转后仍 verify)、A5(破坏性插件:diskclean/organize 回滚归位)、
A6(写守卫二档)、A7(只读插件)、A11(copilot 沙箱 `--allow-write` 边界)、A12(插件清单)、
A13(蓝图 UI 四道防护)、A14(host bootc switch 的 `--yes`/`--digest` 闸门)。

## 判据出处

- 探针:`tests/real-machine/probe-first-run.sh`(T3b 段)
- 修复:PR #200(`cmd/daedalus-tx/service_set.go`:`enabled_state` 进快照 +
  `serviceSetInverseEnabledVerbs` 恢复 enabled 维度)
- 回归测试:`cmd/daedalus-tx/service_set_rollback_test.go`
  (disabled→enabled 回滚发 `disable`、enabled→disabled 发 `enable`、static 表外不发动词、
  旧日志无 `enabled_state` 键时不凭空猜动词但记跳过原因)
- issue:`Daedalusys/daedalus-core#195`(已关,附本轮原文)
