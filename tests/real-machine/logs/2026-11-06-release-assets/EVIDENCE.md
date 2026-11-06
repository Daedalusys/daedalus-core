# 2026-11-06 · 发布制品晋级:qcow2 / ISO 从 90 天 artifact 到 GitHub Release 资产

> 对应 plan `docs/superpowers/plans/2026-11-06-release-artifacts.md`
> (spec `.plans/cd-deployment-feasibility.md` §4 的 **P2-3**、§3.5 第 5 条;issue **core#84**)。
> 落地脚本 `scripts/release-asset-name.sh`,发布腿 `build-qcow2` 与 `deploy-verify-iso`。

## 结论

出货档 run 之后,qcow2 与 ISO 以 `<kind>-<version>.<ext>` 的名字挂上了 GitHub Release,
且**与同一 run 里被验证的那一份字节一致**。

- Release: <https://github.com/Daedalusys/daedalus-core/releases/tag/9683defbf631>
- run: `42181586948`(`workflow_dispatch`,`ref=feat/release-artifacts`,`PUSH=true`,`VARIANT=headless`)
- tag `9683defbf631` 实测指向 `9683defbf63118ba42f0717a99e3f41f10a86125` —— 正是本次 run 的
  `GITHUB_SHA`/检出 commit,即 tag 与货一一对应。

| 资产名 | 大小(字节) | sha256(GitHub 侧 digest) |
|---|---|---|
| `qcow2-9683defbf631.qcow2` | 1314914304 | `598205130a8365cb72ba532cd3b44eff020c05b228c4011edce777b5e342a36b` |
| `iso-9683defbf631.iso` | 1212784640 | `892b5b003d816aca97f3c4b62649aeca9d38ab3908a81c1221c4ff9ef1c1574a` |

## 判定只看发布腿本身,不拿整个 run 的 conclusion 当判据

本仓自托管 runner 只有一个,同一 run 里三条重 job 会排队,而**排队时间计入
`timeout-minutes`** —— 后排队的那条可能被取消。故本计划的两条发布腿按步骤级结论判定:

| job | 步骤 | 结论 |
|---|---|---|
| `build-qcow2` (4m48s) | `Promote qcow2 to GitHub Release` | ✓ |
| `deploy-verify-iso` (17m29s) | `Promote ISO to GitHub Release` | ✓ |
| `deploy-verify-kvm` (7m15s) | `Boot the qcow2 in KVM and wait for login prompt` | ✗ 见下「与本计划无关的红」 |

发布腿创建 Release 的日志实证:

```
https://github.com/Daedalusys/daedalus-core/releases/tag/9683defbf631
已晋级 Release 资产: qcow2-9683defbf631.qcow2
```

## 资产与被验证的那一份字节一致(核对方式 + 实测值)

两侧取**同一 run、同一路径**的产物,再比 sha256:

**qcow2** —— `build-qcow2` 里构建出来的 `output/qcow2/disk.qcow2` 同时被「上传 artifact」与
「上传 Release」引用(同一文件、不重新构建);`deploy-verify-kvm` 下载**该 run 的 artifact**
后引导它。该 job 日志自己打印了它引导的那份的哈希:

```
qcow2 sha256: 598205130a8365cb72ba532cd3b44eff020c05b228c4011edce777b5e342a36b
OK qcow2 digest 引导前后一致(交付产物字节未变): 598205130a8365cb72ba532cd3b44eff020c05b228c4011edce777b5e342a36b
```

与上表 `qcow2-9683defbf631.qcow2` 的 digest **逐字相同**。

靶机侧独立复算(不经 CI 自报)同一路径文件的哈希,同样命中:

```
$ sha256sum /opt/actions-runner/_work/daedalus-core/daedalus-core/qcow2/qcow2/disk.qcow2
598205130a8365cb72ba532cd3b44eff020c05b228c4011edce777b5e342a36b
$ stat -c 'mtime=%y size=%s' …
mtime=2026-11-06 16:22:26 +0800 size=1314914304
```

mtime `08:22:26 UTC` 正是本 run 里 `deploy-verify-kvm` 下载该 artifact 的时刻,size 与 Release 一致
—— 三处独立命中同一哈希。

**ISO** —— `iso-verify.sh` 在 Phase 2 产出 `$VERIFY_WORK/output/*.iso`,Phase 3 用**同一文件**
引导验证,发布腿上传的也是它(不重新构建)。故核对点是「同 run 的 artifact 里那份 ISO」:

```
$ gh run download 42181586948 --repo Daedalusys/daedalus-core \
      --name deploy-verify-iso-evidence --dir ./iso
$ sha256sum iso/output/bootc-almalinux-10-generic-iso-x86_64.iso
892b5b003d816aca97f3c4b62649aeca9d38ab3908a81c1221c4ff9ef1c1574a
size=1212784640
```

与上表 `iso-9683defbf631.iso` 的 digest **逐字相同**,size 也相同 —— 即被引导验证的那份
== 被发布的那份。注意两侧**文件名**不同(artifact 里是构建器给的
`bootc-almalinux-10-generic-iso-x86_64.iso`,Release 上是 `iso-9683defbf631.iso`):
名字由发布腿统一,内容逐字节不变。

### 顺带发现(不属本计划范围,记录备查)

1. **靶机上"事后复算"不可靠**:共享靶机的 runner 工作区会被后续 run 覆盖。实测同一路径
   `iso-verify/output/bootc-almalinux-10-generic-iso-x86_64.iso` 在我这个 run 结束之后
   (`mtime 16:28:56 +0800` = `08:28:56 UTC`)已被另一次 run 重写,内容与 Release 里的不同。
   故 ISO 侧的核对改走**本 run 的 artifact**这条不会被覆盖的路径。
2. **ISO 构建不是字节可复现的**:被覆盖的那份与本次发布的那份 **size 完全相同**
   (`1212784640`)但 sha256 不同 —— 同一输入两次构建产出不同字节。这不影响本计划的承诺
   (被验证的那份 = 被发布的那份,是**同一份文件**,不是"两次构建结果相同"),但意味着
   「拿 sha256 当 ISO 的可复现性判据」在这里不成立。

## 与本计划无关的红:`deploy-verify-kvm` 的引导步骤

```
XX qemu 提前退出(退出码见日志),未出现 login
   qemu-system-x86_64: -netdev user,id=net0,hostfwd=tcp::2222-:22:
   Could not set up host forwarding rule 'tcp::2222-:22'
判定     : QEMU_EXITED
```

根因是**宿主 2222 端口被占**(靶机共享;当时 `ss -ltnp | grep :2222` 显示 `LISTEN 0.0.0.0:2222`),
`tests/real-machine/bootc-verify.sh` 硬编码 `hostfwd=tcp::2222-:22` 且无冲突检测,于是 qemu 退出后
判定落到 `QEMU_EXITED` —— 与「产物引导不起来」同一个结论,容易被误读成产物缺陷。
已开 **core#88** 记录,本计划不现场修(该文件不在本计划 lane 内)。

同一条 job 的产物侧自证仍然成立(见上「qcow2 digest 引导前后一致」),即 qcow2 本身完整。

## 修复前的中间产物已清理

首次实跑(run `42179282860`,修复前)暴露了一个真缺陷:`gh release upload` 的
`路径#标签` **只设资产 label、不改资产名**(实测 `label=qcow2-743a0bf107a4.qcow2` 而
`name=disk.qcow2`)。修法是上传前按目标名在 `$RUNNER_TEMP` 下硬链接一份再上传。

该次 run 留下一个资产名不合规的 Release,已连同 tag 一起删除(清单先留档备查):

```
$ gh release delete 743a0bf107a4 --repo Daedalusys/daedalus-core --yes --cleanup-tag
$ gh release list --repo Daedalusys/daedalus-core --limit 5
9683defbf631	Latest	9683defbf631	2026-11-06T07:45:22Z
$ gh api repos/Daedalusys/daedalus-core/git/ref/tags/743a0bf107a4
{"message":"Not Found", ... "status":"404"}
```

删除前它挂着的两个资产(资产名不合规,真实名字只在 label 里):

| 资产 name(修复前) | label(才是应有的名字) | 大小 | sha256 |
|---|---|---|---|
| `disk.qcow2` | `qcow2-743a0bf107a4.qcow2` | 1315701248 | `1b80556860888d88ae900cb9fc4efd419557aba1b72b00512c9168f9882a798d` |
| `bootc-almalinux-10-generic-iso-x86_64.iso` | `iso-743a0bf107a4.iso` | 1212784640 | `a61ae72b1f1eebe82bdc594ee5ad93faeddef58e7db34152c968af3758945ce3` |

删它的理由:资产名不合规(照资产名拉会拿到错名的文件)、tag 指向的 commit 已被 force push
覆盖成不可达、留着只污染 Release 列表。删除后 Release 列表只剩规范的 `9683defbf631`。

## 尚未覆盖

- 只在 `workflow_dispatch`(出货档)上实跑过;`refs/tags/v*` 那条分支只做了本地表驱动验证
  (`tests/ci/release-asset-name-test.sh` 的 tag 用例 + 本地假 gh harness 的 tag 用例),
  没有真的打过 tag 出货。
- `deploy-verify-kvm` 的红未在本计划内修复(见 core#88)。
