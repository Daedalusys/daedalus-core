# Daedalus：实机调试缺口 + CD 部署可行性评估

- status: archived（结论有效期 2026-11-06；**本文结论已被后续落地取代**——2026-11-15 起 CD 已在生产形态运行：daedalus-core/.github/workflows/build.yml 的四条自托管实机腿 deploy-verify-{kvm,bootc-switch,iso,guest-probes} 全绿（main run 44016226073，2026-11-17 04:18→06:03，8/8 success）。本文「零自托管 runner / 零部署腿 / 零 bootc switch」的判断不再成立，正文保留作历史评估，不删改）
- created: 2026-11-06
- archived-at: 2026-11-17
- 前置阅读：`.plans/real-machine-verification.md`（18 项实机验证计划）、`daedalus-core/docs/superpowers/specs/2026-10-17-fleet-control-plane-design.md`（fleet 设计稿）、`daedalus-core/docs/superpowers/plans/2026-11-05-p0.5-plan-c-control-plane.md`（Plan C 控制面）

---

## TL;DR

1. **产线齐全，但没有任何一步把系统真的启起来。** 六个仓、9 个 workflow、16 个 job，全部跑在 GitHub 托管 `ubuntu-24.04` 上，**零自托管 runner、零 `bootc switch/install`、零 SSH 到真机、零 VM 引导**。镜像能构建、能推 `ghcr.io`、能出 qcow2，然后就到此为止。
2. **真机验证已有成熟计划但大面积停摆。** `.plans/real-machine-verification.md` 的 18 项里，已完成的只有「仓同步 / harness 入库 / 本地 user-bus 集成 / tx 守门 / agent 安装 mTLS / qcow2 引导（ThinkPad 一次）」；ostree 部署链、systemd 沙箱反证、audit 真 inode、破坏性插件、fleet 控制面 E2E 等仍是**全零覆盖**或 BLOCKED。卡点清一色是「人肉 sudo 密码 / 缺硬件 / 缺 LLM key」。
3. **CD 的机制不存在，但地基基本都在。** 触发点（`:latest`/`:upstream` 镜像晋升）、被测脚本（`tests/real-machine/bootc-verify.sh` 等）、靶机（`Masterwet_Server` 实测够格）都有；缺的是**一台上自托管 runner 的靶机 + 一个 `workflow_run`/发布触发的部署腿 + 制品晋级为可复现 release**。
4. **关键陷阱：不能拿产品自己的 fleet 当 CD 机制。** hub→agent 的「下发意图→应用」整条链结构齐全，但执行器是空的——`applyStub` 只打日志返回 `nil`，却回报 `SUCCESS`（`daedalus-agent/internal/rpc/agent_service.go:211-221`）。在它变成真执行器之前，任何「fleet 部署成功」都是假的。**CD 必须是外部工具（自托管 runner + bootc/KVM），而不是用 Daedalus 部署 Daedalus。**

---

## 1. 现状事实（有据可查）

### 1.1 仓与职责

| 仓 | 职责 | CI 文件 | 最新 release |
|---|---|---|---|
| `daedalus-core` | 镜像编排 + 5 个 core runtime（host/audit/tx/smoke/plugin-pack）+ copilot 源码 + `internal/fleet` | `build.yml` | 无（镜像即发布物，禁止独立 release） |
| `daedalus-sdk` | 安全核心包 + objectmodel + proto | `release.yml`/`test.yml` | v0.1.1 |
| `daedalus-plugins` | 11 个 Go 能力插件 monorepo | `release.yml`/`test.yml` | v0.3.0 |
| `almalinux-bootc` | 基座镜像 fork（10-kitten headless） | `build.yml`/`rebase-check.yml` | 无（滚动 tag） |
| `daedalus-agent` | 异构被控单二进制 | `ci.yml` | 无（已登记缺口） |
| `daedalus-ci-watch` | CI 红绿巡检（Windmill） | `ci.yml` | 无 remote 之外的 release |

### 1.2 流水线（`.github/workflows/`，全部 `ubuntu-24.04`）

- **`daedalus-core/build.yml`**（唯一与交付相关的 workflow）：
  - `test` job：go/deno/i18n/drift/镜像守门；其中一步跑 **`bootc-verify.sh --only-preflight --no-kvm`**——真 KVM 引导腿被显式关掉（`build.yml:256-261`）。
  - `build-image` job：`just build` → podman 构建 → 插件清单/零残留/bootc lint → 推 `ghcr.io/daedalusys/daedalus-os:<sha>`；`main`/tag/dispatch(PUSH=true) 时晋升 `:latest` + `:upstream`（`:upstream` 注释明写「给 KVM guest 上 `bootc upgrade` 用的稳定别名」，但**那是一个注释，不是一步**）。
  - `build-qcow2` job：`bootc-image-builder --local --type qcow2`（rootful 容器）→ 上传 Actions artifact `daedalus-os-qcow2-headless`，**90 天过期，从不进 Release**。
- **`almalinux-bootc/build.yml`**：构建/签名/推基座镜像；`verify-headless-live` 用**容器**（`/sbin/init` + sshd，`-p 127.0.0.1:22022`）冒充「live」，且 `continue-on-error: true`（`build.yml:229`）。
- **`daedalus-agent/ci.yml`**：`install-test` 在 `jrei/systemd-ubuntu:22.04` **Docker 容器**内断言 `systemctl is-active`——容器，不是宿主。
- **`daedalus-plugins`/`daedalus-sdk` release**：打 tag → 11 个 `*.plugin.zip` / 源码 tarball+SBOM 进 GitHub Release。

### 1.3 制品链（跨仓耦合）

```
daedalus-plugins @v*  ──11 × *.plugin.zip──►  core build-image（**未钉版本，取 latest**，build.yml:397）
daedalus-sdk     @v*  ──goreleaser source+SBOM──►（无 SLSA provenance，sdk#40）
almalinux-bootc  @main ──► ghcr :10-kitten（cosign 签名，digest 被 core Containerfile:10 钉死）
daedalus-core    @main ──► ghcr daedalus-os:{<sha>,latest,upstream} + qcow2 artifact
```

**可复现性风险**：plugins→core 这条边不钉版本；SDK 边名义上钉 `go.mod` 版本，但当前 `go.mod` 是占位 `v0.0.0`，实际回落到默认分支——等于**两条边都不钉**。

### 1.4 门禁姿态（GitHub 实测）

| 仓 | `main` 分支保护 | environments | 自托管 runner |
|---|---|---|---|
| daedalus-core | 无（404） | 0 | 0 |
| daedalus-plugins | 有（strict, required check） | 0 | 0 |
| daedalus-sdk | 有 | 0 | 0 |
| almalinux-bootc | 有（宽松，允许 force-push） | 0 | 0 |
| daedalus-agent | 无（404） | 0 | 0 |

组织级自托管 runner：**0**。没有任何 workflow 使用 `workflow_run` / `environment:` / `deployment:`。

---

## 2. 哪些部分必须实机调试

只有真机 / 真内核 / 真硬件 / 真 KVM 才能证伪的面（本地单测用 fake/fixture 撑着，永远抓不到）。按可执行宿主分层。

| # | 验证面 | 为什么本地测不到 | 现状 | 宿主需求 |
|---|---|---|---|---|
| A1 | **qcow2 / ISO 真引导** | CI 只产出不引导 | qcow2 已过一次（2026-10-29 ThinkPad，证据已入库）；**ISO 零覆盖** | x86-64-v3 + KVM |
| A2 | **ostree 部署链** `bootc install`→引导→`switch`→`upgrade`→`rollback` | 「原子更新+一键回滚」是产品核心主张 | 🟡 `switch`/`rollback` 已验(P2-1);`install to-disk` 与 `upgrade` 仍 PROBE_WIP/BLOCKED(见注 2) | x86-64-v3 + KVM + `bootc` + loop/qcow2 目标盘 |
| A3 | **22 个 systemd unit + 17 个 drop-in 真加载** | 纸面声明 | ✅ 静态段 core#63 已修;**真 guest 内五条沙箱反证全部成立**(EROFS / DynamicUser / credentials / 回环vs外部 / SIGSYS 159) | 可跑镜像的宿主 |
| A4 | **audit 追加写保护 + 轮转** | 集成测试用 `noopAttrs{}` 注入，`chattr +a` 从未碰真 inode | 🟡 追加写保护 + verify 已验(`-----a-----` / `truncate` EPERM / chain intact);**轮转 FAIL** → core#92 | root / sudo |
| A5 | **破坏性插件** diskclean / organize / blueprint apply | 「唯一会删用户数据的路径」零覆盖，测试从不 spawn `daedalus-tx` | ✅ guest 内验完:blueprint 失败回滚复原 sha256;disk.clean/organize 真 tx 三段 + rollback;宿主 lsblk 不变 | KVM guest |
| A6 | **fs 写路径守卫** | `pathguard.ValidatePath(path, true)` 把 `write` 形参丢弃 → 二档写守卫（拒 `/etc/shadow` 等）对 `fs.write_file` **不生效** | ✅ **已修并合并**(plugins#78)；真机复现过缺陷，修复后 A6 探针真机判 PASS（`write denied: path resolves to read-only system dir "/etc"`） | 真机复现 |
| A7 | **只读插件真数据源** service/pkg/sysinfo/proc/trace/dupe | fixture 期望 vs 真输出；`dupe` 无 inode 会把硬链接报重复 | ✅ 六条全跑;dupe 硬链接误报**已修并合并**(plugins#77)；真机前后对照 `wasted_bytes` 54→27、`files` 3→2 | 装好的产品/镜像 |
| A8 | **agent 真机 install/mTLS/uninstall** | 已做（Ubuntu 24.04）并抓出 P0（`install.sh` ExecStart 缺 `daemon`→agent#2） | 🟡 Ubuntu 已通；**apply 仍是 stub** | 异构宿主 |
| A9 | **fleet 控制面 E2E**（hub→agent 下发 Plan） | `tests/fleet/EVIDENCE/2026-11-XX-plan-c-e2e.md` 有 **21 个 `<待填>`**，从未真跑；且 `applyStub` 让结论注定为假 | ⛔ 从未跑 + 执行器空 | 1 控制面 + 3 异构被控 |
| A10 | **异构 E2E**（Ubuntu/Debian/CentOS） | 只完成 1/3（Ubuntu） | 🟡 Debian/CentOS 无宿主 | ≥2G RAM、systemd、sudo 的宿主 ×2 |
| A11 | **copilot 真 LLM / watchdog / Deno 权限边界** | 缺真 key | 🟡 Deno 权限边界成立;`--dry-run` 被 core#89 挡住(部署里插件目录 0700);watchdog/真 provider `BLOCKED: no key` | key + 真机 |
| A12 | **镜像内插件 degraded 盘点** | 8 个设备插件「有 manifest 无 bin」(core#53 已 CLOSED) | ✅ root 口径 12 插件全 ok(集合从 76 `CAPS` 派生);非 root 口径 copilot degraded → core#89 | 同上 |

**注 2**（2026-11-07，guest 实机探针套件；2026-11-08 更新状态）：A3/A5/A7/A12 已全绿；A4 3/4、A6 复现成立、A11 部分（缺 key）—— 这三个的**缺陷本体已修并合并**（见注 3），探针也已升级为可判定断言（A4-4 / A6 / A7-6 / A11-1）。A2 残余 `bootc install to-disk` / `bootc upgrade` 仍 PROBE_WIP/BLOCKED。证据 `daedalus-core/tests/real-machine/logs/2026-11-07-guest-probes/EVIDENCE.md`，修复后证据 `logs/2026-11-08-a6-fs-write-guard/` 与 `logs/2026-11-08-plugins76-hardlink/`（后者含修复前后对照）。**注意**：探针在 CI 上的转 PASS 尚未被确认 —— core#100 修好门禁缓存污染后需重跑一次出货档 CI。

**注 3**（2026-11-07～11-08，core#88/#89/#91/#92 + plugins#76 全部已修并合并；门禁缓存污染 core#100 与 A7-6 加固 core#101 亦已合并）：
- **core#92 → sdk#45**（MERGED，main @ 73457ce）。查证发现是**两层叠加缺陷**：① `O_RDWR` 打开 `+a` 文件被内核 `may_open` 判 EPERM（issue 记述的那层）；② `unix.IoctlSetInt` 按值直传而 `FS_IOC_SETFLAGS` 要求指针 → EFAULT（一直被 ①憋住、从未暴露）。两层都改（`O_RDWR|O_APPEND` + `IoctlSetPointerInt`），并补真 inode 用例（此前 `Rotate` 全部注入 `noopAttrs`，`FS_IOC_SETFLAGS` 从未碰真 inode）。**判据必须在 ext4/xfs 上做**：btrfs（靶机根 fs）对 `+a` 语义不同，连 `O_APPEND` 都拒，会误判。回归有效性用「分别只退回一层」证明。
- **core#89 → core#96**（MERGED，main @ 851bbcf）。根因用**逐层 tar** 定位：同一目录在两层里 mode 相反（layer2 `drwx------` / layer7 `drwxr-xr-x`），其余 10 个插件两层都 755；`podman export`/`podman run` 只给 overlayfs 合并视图（755），掩盖了层内真相。机制：ostree union checkout 对**已存在**目录不 `fchmod`，最终 mode 由「首次出现那一层」决定。修在源头（`mktemp -d` 的 0700 在 `mv` 前归一化成 0755），并加两道钉子（构建产物侧 + 镜像逐层首现侧）。
- **core#91 → sdk#46 + plugins#78 + core#99**（全 MERGED）。**不改 `pathguard` 包行为**，只让 fs 消费方显式走二档。执行中 worker 识别出**计划本身有逻辑矛盾**：Task 1 要求「不存在目标必拒」而 Task 2 要求「`write_file` 能新建文件」——字面形态会打挂主路径与既有 `TestWriteFile_MkdirListMove`。裁定：目标端走消费方助手 `validateWriteTarget`（先一档规范化，再对**最深的已存在祖先**走二档），`move_file.src` 仍走裸 `ValidateWritePath`（移动=读出+删除，必须知道 inode）。详见 `daedalus-core/docs/superpowers/plans/archive/2026-11-07-core91-fs-write-guard.ledger.md`。
- **plugins#76 → plugins#77 + core#98**（全 MERGED）。dupe 采集 `(dev, ino)` 并按**复合键**折叠同源文件（只看 `Ino` 会误折叠跨设备同号文件）；`dev/ino` 取不到时**不折叠**（宁可漏不错）。真机前后对照：`files` 从 3 个降到 2 个，`wasted_bytes` 54 → 27。
- **core#88 → core#97**（MERGED）。`real-machine-verify.sh` 的 hostfwd 写死 2222（另两个脚本早有 `VERIFY_SSH_PORT`）→ 统一到共享 `lib/ssh-port.sh`，并在起 qemu 前探测端口占用：被占则**显式报冲突并退 3**，不再落到 `QEMU_EXITED`（那会让查的人先怀疑产物坏了）。
- **门禁缓存污染 → core#100（MERGED，main @ ff367bd）**：run 42376538371（已含 #89/#92 修复）仍判 FAIL，真因是 `driver_init` 复用了 **12:09 生成的旧 qcow2**（日志 `OK 复用已构建产物`）——自托管 runner 的 `VERIFY_WORK` 跨 run 持久化，而复用判据只看「文件在不在」，不看被测镜像是否变了。**这条不修，所有实机门禁的结论都不可信。** 修法：复用前比对被测镜像的 **config digest**（`.Id`，重推不变；manifest digest 会漂移），可变 tag（latest/main/分支名）先刷新；取不到 digest / 指纹为空 / 判定函数不可用 一律 fail-closed；`VERIFY_WORK` 按 `run_id` 隔离；旧产物目录有界清理（否则磁盘吃满会让门禁以「镜像坏了」的样子红）。顺带发现**另一个假绿来源**：从 CI artifact 下载的 qcow2 不是本机从 `$IMAGE` 构建的，不能算「`$IMAGE` 可引导」的证据，结论里必须带产物来源。
- **ISO 腿 `P2-2b NOT_DONE` 是独立问题**（未修）：run 42376538371 的 ISO 腿等 40 分钟未完成装机（`P2-2a` 四条全 PASS），与 #89/#92 无关。需 core#100 修好门禁后重跑一次出货档 CI 才能判断真因。

**注**：`.plans/real-machine-verification.md` 的 Todos 7–14/16/18 与上表一一对应；该计划在 2026-10-29 收工时的阻塞项是：ThinkPad 免密 sudo、≥40G 的 x86-64-v3 宿主、Debian/CentOS 宿主、LLM key。**其中「≥40G 的 x86-64-v3 宿主」现在有解（见 §3.4）。**

---

## 3. CD 部署可行性

### 3.1 「每次发版」的现成锚点

| 事件 | 现在会怎样 | 能否当 CD 触发 |
|---|---|---|
| core 推 `main` | 构建镜像 → 推 `:<sha>` + 晋升 `:latest`/`:upstream` + 出 qcow2 | ✅ 最自然（镜像 `:latest` 就是「已发版」） |
| core 打 `v*` tag | 同上 | ✅ |
| core `workflow_dispatch` + `PUSH=true` | 同上 | ✅（手动推） |
| plugins 打 `v*` tag | 出 11 个 zip | ⚠️ 需先重出镜像，才有新镜像可部署 |
| sdk 打 `v*` tag | 出 source+SBOM | ⚠️ 同上 |
| bootc 基座 `main`/周 cron | 出基座镜像 | ⚠️ 只影响后续 core 构建 |

**结论**：CD 的触发点就绑 **core 镜像的 `:latest`/`:upstream` 晋升**。用 `on: workflow_run`（`build-image` 成功后）或直接在 `build-image` 里加一个自托管 job（`if: 出货档`）即可。

### 3.2 四种「部署到机器」的机制（对比）

| 机制 | 做法 | 保真度 | 是否动宿主盘 | 适配现有资产 |
|---|---|---|---|---|
| **M1 bootc 原生** | 靶机本身是 daedalus-atomic 节点；`bootc switch :latest` / `bootc upgrade` → 重启进新 deployment | 最高（同时验证 A2 的核心主张） | 会（要 loop/qcow2 或专用机） | `just qemu` 逻辑、`bootc-verify.sh` 部分 |
| **M2 KVM guest** | 宿主普通；把 qcow2/ISO 起成 QEMU/KVM guest，串口等 `login:`，再在 guest 内跑 harness | 高（真内核/真启动） | 不会（只碰镜像文件） | **`tests/real-machine/bootc-verify.sh` 就是干这个的** |
| **M3 容器** | `podman run --privileged --systemd=always` 直接跑镜像，跑 A–F 探针 | 中（无真引导、无真硬件） | 不会 | `tests/deploy/headless-test.sh` 已就绪 |
| **M4 fleet 自部署** | 控制面推 Plan 给 agent 节点 | —— | —— | ⛔ **不可用**：`applyStub` 空转还报 SUCCESS |

### 3.3 推荐架构：M2 为主力，M1 专测「原子更新」卖点，M3 做快速前哨

```
                     发版事件（core :latest/:upstream 晋升）
                                   │
                    ┌──────────────┴───────────────┐
                    ▼                              ▼
        [快速前哨] M3 容器探针            [主力] M2 KVM 引导
        headless-test.sh A–F             bootc-verify.sh：qcow2/ISO → 串口 login
        （秒级，先生一道闸）              （分钟级，真引导；PANIC/TIMEOUT 即红）
                                               │
                                               ▼
                                   [专测] M1 bootc switch/upgrade/rollback
                                   （qrcow2 内或专用牺牲机，验证 A2 主张）
                                               │
                                               ▼
                                   证据回传：串口日志 / bootc status 快照 /
                                   sha256 / qcow2 digest → artifact + 评论到 commit
```

- **M2 是主力**：现有 `tests/real-machine/bootc-verify.sh`（`--only-preflight` 已能在任何机跑守门；去掉 `--no-kvm` 即是全流程）几乎可直接搬到自托管 runner，判定输出 `LOGIN`/`PANIC`/`QEMU_EXITED`/`TIMEOUT_EMPTY`，产物在 `~/daedalus-verify/`。
- **M1 只在专用牺牲机上做**，且**绝不指向宿主真盘**（`truncate -s 40G` + `losetup`，或直接在 M2 的 guest 内做）——这是 `.plans/real-machine-verification.md` 的 Must-NOT 红线。
- **M3 先在托管 runner 上就能加**（`podman run` 不需要 KVM），几乎零成本，先把「镜像能起 systemd + 插件不 degraded」这道快闸建起来。

### 3.4 靶机：`Masterwet_Server`（10.66.0.9）实测够格

2026-11-06 实测（此前 10-29 记录为「不可达」，现已恢复）：

| 项 | 实测值 | 判定 |
|---|---|---|
| CPU | 12th Gen i7-12700H，20 核，**avx2/bmi1/bmi2/fma/movbe/xsave 全有** | ✅ x86-64-v3，能跑目标镜像 |
| KVM | `/dev/kvm` `crw-rw-rw-`（免 sudo 可写） | ✅ |
| 内存 | 15 GiB（可用 9.5 G） | ✅ |
| 磁盘 | `/` 451 G 可用（NVMe） | ✅ ≥40 G 富余 |
| 工具 | podman / qemu-system-x86_64 / qemu-img / just / git / deno **全在位** | ✅ |
| 缺 | `bootc`/`ostree`/`rpm-ostree` 未装；`sudo -n` 需密码 | ⚠️ 两项待人类 |
| 网络 | ghcr 可达（401=需认证） | ✅ |

**其他候选**（不推荐做 VM 主力）：`ThinkPad` 当前 SSH 拒连（192.168.1.4）；`Ali_ECS` avx2 但**无 KVM**、仅 14 G——适合当**异构被控**（A8/A10 的 Ubuntu 宿主）；`Team_VPS` 无 KVM、3.6 G；`Fedora_Jiaolong` 超时。

### 3.5 要变成真 CD 还缺什么

1. **一台自托管 runner**（`actions/runner`，标签如 `[self-hosted, linux, x64, daedalus-kvm]`），装在 `Masterwet_Server` 上，配 systemd 自启。
2. **免密 sudo**：给 runner 专用用户 `NOPASSWD`（装 `bootc`/`ostree`、`losetup`、挂载、`chattr`、起 guest 都要）。
3. **一个部署腿**：`workflow_run`（`build-image` 成功）或 `build-image` 内追加 `deploy-verify` job（`runs-on: [self-hosted,...]`，`if: 出货档`），把 `bootc-verify.sh` + `headless-test.sh` 跑起来，红则 job 红。
4. **制品晋级**：qcow2（甚至 ISO）从 90 天 artifact 提为 **GitHub Release 资产**；ISO 接入 CI（现在 `just iso` 只本地）。
5. **可复现性**：plugins→core 钉版本（现在取 latest）；SDK 去掉 `v0.0.0` 占位回落。
6. **结果回传**：串口日志/`bootc status`/digest 作为 artifact + 回评到触发 commit/PR。
7. **（可选）隔离策略**：M1 的 bootc 换部署要么在 guest 内做，要么用一台可随时清空的专用机；破坏性验证（A5）只在 guest 里跑。

---

## 4. 最小可行 CD（分阶段，按性价比排序）

**P0（半天，零新硬件）**：在现有托管 runner 上加 M3 容器探针 job——pull `:sha` → `podman run --privileged --systemd=always` → `headless-test.sh` A–F → 红则 job 红。先把「构建绿 ≠ 能跑」这条缝补上。

**P1（1–2 天，需 `Masterwet_Server` + 免密 sudo）**：装自托管 runner；加 M2 job——`bootc-verify.sh`（qcow2 全流程）在 KVM 上真引导，串口 `login:` 为通过、`PANIC`/`TIMEOUT` 为失败；串口日志入库+回评。**这一步直接把 A1 变成每次发版自动有的证据。**

**P2（部分完成，2026-11-06）**：
- **P2-1 ✅ 换部署/回滚真机验证**：`bootc switch --apply` + `bootc rollback --apply` 在真 KVM guest 内跑通，判定 `SWITCH_ROLLBACK_OK`（双 digest + 内容 marker 证据）。脚本 `tests/real-machine/bootc-switch-verify.sh`，证据 `tests/real-machine/logs/2026-11-06-bootc-switch/`，CI 门禁 `deploy-verify-bootc-switch`。**产品的「原子更新+一键回滚」主张第一次有自动化的实机判据。**
- **P2-2 ✅ ISO 产出 + 引导 + 无人值守装机**(2026-11-06,rev-2 闭环)。此前 `just iso`(legacy `bootc-image-builder --type iso`)**完全构建不出 ISO**(osbuild 每个包下载都失败,上游已把该 image type 标为 legacy 不再支持 —— 6 组对照实验定性,见 core#84)。现已改用官方推荐的新 CLI `image-builder-cli` + `generic-iso`,并把 ISO 载荷补在**一次性派生镜像**里(发布产物一格不动)。判据分两段:**P2-2a** = ISO 引导出可用的 live 系统,用 SSH 自证(登录 / 根是 live 介质 / 系统就是本镜像;live 内核参数没有 `console=ttyS0`,串口不可能有输出,故判据落在 SSH 上);**P2-2b** = live 环境里的 oneshot 单元对空虚拟盘 `bootc install to-disk`,装完重启 → 引导该盘 → 串口 `login:`,并独立断言目标盘有分区表。靶机实跑判定 `INSTALLED_LOGIN`,退出码 0。脚本 `tests/real-machine/iso-verify.sh` + `iso-autoinstall.{sh,service}`,证据 `tests/real-machine/logs/2026-11-06-iso-unattended-install/`,CI 门禁 `deploy-verify-iso`(PR #85 + 本次)。
  - **更正(推翻了本文件早前两版归因)**:①「用 blueprint 给 generic-iso 注入 kickstart」这条路**不存在** —— 实测传了 blueprint 构建仍成功但 manifest 里零 kickstart 痕迹(generic-iso 的 manifest 生成根本不接 blueprint),而且 **generic-iso 里没有安装器**(payload 里 `rpm -q anaconda` = not installed),GRUB 的 "Install …" 只是菜单文案;`anaconda-iso` 被新 CLI 硬拒。②「安装器停在交互态」也是错的 —— 真因是 payload 的 initramfs **缺 `dmsquash-live`**,那张 ISO 连 live 根都挂不起来(这才是「GRUB 之后串口再无输出、挂空盘 20 分钟目标盘零写入」的原因)。文档里 "unattended, installs to the first disk" 说的是 **`anaconda-iso`**,与本更正一致。
  - **已知边界**:P2-2b 在 CI 里是**硬判据**(`deploy-verify-iso` 显式 `VERIFY_REQUIRE_INSTALL: "1"`,脚本默认也是 1;`LIVE_OK` 不算绿,只有本地排查可临时设 0);装好的系统 root 锁定、不设口令,判据停在串口 `login:`,不做登录后交互验证;目标盘设备名由派生层 `iso.yaml` 点名(`daedalus.install.disk=/dev/vda`),换设备名要重新出 ISO;CI 侧这条腿尚待一次出货档触发确认(本证据是靶机手工实跑)。
  - ⚠️ **本轮我先给出过两个错误结论**（「阿里云镜像替换」/「kitten 基座」），均被后续对照实验推翻，以本条为准。
- **P2-2 的独立收获（已修，PR #83）**：镜像 `/etc/dnf/vars/` 缺 `releasever`，导致**容器/构建器上下文里装不了包**（`dnf install` 失败）——`64-dnf-mirrors.sh` 补 `releasever=10`，实测 `dnf download` 恢复正常。**更正**：P0 的 C3 探针失败此前被判为「环境问题」并改成 SKIP，**那个判断是错的**，与本缺陷同源。
- **P2-3 🟡 实施完成、待合并**：qcow2/ISO 晋级为 GitHub Release 资产的代码在 **PR #90**（OPEN, MERGEABLE），靶机实跑已产出 Release `9683defbf631`（`qcow2-9683defbf631.qcow2` 1314914304B / `iso-9683defbf631.iso` 1212784640B），且 qcow2 的 sha256 在「腿日志 / 靶机独立复算 / Release digest」三处同一值。**该能力尚未进 main**，故不标 ✅。
  出货档实跑 run `42181586948` 产出 Release `9683defbf631`：`qcow2-9683defbf631.qcow2`（1314914304 B）
  与 `iso-9683defbf631.iso`（1212784640 B），见
  <https://github.com/Daedalusys/daedalus-core/releases/tag/9683defbf631>。tag 实测指向本次 run 的
  commit（与 `GITHUB_SHA` 逐字相同）；qcow2 的 sha256 在 CI 自报、靶机独立复算、Release digest 三处命中
  同一值。证据见 `daedalus-core/tests/real-machine/logs/2026-11-06-release-assets/EVIDENCE.md`。
- 注：`bootc upgrade` 与 guest 内手工 `bootc install to-disk` 未单独跑（后者在 builder 出的 qcow2 里已由 builder 内部完成）；见证据文件「尚未覆盖」段。

**P3（P0.5 之后）**：异构 fleet 测试床——`Masterwet_Server` 当控制面 + `Ali_ECS`（Ubuntu）等当被控，跑 `plan-c-e2e.sh`。**前置：`applyStub` 换成真执行器**（见 §5）。

---

## 5. 风险与红线

1. **别用空转的 fleet 冒充 CD**（最重要）：`applyStub`（`agent_service.go:211-221`）回报 SUCCESS 却什么都不做；`Heartbeat.pending_intents` 恒 0（agent#9）。在换真执行器前，A9 的「成功」都是假的。**CD 走外部工具，不走产品自身。**
2. **别碰宿主真盘**：所有 `bootc install`/破坏性插件只在 loop/qcow2/qcow2-VM 内。宿主 `lsblk`/`fstab`/`cmdline` 前后必须一致。
3. **别把容器当 VM**：「容器内 systemd 起来了」「sshd active」≠「镜像能引导」，两者是不同证据等级。
4. **自我托管 runner 的安全面**：runner 上跑 PR 代码 = 任意代码执行；**只对受信任的 main/tag 出货档触发实机腿**，不要对 `pull_request` 开放自托管 runner。
5. **可复现性**：不钉版本 + 占位回落 → 同一 commit 不同天构建出不同镜像。CD 要可信，先钉死跨仓版本。
6. **缺 LLM key**：A11 只能验 `--dry-run` + watchdog，真 provider 需你给 key。

---

## 6. 需要你拍板

1. **靶机**：是否用 `Masterwet_Server`（10.66.0.9）当第一台自托管 runner？它现在够格，但要你在交互终端上装 runner 并给免密 sudo（+ 装 `bootc`/`ostree`）。还是另外给一台专用机？
2. **启动范围**：先做 P0（容器探针，零硬件）还是直接上 P1（真 KVM 引导）？
3. **M1 的宿主策略**：bootc 换部署是在 **guest 内**做（最安全，推荐），还是用一台**可随时清空的专用牺牲机**？
4. **制品晋级**：是否同意把 qcow2/ISO 提为 GitHub Release 资产并把 ISO 接入 CI？
5. **可复现性**：是否现在就把 plugins→core 的版本边钉死（会触及现有 release 流程）？

---

## 7. P0 + P1 落地清单（2026-11-06 拍板：靶机 `Mastwet_Server`；bootc 换部署在 guest 内）

### 7.1 P0 —— 容器行为探针（托管 runner，零新硬件）

**改动 1：`daedalus-core/tests/deploy/headless-test.sh` 增加 `local` 模式 + 退出码契约**

- `REMOTE_HOST` 为空或 `local` 时，`rsh()` 走本地 `bash -c`（不再 ssh）；默认保持 `Mastwet_Server` 兼容既有用法。
- 容器配方去掉 `--network=host`（`rescue-path.sh:80-87` 已证该选项会抢宿主 22 端口、让镜像的 NetworkManager 动宿主网卡）——所有探针都走 `podman exec`，不需要宿主网络。改用 `--privileged --systemd=always --cgroupns=host --tmpfs /run --tmpfs /tmp`。
- **新增退出码契约**：SUMMARY 后统计 `RESULT[*]` 里的 `FAIL` 数，>0 则 `exit 1`（现在恒 `exit 0`，是假绿）。
- `--selftest` 保持绿。

**改动 2：`daedalus-core/.github/workflows/build.yml` 的 `build-image` job 内、`Verify Image Bootc Lint And Variant` 之后加一步**

```yaml
- name: Headless deployment probes (systemd-in-container)
  shell: bash
  working-directory: daedalus-core
  env:
    REMOTE_HOST: local
    LOG_DIR: ${{ runner.temp }}/daedalus-headless-logs
  run: |
    # x86-64-v3 前置:镜像 glibc 硬要求;缺则**响亮失败**,绝不静默跳过
    missing=""
    for f in avx2 bmi1 bmi2 fma movbe xsave; do
      grep -qw "$f" /proc/cpuinfo || missing="$missing $f"
    done
    [ -z "$missing" ] || { echo "ERROR: runner CPU 不支持 x86-64-v3,缺:$missing"; exit 1; }
    bash tests/deploy/headless-test.sh localhost/daedalus-os:latest
```

- 镜像就在本 job 本地存储里（刚 `just build`），无需 ghcr 拉取。
- 门禁范围：容器未能到 running/degraded（脚本 `exit 3`）+ 任一探针 FAIL（新退出码契约）→ job 红。**不接受**「打了 SUMMARY 但恒绿」。

### 7.2 P1 —— KVM 真引导（自托管 runner on `Mastwet_Server`）

**改动 3：`build.yml` 加 job `deploy-verify-kvm`**

- `runs-on: [self-hosted, linux, x64, daedalus-kvm]`；`needs: build-qcow2`；`if:` 同 `build-qcow2` 的出货档；`timeout-minutes: 45`（防 runner 未注册时无限排队）。
- 步骤：checkout（`persist-credentials: false`）→ `actions/download-artifact@9000827…` 取 `daedalus-os-qcow2-headless` → `sudo bash tests/real-machine/bootc-verify.sh --qcow2 <qcow2>` → `actions/upload-artifact@cf430e0…` 上传 `$WORK`（串口日志/lsblk diff/完整日志）→ 非零即 job 红。
- 复用已入库的 `bootc-verify.sh`（判定 `LOGIN`/`PANIC`/`QEMU_EXITED`/`TIMEOUT_EMPTY`，退出码 0/1/2）。
- **前提**：靶机上要有带 `daedalus-kvm` 标签的自托管 runner（见 `.plans/cd-runner-setup-masterwet.md`）。

### 7.3 P2 预告（不在本轮）

- M1：`bootc install`→`switch`→`upgrade`→`rollback`，全在 qcow2 guest 内（新增 `tests/real-machine/bootc-switch-verify.sh`）。
- qcow2/ISO 提为 GitHub Release 资产；`just iso` 接入 CI。

### 7.4 验收方式与实际结果

- **P0：✅ 已在 CI 验证（2026-11-06，PR #79，run `42115944550`）。**
  - 本地：`bash -n` + `--selftest` + `tests/ci/workflow-hygiene.sh`（四仓）全绿。
  - CI：`build-image` job 的 `Headless deployment probes` 步真起容器、13 项探针全跑完，**`结论: 0 项 FAIL`**：
    `A1 PASS / A2 PASS / B1 PASS / B2 SKIP(无 host 单元) / B3 PASS / C1 PASS / C2 NOTE / C3 SKIP(仓库 404,未验证) / D1 PASS / D2 PASS / E1 PASS(verified 4 entries; hash chain intact) / E2 NOTE / F1 PASS`。
  - **首轮（run `42114659083`）红过**，2 项 FAIL 均非产品缺陷，已修 harness：
    1. **A2 断言过期**——旧断言要求 `Wants=daedalus-host.service`，而该单元在镜像里不存在（悬空弱依赖，core#63 第 4 条，**PR #65 `61b80c9` 已删除**）。等于「要求缺陷必须存在」。改为断言正确的单元形态（`[Unit]` + `ExecStart=...daedalus-host` + `WantedBy=multi-user.target`）。
    2. **C3 环境问题误判**——`dnf install` 撞 AL10 kitten `crb` 仓库 404（容器网络/上游仓库镜像问题，非镜像缺陷）。改为识别「仓库元数据不可达」→ `SKIP`（未验证），与 `/usr` 只读时的既有口径一致；真安装失败仍 `FAIL`。
  - **P0 顺带证实的镜像事实**：镜像能在真容器里起 systemd（degraded 用 3s）、控制器自启并写审计、`drift`/`ownership` 子命令可用、审计链完好、`daedalus-smoke` 通过。
- **P1：✅ 已在真机验证（2026-11-06，run `42127646978`，main 分支）。**
  - 靶机 runner（`Masterwet_Server`，标签 `daedalus-kvm`）上线后**自动领走**了一条排队的 P1 job（此前 runner 离线，job 一直挂着等）。
  - 四个 job 全绿；`Deploy Verify (real KVM boot)` 判定 **`LOGIN`**：
    `AlmaLinux Kitten 10 (Lion Cub)` / `Kernel 6.12.0-273.el10.x86_64` / `10 login:`；无 panic；`lsblk` 与引导前一致（宿主盘未被写入）。
  - 证据 artifact `deploy-verify-kvm-evidence`（7 文件：`serial-console.log` / `bootc-verify.log` / `lsblk-*` / `cmdline` / `fstab`）。
  - **顺带发现并修掉一个证据链缺陷**：`bootc-verify.sh` 原先用 `-drive file=…,format=qcow2` **读写**挂载，guest 一启动就把交付的 qcow2 写脏（实测同一文件 sha256 由 `c9d680…` 变 `77b06e…`），导致证据里记的 digest **无法复现交付产物**。改为 `snapshot=on`（写入落临时 overlay）+ 脚本末尾**断言 digest 前后一致**，不一致即判失败。已在真机实测通过（`引导前 77b06e… == 引导后 77b06e…`，`产物完整=yes`）。
- **runner 上线踩到的三个坑（均已修，收在 PR #81）**：Fedora SELinux 不让 systemd 执行 `~/actions-runner`（→ 改装 `/opt` + `restorecon`）；`svc.sh install` 拒绝覆盖残留 unit 文件（→ 先删再装）；`sudo -v` 的凭据缓存让 `sudo -n true` 假成功，导致 NOPASSWD **从没写过**（→ 改成无条件写 drop-in）。跨境外网还加了 `gh-proxy.com` 回退 + 重试 + gzip 校验。
- **两个 job 的取舍**：`Deploy Verify (real KVM boot)` 只对出货档（main / tag / dispatch PUSH=true）触发，**从不**对 PR / `feat/*` 开放 —— 自托管 runner 会执行构建产物，这是安全底线。
- harness 与 workflow 改动走 PR 给人审，**不自动合并**。

---

## 8. 剩余工作索引（2026-11-06 按 writing-plans 规范拆成 4 个独立计划）

剩余工作横跨多个独立子系统，按「一个计划一个可独立验收的子系统」拆开：

| 计划 | 位置 | 解决 | 规模 | 外部依赖 |
|---|---|---|---|---|
| ① 发布制品晋级 | `daedalus-core/docs/superpowers/plans/2026-11-06-release-artifacts.md` | qcow2/ISO 从 90 天 artifact → GitHub Release 资产（P2-3） | 3 task | 无 |
| ② ISO 无人值守装机闭环 | `daedalus-core/docs/superpowers/plans/2026-11-06-iso-unattended-install.md` | 「安装 → 重启 → `login:`」最后一跳 | 4 task | 无（kickstart 自制） |
| ③ guest 实机探针套件 | `daedalus-core/docs/superpowers/plans/2026-11-06-guest-real-machine-probes.md` | §2 剩余 8 面（A3/A4/A5/A6/A7/A12/A2残余/copilot） | 10 task | **LLM key**（仅 copilot 真 provider 那条，缺则记 BLOCKED） |
| ④ agent 真执行器 + fleet E2E | `daedalus-agent/docs/superpowers/plans/2026-11-06-agent-real-executor-and-fleet-e2e.md` | `applyStub` 空转 → 真改机器；异构三宿主 E2E | 8 task | **Debian 12 + CentOS Stream 9 宿主各一台**（缺则记 BLOCKED） |

### 已完成（不再需要计划）

- P0 容器行为探针门禁（CI 绿，`结论: 0 项 FAIL`）——PR #79
- P1 qcow2 真 KVM 引导门禁（`LOGIN`）——PR #79
- P2-1 bootc switch/rollback 真机门禁（`SWITCH_ROLLBACK_OK`，双 digest+marker 证据）——PR #82
- P2-2 ISO 产出 + 引导门禁（`INSTALLER`）——PR #85
- `releasever` 修复（镜像在容器/构建器上下文装不了包）——PR #83
- qcow2 证据链修复（`snapshot=on` + digest 断言）——PR #81
- runner 上线三坑（SELinux / unit 残留 / NOPASSWD 假绿）——PR #81

### 待人类提供（其余全部可 agent 自跑）

| 事项 | 用于 | 不给的后果 |
|---|---|---|
| 一个真实 LLM API key（OpenAI 或 Anthropic） | 计划 ③ 的 copilot 真 provider | 该条记 `BLOCKED: no key`，其余照跑 |
| Debian 12 + CentOS Stream 9 宿主各一台（≥2GB RAM、systemd、sudo） | 计划 ④ 的异构 E2E | 异构只完成 1/3（Ubuntu），另两条写 BLOCKED 并列规格 |
| 是否允许发一封测试告警邮件 | ci-watch 的「红 → 发信」验证 | 该路径未验证（与本批计划无关） |
| 合并 PR #83 / #85 | 让两条新门禁生效 | 门禁代码在分支上，未进 main |

