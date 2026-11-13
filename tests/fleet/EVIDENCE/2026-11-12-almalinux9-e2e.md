# agent 四台矩阵 E2E 证据 — AlmaLinux 9(`vma`)入场(2026-11-12 真机)

> 执行入口:`tests/fleet/agent-e2e.sh` + `tests/fleet/provision-kvm-hosts.sh`。
> **判定独立于被验对象**:低危腿不采信控制面 / agent 的 `status` / `result` / `tx_id`,
> 只采信目标机上包管理器跑出来的事实。
> 本文件每一段都是**真实输出的原文粘贴**;不符之处集中在 §7,不隐瞒。

- **执行日期**:2026-11-12(20:39–20:44 本地 / 12:39–12:44 UTC)
- **执行人**:orca worker(`core#112` 计划 Task 2/3/4;inline 执行,未派 subagent)
- **控制面**:`daedalus-core` @ `29882da`(`feat(fleet): 靶场扩到四台,新增 AlmaLinux 9(vma) 作 RHEL 9 行临时宿主`)
  `bin/daedalus-host` sha256 `57306fd2b027089adf8e57313b93651067a27aafb9145f9e79b62793688c6ee8`(`just go-build`)
- **被控端**:`daedalus-agent` @ `456e40c`(merge `Daedalusys/daedalus-agent#19`,per-kind rollback)
- **靶场**:KVM 宿主 `Masterwet_Server`(宿主名 `undef-dev`),**四台** guest 同时常驻
  (`provision-kvm-hosts.sh --up` 起好;`--status` 四台 `alive=yes`)
- **出货包**:`daedalus-agent-456e40c.tar.gz`
  (sha256 `d4ae8e557daf48158aa52cb7391592a82b24f891e11184b97a7f3f5e981ac4eb`)
- **vma 基础镜像**:`AlmaLinux-9-GenericCloud-9.8-20261112.x86_64.qcow2`
  (sha256 `bf9088375c94dd5df7c22ab78f6183eea007a1c66076224e115d4e2c26205fa4`,与上游 `CHECKSUM` 逐字节一致)

---

## 0. 结论速览

| 项 | 结果 |
| --- | --- |
| `--up`(靶场起第四台) | 退出码 **0**:vma 12G overlay + cloud-init + `dnf makecache`+`rsync` + CA/证书/agent 配置全铺好 |
| `--status` | **四台 `alive=yes`**(vmu/vmd/vmc/vma),宿主 `available` 7909 MB |
| `--selftest`(本地,不 ssh) | **PASS 18 通过 / 0 失败**(退出码 0) |
| 真机四台矩阵 | **25 通过 / 0 失败**(退出码 0):低危腿四台全跑、目标机 `rpm -q` 独立核实 nginx 已装;高危腿第一批即被拒 → `dirty`,`tx_id` 全空 |
| 矩阵低危腿(`package.set nginx`) | 四台 `plan submit` `complete` + 四条非空 `tx_id`;目标机独立核实已装(vma 上是 `nginx-1.20.1-28.el9_8.6.alma.1.x86_64`) |
| 高危 Plan(含"`package` 无回滚点"节点) | `dirty`,拒绝文案点名回滚点缺失,`tx_id` 全空,`recovery` 为空;vma 上 `kernel-core` 的 `INSTALLTIME` 仍是镜像出厂时间 |
| `support_level` 分级 | vmu=supported / vmd=supported / **vmc=experimental** / **vma=experimental(`almalinux`)** |

> 一句话:**`ID=almalinux` 被判成 `experimental`,不是 `supported`** —— 分级来自目标机自己的
> `/etc/os-release`,`supportMatrix` 一行未改。AlmaLinux 9 是本次新增的第四台,链路(install → mTLS
> 注册 → 低危下发 → 目标机落地 → 高危拒绝)与前三台同一条脚本、同一张 CA。

---

## 1. 靶场:`--up` 与 `--status`

### 1.1 `--up` 的原始尾部输出(退出码 0)

```console
$ cd /var/lofibass_ssd/code/Daedalus/daedalus-core
$ bash tests/fleet/provision-kvm-hosts.sh --up
UP2_EXIT=0
```

```text
  ok   - vma:overlay 已建(12G,backing qcow2)
  ok   - vma:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vma/seed.iso
  ok   - vma:qemu 已启动(pid 4070003)
  ok   - vma:ssh 已通
  ok   - vma:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 adb18e5c2791,mode 644)
  ok   - vma:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 d1bdbf4073e9,mode 600)
  ok   - vma:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vma:/etc/daedalus-agent/config.yaml 已铺设(sha256 d5a8780360bf,mode 600)
  ok   - vma:dnf makecache + rsync 已装(有回滚点)
  ok   - vma:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vma/.prepped)
== 收敛校验(读 guest 内的事实)
  ok   - vmu:agent_id=e2e-u24
  ok   - vmu:TLS 材料齐(agent.pem/agent.key/ca.pem)
  ok   - vmu:rsync 在位(/usr/bin/rsync)→ 有回滚点
  ok   - vmd:agent_id=e2e-d12
  ok   - vmd:TLS 材料齐(agent.pem/agent.key/ca.pem)
  ok   - vmd:rsync 在位(/usr/bin/rsync)→ 有回滚点
  ok   - vmc:agent_id=e2e-c9
  ok   - vmc:TLS 材料齐(agent.pem/agent.key/ca.pem)
  ok   - vmc:无 rsync → SNAPSHOT_CAPABILITY_NONE
  ok   - vma:agent_id=e2e-a9
  ok   - vma:TLS 材料齐(agent.pem/agent.key/ca.pem)
  ok   - vma:rsync 在位(/usr/bin/rsync)→ 有回滚点
== 宿主上的产物
  CA:            /home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  控制面客户端:   /home/udefine-dev/daedalus-fleet-kvm/pki/client.pem / /home/udefine-dev/daedalus-fleet-kvm/pki/client.key
  vmu server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  vmd server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  vmc server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  vma server: /home/udefine-dev/daedalus-fleet-kvm/pki/vma.pem
  guest 内已预置: /etc/daedalus-agent/config.yaml + tls/{agent.pem,agent.key,ca.pem}
UP2_EXIT=0
```

### 1.2 `--status` 全量输出(矩阵**跑之前**取,退出码 0)

```console
$ bash tests/fleet/provision-kvm-hosts.sh --status
```

```text
== 宿主:undef-dev(udefine-dev)

== 四台被控机
  vmu  pid=3537293  alive=yes ssh=127.0.0.1:9501(ok) agent=127.0.0.1:9471(fwd=open) guest:9443监听=yes
       guest 内: e2e-u24
       -rw------- 1 root root  711 Nov 11 01:33 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:33 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:33 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:33 /etc/daedalus-agent/tls/ca.pem
  vmd  pid=3537553  alive=yes ssh=127.0.0.1:9502(ok) agent=127.0.0.1:9472(fwd=open) guest:9443监听=yes
       guest 内: e2e-d12
       -rw------- 1 root root  711 Nov 11 01:34 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:34 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:34 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:34 /etc/daedalus-agent/tls/ca.pem
  vmc  pid=3537875  alive=yes ssh=127.0.0.1:9503(ok) agent=127.0.0.1:9473(fwd=open) guest:9443监听=yes
       guest 内: e2e-c9
       -rw-------. 1 root root  710 Nov 10 20:35 /etc/daedalus-agent/config.yaml
       -rw-------. 1 root root 1704 Nov 10 20:35 /etc/daedalus-agent/tls/agent.key
       -rw-r--r--. 1 root root 1610 Nov 10 20:35 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r--. 1 root root 1919 Nov 10 20:35 /etc/daedalus-agent/tls/ca.pem
  vma  pid=4070003  alive=yes ssh=127.0.0.1:9504(ok) agent=127.0.0.1:9474(fwd=open) guest:9443监听=no
       guest 内: e2e-a9
       -rw-------. 1 root root  710 Nov 12 12:42 /etc/daedalus-agent/config.yaml
       -rw-------. 1 root root 1704 Nov 12 12:42 /etc/daedalus-agent/tls/agent.key
       -rw-r--r--. 1 root root 1610 Nov 12 12:42 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r--. 1 root root 1919 Nov 12 12:42 /etc/daedalus-agent/tls/ca.pem

== 宿主上的产物
  total 52
  -rw-------. 1 udefine-dev udefine-dev 3268 Nov 11 08:45 ca.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1919 Nov 11 08:45 ca.pem
  -rw-r--r--. 1 udefine-dev udefine-dev   41 Nov 12 20:41 ca.srl
  -rw-------. 1 udefine-dev udefine-dev 1708 Nov 11 08:45 client.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1594 Nov 11 08:45 client.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 12 20:41 vma.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 12 20:41 vma.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmc.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmc.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmd.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmd.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmu.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmu.pem
  ok   - 四台 guest 全部在跑且 ssh 可达
```

`guest:9443监听=no` 是 **vma 刚起、agent 还没装**的正常态(install.sh 由矩阵那一步执行);
矩阵跑完后同一份 `--status` 里 vma 变 `guest:9443监听=yes`(见 §5.3)。

### 1.3 宿主内存与端口(9504/9474)

```console
$ ssh Masterwet_Server 'free -m | head -2; ss -lntp | grep -E ":(9504|9474)\b"'
```

```text
               total        used        free      shared  buff/cache   available
Mem:           15681        7771        5250         379        3255        7909
---
LISTEN 0      1          127.0.0.1:9504       0.0.0.0:*    users:(("qemu-system-x86",pid=4070003,fd=18))
LISTEN 0      1          127.0.0.1:9474       0.0.0.0:*    users:(("qemu-system-x86",pid=4070003,fd=17))
```

`available` 7909 MB ≥ 6 G(计划门槛);两个端口都由 vma 的 qemu 进程监听。

---

## 2. vma guest 内的事实(矩阵跑之前)

```console
$ ssh Masterwet_Server 'ssh -F ~/daedalus-fleet-kvm/ssh_config vma "...事实..."'
```

```text
ID="almalinux"
VERSION_ID="9.8"
PRETTY_NAME="AlmaLinux 9.8 (Olive Jaguar)"
5.14.0-687.66.2.el9_8.x86_64
/dev/vda4      xfs   11261932 1008028  10253904   9% /
inactive
rc=3
Unit daedalus-agent.service could not be found.
cert_path: /etc/daedalus-agent/tls/agent.pem
key_path: /etc/daedalus-agent/tls/agent.key
ca_path: /etc/daedalus-agent/tls/ca.pem
ca_key_path: /etc/daedalus-agent/tls/ca.key
```

- `ID=almalinux` / `VERSION_ID=9.8` / 内核 `5.14.0-687.66.2.el9_8` / 根文件系统 **xfs**。
- 四个 TLS 路径都非空(生成器铺的同一张 CA)。
- `is-active` 此时是 `inactive`(`rc=3`,unit 还不存在)—— 与计划 Task 2 Step 3 的 Expected 不符,
  原因与处置见 §7.1。

---

## 3. `--selftest` 真实输出(退出码 0)

```console
$ cd /var/lofibass_ssd/code/Daedalus/daedalus-core
$ bash tests/fleet/agent-e2e.sh --selftest
SELFTEST_EXIT=0
```

```text
== 控制面判定(复用 core 既有本地 fake agent 判据,不重复实现控制面半)
  === RUN   TestFleetE2E_LowRiskPlanCompletes
  --- PASS: TestFleetE2E_LowRiskPlanCompletes (0.05s)
  === RUN   TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot
  --- PASS: TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot (0.02s)
  PASS
  ok  	github.com/Daedalusys/daedalus-core/tests/integration	0.072s
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

== 回滚点判定:按 intent kind + 节点快照能力(不按风险等级)
  ok   - file on agent → 有内建单文件 copy 回滚点(与宿主快照无关)
  ok   - file on atomic → 不替 daedalus-tx 承诺,按节点快照能力判
  ok   - package on agent + 快照能力 NONE → 无回滚点(高危腿的选点条件)
  ok   - package on agent + 快照能力 BTRFS → 有回滚点(高危腿不选它)

== 负对照:说谎的 agent(控制面报 complete,目标机上并没有装上)
  ok   - Plan complete 但目标机无此包 → 判定不通过(applyStub 的教训已钉住)
  ok   - 正对照:Plan complete + 目标机确证已装 → 判定通过

== 生成的 Plan 过真校验器(空注册表 ⇒ 只应在"节点未注册"处停)
  ok   - 生成的低危 Plan 通过 Plan 校验(在"节点未注册"处才停,说明结构合法)
  ok   - 反证:改坏 blast_radius.count 后校验器确实拒(说明上一条不是空过)

agent-e2e: 18 通过 / 0 失败
```

---

## 4. 真机四台矩阵真实输出(退出码 0)

```console
$ ssh Masterwet_Server 'cd ~/daedalus-e2e/core && \
  DAEDALUS_FLEET_CLIENT_CERT=$HOME/daedalus-fleet-kvm/pki/client.pem \
  DAEDALUS_FLEET_CLIENT_KEY=$HOME/daedalus-fleet-kvm/pki/client.key \
  DAEDALUS_FLEET_CA=$HOME/daedalus-fleet-kvm/pki/ca.pem \
  DAEDALUS_AGENT_TARBALL=$HOME/daedalus-e2e/artifacts/daedalus-agent-456e40c.tar.gz \
  bash tests/fleet/agent-e2e.sh --hosts \
    ubuntu=vmu@localhost:9471 debian12=vmd@localhost:9472 \
    centos9=vmc@localhost:9473 alma9=vma@localhost:9474'
MATRIX_EXIT=0
```

```text
fleet 根目录:/tmp/tmp.m2IYZ47jrc

== 被控机基础信息(目标机自己的 /etc/os-release;不采信 agent 自报)
  ok   - ubuntu(vmu): ID=ubuntu VERSION_ID=24.04 PRETTY_NAME="Ubuntu 24.04.5 LTS"
    uname -r: 6.8.0-147-generic
  ok   - debian12(vmd): ID=debian VERSION_ID=12 PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
    uname -r: 6.1.0-54-cloud-amd64
  ok   - centos9(vmc): ID=centos VERSION_ID=9 PRETTY_NAME="CentOS Stream 9"
    uname -r: 5.14.0-763.el9.x86_64
  ok   - alma9(vma): ID=almalinux VERSION_ID=9.8 PRETTY_NAME="AlmaLinux 9.8 (Olive Jaguar)"
    uname -r: 5.14.0-687.66.2.el9_8.x86_64

== 逐台安装 daedalus-agent(出货 install.sh)
[install] installing from /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c.tar.gz
[install] stopping existing daedalus-agent.service
[install] installing binary to /opt/daedalus-agent/bin/daedalus-agent
[install] writing unit to /etc/systemd/system/daedalus-agent.service
[install] running postinstall: /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c/scripts/postinstall.sh
[postinstall] creating runtime directories
[postinstall] preserving existing /etc/daedalus-agent/config.yaml
[postinstall] preserving existing /etc/daedalus-agent/README.md
[postinstall] postinstall complete
[install] reloading systemd
[install] enabling + starting daedalus-agent.service
[install] install complete: /opt/daedalus-agent/bin/daedalus-agent
  ok   - ubuntu: install.sh 退出 0
[install] installing from /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c.tar.gz
[install] stopping existing daedalus-agent.service
[install] installing binary to /opt/daedalus-agent/bin/daedalus-agent
[install] writing unit to /etc/systemd/system/daedalus-agent.service
[install] running postinstall: /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c/scripts/postinstall.sh
[postinstall] creating runtime directories
[postinstall] preserving existing /etc/daedalus-agent/config.yaml
[postinstall] preserving existing /etc/daedalus-agent/README.md
[postinstall] postinstall complete
[install] reloading systemd
[install] enabling + starting daedalus-agent.service
[install] install complete: /opt/daedalus-agent/bin/daedalus-agent
  ok   - debian12: install.sh 退出 0
[install] installing from /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c.tar.gz
[install] stopping existing daedalus-agent.service
[install] installing binary to /opt/daedalus-agent/bin/daedalus-agent
[install] writing unit to /etc/systemd/system/daedalus-agent.service
[install] running postinstall: /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c/scripts/postinstall.sh
[postinstall] creating runtime directories
[postinstall] preserving existing /etc/daedalus-agent/config.yaml
[postinstall] preserving existing /etc/daedalus-agent/README.md
[postinstall] postinstall complete
[install] reloading systemd
[install] enabling + starting daedalus-agent.service
[install] install complete: /opt/daedalus-agent/bin/daedalus-agent
  ok   - centos9: install.sh 退出 0
[install] installing from /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c.tar.gz
[install] no running daedalus-agent.service to stop
[install] installing binary to /opt/daedalus-agent/bin/daedalus-agent
[install] writing unit to /etc/systemd/system/daedalus-agent.service
[install] running postinstall: /tmp/daedalus-agent-e2e-4070613/daedalus-agent-456e40c/scripts/postinstall.sh
[postinstall] creating runtime directories
[postinstall] preserving existing /etc/daedalus-agent/config.yaml
[postinstall] wrote /etc/daedalus-agent/README.md
[postinstall] postinstall complete
[install] reloading systemd
[install] enabling + starting daedalus-agent.service
Created symlink /etc/systemd/system/multi-user.target.wants/daedalus-agent.service → /etc/systemd/system/daedalus-agent.service.
[install] install complete: /opt/daedalus-agent/bin/daedalus-agent
  ok   - alma9: install.sh 退出 0

== 登记被控机(daedalus-host node add)
{"node":{"node_id":"e2e-u24-53e2d885","agent_id":"e2e-u24","kind":"agent","addr":"localhost:9471","distro":"ubuntu","distro_version":"24.04","kernel":"","arch":"","hostname":"vmu","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377}}
  ok   - ubuntu: node add localhost:9471(agent_id=e2e-u24)
{"node":{"node_id":"e2e-d12-159930c1","agent_id":"e2e-d12","kind":"agent","addr":"localhost:9472","distro":"debian","distro_version":"12","kernel":"","arch":"","hostname":"vmd","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377}}
  ok   - debian12: node add localhost:9472(agent_id=e2e-d12)
{"node":{"node_id":"e2e-c9-e350c1f1","agent_id":"e2e-c9","kind":"agent","addr":"localhost:9473","distro":"centos","distro_version":"9","kernel":"","arch":"","hostname":"vmc","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377}}
  ok   - centos9: node add localhost:9473(agent_id=e2e-c9)
{"node":{"node_id":"e2e-a9-66ff02b5","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377}}
  ok   - alma9: node add localhost:9474(agent_id=e2e-a9)

== node list 契约
  ok   - 节点数 = 4
  ok   - 每行 support_level ∈ {supported, experimental}
  node list: {"nodes":[{"node_id":"e2e-a9-66ff02b5","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377},{"node_id":"e2e-c9-e350c1f1","agent_id":"e2e-c9","kind":"agent","addr":"localhost:9473","distro":"centos","distro_version":"9","kernel":"","arch":"","hostname":"vmc","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377},{"node_id":"e2e-d12-159930c1","agent_id":"e2e-d12","kind":"agent","addr":"localhost:9472","distro":"debian","distro_version":"12","kernel":"","arch":"","hostname":"vmd","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377},{"node_id":"e2e-u24-53e2d885","agent_id":"e2e-u24","kind":"agent","addr":"localhost:9471","distro":"ubuntu","distro_version":"24.04","kernel":"","arch":"","hostname":"vmu","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487377,"last_seen_unix":1794487377}]}
  node ubuntu: addr=localhost:9471 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_NONE
  node debian12: addr=localhost:9472 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_NONE
  node centos9: addr=localhost:9473 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_NONE
  node alma9: addr=localhost:9474 kind=agent snapshot_capability=SNAPSHOT_CAPABILITY_NONE

== 低危腿:package.set nginx(low)→ 目标机独立核实
  ok   - 低危 Plan status=complete
  ok   - 低危 Plan 每条目标机都有落地记录
  ok   - 每条落地记录都有非空 tx_id(事务 id,不等于"有回滚点")
  plan submit(package.set nginx): {"plan_id":"agent-e2e-low","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-u24-53e2d885"]},{"index":1,"node_ids":["e2e-d12-159930c1"]},{"index":2,"node_ids":["e2e-c9-e350c1f1"]},{"index":3,"node_ids":["e2e-a9-66ff02b5"]}],"results":[{"node_id":"e2e-u24-53e2d885","intent_id":"agent-e2e-low/e2e-u24-53e2d885/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-u24-53e2d885/0","message":""},{"node_id":"e2e-d12-159930c1","intent_id":"agent-e2e-low/e2e-d12-159930c1/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-d12-159930c1/0","message":""},{"node_id":"e2e-c9-e350c1f1","intent_id":"agent-e2e-low/e2e-c9-e350c1f1/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-c9-e350c1f1/0","message":""},{"node_id":"e2e-a9-66ff02b5","intent_id":"agent-e2e-low/e2e-a9-66ff02b5/0","node_kind":"agent","intent_kind":"package","outcome":"success","result":"success","tx_id":"tx-agent-e2e-low/e2e-a9-66ff02b5/0","message":""}],"recovery":[]}
  [独立核实] ubuntu: $ LC_ALL=C dpkg-query -W -f='${Status} ${Version}' nginx 2>&1 || true
    install ok installed 1.24.0-2ubuntu7.19
  ok   - ubuntu: 目标机上确证 nginx 已装(独立于控制面/agent 的自我汇报)
  [独立核实] debian12: $ LC_ALL=C dpkg-query -W -f='${Status} ${Version}' nginx 2>&1 || true
    install ok installed 1.22.1-9+deb12u10
  ok   - debian12: 目标机上确证 nginx 已装(独立于控制面/agent 的自我汇报)
  [独立核实] centos9: $ LC_ALL=C rpm -q nginx 2>&1 || true
    nginx-1.20.1-32.el9.x86_64
  ok   - centos9: 目标机上确证 nginx 已装(独立于控制面/agent 的自我汇报)
  [独立核实] alma9: $ LC_ALL=C rpm -q nginx 2>&1 || true
    nginx-1.20.1-28.el9_8.6.alma.1.x86_64
  ok   - alma9: 目标机上确证 nginx 已装(独立于控制面/agent 的自我汇报)

== 高危腿:只打"package 在该节点上没有回滚点"的节点 → 期望当场 dirty(且什么都没下发)
  ok   - 高危 Plan status=dirty
  ok   - 拒绝文案点名回滚点缺失(逐字含 snapshot 能力字样)
  ok   - 没有任何节点拿到 tx_id(高危 intent 未下发)
  ok   - 没有恢复命令(没有机器被动过)
  plan submit(package.set kernel-core,high): {"plan_id":"agent-e2e-high","status":"dirty","max_risk":"high","batches":[{"index":0,"node_ids":["e2e-u24-53e2d885"]},{"index":1,"node_ids":["e2e-d12-159930c1"]},{"index":2,"node_ids":["e2e-c9-e350c1f1"]},{"index":3,"node_ids":["e2e-a9-66ff02b5"]}],"results":[{"node_id":"e2e-u24-53e2d885","intent_id":"agent-e2e-high/e2e-u24-53e2d885/0","node_kind":"agent","intent_kind":"package","outcome":"persistent","result":"refused_snapshot_capability_none","tx_id":"","message":"节点 e2e-u24-53e2d885 无回滚点(snapshot capability=SNAPSHOT_CAPABILITY_NONE/\"SNAPSHOT_CAPABILITY_NONE\"):high-risk intent 拒绝下发——无回滚点 ≠ 可继续"}],"recovery":[]}

agent-e2e: 25 通过 / 0 失败
```

**矩阵最后一行(原文)**:`agent-e2e: 25 通过 / 0 失败`,退出码 `0`。
低危腿**四台全跑**(不再有"跳过"行);高危腿在第一批就被拒 → 立即 `dirty` 暂停
(`batches` 里四台都在,`results` 只有第一条,且 `tx_id` 为空)。

---

## 5. 目标机上的独立核实

### 5.1 vma 上 nginx 的安装事实

```text
$ ssh vma "LC_ALL=C rpm -q nginx"
nginx-1.20.1-28.el9_8.6.alma.1.x86_64
$ ssh vma "LC_ALL=C rpm -q --qf '%{NAME} INSTALLTIME=%{INSTALLTIME:date}\n' nginx kernel-core"
nginx INSTALLTIME=Thu Nov 12 12:43:01 2026
kernel-core INSTALLTIME=Thu Nov 12 09:32:58 2026
```

- `nginx` 的 `INSTALLTIME` 落在**矩阵运行窗口内**(12:42–12:43 UTC),证明确实是这轮低危腿装的;
- `kernel-core` 的 `INSTALLTIME` 是**镜像出厂时间**(09:32,cloud-init 之前),说明高危腿
  "什么都没下发"在 vma 上同样成立。

### 5.2 agent 在 vma 上确实跑起来了

```text
$ ssh vma "sudo systemctl is-active daedalus-agent; sudo /opt/daedalus-agent/bin/daedalus-agent version; sudo grep -E '^agent_id:' /etc/daedalus-agent/config.yaml"
active
daedalus-agent 0.1.0-dev
agent_id: e2e-a9
```

### 5.3 矩阵跑完后同一份 `--status`

```text
  vma  pid=4070003  alive=yes ssh=127.0.0.1:9504(ok) agent=127.0.0.1:9474(fwd=open) guest:9443监听=yes
       guest 内: e2e-a9
```

---

## 6. `node list` 原始 JSON(单独复现,用于分级取证)

矩阵用的是临时 fleet 根目录,跑完即回收;下面在同一宿主上用独立目录重新登记四台后取 `node list`
(命令各参数与矩阵的 `node add` 完全一致,只有 `DAEDALUS_FLEET_DIR` 不同):

```console
$ ssh Masterwet_Server '... export DAEDALUS_FLEET_DIR=$HOME/work/core112-nodelist; \
    bin/daedalus-host node add localhost:9474 --agent-id e2e-a9 --distro almalinux --distro-version 9.8 --hostname vma; \
    bin/daedalus-host node list | jq -r ".nodes[] | \"\(.hostname)\t\(.distro)\t\(.distro_version)\t\(.support_level)\""'
```

```text
vma	almalinux	9.8	experimental
vmc	centos	9	experimental
vmd	debian	12	supported
vmu	ubuntu	24.04	supported
```

`node list` 原始 JSON:

```json
{"nodes":[{"node_id":"e2e-a9-66ff02b5","agent_id":"e2e-a9","kind":"agent","addr":"localhost:9474","distro":"almalinux","distro_version":"9.8","kernel":"","arch":"","hostname":"vma","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487402,"last_seen_unix":1794487402},{"node_id":"e2e-c9-e350c1f1","agent_id":"e2e-c9","kind":"agent","addr":"localhost:9473","distro":"centos","distro_version":"9","kernel":"","arch":"","hostname":"vmc","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794487402,"last_seen_unix":1794487402},{"node_id":"e2e-d12-159930c1","agent_id":"e2e-d12","kind":"agent","addr":"localhost:9472","distro":"debian","distro_version":"12","kernel":"","arch":"","hostname":"vmd","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487402,"last_seen_unix":1794487402},{"node_id":"e2e-u24-53e2d885","agent_id":"e2e-u24","kind":"agent","addr":"localhost:9471","distro":"ubuntu","distro_version":"24.04","kernel":"","arch":"","hostname":"vmu","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794487402,"last_seen_unix":1794487402}]}
```

**关键一行**:`"distro":"almalinux"` → `"support_level":"experimental"`。`supportMatrix` 未改,
AlmaLinux 没有被映射成 `rhel`;也没有用 `--distro rhel` 手填进来冒充分级。

---

## 7. 与期望不符之处 / 未验证项

### 7.1 计划 Task 2 Step 3 期望 vma 上 `systemctl is-active daedalus-agent` = `active`,实际 `inactive`

**现象**:`inactive`(`rc=3`),`Unit daedalus-agent.service could not be found.`

**原因**:生成器 `provision-kvm-hosts.sh --up` 只负责"起机 + prep 装包 + 铺 config/TLS",
**不装 agent 二进制与 systemd unit** —— 那一步是 `agent-e2e.sh` 里逐台跑出货 `install.sh`。
vmu/vmd/vmc 上之所以是 `active`,是因为它们在上几轮已经被装过;vma 是新 guest,自然没有 unit。

**处置**:不当失败处理,也不手工装:矩阵的 install 步骤就是"用出货包装上"。矩阵跑完后
§5.2 / §5.3 显示 vma 上 `active` + 9443 监听 + `agent_id: e2e-a9`。**计划里那行 Expected 与生成器的职责边界不符**,
建议后续修订计划时改成"`is-active` 允许 `inactive`(新 guest 尚未装 agent),以矩阵 install 后为准"。

### 7.2 首次 `--up` 卡在镜像下载(repo.almalinux.org ~9–26 KB/s),改用镜像站预置

**现象**:脚本内置的 `https://repo.almalinux.org/.../AlmaLinux-9-GenericCloud-9.8-20261112.x86_64.qcow2`
在宿主上实测 **9–26 KB/s**(curl 自报 ETA 17–23 小时),539 MiB 不可能在合理时间内拉完。

**处置**(未改脚本一行):从 `mirror.nju.edu.cn` 拉同一 dated 文件(实测 ~28 MB/s,20 秒完成),
用**上游 `CHECKSUM`** 校验 `sha256 = bf9088375c94dd5df7c22ab78f6183eea007a1c66076224e115d4e2c26205fa4`,
`qemu-img info` 确认 qcow2/10 GiB 虚拟盘,再放到脚本期望路径
`~/daedalus-fleet-kvm/images/AlmaLinux-9-GenericCloud-9.8-20261112.x86_64.qcow2`,
重跑 `--up` 时脚本按"基础镜像已有"跳过下载(§1.1 第一行 `ok - vma 基础镜像已有(... 540M)`)。
**这属于运行环境的绕行,不是对靶场脚本的改动**;脚本本身仍然钉死官方 URL。

### 7.3 vma 上没有取 nginx 的"执行前基线"

前几轮证据里是"先卸 nginx → 取干净基线 → 跑矩阵 → 再取一次"。本轮 vma 是**全新 guest**,
nginx 从未装过,故没有单独跑一遍卸载+基线;改用 `INSTALLTIME` 取证(§5.1):
`nginx` 的安装时间落在矩阵窗口内、`kernel-core` 的安装时间内仍是镜像出厂时间。判据仍然是
目标机自己的包管理器输出,不采信控制面自报。

### 7.4 被控端 commit 与上一轮不同

上一轮证据(`2026-11-11-agent-e2e.md`)是 `daedalus-agent@6bee8f5`;本轮 `daedalus-agent` 仓
HEAD 已到 `456e40c`(merge `#19`,per-kind rollback 分支)。所以本轮的 25 通过 / 0 失败
**不能**直接横比上一轮的 21 通过 / 0 失败,是"新 agent + 多一台 Alma"的组合结果。

### 7.5 仍未被覆盖的(与上一轮相同的缺口)

- **高危腿只覆盖 `package` kind**;`file` kind 的高危放行路径见 `2026-11-11-agent-file-e2e.md`,本轮未重跑。
- **"有回滚点的节点"仍无真机样本**:四台 agent 的 `snapshot_capability` 全是 `SNAPSHOT_CAPABILITY_NONE`
  (agent 已停用宿主快照探测),所以"高危腿跳过有回滚点的节点"只由 `--selftest` 钉住。
  生成器 `--up` 文案"rsync 在位 → 有回滚点"与 agent 侧新语义不符(计划 Review Focus 已列出),
  本轮按边界**未改文案**。
- **人审恢复路径**(`plan advance --skip/--force`、本地 `daedalus-tx rollback`)、`ctx` 取消、
  锁被占等错误分桶未在 vma 上逐一制造 —— 由 `--selftest` 与 `tests/fleet/plan-c-e2e.sh` 覆盖。
- **重复投递幂等**:vmu/vmd/vmc 是"重装一遍 nginx"(上一轮已装),vma 是"从未装 → 装上";
  两种都跑通,但没做"同一 Plan 连发两次"的显式验证。

---

## 8. 真 RHEL 9/10 路径(外部阻塞,本文不解决)

计划 Task 5 列的三条取得途径(按代价排序),这里原样引用,**本轮未执行**:

1. **找一台能连 `cdn.redhat.com` 的机器**(宿主上 `cdn.redhat.com` 返回 `000`,是网络面问题,
   不是账号问题):开发者订阅下载 RHEL 9/10 cloud image → 落到 `~/daedalus-fleet-kvm/images/` →
   生成器里加 `rhel9` family(镜像 + 端口 9505/9475)→ `--up`。
2. **离线 ISO + 本地 repo**:下载 RHEL 9/10 dvd ISO(同样要能连 CDN)→ guest 装完后把 ISO 当本地
   yum repo,`dnf` 不走 CDN。代价:guest 体积与准备步骤翻倍。
3. **暂时不取真 RHEL**:`core#112` 保持"缺异构宿主",AlmaLinux 证据作兼容性佐证,矩阵分级不动。

真机到货后的验收命令(逐字,来自计划):

```bash
bash tests/fleet/agent-e2e.sh --hosts rhel9=<ssh别名>@<可拨地址>
bin/daedalus-host node list | jq -r '.nodes[] | select(.distro=="rhel") | .support_level'
```

只有当 `rhel9` → `supported`、且证据落 `tests/fleet/EVIDENCE/<日期>-rhel-e2e.md` 时,`core#112` 才可关闭。

---

## 9. 结论

- [x] 第四台 `vma`(AlmaLinux 9.8 / xfs / 2 GB / ssh 9504 / agent 9474)真跑起来了,
      `--status` 四台 `alive=yes`,宿主 `available` 7909 MB、两个端口都在监听(§1)。
- [x] `--selftest` 18 通过 / 0 失败,退出码 0(§3)。
- [x] 四台矩阵 **25 通过 / 0 失败**,退出码 0:低危腿四台全跑,目标机独立核实 nginx 已装(§4、§5.1)。
- [x] 高危腿 `status=dirty`、`tx_id` 全空、`recovery` 为空;vma 上 `kernel-core` 的 `INSTALLTIME`
      与镜像出厂一致(§4、§5.1)。
- [x] `alma9`: **experimental**(`ID=almalinux` 不在 spec §7.1 已拍矩阵内)。RHEL 9 行仍**未验**——
      AlmaLinux 是 RHEL 9 的 1:1 重建,链路等价性可证,分级等价性不可证。
- [x] 真 RHEL 9/10 的宿主取得与阻塞见 §8(真 RHEL 路径);core#112 **不因本文关闭**。
