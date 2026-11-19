# Release 资产本体验收 —— `v0.1.1`(2026-11-20)

**被测资产**:`Daedalusys/daedalus-core` release `v0.1.1` 的 `qcow2-v0.1.1.qcow2`
（1,336,803,328 B；sha256 `9c0a5daa8dafaae57f9bf067486836ca87efe207dfbb29503680ee9c4190abe6`）
**宿主**:`Masterwet_Server`（Fedora 44 / i7-12700H / `/dev/kvm` 可写 / x86-64-v3）
**脚本**:`tests/real-machine/try-release-asset.sh`（`SKIP_DOWNLOAD=1`，用下载到的那份字节）
**原始输出**:`download-and-verify.log`、`try-release-asset.log`

与 v0.1.0 那轮的分工相同:这一轮问的是「**用户下载到的那份东西能不能用**」,
而不是「镜像里有没有账号」。区别是本轮**真的从网上取件**。

## 取件 + 校验(按用户的走法)

`ghfast.top` 加速站(直连约 0.1 MB/s → 6 小时以上;实测加速站这次约 1.8 MB/s):

```
download_rc=0
--- size ---
1336803328 qcow2-v0.1.1.qcow2
--- sha256sum -c ---
qcow2-v0.1.1.qcow2: OK
verify_rc=0
```

即:加速站给的字节与 Release 上发布的 `SHA256SUMS` **逐字节一致**。
(`SHA256SUMS` 本身是流水线从即将上传的那份字节现算的,不是手工上传 —— 见 PR #201。)

## 引导 + 上手

```
PASS  R1 Release 资产可下载且是合法 qcow2
PASS  R2 引导后可用发布账号 daedalus 登录(口令登录,无需注入任何东西)
PASS  R3 试用账号有免密 sudo,14 插件全 ok,smoke 通过
PASS  R4 package.set 在 immutable 节点上清晰 fail-closed(逐字文案)
判定: PASS
```

R2/R3 的实测值：

```
host=10.0.2.15 user=daedalus uid=1000
PRETTY_NAME="AlmaLinux Kitten 10 (Lion Cub)"
VARIANT_ID="daedalus-os"
sudo=yes
host_list_ok=14
host_list_degraded=0
smoke=SMOKE OK
```

串口里确实出现了 `login:` 提示符（`serial-v0.1.1.log` 第 329 行），QEMU 由脚本自行收尾。

## 为什么这一轮不依赖 CI 的 `real KVM boot` 腿

那条腿在 tag 构建里卡在 **`Download qcow2 artifact`**：runner 从 GitHub 拉 1.3 GB 的
artifact 只有 ~0.5 MB/s（同一台机器上 `ghfast.top` 是 1.8 MB/s）。那是**分发链路**的问题,
不是产物的问题 —— 所以这里直接引导**已发布的字节**来回答同一个问题,而且答得更硬:
CI 那条腿引导的是重新下载的 artifact 副本,这里引导的是**用户下载到的那份文件**
(校验和已对齐)。

## 判据出处

- 脚本:`tests/real-machine/try-release-asset.sh`（R1–R4）
- 加速站可用性的实测口径与「校验通过才算数」的措辞见 `docs/quickstart.md` §1
- 该版本修了什么:`service.set` 的 `enabled` 维度回滚(#195,证据见
  `tests/real-machine/logs/2026-11-19-service-enabled-rollback/`)
