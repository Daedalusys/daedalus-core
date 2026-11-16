# 落点体检真机证据 —— 包会落到 /boot 就拒绝(2026-11-16,sdk#114)

> **状态**:已执行,**两次全绿**(`46 通过 / 0 失败`,`PLAN_C_EXIT=0`)。两条分支各跑一遍:
> **rpm 分支**(RHEL 9.9,已装包走本地清单)与 **apt 分支**(Ubuntu 24.04,已装包走
> `dpkg-query -L`)。
>
> **执行人**:loficore(协调者 Native 执行;靶场宿主 `Masterwet_Server` = CI runner `undef-dev`)
> **驱动**:`tests/fleet/plan-c-e2e.sh` 的「落点体检腿」(本轮新增,见 PR #161)
> **控制面**:`daedalus-host` core 0.1.2,源码 `1fb53c1e` 构建,`sha256:01d02cd238104764…`,18,028,334 B
> **被控端**:`daedalus-agent` **`9f1b286`**(PR #39 合入:`feat(package-landing-risk)` +
>   `fix(exec): no-op 不做落点体检 + 符号链接按自己的位置算落点`),
>   tarball `daedalus-agent-9f1b286.tar.gz`(`sha256:dd75837b8a52bbe7…`),
>   二进制 `sha256:1334dcb2147bcdd2…`
> **被控端自报版本**(四台逐台核对,非推断):
>   `daedalus-agent 0.1.0-dev commit=9f1b286 built=2026-11-16T11:27:09Z`
> **靶场**:KVM 宿主 `Masterwet_Server`(`10.66.0.9`,20 核,`/dev/kvm`),qemu 用户态网络;
>   控制面跑在宿主上(agent 端口只在宿主 `127.0.0.1:9471-9476` 转发)
> **矩阵**:Ubuntu 24.04 / Debian 12 / RHEL 9.9 / CentOS Stream 9(无快照腿)

## 1. 这件事是什么

控制面判定"高危"靠**包名前缀表**(`kernel`、`linux-image`……)。换个发行版换个包名就漏判,
而漏判的后果正是 MVP 承诺的反面:内核/固件类变更被当成低危 → 失败自动重试 → 整批继续。

落点体检是**第二道闸**,放在 agent 侧、**执行之前**:先看这个包会往哪落文件,
命中 `/boot`、内核模块、固件、init 就拒绝。它与前缀表**叠加**,不取代前缀表。

## 2. 命令原文

```bash
# 宿主上(~/daedalus-e2e/run-landing.sh;每轮用独立 fleet 根)
./run-landing.sh                                        # rpm 分支(默认 rhel9-a)
DAEDALUS_LANDING_MACHINE=ubuntu24-a ./run-landing.sh     # apt 分支
```

驱动实际调用:

```bash
bash tests/fleet/plan-c-e2e.sh --hosts \
  ubuntu24-a=localhost:9471 debian12-a=localhost:9472 \
  rhel9-a=localhost:9475 legacy-no-snapshot=localhost:9473
```

## 3. 两条分支的原文

### 3.1 rpm 分支(RHEL 9.9,目标 `shim-x64`)

```
== 落点体检腿:控制面按包名判 low,agent 按落点兜底拒(高危在装之前被挡住)
  ok   - 落点体检腿:目标 = shim-x64 @ rhel9-a(已装、落点含 /boot、不在控制面高危包名表)
  ok   - 前置快照:shim-x64 = 16.1-7.el9;/boot/efi/EFI/BOOT/BOOTX64.EFI mtime = 1770411678
  ok   - 落点体检腿前置:rhel9-a 的 agent 审计链 = 50 行
  plan submit plan-landing-critical.json: {"plan_id":"f00dfeed1a2b3c4d","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["rhel9-a-711fbb3e"]}],"results":[{"node_id":"rhel9-a-711fbb3e","intent_id":"f00dfeed1a2b3c4d/rhel9-a-711fbb3e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"persistent_failure","tx_id":"","message":"apply failed, no rollback point for kind=package: exec: persistent failure: refused: package shim-x64 lands on critical path /boot/efi/EFI/BOOT/BOOTX64.EFI (high-risk by landing points; control plane classified it low)"}],"recovery":[]}
  ok   - 落点体检腿 → 控制面按包名判 low(max_risk=low;判 high 就走别的通道了)
  ok   - 落点体检腿 → status=dirty
  ok   - 落点体检腿 → 单台持久失败(outcome=persistent / result=persistent_failure)
  ok   - 落点体检腿 → 文案点名 critical path(是 agent 按落点拒的)
  ok   - 落点体检腿 → 文案不含控制面准入的中文措辞「无回滚点」
  ok   - 目标机实况:shim-x64 版本未变(16.1-7.el9)
  ok   - 目标机实况:/boot/efi/EFI/BOOT/BOOTX64.EFI mtime 未变(1770411678)
  ok   - agent 审计链 50 → 51 行:intent 确实到过那台机(与 atomic 那台的零增长相反)
  ok   - agent 审计链留痕(原文):{"args": {"kind": "package", "target": "shim-x64"}, "entry_hash": "645ca204ec6e7cb46a90d0c3d360eac398452ec7d45019f5ed25288b66076249", "identity": "f00dfeed1a2b3c4d/rhel9-a-711fbb3e/0", "outcome": "apply_failed: exec: persistent failure: refused: package shim-x64 lands on critical path /boot/efi/EFI/BOOT/BOOTX64.EFI (high-risk by landing points; control plane classified it low) (no rollback point for kind=package)", "policy_version": "1.0", "prev_hash": "aa3f16d2ffff73eaf42d1bab932de0d3bbcb2cdfa27e609df527102a38b51b0c", "timestamp": "2026-11-16T11:42:16.913845+00:00", "tool": "apply_intent.failure"}
```

### 3.2 apt 分支(Ubuntu 24.04,目标 `base-files`)

```
== 落点体检腿:控制面按包名判 low,agent 按落点兜底拒(高危在装之前被挡住)
  ok   - 落点体检腿:目标 = base-files @ ubuntu24-a(已装、落点含 /boot、不在控制面高危包名表)
  ok   - 前置快照:base-files = 13ubuntu10.5;/boot mtime = 1794464315
  ok   - 落点体检腿前置:ubuntu24-a 的 agent 审计链 = 94 行
  plan submit plan-landing-critical.json: {"plan_id":"f00dfeed1a2b3c4d","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["ubuntu24-a-7b3dbdd2"]}],"results":[{"node_id":"ubuntu24-a-7b3dbdd2","intent_id":"f00dfeed1a2b3c4d/ubuntu24-a-7b3dbdd2/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"persistent_failure","tx_id":"","message":"apply failed, no rollback point for kind=package: exec: persistent failure: refused: package base-files lands on critical path /boot (high-risk by landing points; control plane classified it low)"}],"recovery":[]}
  ok   - 落点体检腿 → 控制面按包名判 low(max_risk=low;判 high 就走别的通道了)
  ok   - 落点体检腿 → status=dirty
  ok   - 落点体检腿 → 单台持久失败(outcome=persistent / result=persistent_failure)
  ok   - 落点体检腿 → 文案点名 critical path(是 agent 按落点拒的)
  ok   - 落点体检腿 → 文案不含控制面准入的中文措辞「无回滚点」
  ok   - 目标机实况:base-files 版本未变(13ubuntu10.5)
  ok   - 目标机实况:/boot mtime 未变(1794464315)
  ok   - agent 审计链 94 → 95 行:intent 确实到过那台机(与 atomic 那台的零增长相反)
  ok   - agent 审计链留痕(原文):{"args": {"kind": "package", "target": "base-files"}, "entry_hash": "901605e2ef1e7ec70e8e67530d9a0e6d74c1efdb94297a30506ba3c3aebc044f", "identity": "f00dfeed1a2b3c4d/ubuntu24-a-7b3dbdd2/0", "outcome": "apply_failed: exec: persistent failure: refused: package base-files lands on critical path /boot (high-risk by landing points; control plane classified it low) (no rollback point for kind=package)", "policy_version": "1.0", "prev_hash": "4a02cd4040510adb56d70fffb87d52a3845731471e23dbe546161dfbf62dd7f8", "timestamp": "2026-11-16T11:42:31.191616+00:00", "tool": "apply_intent.failure"}
```

## 4. 汇总

```
plan-c-e2e: 46 通过 / 0 失败      (rpm 分支)
PLAN_C_EXIT=0
plan-c-e2e: 46 通过 / 0 失败      (apt 分支)
PLAN_C_EXIT=0
```

## 5. 这一腿证明了什么

| 判据 | 实测 |
| --- | --- |
| 控制面按**包名**判 low(`shim-x64` / `base-files` 都不在 `high_risk_packages` 表里) | `max_risk=low` |
| agent 在**执行之前**按**落点**拒 | 文案点名 `critical path` + 命中的那条路径 |
| 拒的是**持久**失败(不许自动重试) | `outcome=persistent` / `result=persistent_failure` |
| 目标机**没被动过** | 包版本未变;`/boot` 下那个文件的 mtime 未变 |
| 拒绝发生在**目标机上**(不是控制面拦在前面) | agent 审计链 **50 → 51 行**、**94 → 95 行**,并留下带 `critical path` 的原文 |

三条腿的对照关系值得记一笔:同样是"没装上",**atomic 那台是审计链零增长**(RPC 根本没发出去),
**落点体检这台是审计链增长**(intent 到了,被 agent 按落点挡住)。两者都不是"装了但报错"。

文案里那句英文 `no rollback point for kind=package` 是 agent 给**所有** apply 失败加的外壳,
不是控制面准入拒的信号 —— 控制面准入拒的措辞是中文「无回滚点」,这一腿断言的就是它**不**出现。

## 6. 过程中踩的三个坑(如实记:三个都是**驱动侧**,不是产品缺陷)

| # | 症状 | 真因 | 处置 |
| --- | --- | --- | --- |
| ① | `/boot` 下文件的 mtime 前后都是 `?`(读不到) | RHEL 的 `/boot`、`/boot/efi` 是 `700 root:root`,ssh 用户连 `stat` 都读不到 —— **读不到被当成了"没变"** | `sudo stat`;读不到直接判 FAIL,不假装"没变" |
| ② | apt 分支恒判"前置条件不成立" | 用了 `rpm -ql … \| head -n 1 \|\| dpkg-query -L …`:管道里 `head` 的退出码**永远是 0**,`rpm` 根本不存在时也会静默吞掉,`dpkg-query` 一条都不跑 | 改成 `if command -v rpm; then …; else …; fi` |
| ③ | Ubuntu 上找不到候选包 | `grep '^/boot/'` 匹配不到 `base-files` 拥有的**路径 `/boot` 本身**(没有尾斜杠) | 与 agent 的判定口径对齐:`^/boot($|/)`;且落点归属改由"该包自己的清单"确认(`dpkg -S` 是子串匹配,会把 `/usr/lib/bootloader` 也带进来) |

> ①是这三个里最危险的一个:**核实通道读不到值,断言就会"因为空而通过"**。
> 这一类"查不了"必须 fail-closed,不能当成"没变"。

## 7. 未覆盖(如实记)

1. **"未装包"那条路径没在真机上走到**:两条分支验的都是"已装 → 本地清单";
   "未装 → 下载到临时目录再列"(`apt-get download` + `dpkg-deb -c` / `dnf download --destdir` + `rpm -qlp`)
   只在 `daedalus-agent` 的单测里用假宿主验过 —— 真机上跑它要联网,还会撞上
   `dnf download`(dnf-plugins-core 提供)在最小化安装上缺失的情况。
2. **"工具缺失 → 跳过体检"这条分支没在真机上走到**(同上,单测覆盖)。
3. **postinst 副作用**没算独立维度(任意脚本,静态判不了)—— 这是**有意不做**,不是遗漏。
4. 只覆盖 `package.set` 一个 kind;`file` / `service` / `container` 三类没动。
5. `daedalus-host` 控制面未改:本轮**没有**动前缀表,也没有把判定搬回控制面。
