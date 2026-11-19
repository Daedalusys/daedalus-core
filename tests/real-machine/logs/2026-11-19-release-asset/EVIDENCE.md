# Release 资产本体验收 —— `qcow2-v0.1.0.qcow2`(2026-11-19)

**被测资产**:`Daedalusys/daedalus-core` release `v0.1.0` 的 `qcow2-v0.1.0.qcow2`
（1,335,164,928 B；sha256 `a6ea3a24966a218a7f18dcba741bee1eeff1c9f094674afcd90be07e601ae32e`）
**宿主**:`Masterwet_Server`（Fedora 44 / i7-12700H / `/dev/kvm` 可写 / x86-64-v3）
**脚本**:`tests/real-machine/try-release-asset.sh`
**原始输出**:`release-asset-try.log`

这一轮问的是**别的那个问题**：镜像里有没有账号（`probe-first-run.sh` 的 T0）不等于
「下载到的那份产物能不能被人用」。本轮直接把 Release 上的 qcow2 拉下来、引导、用**发布
账号**登录、敲命令。

## 判定

```
PASS  R1 Release 资产可下载且是合法 qcow2
PASS  R2 引导后可用发布账号 daedalus 登录(口令登录,无需注入任何东西)
PASS  R3 试用账号有免密 sudo,14 插件全 ok,smoke 通过
PASS  R4 package.set 在 immutable 节点上清晰 fail-closed(逐字文案)
```

R3 的实测输出：

```
host=10.0.2.15 user=daedalus uid=1000
PRETTY_NAME="AlmaLinux Kitten 10 (Lion Cub)"
VARIANT_ID="daedalus-os"
sudo=yes
host_list_ok=14
host_list_degraded=0
smoke=SMOKE OK
bootc={"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","
```

即：**下载 → 引导 → 用文档里的账号口令登录 → 有 sudo → 14 个插件全 ok → 自检通过**，
这条链在**出货资产本身**上成立（此前只在镜像层面验过）。

## 这次是「本地取件」而不是从 GitHub 下载

产物字节直接从构建该资产的 runner 工作目录取（`/opt/actions-runner/_work/.../qcow2/qcow2/disk.qcow2`），
字节数与 Release 资产一致（1,335,164,928），sha256 同上。原因是 **GitHub release-asset
下载在当前网络只有 ~0.1 MB/s**（直连 6 小时以上）—— 那是分发问题，不是产物问题，
已另开 [#196](https://github.com/Daedalusys/daedalus-core/issues/196)：
加速站实测 1.3–1.9 MB/s（≈15 分钟），并且 Release 现在带 `SHA256SUMS` 供校验。

## 遗留

- 试用账号口令是**公开**的（alpha 试用镜像的设计取舍，见 build.yml 的出货档注释）；
  要一个干净镜像就自己 `just build`（不传 `DAEDALUS_DEV_USER`/`DAEDALUS_DEV_PASS`）。
- `service.set` 的 `enabled` 维度回滚不掉（[#195](https://github.com/Daedalusys/daedalus-core/issues/195)）：
  本脚本只验了 `package.set` 的边界文案，没验 enabled 回滚。
