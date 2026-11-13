# agent 宿主快照能力上报与两条回归钉子 — AlmaLinux 9 + LVM 真机证据(2026-11-12)

> 执行入口:`tests/fleet/agent-e2e.sh`(Task 5 Step 3)与 `tests/fleet/agent-file-e2e.sh`(file 腿),
> 驱动跑不到的腿按 `2026-11-11-agent-e2e.md` §5 的同一做法、走**同一条控制面路径**
> (`node add` → `plan submit`)手工补验,不改驱动。
> **判定独立于被验对象**:控制面 / agent 的 `status` / `result` / `tx_id` 一律不作判据,
> 只采信目标机 `rpm` / `file` / `ls` / `sha256sum` / `lvs` 跑出来的事实。
> 本文件每一段都是**真实输出的原文粘贴**;不符期望的部分见 §9,一行没删。

- **执行日期**:2026-11-12
- **执行人**:orca worker(`daedalus-agent#8` Task 5:真机取证)
- **控制面**:`daedalus-core` @ `662e4bd`(`bin/daedalus-host` sha256 `d59b6301…`),
  含 `392d779`(agent 节点高危闸门只按 kind 判)、`f81dbc9`(四台矩阵证据)
- **被控端**:`daedalus-agent` @ `2b9ae0e`
  (`bin/daedalus-agent` sha256 `410e19647911d25420aa367576a394d4ad5e198a23a02a70c35dadfb6406c942`),
  含 `5690d56`(Detect 恢复真实探测)、`9defa84`(`SelectBackend(point, capability)`)、`2b9ae0e`(lvm / snapper 测试)
- **靶场**:KVM 宿主 `Masterwet_Server`(`undef-dev`)上的第四台 guest `vma`
  (AlmaLinux 9.8,`agent_id=e2e-a9`,ssh 转发口 9504 / agent 转发口 9474)
- **出货包**:`daedalus-agent-2b9ae0e.tar.gz`
  (sha256 `7de25a564ff5e18087c310307f52e9b92bd5de1bbcfeebfaf523473a00f9bf7e`)

---

## 0. 结论速览

| 项 | 期望 | 实测 | 结果 |
| --- | --- | --- | --- |
| 卷组造出来了 | `vgs` 输出 `vgtest` | `vgs` 原文 `vgtest`;`lvs` 有 `data` 1G | **符合** |
| `snapshot_capability` 上报 | `SNAPSHOT_CAPABILITY_LVM_SNAP`(lvm > rsync;vma 无 btrfs / snapper) | `node list` 与 agent 日志均为 `SNAPSHOT_CAPABILITY_LVM_SNAP` | **符合** |
| `support_level` | `experimental`(distro=`almalinux`) | `experimental` | **符合** |
| 钉子一:LVM 宿主上 package 高危仍被拒 | `status=dirty`、`tx_id` 全空、文案点名回滚点缺失 | 手工补验:全中;`kernel-core` 执行前后逐字一致 | **符合**(但不是驱动跑出来的,见 §9 D2) |
| 钉子二:LVM 宿主上 file intent 仍走 copy | 快照是普通文件、大小与被改文件同级,不是 LV | 快照 `0.snap` = `ASCII text` / 39 字节 / sha256 与被改前文件相同;`lvs` 无新增快照 LV | **符合** |
| `agent-e2e.sh` 矩阵 | 低危腿通 + 高危腿 dirty | **7 通过 / 3 失败,退出码 1** | **不符合**(§5、§9 D1/D2) |
| `agent-file-e2e.sh` file 腿 | 单台也能跑 | **5 通过 / 2 失败,退出码 1**(xfs 腿 3 条断言全过;两条失败是驱动硬要求 ext4+xfs 各一条) | **部分不符合**(§7、§9 D4) |

> 一句话:**能力上报恢复了、两条钉子都还钉着**;但 `agent-e2e.sh` 跑不绿,而且是两个各自独立的原因
> —— 一个是 agent 侧的真实回归(低危 package 被拒,§9 D1),一个是驱动镜像逻辑与控制面脱节
> (§9 D2)。两者都没有为了变绿做任何改动。

---

## 1. 靶场与前置(目标机自己的输出)

```text
## vma /etc/os-release(前 3 个字段)
NAME="AlmaLinux"
VERSION="9.8 (Olive Jaguar)"
ID="almalinux"
-- uname -r / df -T /
5.14.0-687.66.2.el9_8.x86_64
/dev/vda4      xfs   11261932 1057220  10204712  10% /
```

(矩阵输出里同一台机器的 `PRETTY_NAME="AlmaLinux 9.8 (Olive Jaguar)"` 与上面三个字段互证。)

单块 virtio 盘,没有 `/dev/vdb`(与计划 Task 5 Step 1 的说明一致):

```text
== block devices
NAME    SIZE TYPE
sr0     366K rom
sr1    1024M rom
vda      12G disk
├─vda1    1M part
├─vda2  200M part
├─vda3    1G part
└─vda4 10.8G part
```

**计划没写的前置**:这台 guest 上**没有装 lvm2**(`vgs` / `pvcreate` / `vgcreate` / `lvcreate` /
`lvconvert` 全部 MISSING),先补装:

```console
$ ssh vma "for b in vgs pvcreate vgcreate lvcreate losetup lvconvert; do printf '%s: ' \$b; command -v \$b || echo MISSING; done"
vgs: MISSING
pvcreate: MISSING
vgcreate: MISSING
lvcreate: MISSING
losetup: /usr/sbin/losetup
lvconvert: MISSING

$ ssh vma "sudo dnf -y install lvm2"
...
Installed:
  device-mapper-event-9:1.02.207-4.el9_8.1.x86_64
  device-mapper-event-libs-9:1.02.207-4.el9_8.1.x86_64
  device-mapper-persistent-data-1.1.0-1.el9.x86_64
  libaio-0.3.111-13.el9.x86_64
  libnvme-1.16.1-3.el9_8.x86_64
  lvm2-9:2.03.33-4.el9_8.1.x86_64
  lvm2-libs-9:2.03.33-4.el9_8.1.x86_64
Complete!
RC=0
/usr/sbin/vgs
```

agent 身份来自它自己的配置(不是自报):

```text
agent_id: e2e-a9
data_dir: /var/lib/daedalus-agent
audit_dir: /var/log/daedalus-agent
```

---

## 2. 造 LVM 卷组(计划 Task 5 Step 1,原文)

guest 只有 `/dev/vda`,故用 loop 设备文件:

```console
$ ssh vma "set -x; sudo truncate -s 2G /var/tmp/vgdisk.img; ls -l /var/tmp/vgdisk.img; sudo losetup --show -f /var/tmp/vgdisk.img"
+ sudo truncate -s 2G /var/tmp/vgdisk.img
+ ls -l /var/tmp/vgdisk.img
-rw-r--r--. 1 root root 2147483648 Nov 12 12:56 /var/tmp/vgdisk.img
+ sudo losetup --show -f /var/tmp/vgdisk.img
/dev/loop0
```

```console
$ ssh vma "set -x; sudo pvcreate -f /dev/loop0; sudo vgcreate vgtest /dev/loop0; sudo lvcreate -n data -L 1G vgtest; sudo vgs --noheadings -o vg_name; sudo vgs; sudo lvs"
+ sudo pvcreate -f /dev/loop0
  Physical volume "/dev/loop0" successfully created.
  Creating devices file /etc/lvm/devices/system.devices
+ sudo vgcreate vgtest /dev/loop0
  Volume group "vgtest" successfully created
+ sudo lvcreate -n data -L 1G vgtest
  Logical volume "data" created.
+ sudo vgs --noheadings -o vg_name
  vgtest
+ sudo vgs
  VG     #PV #LV #SN Attr   VSize  VFree
  vgtest   1   1   0 wz--n- <2.00g 1020.00m
+ sudo lvs
  LV   VG     Attr       LSize Pool Origin Data%  Meta%  Move Log Cpy%Sync Convert
  data vgtest -wi-a----- 1.00g
```

最后一行 `vgs --noheadings -o vg_name` 输出 **`vgtest`**,与计划 Expected 一致。

---

## 3. 构建与同步(命令与 sha256)

```console
$ cd ../daedalus-agent && go build -trimpath -o bin/daedalus-agent ./cmd/daedalus-agent
BUILD_RC=0
$ ./bin/daedalus-agent version
daedalus-agent 0.1.0-dev
$ sha256sum bin/daedalus-agent
410e19647911d25420aa367576a394d4ad5e198a23a02a70c35dadfb6406c942  bin/daedalus-agent

$ tar -tzf dist/daedalus-agent-2b9ae0e.tar.gz
daedalus-agent-2b9ae0e/
daedalus-agent-2b9ae0e/bin/
daedalus-agent-2b9ae0e/bin/daedalus-agent
daedalus-agent-2b9ae0e/scripts/
daedalus-agent-2b9ae0e/scripts/install.sh
daedalus-agent-2b9ae0e/scripts/postinstall.sh
daedalus-agent-2b9ae0e/scripts/uninstall.sh
$ sha256sum dist/daedalus-agent-2b9ae0e.tar.gz
7de25a564ff5e18087c310307f52e9b92bd5de1bbcfeebfaf523473a00f9bf7e

$ cd ../daedalus-core && just go-build        # rc=0
$ sha256sum bin/daedalus-host
d59b630137ac06d4984f8144ab3136bf68cf4fe66d758e4e224228a37d4002c1
```

同步到 KVM 宿主(三条 `rsync` 全部 rc=0;CA 私钥 `ca.key` 只留在宿主原地):

```console
$ rsync -az bin/daedalus-host Masterwet_Server:~/daedalus-e2e/core/bin/          # rc=0
$ rsync -az --delete tests/fleet Masterwet_Server:~/daedalus-e2e/core/tests/    # rc=0
$ rsync -az ../daedalus-agent/dist/daedalus-agent-2b9ae0e.tar.gz \
          Masterwet_Server:~/daedalus-e2e/artifacts/                            # rc=0
$ ssh Masterwet_Server 'sha256sum ~/daedalus-e2e/artifacts/daedalus-agent-2b9ae0e.tar.gz ~/daedalus-e2e/core/bin/daedalus-host'
7de25a564ff5e18087c310307f52e9b92bd5de1bbcfeebfaf523473a00f9bf7e  /home/udefine-dev/daedalus-e2e/artifacts/daedalus-agent-2b9ae0e.tar.gz
d59b630137ac06d4984f8144ab3136bf68cf4fe66d758e4e224228a37d4002c1  /home/udefine-dev/daedalus-e2e/core/bin/daedalus-host
```

---

## 4. 上报值:agent 日志 + `node list`

装好(驱动 Step 2 的 `install.sh`)后**新旧两行日志同时在场**,这是"探测从恒报 NONE 恢复成真探测"最直接的对照:

```text
$ ssh vma "sudo journalctl -u daedalus-agent --no-pager | grep -i 'snapshot capability' | tail -3"
Nov 12 12:42:57 vma daedalus-agent[2223]: 2026/11/12 12:42:57 agent: snapshot capability reported as NONE by design (host snapshot mechanisms are not probed); rollback points are per-kind and use the built-in copy backend
Nov 12 12:58:11 vma daedalus-agent[3997]: 2026/11/12 12:58:11 agent: snapshot capability=SNAPSHOT_CAPABILITY_LVM_SNAP
```

(第一行是**上一版二进制**留下的历史日志;第二行是本次 `2b9ae0e` 重启后的。)

安装态二进制与本地出货件逐字节相同(排除"跑的不是新代码"):

```text
$ sha256sum (本地) bin/daedalus-agent
410e19647911d25420aa367576a394d4ad5e198a23a02a70c35dadfb6406c942
$ ssh vma "sudo sha256sum /opt/daedalus-agent/bin/daedalus-agent"
410e19647911d25420aa367576a394d4ad5e198a23a02a70c35dadfb6406c942  /opt/daedalus-agent/bin/daedalus-agent
```

`node list` 原文(驱动 Step 3 的输出):

```text
== node list 契约
  ok   - 节点数 = 1
  ok   - 每行 support_level ∈ {supported, experimental}
  node list: {"nodes":[{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488669,"last_seen_unix":1794488669}]}
  node alma9: addr=localhost:9474 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP
```

`snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP`(lvm 优先于 rsync;该宿主无 btrfs / snapper),
`support_level=experimental` —— 与计划 Step 2 的 Expected 一致。

---

## 5. `agent-e2e.sh` 矩阵(命令、退出码、stdout 全文)

```console
$ ssh Masterwet_Server "cd ~/daedalus-e2e/core && \
  DAEDALUS_FLEET_CLIENT_CERT=\$HOME/daedalus-fleet-kvm/pki/client.pem \
  DAEDALUS_FLEET_CLIENT_KEY=\$HOME/daedalus-fleet-kvm/pki/client.key \
  DAEDALUS_FLEET_CA=\$HOME/daedalus-fleet-kvm/pki/ca.pem \
  DAEDALUS_AGENT_TARBALL=\$HOME/daedalus-e2e/artifacts/daedalus-agent-2b9ae0e.tar.gz \
  bash tests/fleet/agent-e2e.sh --hosts alma9=vma@localhost:9474"
MATRIX_EXIT=1
```

```text
fleet 根目录:/tmp/tmp.rnyJFPkxEH

== 被控机基础信息(目标机自己的 /etc/os-release;不采信 agent 自报)
  ok   - alma9(vma): ID=almalinux VERSION_ID=9.8 PRETTY_NAME="AlmaLinux 9.8 (Olive Jaguar)"
    uname -r: 5.14.0-687.66.2.el9_8.x86_64

== 逐台安装 daedalus-agent(出货 install.sh)
[install] installing from /tmp/daedalus-agent-e2e-4076743/daedalus-agent-2b9ae0e.tar.gz
[install] stopping existing daedalus-agent.service
[install] installing binary to /opt/daedalus-agent/bin/daedalus-agent
[install] writing unit to /etc/systemd/system/daedalus-agent.service
[install] running postinstall: /tmp/daedalus-agent-e2e-4076743/daedalus-agent-2b9ae0e/scripts/postinstall.sh
[postinstall] creating runtime directories
[postinstall] preserving existing /etc/daedalus-agent/config.yaml
[postinstall] preserving existing /etc/daedalus-agent/README.md
[postinstall] postinstall complete
[install] reloading systemd
[install] enabling + starting daedalus-agent.service
[install] install complete: /opt/daedalus-agent/bin/daedalus-agent
  ok   - alma9: install.sh 退出 0

== 登记被控机(daedalus-host node add)
{"node":{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488669,"last_seen_unix":1794488669}}
  ok   - alma9: node add localhost:9474(agent_id=e2e-a9)

== node list 契约
  ok   - 节点数 = 1
  ok   - 每行 support_level ∈ {supported, experimental}
  node list: {"nodes":[{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488669,"last_seen_unix":1794488669}]}
  node alma9: addr=localhost:9474 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP

== 低危腿:package.set nginx(low)→ 目标机独立核实
  ok   - 低危 Plan 每条目标机都有落地记录
  FAIL - 低危 Plan status=complete(jq: .status == "complete")
    实际输出: {"plan_id":"agent-e2e-low","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"agent-e2e-low/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"backend_unavailable: rollback: this kind has no rollback point; refuse"}],"recovery":[]}
  plan submit(package.set nginx): {"plan_id":"agent-e2e-low","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"agent-e2e-low/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"backend_unavailable: rollback: this kind has no rollback point; refuse"}],"recovery":[]}
  FAIL - 每条落地记录都有非空 tx_id(事务 id,不等于"有回滚点")(jq: all(.results[]; .tx_id != ""))
    实际输出: {"plan_id":"agent-e2e-low","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"agent-e2e-low/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"backend_unavailable: rollback: this kind has no rollback point; refuse"}],"recovery":[]}
  [独立核实] alma9: $ LC_ALL=C rpm -q nginx 2>&1 || true
    nginx-1.20.1-28.el9_8.6.alma.1.x86_64
  ok   - alma9: 目标机上确证 nginx 已装(独立于控制面/agent 的自我汇报)

== 高危腿:只打"package 在该节点上没有回滚点"的节点 → 期望当场 dirty(且什么都没下发)
  alma9: package 在该节点有回滚点,高危腿不打(否则 kernel-core 会真被装上)

agent-e2e: 7 通过 / 3 失败
  FAIL - 没有任何"package 无回滚点"的节点,高危腿无法按预期执行(不下发高危变更);记为偏差
```

**矩阵最后一行(原文)**:`agent-e2e: 7 通过 / 3 失败`,退出码 `1`。

两条失败腿的成因互相独立,见 §9 D1(低危腿:agent 真的拒了,是产品侧回归)与 D2(高危腿:
驱动的选点镜像仍是旧语义,一条都没打出去)。

---

## 6. 补验:两条腿走同一条控制面路径(驱动跑不到的那部分)

驱动的高危腿因 §9 D2 空转,低危腿的"确证已装"因 §9 D3 空过。两条腿都按
`2026-11-11-agent-e2e.md` §5 的同一做法补验:同样的 `daedalus-host`、同样的
`node add` → `plan submit`、同样的 mTLS 三件套,只是不经过驱动的选点逻辑。
低危腿改用目标机上**执行前就不存在**的包(`tree`),这样"装了没有"才是一个可观察的变化。

```console
$ ssh Masterwet_Server "cd ~/daedalus-e2e/core && \
  DAEDALUS_FLEET_CLIENT_CERT=\$HOME/daedalus-fleet-kvm/pki/client.pem \
  DAEDALUS_FLEET_CLIENT_KEY=\$HOME/daedalus-fleet-kvm/pki/client.key \
  DAEDALUS_FLEET_CA=\$HOME/daedalus-fleet-kvm/pki/ca.pem \
  bash ~/work/hostcap-probe.sh"
PROBE_EXIT=0
```

(补验脚本只落在宿主 `~/work/`:`hostcap-probe.sh` / `hostcap-file-snap.sh` /
`hostcap-absent.sh` / `hostcap-lvmerge.sh`。本轮按边界**只提交本证据文件**,脚本未入库;
它们做的事就是本文件里逐条列出的命令 —— `node add` + `plan submit` + ssh 到目标机探针。)

```text
## node add
{"node":{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488679,"last_seen_unix":1794488679}}
## node list
{"nodes":[{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488679,"last_seen_unix":1794488679},{"node_id":"e2e-a9-6ed9c824","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488411,"last_seen_unix":1794488575}]}
node_id=e2e-a9-586f6c2e support_level=experimental snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP
## 执行前目标机探针
tree: package tree is not installed
kernel-core: kernel-core-5.14.0-687.66.2.el9_8.x86_64
## 低危腿补验(package.set tree,low)
{"plan_id":"hostcap-low","status":"dirty","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"hostcap-low/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"backend_unavailable: rollback: this kind has no rollback point; refuse"}],"recovery":[]}
## 执行后目标机探针(tree)
tree: package tree is not installed
## 高危腿补验(package.set kernel-core,high)
{"plan_id":"hostcap-high","status":"dirty","max_risk":"high","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"hostcap-high/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"节点 e2e-a9-586f6c2e 无回滚点(snapshot capability=SNAPSHOT_CAPABILITY_NONE/\"SNAPSHOT_CAPABILITY_LVM_SNAP\"):high-risk intent 拒绝下发——无回滚点 ≠ 可继续"}],"recovery":[]}
## 执行后目标机探针(kernel-core)
kernel-core: kernel-core-5.14.0-687.66.2.el9_8.x86_64
```

(两行 `node list`:同一个 agent 重启后 `Register` 会拿到新的 `node_id`,旧行是上一轮登记的残留;
本轮所有 plan 都打在 `e2e-a9-586f6c2e` 这一行上。)

**钉子一(高危)**:自报 `LVM_SNAP` 的节点上,`package.set kernel-core(high)` 仍然是
`status=dirty` + `tx_id` 全空 + 文案点名回滚点缺失,且 `kernel-core` 执行前后**逐字一致**
(`kernel-core-5.14.0-687.66.2.el9_8.x86_64`)⇒ 高危 intent 没有下发到机器。
文案里 `snapshot capability=SNAPSHOT_CAPABILITY_NONE/"SNAPSHOT_CAPABILITY_LVM_SNAP"` 这一段:
前者是控制面在拒绝分支里**固定写入**的"无回滚点"常量(`internal/fleet/scheduler.go` 的
`snapshotCapNone`),后者是节点**自报**的能力值(`%q` 格式化)。两者并列恰好说明:准入判定
(`392d779` 之后的 `intentHasRollbackPoint`,agent 节点只看 kind)没有采信那个自报的 `LVM_SNAP`。

**低危(`tree`)**:`status=dirty`,agent 以
`backend_unavailable: rollback: this kind has no rollback point; refuse` 拒绝,
`tree` 执行前 / 执行后都是 `package tree is not installed` ⇒ **低危 package 在 agent 上现在也做不了**。
这是本轮发现的真实回归,见 §9 D1。

---

## 7. file 腿

### 7.1 驱动(`agent-file-e2e.sh`,xfs 腿原文)

```console
$ ssh Masterwet_Server "cd ~/daedalus-e2e/core && \
  DAEDALUS_FLEET_CLIENT_CERT=\$HOME/daedalus-fleet-kvm/pki/client.pem \
  DAEDALUS_FLEET_CLIENT_KEY=\$HOME/daedalus-fleet-kvm/pki/client.key \
  DAEDALUS_FLEET_CA=\$HOME/daedalus-fleet-kvm/pki/ca.pem \
  DAEDALUS_FLEET_DIR=\$HOME/daedalus-e2e/fleet-file-vma \
  bash tests/fleet/agent-file-e2e.sh --hosts alma9=vma@localhost:9474"
FILE_E2E_EXIT=1
```

```text
fleet 根目录:/home/udefine-dev/daedalus-e2e/fleet-file-vma

== 被控机基础信息(目标机自己的 /etc/os-release 与根文件系统类型)
  ok   - alma9(vma): ID=almalinux VERSION_ID=9.8 根文件系统=xfs

== 登记被控机(daedalus-host node add → 拿 node_id)
  ok   - alma9: node add localhost:9474(agent_id=e2e-a9) → node_id=e2e-a9-586f6c2e support_level=experimental
    node add: {"node":{"node_id":"e2e-a9-586f6c2e","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794488701,"last_seen_unix":1794488701}}

== 选腿:ext4 与 xfs 各一条低危腿
  FAIL - 没有 ext4 宿主,低危腿(spec §7 要求 ext4 与 xfs 各跑一次)无法执行
  [腿] xfs-alma9: target=/tmp/daedalus-e2e-file.conf content=  e   2   e   -   f   i   l   e   -   v   m   a  \n
    [基线] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      stat: cannot statx '/tmp/daedalus-e2e-file.conf': No such file or directory
    [基线] 不存在
    [计划(节选,完整 JSON 由驱动打印)] {"id": "agent-file-e2e-xfs-alma9", "intents": [{"kind": "file",
             "target": "/tmp/daedalus-e2e-file.conf", "spec": {"content": "e2e-file-vma\n"}, "risk_class": "low"}] …}
    [控制面自报(exit=0,不作判据)] {"plan_id":"agent-file-e2e-xfs-alma9","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"agent-file-e2e-xfs-alma9/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-agent-file-e2e-xfs-alma9/e2e-a9-586f6c2e/0","message":""}],"recovery":[]}
    [独立核实] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      644 13 regular file
    [独立核实] $ sudo sha256sum /tmp/daedalus-e2e-file.conf
      4a2fbf0ede85dacc17800bf36e82cb8b7b3abf8020c5daed2c409e270b1da13e
    [独立核实] $ sudo cat -A /tmp/daedalus-e2e-file.conf
      e2e-file-vma$
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf 存在且是普通文件
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf 内容逐字节相符(sha256 4a2fbf0ede85dacc17800bf36e82cb8b7b3abf8020c5daed2c409e270b1da13e)
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf mode=644

== 高危路径腿:/etc/daedalus-e2e.conf(file 在 agent 节点上有内建单文件快照,应放行)

== 收尾:删掉本驱动写下的文件(高危路径那条必须删)
  FAIL - 没有可用的 ext4 宿主,高危路径腿无法执行
  alma9: $ sudo rm -f /etc/daedalus-e2e.conf → rc=0
  alma9: $ sudo rm -f /tmp/daedalus-e2e-file.conf → rc=0

agent-file-e2e: 5 通过 / 2 失败
```

xfs 腿三条断言全过(file intent 在 xfs 上落地、内容逐字节相符、mode 644);两条失败都是
"没有 ext4 宿主"——驱动硬要求 ext4 与 xfs 各一条,本轮只有 `vma` 一台(§9 D4)。

### 7.2 快照落成普通文件(不是 LV)—— 计划 Task 5 Step 3(a)

驱动不打印 dataDir 里的快照;用一条独立的 file intent 观察快照本体:目标文件**先存在**(39 字节),
提交 `file` intent 改成另一段内容,然后看 `/var/lib/daedalus-agent/snapshots/`。

```console
$ ssh Masterwet_Server "cd ~/daedalus-e2e/core && \
  DAEDALUS_FLEET_CLIENT_CERT=\$HOME/daedalus-fleet-kvm/pki/client.pem \
  DAEDALUS_FLEET_CLIENT_KEY=\$HOME/daedalus-fleet-kvm/pki/client.key \
  DAEDALUS_FLEET_CA=\$HOME/daedalus-fleet-kvm/pki/ca.pem \
  bash ~/work/hostcap-file-snap.sh"
FILESNAP_EXIT=0
```

```text
node_id=e2e-a9-586f6c2e support_level=experimental
## 1. 造基线文件(已存在,39 字节)
644 39 regular file
ce0521f9f78cb00733a5dd093f6cd4f71c8d539f160f9983ea9b954ad553c83e  /tmp/daedalus-e2e-snap.conf
baseline-content-0123456789-abcdefghij$
## 2. 提交 file intent(low,target=/tmp/daedalus-e2e-snap.conf)
{"plan_id":"hostcap-file-snap","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-586f6c2e"]}],"results":[{"node_id":"e2e-a9-586f6c2e","intent_id":"hostcap-file-snap/e2e-a9-586f6c2e/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-hostcap-file-snap/e2e-a9-586f6c2e/0","message":""}],"recovery":[]}
## 3. 快照目录(原文)
total 4
drwx------. 2 root root 20 Nov 12 13:02 .
drwxr-xr-x. 6 root root 62 Nov 12 13:00 ..
-rw-r--r--. 1 root root 39 Nov 12 13:04 0.snap
## 4. 快照是什么(file / ls -l / sha256 / 内容)
/var/lib/daedalus-agent/snapshots/0.snap: ASCII text
-rw-r--r--. 1 root root 39 Nov 12 13:04 /var/lib/daedalus-agent/snapshots/0.snap
ce0521f9f78cb00733a5dd093f6cd4f71c8d539f160f9983ea9b954ad553c83e  /var/lib/daedalus-agent/snapshots/0.snap
baseline-content-0123456789-abcdefghij$
## 5. 被改后的目标文件
644 23 regular file
c999c702c3795447739b8e193a651e5cd1afbffd8d3e713d4610a8cc16225671  /tmp/daedalus-e2e-snap.conf
applied-after-snapshot$
## 6. LVM 侧:卷组里没有新增快照 LV
  LV   VG     Attr       LSize Pool Origin Data%  Meta%  Move Log Cpy%Sync Convert
  data vgtest -wi-a----- 1.00g
## 7. 收尾
rm rc=0
```

**钉子二**:LVM 宿主上的 file intent 走的是**内建 copy 后端** ——
快照 `0.snap` 是 `ASCII text`、39 字节(与被改前的目标文件同大小)、sha256
`ce0521f9…` 与基线文件**逐字节相同**;同一时刻 `lvs` 里只有一个 `data` LV,
**没有新增任何快照 LV**。如果 `SelectBackend` 继续只看 capability,这里会出现 `lvcreate -s` 的快照卷
(或直接被 `lvm.go` 的 `/dev/` 守卫拒掉)。

(本轮还观测到 copy 后端的另一种形态:快照时目标文件**不存在**时,落地的是
`0.snap.absent` 标记文件,内容是源路径加换行 —— 下面这次是 30 字节 / `/tmp/daedalus-e2e-absent.conf`;
§7.1 那次"基线不存在"的 file intent 留下的是 28 字节 / `/tmp/daedalus-e2e-file.conf`。
两种形态都是普通文件,都不是 LV。)

```text
## 快照目录
total 4
drwx------. 2 root root 27 Nov 12 13:05 .
drwxr-xr-x. 6 root root 62 Nov 12 13:00 ..
-rw-------. 1 root root 30 Nov 12 13:08 0.snap.absent
## 标记文件本体
/var/lib/daedalus-agent/snapshots/0.snap.absent: ASCII text
-rw-------. 1 root root 30 Nov 12 13:08 /var/lib/daedalus-agent/snapshots/0.snap.absent
/tmp/daedalus-e2e-absent.conf$
## 目标文件(确实是新文件)
644 23 regular file
created-by-file-intent$
rm rc=0
```

---

## 8. `lvconvert --merge` 的原始输出(不承诺卷级还原的依据)

### 8.1 计划里那条原命令

```console
$ ssh vma "sudo lvconvert --merge vgtest/data"
  vgtest/data cannot be merged because --trackchanges was not used.
rc=5
```

`vgtest/data` 是普通 LV 而不是快照 —— LVM 直接拒。这段本身就是一条事实:merge 的前提是
"`data` 得先是一个快照 LV",而 agent 侧根本没有 producer 去建这个快照(本期无 `PointVolume` 消费者)。

### 8.2 真建一个快照 LV,看 merge 什么时候才生效

```text
## B1) 在 data 上建 xfs 并挂载,写 v1
mkfs rc=0
mount rc=0
v1
## B2) 建快照 LV
  Logical volume "data_snap" created.
rc=0
  LV        VG     Attr       LSize   Pool Origin Data%  Meta%  Move Log Cpy%Sync Convert
  data      vgtest owi-aos---   1.00g
  data_snap vgtest swi-a-s--- 512.00m      data   0.00
## B3) 改 origin(写 v2,此时快照里仍是 v1)
v2
## B4) origin 仍挂载时 merge(期望:不立刻生效/推迟到下次激活)
  Delaying merge since origin is open.
  Merging of snapshot vgtest/data_snap will occur on next activation of vgtest/data.
rc=0
v2
(merge 命令返回后,/mnt/vgtest/payload 仍是上面这个值)
  LV   Attr       Origin
  data Owi-aos---
## B5) 卸载后再激活,merge 才落地
umount rc=0
  WARNING: Command on LV vgtest/data_snap is invalid on LV with properties: lv_is_merging_cow .
  Command not permitted on LV vgtest/data_snap.
rc=5
deactivate rc=0
activate rc=0
mount rc=0
v1
## B6) 收尾(保留 VG,只拆挂载与快照残留)
  LV   VG     Attr       LSize Pool Origin Data%  Meta%  Move Log Cpy%Sync Convert
  data vgtest -wi-a----- 1.00g
  vgtest
```

**结论(事实,不是推断)**:`lvconvert --merge` 在 origin 被占用时**只登记意图**
(`Delaying merge since origin is open.` / `will occur on next activation`),
命令返回 `rc=0` 而目标文件**仍是新内容 `v2`**;直到 `umount` + `lvchange -an/-ay`
(等价于"卸载或重启")之后,内容才回到 `v1`。⇒ agent **承诺不了即时卷级还原**,
这正是本计划把卷级回滚留给 atomic 侧、不在 agent 生产路径接 LVM / snapper 后端的依据。

收尾后 `vgs` 仍是 `vgtest`(卷组保留,能力上报的物理前提未被破坏),`lvs` 只剩 `data`。

---

## 9. 与期望不符之处(逐条,未做任何"凑绿"改动)

### D1【产品侧回归】低危 package intent 在 agent 上被拒

- **现象**:`package.set tree(low)` 在 `vma` 上 `status=dirty`,agent 回
  `backend_unavailable: rollback: this kind has no rollback point; refuse`;目标机 `tree` 执行前后都是
  `package tree is not installed`(§6)。驱动的低危腿(`package.set nginx(low)`)同样是这个结局(§5)。
- **根因**(只读排查,未改代码):`internal/rpc/apply_intent.go` 的准入顺序是
  `PointNone && HIGH → 拒`(未变),但第 3 步**无条件**调用 `s.selectedBackend(point)`,
  而 `SelectBackend(PointNone, …)` 恒返回错误。于是"低危 + 无回滚点"从"放行"变成了
  "后端选不出来 → 拒"。`9defa84` 之前 `SelectBackend(capability)` 对 `NONE/UNSPECIFIED` 返回
  `&CopyBackend{}`,低危腿因此能过(`2026-11-11-agent-e2e.md` §10.5 的三台就是那样过的)。
- **为什么单测是绿的**:`internal/rpc/handlers_test.go:484 TestApplyIntent_LowRiskWithoutRollbackPoint_Allowed`
  走的是测试注入路径 —— `agentService.selectedBackend` 在 `s.backend != nil` 时**短路返回注入的 fake**,
  根本不调 `rollback.SelectBackend`。生产路径(`s.backend == nil`)在这条用例里没有被覆盖。
- **与计划的关系**:计划 Task 2 写的是"准入判定保持逐字不变"+`PointNone → 错误`;两句话字面上都实现了,
  合起来却把低危 package 关掉了。这是计划层面的交互缺陷(不是本轮引入的偏差),需要单开一刀修。
- **本轮处置**:按边界不改 Go 源码,如实记录;两条腿都没有为了变绿去调断言或改驱动。

### D2【驱动缺陷】`agent-e2e.sh` 的高危腿一条没打

- **现象**:`alma9: package 在该节点有回滚点,高危腿不打` → `FAIL - 没有任何"package 无回滚点"的节点`。
- **根因**:驱动的 `node_has_rollback_point`(以及 `--selftest` 里
  `package on agent + 快照能力 BTRFS → 有回滚点` 这条断言)仍是**旧语义**:非 `file` 的 kind
  只要 `snapshot_capability` 不是空/NONE/UNSPECIFIED 就算"有回滚点"。控制面在 `392d779` 之后
  对 agent 节点**只按 kind 判**,`SNAPSHOT_CAPABILITY_LVM_SNAP` 不再意味着 package 有回滚点。
  两边脱节 ⇒ 驱动把唯一那台节点从高危腿里筛掉了。
- **本轮处置**:不改驱动(任务边界),用 §6 的同路径手工补验拿到高危腿的 `plan submit` 原文。
  钉子一的判定因此**不是**驱动给出的,而是手工补验给出的 —— 结论成立,但证据链形态与
  `2026-11-11-agent-e2e.md` §5 相同(那条腿也是手工补验)。

### D3【驱动弱点】低危腿的"独立核实"是空过

- 驱动的独立核实只查"包现在装着没有",**不取执行前基线**。本轮第一次提交的
  `package.set nginx(low)` 就返回 `dirty` + `tx_id` 全空(§5),而同一段里
  `ok - alma9: 目标机上确证 nginx 已装` 依然通过;补验开始时 `rpm -q nginx` 给的是
  `nginx-1.20.1-28.el9_8.6.alma.1.x86_64` —— 它是本轮跑之前就装着的(上一轮 AlmaLinux 矩阵留下的)。
  这条 `ok` 不能作为"本轮低危腿落地"的证据;§6 用 `tree`(执行前就 absent)重做了一遍,
  结论是**没有落地**。

### D4【驱动设计】`agent-file-e2e.sh` 单台宿主跑不绿

- 驱动硬要求"ext4 与 xfs 各一条腿",只给 `vma`(xfs)时会记两条
  `没有 ext4 宿主 / 没有可用的 ext4 宿主` 失败,退出码 1。xfs 腿本身 3 条断言全过。
- 本轮按任务要求只跑 `vma`,没有为了凑绿把别的 guest 拉进来(那会让另一台机器的
  `/etc/daedalus-e2e.conf` 等文件被动到,超出本任务范围)。

### D5【计划漏项】guest 上没有 lvm2

- 计划 Task 5 Step 1 直接从 `pvcreate` 开始;实测这台 AlmaLinux 9.8 上 `vgs/pvcreate/vgcreate/lvcreate/lvconvert`
  全部缺失,需要先 `dnf -y install lvm2`(§1)。这一步会改变 guest 的软件状态,记在此处以备复现。

### D6【计划措辞】`lvconvert --merge vgtest/data` 对普通 LV 必然报错

- 计划 Step 4 给的命令(`lvconvert --merge vgtest/data`)在 `data` 不是快照时返回
  `cannot be merged because --trackchanges was not used.`(§8.1)。要观察"推迟到下次激活"的
  真实行为,必须先 `lvcreate -s` 建快照 LV(§8.2)。

### 未覆盖项

- `agent-e2e.sh --selftest` 本轮没跑(它的选点断言本身是 §9 D2 的旧语义,跑了也只能证明驱动自洽)。
- 卷级回滚(真正的整卷还原)未验证,且**不在本计划范围**(§8 只是"不承诺即时还原"的物理依据)。
- `snapper` 后端的真机行为未验证:该宿主没有 `snapper`(只有 `lvm`),上报优先级里的
  snapper > lvm 分支由 agent 单测覆盖(`5690d56`)。

---

## 10. 结论

- [x] `vma`(AlmaLinux 9.8 / xfs)上用 loop 设备造出 LVM 卷组 `vgtest`(`vgs` 原文为证)。
- [x] 恢复探测后的 agent(`2b9ae0e`)在 `node list` 里上报
      `snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP`、`support_level=experimental`;
      agent 日志同时留下"旧二进制恒报 NONE / 新二进制报 LVM_SNAP"的对照。
- [x] **钉子一成立**:自报 `LVM_SNAP` 的节点上,`package.set kernel-core(high)` 仍然
      `dirty` + `tx_id` 全空 + 文案点名回滚点缺失,`kernel-core` 执行前后逐字一致(§6)。
- [x] **钉子二成立**:`file` intent 在 LVM 宿主上仍走内建 copy 后端 —— 快照是 39 字节
      `ASCII text`、sha256 与被改前文件相同,`lvs` 里没有新增快照 LV(§7.2)。
- [x] `lvconvert --merge` 的生效条件有原始输出支撑:origin 占用时只"登记",卸载/重启后才落地(§8)。
- [ ] **`agent-e2e.sh` 不是绿的**(7 通过 / 3 失败):低危腿因 **D1**(agent 侧真实回归)被拒,
      高危腿因 **D2**(驱动镜像逻辑过期)没打出去。两者都记在 §9,没有改代码、没有改驱动、没有改断言。

---

## 11. 修复后复跑(2026-11-12)

> D1 / D2 / D3 三条修复落地后的复跑。§1–§10 原样保留(上一轮的失败留在原地,不删不改)。
> 边界:本轮**不改 Go 源码、不改驱动、不改生成器、不改 `supportMatrix`**;仓库里只有本文件变。

### 11.1 三条修复与本次二进制

| 缺陷 | 修复 | commit |
| --- | --- | --- |
| D1 低危 `package` intent 被 `backend_unavailable` 误拒 | 无回滚点的 intent 不再选后端;有回滚点却拿不到后端时 fail-closed | `daedalus-agent` `92a6fa7` |
| D2 驱动 `node_has_rollback_point` 仍是旧语义(宿主能力参与准入) | agent 节点只按 kind 判,与控制面 `392d779` 一致 | `daedalus-core` `95b71c0` |
| D3 低危腿"独立核实"空过(不取执行前基线) | 低危腿执行前加包管理器基线闸门:已装着就不下发并记 FAIL | `daedalus-core` `95b71c0` |

构建 / 同步(命令与 sha256 原文):

```console
$ cd ../daedalus-agent && go build -trimpath -o bin/daedalus-agent ./cmd/daedalus-agent
BUILD_RC=0
$ ./bin/daedalus-agent version
daedalus-agent 0.1.0-dev
$ sha256sum bin/daedalus-agent
3ee13c39024ce7682e50d52e42adf2519588b67d6d49f1d34375818c94b74e94  bin/daedalus-agent

$ sha=$(git rev-parse --short HEAD)     # 92a6fa7
$ tar -czf dist/daedalus-agent-$sha.tar.gz -C /var/tmp/agentpack-$sha daedalus-agent-$sha
$ tar -tzf dist/daedalus-agent-92a6fa7.tar.gz
daedalus-agent-92a6fa7/
daedalus-agent-92a6fa7/bin/
daedalus-agent-92a6fa7/bin/daedalus-agent
daedalus-agent-92a6fa7/scripts/
daedalus-agent-92a6fa7/scripts/install.sh
daedalus-agent-92a6fa7/scripts/install_test.sh
daedalus-agent-92a6fa7/scripts/postinstall.sh
daedalus-agent-92a6fa7/scripts/uninstall.sh
$ sha256sum dist/daedalus-agent-92a6fa7.tar.gz
629672508826fe004037601b67f5c80eea1a344882d9e11974f00e1a7386932b  dist/daedalus-agent-92a6fa7.tar.gz

$ cd ../daedalus-core && just go-build        # rc=0
$ sha256sum bin/daedalus-host
cf702bab97602b927dae1ea6416d76db67b5800420bbc54cf9ac5a810d121a56  bin/daedalus-host
$ git rev-parse --short HEAD
95b71c0

$ rsync -az bin/daedalus-host Masterwet_Server:~/daedalus-e2e/core/bin/                       # rc=0
$ rsync -az --delete tests/fleet Masterwet_Server:~/daedalus-e2e/core/tests/                   # rc=0
$ rsync -az ../daedalus-agent/dist/daedalus-agent-92a6fa7.tar.gz Masterwet_Server:~/daedalus-e2e/artifacts/  # rc=0
$ ssh Masterwet_Server 'sha256sum ~/daedalus-e2e/artifacts/daedalus-agent-92a6fa7.tar.gz ~/daedalus-e2e/core/bin/daedalus-host'
629672508826fe004037601b67f5c80eea1a344882d9e11974f00e1a7386932b  /home/udefine-dev/daedalus-e2e/artifacts/daedalus-agent-92a6fa7.tar.gz
cf702bab97602b927dae1ea6416d76db67b5800420bbc54cf9ac5a810d121a56  /home/udefine-dev/daedalus-e2e/core/bin/daedalus-host
```

四台 guest 的**安装态**二进制与出货件逐字节一致(排除"跑的还是旧代码";每台第二行是该台的
`tree` 探针,与 §11.5 的独立核实互证。`vmu`/`vmd` 的 `dpkg-query` 输出不带换行,原文因此与
下一个 `==` 连在一起):

```text
== vmu
3ee13c39024ce7682e50d52e42adf2519588b67d6d49f1d34375818c94b74e94  /opt/daedalus-agent/bin/daedalus-agent
install ok installed 2.1.1-2ubuntu3.24.04.2== vmd
3ee13c39024ce7682e50d52e42adf2519588b67d6d49f1d34375818c94b74e94  /opt/daedalus-agent/bin/daedalus-agent
install ok installed 2.1.0-1== vmc
3ee13c39024ce7682e50d52e42adf2519588b67d6d49f1d34375818c94b74e94  /opt/daedalus-agent/bin/daedalus-agent
tree-1.8.0-10.el9.x86_64
== vma
3ee13c39024ce7682e50d52e42adf2519588b67d6d49f1d34375818c94b74e94  /opt/daedalus-agent/bin/daedalus-agent
tree-1.8.0-10.el9.x86_64
```

### 11.2 驱动自检:`21 通过 / 0 失败`

```console
$ bash tests/fleet/agent-e2e.sh --selftest
SELFTEST_EXIT=0
```

```text

== 控制面判定(复用 core 既有本地 fake agent 判据,不重复实现控制面半)
  === RUN   TestFleetE2E_LowRiskPlanCompletes
  --- PASS: TestFleetE2E_LowRiskPlanCompletes (0.05s)
  === RUN   TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot
  --- PASS: TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot (0.01s)
  PASS
  ok  	github.com/Daedalusys/daedalus-core/tests/integration	0.066s
  ok   - 低危 Plan 全量落地 + 高危遇无回滚点节点当场 dirty(真 mTLS + 真 Scheduler)

== 目标机独立核实:包管理器输出判定(不采信任何自我汇报)
  ok   - dpkg 报告 install ok installed → 判已装
  ok   - dpkg 单行格式 install ok installed <版本> → 判已装
  ok   - dpkg 字段占位符没被展开(写成 rpm 的 %{Status})→ 判没装上
  ok   - dpkg 报告 no packages found → 判没装上
  ok   - rpm 报告 nginx-1.24.0-2.el9.x86_64 → 判已装
  ok   - rpm 报告 package nginx is not installed → 判没装上
  ok   - rpm 报本地化提示(zh_CN 的"未安装软件包")→ 仍判没装上
  ok   - 探测命令失败(空输出)→ 判没装上(fail-closed,不猜)
  ok   - rpm 输出形状不认识 → 判没装上

== 回滚点判定:agent 只按 kind 判;atomic 按节点快照能力
  ok   - file on agent → 有内建单文件 copy 回滚点(与宿主快照无关)
  ok   - file on atomic → 不替 daedalus-tx 承诺,按节点快照能力判
  ok   - package on agent + 快照能力 NONE → 无回滚点(高危腿的选点条件)
  ok   - package on agent + 快照能力 BTRFS → 仍无回滚点(能力不参与准入)
  ok   - package on agent + 快照能力 LVM_SNAP → 仍无回滚点(merge 要卸载/重启)
  ok   - package on agent + 快照能力 SNAPPER → 仍无回滚点
  ok   - package on atomic + 快照能力 LVM_SNAP → atomic 仍按能力判

== 负对照:说谎的 agent(控制面报 complete,目标机上并没有装上)
  ok   - Plan complete 但目标机无此包 → 判定不通过(applyStub 的教训已钉住)
  ok   - 正对照:Plan complete + 目标机确证已装 → 判定通过

== 生成的 Plan 过真校验器(空注册表 ⇒ 只应在"节点未注册"处停)
  ok   - 生成的低危 Plan 通过 Plan 校验(在"节点未注册"处才停,说明结构合法)
  ok   - 反证:改坏 blast_radius.count 后校验器确实拒(说明上一条不是空过)

agent-e2e: 21 通过 / 0 失败
```

D2 修掉后,驱动里"有没有回滚点"的判定与控制面同源,自检里
`package on agent + 快照能力 BTRFS / LVM_SNAP / SNAPPER → 仍无回滚点` 三条**首次全绿**
——上一轮的自检按旧语义写,跑了也只能证明驱动自洽,故没跑(§9「未覆盖项」)。

### 11.3 矩阵命令、换包与执行前基线

```console
$ ssh Masterwet_Server 'bash -s' <<'EOS'
  cd "$HOME/daedalus-e2e/core"
  export DAEDALUS_FLEET_CLIENT_CERT="$HOME/daedalus-fleet-kvm/pki/client.pem"
  export DAEDALUS_FLEET_CLIENT_KEY="$HOME/daedalus-fleet-kvm/pki/client.key"
  export DAEDALUS_FLEET_CA="$HOME/daedalus-fleet-kvm/pki/ca.pem"
  export DAEDALUS_AGENT_TARBALL="$HOME/daedalus-e2e/artifacts/daedalus-agent-92a6fa7.tar.gz"
  bash tests/fleet/agent-e2e.sh \
    --hosts ubuntu=vmu@localhost:9471 debian12=vmd@localhost:9472 centos9=vmc@localhost:9473 alma9=vma@localhost:9474 \
    --package tree
EOS
MATRIX_EXIT=0
```

**为什么换包**:四台上的 `nginx` 是上一轮装上的(§5 / §9 D3),留着它会让 D3 新加的基线闸门
把四台全部拦下 —— 那正是闸门要防的"空过"。`tree` 在四台上都 absent,本轮用它。

矩阵开跑**之前**在四台上取的基线(原文;`tree` 之外的行是同一趟探针顺带取的
os-release / TLS 目录 / agent_id,以及另一发行版探针的 `command not found` 噪声):

```text
== vmu
  vmu
  ID=ubuntu VER=24.04
  agent.key
  agent.pem
  ca.pem
  e2e-u24
  bash: line 1: rpm: command not found
  dpkg-query: no packages found matching tree
== vmd
  vmd
  ID=debian VER=12
  agent.key
  agent.pem
  ca.pem
  e2e-d12
  bash: line 1: rpm: command not found
  dpkg-query: no packages found matching tree
== vmc
  vmc
  ID=centos VER=9
  agent.key
  agent.pem
  ca.pem
  e2e-c9
  package tree is not installed
  bash: line 1: dpkg-query: command not found
== vma
  vma
  ID=almalinux VER=9.8
  agent.key
  agent.pem
  ca.pem
  e2e-a9
  package tree is not installed
  bash: line 1: dpkg-query: command not found
```

四台全部 absent ⇒ 基线干净,闸门放行(D3 的闸门本身没有打印 ok,它只在拦下时打印 FAIL,
这是它的设计)。

### 11.4 `node list` 原文(驱动 Step 3)

```text
== node list 契约
  ok   - 节点数 = 4
  ok   - 每行 support_level ∈ {supported, experimental}
  node list: {"nodes":[{"node_id":"e2e-a9-633a3815","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794490411,"last_seen_unix":1794490411},{"node_id":"e2e-c9-0278240b","agent_id":"e2e-c9","kind":"agent","addr":"localhost:9473","distro":"centos","distro_version":"9","kernel":"","arch":"","hostname":"vmc","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794490411,"last_seen_unix":1794490411},{"node_id":"e2e-d12-59ce7a9b","agent_id":"e2e-d12","kind":"agent","addr":"localhost:9472","distro":"debian","distro_version":"12","kernel":"","arch":"","hostname":"vmd","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_RSYNC_TARGET","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794490411,"last_seen_unix":1794490411},{"node_id":"e2e-u24-f3468e28","agent_id":"e2e-u24","kind":"agent","addr":"localhost:9471","distro":"ubuntu","distro_version":"24.04","kernel":"","arch":"","hostname":"vmu","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_RSYNC_TARGET","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794490411,"last_seen_unix":1794490411}]}
  node ubuntu: addr=localhost:9471 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_RSYNC_TARGET
  node debian12: addr=localhost:9472 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_RSYNC_TARGET
  node centos9: addr=localhost:9473 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_NONE
  node alma9: addr=localhost:9474 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_LVM_SNAP

```

| host | distro | support_level | snapshot_capability |
| --- | --- | --- | --- |
| `ubuntu` | ubuntu 24.04 | `supported` | `SNAPSHOT_CAPABILITY_RSYNC_TARGET` |
| `debian12` | debian 12 | `supported` | `SNAPSHOT_CAPABILITY_RSYNC_TARGET` |
| `centos9` | centos 9 | `experimental` | `SNAPSHOT_CAPABILITY_NONE` |
| `alma9` | almalinux 9.8 | `experimental` | `SNAPSHOT_CAPABILITY_LVM_SNAP` |

四台 `kind` 都是 `agent`;按 kind 判,`package` 在四台上都**没有**回滚点(与自报的快照能力无关),
这正是高危腿的选点条件。

### 11.5 低危腿:`package.set tree(low)` 四台 complete + 目标机独立核实

```text
== 低危腿:package.set tree(low)→ 目标机独立核实
  ok   - 低危 Plan status=complete
  ok   - 低危 Plan 每条目标机都有落地记录
  ok   - 每条落地记录都有非空 tx_id(事务 id,不等于"有回滚点")
  plan submit(package.set tree): {"plan_id":"agent-e2e-low","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-u24-f3468e28"]},{"index":1,"node_ids":["e2e-d12-59ce7a9b"]},{"index":2,"node_ids":["e2e-c9-0278240b"]},{"index":3,"node_ids":["e2e-a9-633a3815"]}],"results":[{"node_id":"e2e-u24-f3468e28","intent_id":"agent-e2e-low/e2e-u24-f3468e28/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-u24-f3468e28/0","message":""},{"node_id":"e2e-d12-59ce7a9b","intent_id":"agent-e2e-low/e2e-d12-59ce7a9b/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-d12-59ce7a9b/0","message":""},{"node_id":"e2e-c9-0278240b","intent_id":"agent-e2e-low/e2e-c9-0278240b/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-c9-0278240b/0","message":""},{"node_id":"e2e-a9-633a3815","intent_id":"agent-e2e-low/e2e-a9-633a3815/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-a9-633a3815/0","message":""}],"recovery":[]}
  [独立核实] ubuntu: $ LC_ALL=C dpkg-query -W -f='${Status} ${Version}' tree 2>&1 || true
    install ok installed 2.1.1-2ubuntu3.24.04.2
  ok   - ubuntu: 目标机上确证 tree 已装(独立于控制面/agent 的自我汇报)
  [独立核实] debian12: $ LC_ALL=C dpkg-query -W -f='${Status} ${Version}' tree 2>&1 || true
    install ok installed 2.1.0-1
  ok   - debian12: 目标机上确证 tree 已装(独立于控制面/agent 的自我汇报)
  [独立核实] centos9: $ LC_ALL=C rpm -q tree 2>&1 || true
    tree-1.8.0-10.el9.x86_64
  ok   - centos9: 目标机上确证 tree 已装(独立于控制面/agent 的自我汇报)
  [独立核实] alma9: $ LC_ALL=C rpm -q tree 2>&1 || true
    tree-1.8.0-10.el9.x86_64
  ok   - alma9: 目标机上确证 tree 已装(独立于控制面/agent 的自我汇报)

```

`status=complete` 只是控制面自报,不作判据;判据是每台 `[独立核实]` 的包管理器输出 ——
四台分别是 `install ok installed 2.1.1-2ubuntu3.24.04.2` / `install ok installed 2.1.0-1` /
`tree-1.8.0-10.el9.x86_64` ×2,而执行前四台都是 absent(§11.3),所以"装上了"是本轮的**净变化**。
每条落地记录的 `tx_id` 都非空(`tx-agent-e2e-low/<node_id>/0`)。

### 11.6 高危腿:`package` 在四台上都没有回滚点 → 当场 dirty、一个 `tx_id` 都没有

```text
== 高危腿:只打"package 在该节点上没有回滚点"的节点 → 期望当场 dirty(且什么都没下发)
  ok   - 高危 Plan status=dirty
  ok   - 拒绝文案点名回滚点缺失(逐字含 snapshot 能力字样)
  ok   - 没有任何节点拿到 tx_id(高危 intent 未下发)
  ok   - 没有恢复命令(没有机器被动过)
  plan submit(package.set kernel-core,high): {"plan_id":"agent-e2e-high","status":"dirty","max_risk":"high","batches":[{"index":0,"node_ids":["e2e-u24-f3468e28"]},{"index":1,"node_ids":["e2e-d12-59ce7a9b"]},{"index":2,"node_ids":["e2e-c9-0278240b"]},{"index":3,"node_ids":["e2e-a9-633a3815"]}],"results":[{"node_id":"e2e-u24-f3468e28","intent_id":"agent-e2e-high/e2e-u24-f3468e28/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"节点 e2e-u24-f3468e28 无回滚点(snapshot capability=SNAPSHOT_CAPABILITY_NONE/\"SNAPSHOT_CAPABILITY_RSYNC_TARGET\"):high-risk intent 拒绝下发——无回滚点 ≠ 可继续"}],"recovery":[]}

```

`batches` 里列的是四台(`index` 0–3),`results` 只有一条 —— 控制面在第一个 dirty 批次就收尾,
后续批次不再下发;这是控制面的编排行为,本轮如实记录、不作断言。

### 11.7 汇总行

```text
agent-e2e: 25 通过 / 0 失败
```

25 条 = 基础信息 4 + install 4 + node add 4 + node list 契约 2 + 低危 Plan 断言 3 +
独立核实 4 + 高危 Plan 断言 4。退出码 `0`。

### 11.8 file 腿(D4 原样存在)

```console
$ ssh Masterwet_Server 'bash -s' <<'EOS'
  cd "$HOME/daedalus-e2e/core"
  export DAEDALUS_FLEET_CLIENT_CERT="$HOME/daedalus-fleet-kvm/pki/client.pem"
  export DAEDALUS_FLEET_CLIENT_KEY="$HOME/daedalus-fleet-kvm/pki/client.key"
  export DAEDALUS_FLEET_CA="$HOME/daedalus-fleet-kvm/pki/ca.pem"
  export DAEDALUS_FLEET_DIR="$HOME/daedalus-e2e/fleet-file-vma-rerun"
  bash tests/fleet/agent-file-e2e.sh --hosts alma9=vma@localhost:9474
EOS
FILE_E2E_EXIT=1
```

```text
fleet 根目录:/home/udefine-dev/daedalus-e2e/fleet-file-vma-rerun

== 被控机基础信息(目标机自己的 /etc/os-release 与根文件系统类型)
  ok   - alma9(vma): ID=almalinux VERSION_ID=9.8 根文件系统=xfs

== 登记被控机(daedalus-host node add → 拿 node_id)
  ok   - alma9: node add localhost:9474(agent_id=e2e-a9) → node_id=e2e-a9-633a3815 support_level=experimental
    node add: {"node":{"node_id":"e2e-a9-633a3815","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_LVM_SNAP","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794490444,"last_seen_unix":1794490444}}

== 选腿:ext4 与 xfs 各一条低危腿
  FAIL - 没有 ext4 宿主,低危腿(spec §7 要求 ext4 与 xfs 各跑一次)无法执行
  [腿] xfs-alma9: target=/tmp/daedalus-e2e-file.conf content=  e   2   e   -   f   i   l   e   -   v   m   a  \n

…([计划] JSON 全文在此省略,与上一轮同形;从 [控制面自报] 起的原文如下)…
    [控制面自报(exit=0,不作判据)] {"plan_id":"agent-file-e2e-xfs-alma9","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-a9-633a3815"]}],"results":[{"node_id":"e2e-a9-633a3815","intent_id":"agent-file-e2e-xfs-alma9/e2e-a9-633a3815/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-agent-file-e2e-xfs-alma9/e2e-a9-633a3815/0","message":""}],"recovery":[]}
    [独立核实] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      644 13 regular file
    [独立核实] $ sudo sha256sum /tmp/daedalus-e2e-file.conf
      4a2fbf0ede85dacc17800bf36e82cb8b7b3abf8020c5daed2c409e270b1da13e
    [独立核实] $ sudo cat -A /tmp/daedalus-e2e-file.conf
      e2e-file-vma$
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf 存在且是普通文件
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf 内容逐字节相符(sha256 4a2fbf0ede85dacc17800bf36e82cb8b7b3abf8020c5daed2c409e270b1da13e)
  ok   - xfs-alma9: 目标机上 /tmp/daedalus-e2e-file.conf mode=644

== 高危路径腿:/etc/daedalus-e2e.conf(file 在 agent 节点上有内建单文件快照,应放行)
  FAIL - 没有可用的 ext4 宿主,高危路径腿无法执行

== 收尾:删掉本驱动写下的文件(高危路径那条必须删)
  alma9: $ sudo rm -f /etc/daedalus-e2e.conf → rc=0
  alma9: $ sudo rm -f /tmp/daedalus-e2e-file.conf → rc=0

agent-file-e2e: 5 通过 / 2 失败
```

xfs 腿三条断言全过(落地 / 内容逐字节 / mode=644);两条失败都是"没有 ext4 宿主",
与 §9 D4 一字不差 —— 驱动硬要求 ext4 与 xfs 各一条腿,`vma` 只有 xfs(根文件系统
`/dev/vda4 xfs`),单台必然不绿。**本轮没有为了凑绿去拉第二台机器**(那会让另一台
guest 的 `/etc/daedalus-e2e.conf` 等文件被动到,超出本任务范围)。

### 11.9 与期望的逐项对照

| 项 | 期望 | 实测 | 结论 |
| --- | --- | --- | --- |
| 驱动自检 | `21 通过 / 0 失败` | `agent-e2e: 21 通过 / 0 失败`,退出码 0 | **符合**(D2 消除) |
| 低危腿(四台,`tree`) | 基线干净 → `complete` + `tx_id` 非空 + 目标机确证已装 | 全中(§11.3 / §11.5) | **符合**(D1、D3 消除) |
| 高危腿(四台) | 真的打出去 → `status=dirty`、`tx_id` 全空、文案含"无回滚点" | 全中(§11.6) | **符合**(D2 消除) |
| 矩阵汇总行 | `N 通过 / 0 失败` | `agent-e2e: 25 通过 / 0 失败`,退出码 0 | **符合** |
| file 腿(单台) | 驱动要求 ext4 + xfs 各一条 | `agent-file-e2e: 5 通过 / 2 失败`,退出码 1 | **仍不符合(D4 未修)** |

**结论:D1 / D2 / D3 已消除;D4 仍存在。** D4 的成因与本轮修复无关,也没有变化 ——
`agent-file-e2e.sh` 的选腿逻辑硬要求 ext4 与 xfs 各一条(`spec §7`),只给一台 xfs 宿主时
必然记两条"没有 ext4 宿主"失败并退出 1;xfs 腿本身的断言全过,这不是产品缺陷,是驱动的
单台适配缺口。

### 11.10 本轮没动的东西(边界自查)

- 没有卸任何已装包:`nginx` 在四台上仍在;换 `--package tree` 而不是 remove。
- 没有改 Go 源码 / 驱动 / 生成器 / `supportMatrix`;`git status` 里只有本文件。
- 临时产物只落在 KVM 宿主 `~/work/`(`matrix-rerun3.log` / `file-e2e-rerun.log` /
  `probe-facts.sh`)和本机 `/var/tmp/`;没有用宿主 `/tmp`(tmpfs)。
- 两条驱动日志(`matrix-rerun3.log` / `file-e2e-rerun.log`)留在宿主 `~/work/` 备查,
  未入库。
