# A2 校准 —— `bootc install to-disk` / `bootc upgrade`(2026-11-17)

**关联**:`Daedalusys/daedalus-core#105`(本文取代基线里的 `PROBE_WIP` 措辞)
**基线**:`../2026-11-07-guest-probes/EVIDENCE.md` 的「A2 残余」节
**原始输出**:`a2-recalibration-run.log`(一次连续跑完的完整 stdout;下面每个数字都能在
该文件里逐条对上,行号在引用处标出)

## 环境(全部取自靶场自身,不引用任何文档)

| 项 | 值 |
|---|---|
| 靶场宿主 | `Masterwet_Server`,Fedora 44 `7.3.3-200.fc44.x86_64`,`/dev/kvm` 可写,346G 可用 |
| 宿主 bootc | `bootc 1.17.1`(只在宿主侧 triage 用过,不参与判定) |
| **guest 内 bootc** | **`bootc 1.16.13`**(被测运行时;全部判定都在它下面) |
| guest 内 podman | `podman version 6.1.0`,`storage_driver=overlay`,`graph_root=/var/lib/containers/storage` |
| 被测镜像 | `ghcr.io/daedalusys/daedalus-os:latest`,`.Id`=`e47420fd40c1fb0b6734f688c0b5f6df6feb73b4989d5c53cabdf16ac1059bc1`,RepoDigest `sha256:881b03a84ba210c196a70bcf2a694ee4567e430f8d53d252defb2e6d1d53beb3` |
| guest 盘位 | `vda` 10G(系统,`/var`=`/dev/vda4` 8.4G)+ `vdb` 20G(**install 目标**) |
| qemu | `DRIVER_QEMU_SNAPSHOT=1`;`vdb` 是**宿主上的一个 qcow2 文件**,每轮重建 |
| guest 内 ostree | `prepare-root.conf` 在 `/usr/lib/ostree/`(52 字节);booted 部署 `a4cc39d0…`;`/sysroot/ostree/repo` 里 18 个 commit 对象 |

`bootc install to-disk --help` 原文(guest 内 1.16.13,日志 47–70 行):

```
Install to the target block device.

This command must be invoked inside of the container, which will be installed. The container must be
run in `--privileged` mode, and hence will be able to see all block devices on the system.
...
Usage: bootc install to-disk [OPTIONS] <DEVICE>
```

## 判定总表(2026-11-17 一次 run,`判定: PASS`,`EXIT=0`)

| 判定 | 结果 | 证据 |
|---|---|---|
| **A2-1** install to-disk | **PASS** | `rc=0` **且** 目标盘 `/dev/vdb3` 上 `ostree admin status --sysroot` 见到部署 `364dd2f7….0`,origin `ostree-unverified-registry:localhost/daedalus-verify:base`(日志 82–117 行) |
| **A2-2a** 本地 registry + A 推送 | **PASS** | A=`sha256:fb2ad285…`(153 行) |
| **A2-2b** 同一 tag 两个 digest | **PASS** | A=`sha256:fb2ad285…` / B=`sha256:a456a1d4…`(160–161 行) |
| **A2-2c** booted 部署 origin 指向 registry ref | **PASS** | origin `container-image-reference=ostree-unverified-registry:127.0.0.1:5000/daedalus-verify:latest`(171 行) |
| **A2-2d** `upgrade --check` 报有更新 | **PASS** | `Update available for: docker://127.0.0.1:5000/daedalus-verify:latest`(183–189 行) |
| **A2-2e** `upgrade` rc=0 且 staged | **PASS** | staged digest=`sha256:7ab7f890…`(=C)(190–199 行) |
| **A2-2f** 重启后同 ref、digest 变 | **PASS** | ref=`127.0.0.1:5000/daedalus-verify:latest`,digest `a456a1d4…` → `7ab7f890…`;新部署里 `/etc/daedalus-a2-bump` = `bump=c1794911641`(200–201 行) |
| **A2-3** 宿主盘未被碰 | **PASS** | 宿主块设备结构与文件系统 UUID 前后逐字节一致(204–218 行) |

## A2-1 · 旧路径为什么全红(逐 transport 原始错误)

四种 transport 写法 + containers-storage 是**同一条路径**(都要把源镜像重新导入成一个
ostree commit),失败点与 transport 拼写无关。baseline(2026-11-17,同一靶场,同一镜像):

```
source=oci-archive:/home/verify/a2-source.tar              → rc=1   error: Multiple commit objects found
source=oci:/home/verify/a2-source.tar                      → rc=1   error: Multiple commit objects found
source=registry:ghcr.io/daedalusys/daedalus-os:latest      → rc=1   error: Multiple commit objects found
source=docker://ghcr.io/daedalusys/daedalus-os:latest      → rc=1   error: Multiple commit objects found
（外层错误串,四种写法逐字相同）
error: Installing to disk: Creating source info from a given imageref: Subprocess failed: ExitStatus(unix_wait_status(256))
```

澄清版探针(run1)把 `containers-storage:` 形态也加上,逐字同样两行:

```
source=containers-storage:localhost/daedalus-verify:base → rc=1
     error: Installing to disk: Creating source info from a given imageref: Subprocess failed: ExitStatus(unix_wait_status(256))
     error: Multiple commit objects found
```

**所以旧版的 `PROBE_WIP` 措辞「`--source-imgref` 形态未对齐」是误诊**:不是形态(/拼写)
问题,是这条**从 booted 系统直调**的路径本身。

## A2-1 · 受支持形态:在**被安装的镜像**里跑 bootc

bootc 的 `--help` 原文要求这个命令在「被安装的那个容器」里、以 `--privileged` 运行;
1.16.13 还会显式检查 `--pid=host`(先报 `error: Installing to disk: This command must be
run with the podman --pid=host flag`,补上后通过)。探针里的调用(日志 82–102 行):

```bash
sudo podman run --rm --privileged --pid=host --security-opt label=disable \
  -v /var/lib/containers/storage:/var/lib/containers/storage \
  --entrypoint /usr/bin/bootc localhost/daedalus-verify:base \
  install to-disk --wipe /dev/vdb
```

原始输出(节选):

```
Creating root filesystem (xfs) on device /dev/vdb3 (size=20.9 GB)
> mkfs.xfs -f -m uuid=… -L root /dev/vdb3
Creating ESP filesystem
> mkfs.fat /dev/vdb2 -n EFI-SYSTEM
Initializing ostree layout
layers already present: 0; layers needed: 17 (1.3 GB)
Deploying container image...done (7 seconds)
Bootloader: grub
Installing bootloader via bootupd
…
Trimming root
.: 19.5 GiB (20934819840 bytes) trimmed
Finalizing filesystem root
Unmounting filesystems
Installation complete!
RC=0
```

目标盘(日志 109–116 行):

```
vdb           20G
vdb1           1M          # bios-boot
vdb2 vfat    512M          # ESP
vdb3 xfs    19.5G          # root
[候选根分区 vdb3] /mnt/a2probe/ostree/deploy
      default 364dd2f76fc9d9246382f7d68dd298ffeb07888932018b7534e51a8cf16b9cac.0
---origin---
[origin]
container-image-reference=ostree-unverified-registry:localhost/daedalus-verify:base
```

**判据 = rc=0 且目标盘根分区上有 ostree 部署**,两条同时成立。根分区不再假设
「第一个 xfs 分区」(若镜像带独立 `/boot`,那块也是 xfs),改成「该分区里有
`ostree/deploy`」才算根分区。

## A2-2 · 同一 ref、两个 digest

同一次 run 的完整链路(日志 149–201 行):

```
upgrade 前 booted: ref=localhost/daedalus-verify:base  digest=sha256:8b513e03…
A 推 :latest  → sha256:fb2ad285941bd96d80a2ec7d7ad6cde9fded0660f4008345466ba899598c4bbe
B 推 :latest  → sha256:a456a1d49d1d199cfe28fd44ffc111aeef2b3d706d54d3042987317ceae4f4f6   ← 同一 tag、不同 digest
bootc switch --transport registry 127.0.0.1:5000/daedalus-verify:latest   → rc=0
  Queued for next boot: 127.0.0.1:5000/daedalus-verify:latest  Digest: sha256:a456a1d4…
  （重启）origin → container-image-reference=ostree-unverified-registry:127.0.0.1:5000/daedalus-verify:latest
  booted digest=sha256:a456a1d4…                            ← B
C 推 :latest  → sha256:7ab7f890bffd853b117018dbd50b51050fcba16145b04615d3d9f9f6929a63d5
bootc upgrade --check → Update available for: docker://127.0.0.1:5000/daedalus-verify:latest   Digest: 7ab7f890…
bootc upgrade         → rc=0  Queued for next boot  Digest: sha256:7ab7f890…
  （重启）booted: ref=127.0.0.1:5000/daedalus-verify:latest
                 digest=sha256:7ab7f890…                  ← C
                 /etc/daedalus-a2-bump = bump=c1794911641  ← 新内容确实在跑
```

**语义**:`upgrade` 是同一个 ref 的旧 digest → 新 digest;三个 digest 都推在**同一个 tag**
上,不是「换一个 repoTags 去升级」的 switch 语义。`switch` 只用一次,作用是把 booted 部署
的来源指到 registry(见下条),不是本次要验的动作。

## 三个必须知道的机制(都是实测撞出来的)

1. **`bootc switch` 到同样的内容不会改写来源 ref**。ostree commit 内容寻址:同内容 =
   同 commit,commit 元数据里的 `container-image-reference` 还是旧的。run1 里切到与 booted
   完全相同的镜像 → `switch` rc=0 但重启后 ref 仍是 `localhost/daedalus-verify:base`。
   所以第一次切换必须切到**内容不同**的镜像(这里只差一个惰性标记文件 `/etc/daedalus-a2-bump`)。
2. **本地 registry 的数据必须放命名卷**。bind mount 在 SELinux enforcing 下被拒 ——
   registry 日志逐字:`filesystem: mkdir /var/lib/registry/docker: permission denied`,
   客户端看到的是 `received unexpected HTTP status: 500 Internal Server Error`;
   换成 podman 命名卷(`-v a2-registry-data:/var/lib/registry`)后正常,且**重启后数据还在**
   (容器随重启消失,`podman rm -f` 再 `run` 即可 —— 日志 169 行「registry 重启后恢复」)。
3. **`podman push` 不保证把 digest 打进日志**,用 `--digestfile` 落盘再读。

## root cause(旧路径)与结论

- `bootc install to-disk --source-imgref <X>` 从 **booted 系统直调**时,1.16.13 在
  `Creating source info from a given imageref` 这一步失败,子进程(经 skopeo)逐字报
  `error: Multiple commit objects found`;四种 skopeo transport 与 `containers-storage:`
  全部相同 → **与 transport 无关**。
- 同一台机上的 bootc 1.17.1 不再走这一步(宿主侧 triage:`Starting disk installation from
  containers-storage:…` → `ComposeFS install prep`,最后停在宿主的
  `/usr/lib/ostree/prepare-root.conf` 缺失 —— 那是宿主环境问题,镜像里该文件存在)。
- bootc 自己的 `--help` 要求在被安装的容器里调用;该形态一次成功。
- 因此:**A2-1/A2-2 没有产品缺陷**,需要修的是探针的调用姿势。旧 `PROBE_WIP` 的
  「形态未对齐」措辞作废,替换为上面这一条具体原因。
- 未钉死的一层:guest 的 `/sysroot/ostree/repo` 里有 18 个 commit 对象(osbuild 每 stage
  一个),`Multiple commit objects found` 是导入侧的计数判据 —— 与这个仓库形状一致,但
  要把它证成因果需要 bootc 源码级 trace,本轮没做,故只作为观察记录。

## 本轮没验的 / 下一条方向

1. **装出来的盘没有真的引导过**。A2-1 证的是「目标盘上有 ostree 部署 + 引导器装好了」,
   不是「这台盘能开机」。要验需要在第二个 QEMU 里以 `bootindex` 指向 `a2-vdb.qcow2`
   引导(`driver_boot` 的 `-drive` 行现在不接 bootindex),或起一次独立 qemu。这是
   A2 剩下的最后一个空洞。
2. **A2-2 的升级对象是当前 guest 的部署**(经 `switch` 变成 registry 来源),不是
   `/dev/vdb` 上那次 install 的部署。理由同上:bootc upgrade 只作用于 booted 部署,
   要让 vdb 的部署 booted 就得先能引导它(同第 1 条)。
3. 未覆盖:跨大版本 upgrade、`bootc upgrade --apply`(自动重启)语义、多 stateroot。

## 复现

```bash
# 靶场侧:工作副本 rsync 到 ~/a2-recal/core,工作目录固定复用(避免每轮重出 ~7GB)
sudo VERIFY_WORK=$HOME/a2-recal-work VERIFY_OFFLINE=1 \
  bash tests/real-machine/probe-bootc-install-upgrade.sh
```

`VERIFY_OFFLINE=1` 是 guest-driver 自带的显式逃生口:靶场机器上 `artifact_fp_has_digest`
会反复判「镜像内容已变」并重拉 2.25GB;本轮已单独核对本地镜像 `.Id` =
`e47420fd…`(与 qcow2 指纹一致)且 RepoDigests 含远端 manifest-list digest
`881b03a8…`,**内容同一**。CI 侧不需要这个开关(每轮 run 的 VERIFY_WORK 独立、镜像新拉)。
