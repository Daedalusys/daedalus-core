# 2026-11-06 · bootc 部署链真机验证(引导 → switch → rollback)

> 对应 `.plans/real-machine-verification.md` 的 **Todo 10**:`bootc install` → 引导 →
> `switch` → `upgrade` → `rollback`。
> 该项此前**全仓零覆盖、任何机器都没跑过**;本次第一次拿到实机证据。
> 脚本:`daedalus-core/tests/real-machine/bootc-switch-verify.sh`

## 结论

**`SWITCH_ROLLBACK_OK`** —— 换部署与回滚都真的生效,且用**部署 digest + 内容 marker
双重证据**判定,不是「命令返回 0 就算过」。

| 阶段 | boot_id | 部署 imageDigest | marker | 期望 |
|---|---|---|---|---|
| A 基线(引导后) | `d6085f83-f66e-4400-866d-f3a54b88f0b3` | `sha256:bbd00d60…d6625b5` | ABSENT | 基线无 marker |
| B `bootc switch --apply` 后 | `718e9f0a-a18b-44d6-be8f-646a73ec538a` | `sha256:aa1ad770…c2725386` | **PRESENT** | 换成目标镜像 |
| C `bootc rollback --apply` 后 | `17bf2d8a-3256-42ff-a532-78653850232a` | `sha256:bbd00d60…d6625b5` | ABSENT | 回到基线 |

- 三个 **互不相同** 的 boot_id → 观察到三次真实重启(换部署一次、回滚一次)。
- C 的 digest **逐字节等于** A → 回滚确实回到了原部署,不是「切到了另一个」。
- marker(`/usr/share/daedalus-verify-marker`,放在 `/usr` 即每部署独立)在 B 出现、
  在 C 消失,与 digest 变化互相印证。

## 关键输出

`bootc switch` 的返回(来自 `bootc-switch-verify.log`):

```
layers already present: 17; layers needed: 1 (843 bytes)
Deploying...done (2 seconds)
Queued for next boot: ostree-unverified-image:oci-archive:/home/verify/switch-target.tar
Digest: sha256:aa1ad770
```

`bootc status` 的基线快照:

```
apiVersion: org.containers.bootc/v1
kind: BootcHost
spec:
  image: { image: localhost/daedalus-verify:base, transport: registry }
status:
  booted:
    image:
      imageDigest: sha256:bbd00d6084c69228ad2e0dc73d2be6d896634fbea43d6e97585812f62d6625b5
    store: ostreeContainer
    ostree: { stateroot: default, checksum: 5e735e91… }
```

## 环境与做法

- 宿主:`Masterwet_Server`(10.66.0.9),Fedora 44,i7-12700H,x86-64-v3,`/dev/kvm` 可写。
- 镜像:基准 `ghcr.io/daedalusys/daedalus-os:latest`。
- **一次性 verify 镜像**:CI 构建的发布镜像**没有登录凭据**(`78-dev-user.sh` 是凭据
  门控的,CI 不传),而 `bootc switch/rollback` 必须在 guest 内执行 → 必须能登进 guest。
  故在本地**派生**一个一次性镜像(复刻 `78-dev-user.sh` 的用户/组/sudoers/tmpfiles):
  - **不推任何 registry、不进发布产物**;
  - 密码随机生成、用完即弃、不进仓库;
  - 复刻时踩到产品脚本注释里记的坑:`video`/`render` 组在该镜像上**病态半初始化**,
    `useradd -G` 会直接失败,必须先往 `/etc/group` 补条目。
- **切换目标不经 registry**:`bootc switch --transport oci-archive <tar>` 直接从
  OCI 归档切换部署(该 transport 经实机确认被 bootc 1.16.13 支持),切换目标经
  `podman save --format oci-archive` 打包后 scp 进 guest。**全程零凭据、零外部 registry。**
- qcow2 由 `bootc-image-builder --local --type qcow2` 从 verify 镜像出。

## 产物

- `serial-console.log` —— 三次引导的完整串口输出。
- `bootc-switch-verify.log` —— 脚本完整日志(含 switch 原始返回)。

## 尚未覆盖

- `bootc upgrade`(等价于「拉同一镜像的新 digest 再切」)与 `bootc install to-disk`
  本次未单独验证:前者与 switch 同路径,后者在 bootc-image-builder 出的 qcow2 里
  **已经发生过**(builder 内部就做了 `bootc install`),但没有在 guest 里手工再跑一遍。
- 破坏性验证(diskclean / organize / blueprint rollback)仍未做,见
  `.plans/cd-deployment-feasibility.md` §2 的 A5。
