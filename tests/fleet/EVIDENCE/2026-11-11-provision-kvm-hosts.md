# provision-kvm-hosts.sh 真机证据 — 三台异构 KVM 被控机

> 交付物:`tests/fleet/provision-kvm-hosts.sh`(新增;本次唯一改动的被交付文件)。
> 被验对象是**真机**:三台 cloud image 虚机真的在 KVM 宿主上跑起来了,PKI 真的
> 被 `openssl verify` 验过,`ssh vmu/vmd/vmc` 真的通。本文件每一段都是**真实输出的
> 原文粘贴**,不做修饰。

- **执行日期**:2026-11-11
- **执行人**:loficore(orca worker `code-deep`)
- **KVM 宿主**:ssh 别名 `Masterwet_Server`(`undef-dev` / 用户 `udefine-dev`,Fedora 44,
  `/dev/kvm` 可读写,`qemu-system-x86_64` 10.2.2、`cloud-localds`、`openssl` 3.5.9 齐备)
- **被控端**:三台 cloud image 虚机(Ubuntu 24.04 / Debian 12 / CentOS Stream 9)
- **下游消费者**:`tests/fleet/agent-e2e.sh`(本脚本只负责把靶场摆好,不改它;见 §10)

---

## 0. 结论速览

| 项 | 结果 |
| --- | --- |
| `--selftest`(本机干跑,不 ssh / 不碰 KVM / 不联网) | **PASS 46 通过 / 0 失败** |
| `--destroy` → `--up`(真机从零拉起三台) | **EXIT=0**,2 分 07 秒 |
| 再跑一次 `--up`(幂等) | **EXIT=0**,6.6 秒,**没有重建、没有重装、没有重传** |
| `--status` | 三台 `alive=yes`、ssh 可达、两个转发端口都在 |
| 共享 PKI | 一张 CA 校验三份 server 证书 + 一张客户端证书,全部 `OK` |
| 服务端证书 SAN | `DNS:localhost, IP Address:127.0.0.1`(控制面从宿主拨 `127.0.0.1:<端口>`) |
| CA 私钥落地范围 | **只在宿主**;三台 guest 内 `ca.key` 均 `absent` |
| `--destroy --purge-images` → `--up` | 基础镜像真被删、真被重新下载、三台重新建起,**EXIT=0** |
| 回滚点分工 | vmu/vmd 有 `rsync`;vmc 无 `rsync` 无 `btrfs`(→ `SNAPSHOT_CAPABILITY_NONE`) |

---

## 1. 拓扑与约定

三台 guest 的事实表就写在脚本里(`GUEST_NAMES` / `GUEST_FAMILY` / `GUEST_AGENT_ID` /
`GUEST_SSH_PORT` / `GUEST_AGENT_PORT`),本地与远端共用同一份,不存在两套配置漂移:

| guest | distro | 基础镜像 | `agent_id` | ssh 转发 | agent 转发 | 回滚点 |
| --- | --- | --- | --- | --- | --- | --- |
| `vmu` | Ubuntu 24.04.5 LTS | `noble-server-cloudimg-amd64.img` | `e2e-u24` | `127.0.0.1:9501→22` | `127.0.0.1:9471→9443` | 有(`rsync`) |
| `vmd` | Debian 12 (bookworm) | `debian-12-genericcloud-amd64.qcow2` | `e2e-d12` | `127.0.0.1:9502→22` | `127.0.0.1:9472→9443` | 有(`rsync`) |
| `vmc` | CentOS Stream 9 | `CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2` | `e2e-c9` | `127.0.0.1:9503→22` | `127.0.0.1:9473→9443` | **无**(→ `SNAPSHOT_CAPABILITY_NONE`) |

为什么是用户态网络(slirp)而不是网桥:**不动宿主网络配置**,且宿主 `127.0.0.1:<agent端口>`
直接映射到 guest 的 `9443`,控制面就跑在宿主上,拨 `localhost` 正好命中 server 证书的 SAN。

每台 guest 的构造:12G qcow2 overlay(backing = 只读基础镜像)+ `cloud-localds` 造的
NoCloud seed(cloud-init)+ qemu `-daemonize`(pid 落在 `vms/<guest>/qemu.pid`);
guest 用户 `daedalus`(Ubuntu/Debian 进 `sudo` 组、CentOS 进 `wheel`),**免密 sudo、
禁密码、禁 root 登录,只认公钥**。

宿主工作目录(`~/daedalus-fleet-kvm/`):

```text
images/           基础镜像(只读,不覆盖已下载的;--purge-images 才删)
vms/<guest>/      overlay.qcow2 / seed.iso / user-data / meta-data / qemu.pid / console.log
                  + config.yaml(预置给 guest 的那份)+ .prepped(prep 标记)
pki/              共享 CA + 每台一份 server 证书 + 一张控制面客户端证书(CA 私钥只在这)
keys/             guest 的 ssh 密钥对
known_hosts       三台 guest 的主机密钥(按 guest 名归档,不污染用户自己的)
remote.sh         上传到宿主的那份远端程序(按 sha256 跳过重传)
```

---

## 2. `--selftest`:本机干跑,46 条断言全部真跑

`--selftest` **不 ssh、不碰 KVM、不联网**:所有远端动作都被桩替换,并由一个桩 PATH
断言"`ssh`/`qemu`/`curl` 一次都没被真调用"。断言的就是真跑时跑的那份逻辑
(共享库 `SHARED_LIB` 本地与远端同一份,见 §9 坑 8)。

```bash
bash tests/fleet/provision-kvm-hosts.sh --selftest    # 退出码 0
```

```text
### SELFTEST

== 参数解析(真调用本脚本,断言退出码)
  ok   - --help 退出 0
  ok   - 未知参数退出 2(实际 2)
  ok   - --purge-images 不带 --destroy 被拒(退出 2)
  ok   - 参数解析:合法参数退出 0
  ok   - 参数解析:MODE 解析为 status
  ok   - 参数解析:缺省模式是 up

== 端口与布局分配(自检 + 反证)
  ok   - 三台 guest 的名字/agent_id/端口无冲突
  ok   - 反证:两台 ssh 端口撞车被拒
  ok   - 反证:agent 端口撞 ssh 端口被拒
  ok   - 端口均在 1024-65535 且两两不同

== cloud-init / seed 渲染
  ok   - user-data 以 #cloud-config 开头
  ok   - user-data 写对 hostname(vmu)
  ok   - user-data 带上 ssh 公钥
  ok   - user-data 给免密 sudo
  ok   - Ubuntu guest 落进 sudo 组
  ok   - CentOS guest 落进 wheel 组
  ok   - meta-data 的 instance-id 正确
  ok   - config.yaml 带 vmu 自己的 agent_id
  ok   - config.yaml 带 vmc 自己的 agent_id
  ok   - config.yaml 的 cert_path 非空(agent 才开 mTLS)
  ok   - config.yaml 的 key_path 非空(agent 才开 mTLS)
  ok   - config.yaml 的 ca_path 非空(agent 才开 mTLS)
  ok   - config.yaml 的 ca_key_path 非空(agent 才开 mTLS)
  ok   - 三台 guest 的 config.yaml 各自不同(agent_id 不串台)

== qemu 命令行渲染(hostfwd / overlay / seed)
  ok   - vmu 的 ssh 端口转发已渲染
  ok   - vmu 的 agent 端口转发已渲染
  ok   - overlay 挂在 virtio 上
  ok   - NoCloud seed 以 cdrom 挂载
  ok   - qemu 以 daemon 方式起(有 pidfile)
  ok   - qemu 参数一行一个 argv(没有被空格粘成非法选项)
  ok   - -name 与 guest 名分属两个 argv(顺序正确)
  ok   - qemu 选项数符合预期(14 个 - 开头元素)

== 包管理器撞锁的重试语义(失败不能被吞成成功)
  ok   - 被测函数(重试 / prep 门槛)确实已定义
  ok   - 前两次撞锁后第 3 次成功(重试而不是放弃)
  ok   - 始终失败时最终判失败(不会被静默放过;重试 8 次)
  ok   - 全新 guest 即使自带 rsync 也要 prep 一次(刷新旧索引)
  ok   - 已 prep + rsync 在位 → 跳过装包
  ok   - 已 prep 但 rsync 没了 → 判定需要补装
  ok   - vmc 已 prep 且无 rsync/btrfs → 跳过装包(SNAPSHOT_CAPABILITY_NONE 成立)

== 宿主 ~/.ssh/config 标记块的幂等性
  ok   - 写两次结果逐字节相同(幂等)
  ok   - 标记块只出现一次(没有叠加)
  ok   - 用户原有的 ssh 配置内容被保留
  ok   - 标记块里有 vmu/vmd/vmc 三个 Host 条目
  ok   - 标记块可整块移除(销毁用)
  ok   - 移除标记块不动用户原有内容

== 干跑边界:不 ssh、不碰 KVM、不联网
  ok   - selftest 期间未调用 ssh/qemu/网络类命令(桩文件未被触发)

provision-kvm-hosts: 46 通过 / 0 失败
EXIT=0
```

---

## 3. 真机:先 `--destroy` 清成空场,再 `--up` 从零拉起

```bash
bash tests/fleet/provision-kvm-hosts.sh --destroy
bash tests/fleet/provision-kvm-hosts.sh --up
```

`--destroy` 只停虚机、删 `vms/`、摘掉宿主 `~/.ssh/config` 里自己那块标记;
**基础镜像、共享 PKI、guest ssh 密钥都保留**(下一轮 `--up` 才能"复用而不是重建"):

```text
### DESTROY

== 停机与清理
  ok   - vmu:qemu(pid 3533117)已停
  ok   - vmd:qemu(pid 3533312)已停
  ok   - vmc:qemu(pid 3533695)已停
  ok   - vmu:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmu)
  ok   - vmd:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmd)
  ok   - vmc:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmc)
  ok   - 宿主 ~/.ssh/config 标记块已移除
  ok   - 基础镜像保留在 /home/udefine-dev/daedalus-fleet-kvm/images(要删加 --purge-images)
  ok   - 共享 PKI 与 ssh 密钥保留在 /home/udefine-dev/daedalus-fleet-kvm/pki、/home/udefine-dev/daedalus-fleet-kvm/keys
EXIT=0
```

`--up`(从零拉起,2 分 07 秒;`..` 行是"发现 guest 被重建过,自动摘掉宿主 known_hosts
里的旧主机密钥",不是失败):

```text
### UP (fresh)

== 宿主预检
  ok   - qemu-system-x86_64 / qemu-img / cloud-localds / openssl 齐备,/dev/kvm 可写

== 基础镜像(不覆盖已下载的)
  ok   - vmu 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/noble-server-cloudimg-amd64.img,593M)
  ok   - vmd 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/debian-12-genericcloud-amd64.qcow2,334M)
  ok   - vmc 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2,1.2G)

== guest ssh 密钥
  ok   - ssh 密钥:/home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519

== 共享 PKI(一张 CA + 每台一份 server 证书 + 一张控制面客户端证书)
  ok   - 共享 CA 复用:/home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  ok   - vmu server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  ok   - vmd server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  ok   - vmc server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  ok   - 控制面客户端证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/client.pem

== 宿主 ssh 配置
  ok   - 宿主 ~/.ssh/config 已写入标记块(可 ssh vmu/vmd/vmc)

== 三台 guest
  ok   - vmu:overlay 已建(12G,backing qcow2)
  ok   - vmu:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmu/seed.iso
  ok   - vmu:qemu 已启动(pid 3535152)
  ..   - vmu:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmu:ssh 已通
  ok   - vmu:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 4994aacd5705,mode 644)
  ok   - vmu:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 45599cb75ef0,mode 600)
  ok   - vmu:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmu:/etc/daedalus-agent/config.yaml 已铺设(sha256 1af09b1a8b1b,mode 600)
  ok   - vmu:apt-get update + rsync 已装
  ok   - vmu:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmu/.prepped)
  ok   - vmd:overlay 已建(12G,backing qcow2)
  ok   - vmd:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmd/seed.iso
  ok   - vmd:qemu 已启动(pid 3535401)
  ..   - vmd:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmd:ssh 已通
  ok   - vmd:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 2def8d0f3615,mode 644)
  ok   - vmd:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 00948d302627,mode 600)
  ok   - vmd:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmd:/etc/daedalus-agent/config.yaml 已铺设(sha256 1bd0d896646e,mode 600)
  ok   - vmd:apt-get update + rsync 已装
  ok   - vmd:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmd/.prepped)
  ok   - vmc:overlay 已建(12G,backing qcow2)
  ok   - vmc:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmc/seed.iso
  ok   - vmc:qemu 已启动(pid 3535733)
  ..   - vmc:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmc:ssh 已通
  ok   - vmc:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 82527c9092ae,mode 644)
  ok   - vmc:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 20f7b36513fe,mode 600)
  ok   - vmc:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmc:/etc/daedalus-agent/config.yaml 已铺设(sha256 57ef3eac2bfe,mode 600)
  ok   - vmc:dnf makecache + 已确保无 rsync / btrfs(SNAPSHOT_CAPABILITY_NONE)
  ok   - vmc:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmc/.prepped)

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

== 宿主上的产物
  CA:            /home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  控制面客户端:   /home/udefine-dev/daedalus-fleet-kvm/pki/client.pem / /home/udefine-dev/daedalus-fleet-kvm/pki/client.key
  vmu server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  vmd server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  vmc server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  guest 内已预置: /etc/daedalus-agent/config.yaml + tls/{agent.pem,agent.key,ca.pem}
bash tests/fleet/provision-kvm-hosts.sh --up 2>&1  0.12s user 0.05s system 0% cpu 2:06.88 total
EXIT=0
```

---

## 4. 再跑一次 `--up`:必须是 no-op

同一份脚本第二次运行 —— 虚机复用、配置按 sha256 判定"已是目标内容"跳过、prep 标记
已存在跳过装包。**6.6 秒**,输出里没有一次重建/重装:

```text
### UP (2nd, no-op)

== 宿主预检
  ok   - qemu-system-x86_64 / qemu-img / cloud-localds / openssl 齐备,/dev/kvm 可写

== 基础镜像(不覆盖已下载的)
  ok   - vmu 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/noble-server-cloudimg-amd64.img,593M)
  ok   - vmd 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/debian-12-genericcloud-amd64.qcow2,334M)
  ok   - vmc 基础镜像已有(/home/udefine-dev/daedalus-fleet-kvm/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2,1.2G)

== guest ssh 密钥
  ok   - ssh 密钥:/home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519

== 共享 PKI(一张 CA + 每台一份 server 证书 + 一张控制面客户端证书)
  ok   - 共享 CA 复用:/home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  ok   - vmu server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  ok   - vmd server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  ok   - vmc server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  ok   - 控制面客户端证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/client.pem

== 宿主 ssh 配置
  ok   - 宿主 ~/.ssh/config 已写入标记块(可 ssh vmu/vmd/vmc)

== 三台 guest
  ok   - vmu:已在跑(pid 3535152),复用不重建
  ok   - vmu:ssh 已通
  ok   - vmu:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 4994aacd5705),跳过
  ok   - vmu:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 45599cb75ef0),跳过
  ok   - vmu:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmu:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 1af09b1a8b1b),跳过
  ok   - vmu:prep 已做过且状态正确,跳过装包
  ok   - vmd:已在跑(pid 3535401),复用不重建
  ok   - vmd:ssh 已通
  ok   - vmd:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 2def8d0f3615),跳过
  ok   - vmd:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 00948d302627),跳过
  ok   - vmd:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmd:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 1bd0d896646e),跳过
  ok   - vmd:prep 已做过且状态正确,跳过装包
  ok   - vmc:已在跑(pid 3535733),复用不重建
  ok   - vmc:ssh 已通
  ok   - vmc:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 82527c9092ae),跳过
  ok   - vmc:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 20f7b36513fe),跳过
  ok   - vmc:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmc:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 57ef3eac2bfe),跳过
  ok   - vmc:prep 已做过且状态正确,跳过装包

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

== 宿主上的产物
  CA:            /home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  控制面客户端:   /home/udefine-dev/daedalus-fleet-kvm/pki/client.pem / /home/udefine-dev/daedalus-fleet-kvm/pki/client.key
  vmu server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  vmd server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  vmc server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  guest 内已预置: /etc/daedalus-agent/config.yaml + tls/{agent.pem,agent.key,ca.pem}
bash tests/fleet/provision-kvm-hosts.sh --up 2>&1  0.12s user 0.04s system 2% cpu 6.569 total
EXIT=0
```

---

## 5. `--status`:名字 / qemu pid / 存活 / ssh 端口 / agent 端口 / 可达性

```text
### STATUS

== 宿主:undef-dev(udefine-dev)

== 三台被控机
  vmu  pid=3535152  alive=yes ssh=127.0.0.1:9501(ok) agent=127.0.0.1:9471(fwd=open) guest:9443监听=no
       guest 内: e2e-u24
       -rw------- 1 root root  711 Nov 11 01:27 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:27 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:27 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:27 /etc/daedalus-agent/tls/ca.pem
  vmd  pid=3535401  alive=yes ssh=127.0.0.1:9502(ok) agent=127.0.0.1:9472(fwd=open) guest:9443监听=no
       guest 内: e2e-d12
       -rw------- 1 root root  711 Nov 11 01:28 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:28 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:28 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:28 /etc/daedalus-agent/tls/ca.pem
  vmc  pid=3535733  alive=yes ssh=127.0.0.1:9503(ok) agent=127.0.0.1:9473(fwd=open) guest:9443监听=no
       guest 内: e2e-c9
       -rw-------. 1 root root  710 Nov 10 20:29 /etc/daedalus-agent/config.yaml
       -rw-------. 1 root root 1704 Nov 10 20:29 /etc/daedalus-agent/tls/agent.key
       -rw-r--r--. 1 root root 1610 Nov 10 20:29 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r--. 1 root root 1919 Nov 10 20:29 /etc/daedalus-agent/tls/ca.pem

== 宿主上的产物
  total 44
  -rw-------. 1 udefine-dev udefine-dev 3268 Nov 11 08:45 ca.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1919 Nov 11 08:45 ca.pem
  -rw-r--r--. 1 udefine-dev udefine-dev   41 Nov 11 08:45 ca.srl
  -rw-------. 1 udefine-dev udefine-dev 1708 Nov 11 08:45 client.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1594 Nov 11 08:45 client.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmc.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmc.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmd.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmd.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmu.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmu.pem
  ok   - 三台 guest 全部在跑且 ssh 可达
EXIT=0
```

> `guest:9443监听=no` 是**预期**的:agent 二进制由 `agent-e2e.sh` 装(本脚本只负责
> 在装之前把 config + TLS 预置好),此时还没人监听 9443;但 `agent=127.0.0.1:9471(fwd=open)`
> 说明**转发通路已就绪**,agent 一起来控制面就能拨通。

宿主侧再看一眼虚机本体(12G 稀疏盘、backing 指向基础镜像、pid 与 pidfile 一致、
六个转发端口在听):

```text
=== overlay: 12G 虚拟盘,backing 指向基础镜像 ===
vmu  virtual size: 12 GiB (12884901888 bytes) backing file: /home/udefine-dev/daedalus-fleet-kvm/images/noble-server-cloudimg-amd64.img 
vmd  virtual size: 12 GiB (12884901888 bytes) backing file: /home/udefine-dev/daedalus-fleet-kvm/images/debian-12-genericcloud-amd64.qcow2 
vmc  virtual size: 12 GiB (12884901888 bytes) backing file: /home/udefine-dev/daedalus-fleet-kvm/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2 

=== qemu 进程与 pid 文件一致 ===
vmu  pid=3537293 alive
vmd  pid=3537553 alive
vmc  pid=3537875 alive

=== 宿主上的端口转发(user-mode net 的 hostfwd)===
127.0.0.1:9471
127.0.0.1:9472
127.0.0.1:9473
127.0.0.1:9501
127.0.0.1:9502
127.0.0.1:9503

=== 宿主工作目录 ===
images
keys
known_hosts
known_hosts.old
pki
remote.sh
ssh_config
vms

=== 每台 guest 的工作目录 ===
config.yaml
console.log
meta-data
overlay.qcow2
qemu.pid
seed.iso
user-data
```

---

## 6. guest 内的事实(不采信脚本自报,直接读 guest)

```text
=== 三台 guest 自己的事实(不经脚本自报,直接从 guest 里读)===
--- vmu
PRETTY_NAME="Ubuntu 24.04.5 LTS"
VERSION_ID="24.04"
kernel: 6.8.0-147-generic
user: daedalus
agent_id: e2e-u24
config 里 mTLS 四路径齐全: 4
rsync: /usr/bin/rsync
btrfs: /usr/bin/btrfs
sudo: nopasswd-ok
--- vmd
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
VERSION_ID="12"
kernel: 6.1.0-54-cloud-amd64
user: daedalus
agent_id: e2e-d12
config 里 mTLS 四路径齐全: 4
rsync: /usr/bin/rsync
btrfs: none
sudo: nopasswd-ok
--- vmc
VERSION_ID="9"
PRETTY_NAME="CentOS Stream 9"
kernel: 5.14.0-763.el9.x86_64
user: daedalus
agent_id: e2e-c9
config 里 mTLS 四路径齐全: 4
rsync: none
btrfs: none
sudo: nopasswd-ok
```

`config 里 mTLS 四路径齐全: 4` 是关键:agent 的 `config.go` 里 **四个 TLS 路径
(`cert_path`/`key_path`/`ca_path`/`ca_key_path`)全部非空才会开 mTLS**,少一个就
静默降级成"关闭"。预置的 config 长这样(vmu / vmc 各一份,`agent_id` 不同):

```text
--- vmu /etc/daedalus-agent/config.yaml
# 由 tests/fleet/provision-kvm-hosts.sh 预置;不要手改。
# 四个 TLS 路径必须都非空,否则 agent 会把 mTLS 降级成"关闭"(见 agent main.go)。
agent_id: e2e-u24
# 控制面跑在 KVM 宿主上;slirp 用户态网络里宿主的地址是 10.0.2.2。
control_plane_addr: https://10.0.2.2:7443
cert_path: /etc/daedalus-agent/tls/agent.pem
key_path: /etc/daedalus-agent/tls/agent.key
ca_path: /etc/daedalus-agent/tls/ca.pem
# 共享 CA 的私钥只留在宿主上,不铺进 guest:预置的 server 证书有效期 10 年,
# 不会触发 agent 的轮换路径(轮换需要 ca.key)。
ca_key_path: /etc/daedalus-agent/tls/ca.key
data_dir: /var/lib/daedalus-agent
audit_dir: /var/log/daedalus-agent

--- vmc /etc/daedalus-agent/config.yaml
# 由 tests/fleet/provision-kvm-hosts.sh 预置;不要手改。
# 四个 TLS 路径必须都非空,否则 agent 会把 mTLS 降级成"关闭"(见 agent main.go)。
agent_id: e2e-c9
# 控制面跑在 KVM 宿主上;slirp 用户态网络里宿主的地址是 10.0.2.2。
control_plane_addr: https://10.0.2.2:7443
cert_path: /etc/daedalus-agent/tls/agent.pem
key_path: /etc/daedalus-agent/tls/agent.key
ca_path: /etc/daedalus-agent/tls/ca.pem
# 共享 CA 的私钥只留在宿主上,不铺进 guest:预置的 server 证书有效期 10 年,
# 不会触发 agent 的轮换路径(轮换需要 ca.key)。
ca_key_path: /etc/daedalus-agent/tls/ca.key
data_dir: /var/lib/daedalus-agent
audit_dir: /var/log/daedalus-agent

```

> `ca_key_path` 指向 `/etc/daedalus-agent/tls/ca.key` —— 这个文件**故意不存在**于
> guest(共享 CA 私钥只留宿主)。agent 只在证书缺失时才自举 CA/轮换,预置证书有效期
> 到 2036 年,不会走到需要 `ca.key` 的路径;而"路径非空"满足了开 mTLS 的前提。

---

## 7. 共享 PKI:一张 CA、每台一份 server 证书、一张控制面客户端证书

为什么必须共享:控制面对一个 Plan 里的所有节点只持有一份客户端证书,三台各持一套 CA
时 `plan submit` 谁都握不上手;agent 首启又会在 `ca_path` 处自举自己的 CA —— 所以必须
在 agent install **之前**把同一张 CA + 各自签发的 server 证书铺进去。

```text
=== 证书链(同一张 CA 校验三份 server 证书 + 控制面客户端证书)===
vmu     vmu.pem: OK
vmd     vmd.pem: OK
vmc     vmc.pem: OK
client  client.pem: OK

=== SAN / subject / 有效期 ===
vmu  subject=O=Daedalusys, CN=vmu notAfter=Nov  8 00:45:06 2036 GMT     DNS:localhost, IP Address:127.0.0.1
vmd  subject=O=Daedalusys, CN=vmd notAfter=Nov  8 00:45:06 2036 GMT     DNS:localhost, IP Address:127.0.0.1
vmc  subject=O=Daedalusys, CN=vmc notAfter=Nov  8 00:45:06 2036 GMT     DNS:localhost, IP Address:127.0.0.1

=== SAN=DNS:localhost 的用途(控制面从宿主拨 127.0.0.1:<agent端口>)===
    DNS:localhost, IP Address:127.0.0.1

=== CA 私钥只在宿主:guest 内必须没有 ===
vmu  absent
vmd  absent
vmc  absent

=== 三台 guest 的 ca.pem 与宿主同一张(sha256 前 16 位)===
vmu  96d539181ed6d336
vmd  96d539181ed6d336
vmc  96d539181ed6d336
宿主   96d539181ed6d336
```

宿主 `~/.ssh/config` 里那块**可整块摘除的标记块**(`--up` 写入/刷新,`--destroy` 整块摘掉;
上面那段 `--status` 能直接 `ssh vmu` 就是它的作用):

```text
=== 宿主 ~/.ssh/config(整份;脚本的标记块是唯一内容)===
     1	# BEGIN daedalus-fleet-kvm (managed by tests/fleet/provision-kvm-hosts.sh)
     2	Host vmu
     3	    HostName 127.0.0.1
     4	    Port 9501
     5	    User daedalus
     6	    IdentityFile /home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519
     7	    IdentitiesOnly yes
     8	    StrictHostKeyChecking accept-new
     9	    UserKnownHostsFile /home/udefine-dev/daedalus-fleet-kvm/known_hosts
    10	    LogLevel ERROR
    11	Host vmd
    12	    HostName 127.0.0.1
    13	    Port 9502
    14	    User daedalus
    15	    IdentityFile /home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519
    16	    IdentitiesOnly yes
    17	    StrictHostKeyChecking accept-new
    18	    UserKnownHostsFile /home/udefine-dev/daedalus-fleet-kvm/known_hosts
    19	    LogLevel ERROR
    20	Host vmc
    21	    HostName 127.0.0.1
    22	    Port 9503
    23	    User daedalus
    24	    IdentityFile /home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519
    25	    IdentitiesOnly yes
    26	    StrictHostKeyChecking accept-new
    27	    UserKnownHostsFile /home/udefine-dev/daedalus-fleet-kvm/known_hosts
    28	    LogLevel ERROR
    29	# END daedalus-fleet-kvm
    30	

=== prep 标记(每台一份,guest 重建时随目录一起重建)===
-rw-r--r--. 1 udefine-dev udefine-dev 0 Nov 11 09:35 /home/udefine-dev/daedalus-fleet-kvm/vms/vmc/.prepped
-rw-r--r--. 1 udefine-dev udefine-dev 0 Nov 11 09:35 /home/udefine-dev/daedalus-fleet-kvm/vms/vmd/.prepped
-rw-r--r--. 1 udefine-dev udefine-dev 0 Nov 11 09:34 /home/udefine-dev/daedalus-fleet-kvm/vms/vmu/.prepped

=== 标记块里出现的 Host 行 ===
Host vmu
Host vmd
Host vmc
```

---

## 8. 幂等与清理的边界(都真机验过)

### 8.1 `--destroy`(不 purge):镜像 / PKI / 密钥保留

见 §3 的 destroy 输出最后三行:基础镜像保留、共享 PKI 与 ssh 密钥保留。

### 8.2 `--destroy --purge-images`:只多删基础镜像

```text
### DESTROY --purge-images

== 停机与清理
  ok   - vmu:qemu(pid 3535152)已停
  ok   - vmd:qemu(pid 3535401)已停
  ok   - vmc:qemu(pid 3535733)已停
  ok   - vmu:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmu)
  ok   - vmd:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmd)
  ok   - vmc:overlay/seed/pid/log 已删(/home/udefine-dev/daedalus-fleet-kvm/vms/vmc)
  ok   - 宿主 ~/.ssh/config 标记块已移除
  ok   - 基础镜像已删(--purge-images)
  ok   - 共享 PKI 与 ssh 密钥保留在 /home/udefine-dev/daedalus-fleet-kvm/pki、/home/udefine-dev/daedalus-fleet-kvm/keys
bash tests/fleet/provision-kvm-hosts.sh --destroy --purge-images 2>&1  0.12s user 0.03s system 3% cpu 4.795 total
EXIT=0
```

清完之后宿主上的样子(`images/` 整个消失,`pki/` `keys/` 还在,标记块已摘,端口已释放):

```text
### 宿主上的剩余目录
total 48
drwxr-xr-x. 1 udefine-dev udefine-dev    90 Nov 11 09:32 .
drwx------. 1 udefine-dev udefine-dev  1530 Nov 11 08:44 ..
drwxr-xr-x. 1 udefine-dev udefine-dev    48 Nov 11 08:45 keys
-rw-r--r--. 1 udefine-dev udefine-dev   294 Nov 11 09:29 known_hosts
-rw-r--r--. 1 udefine-dev udefine-dev   294 Nov 11 09:28 known_hosts.old
drwxr-xr-x. 1 udefine-dev udefine-dev   160 Nov 11 08:45 pki
-rwxr-xr-x. 1 udefine-dev udefine-dev 32081 Nov 11 09:27 remote.sh
drwxr-xr-x. 1 udefine-dev udefine-dev     0 Nov 11 09:32 vms

### images 目录
ls: cannot access '/home/udefine-dev/daedalus-fleet-kvm/images/': No such file or directory

### ssh config 标记块
(无标记块)

### 端口
(9501-9503/9471-9473 已无监听)
```

### 8.3 purge 之后再 `--up`:重新下载 → 重新建起(EXIT=0)

```text
### UP after purge (re-download)

== 宿主预检
  ok   - qemu-system-x86_64 / qemu-img / cloud-localds / openssl 齐备,/dev/kvm 可写

== 基础镜像(不覆盖已下载的)
  ..   - vmu 基础镜像下载中:https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img

  ok   - vmu 基础镜像就绪(/home/udefine-dev/daedalus-fleet-kvm/images/noble-server-cloudimg-amd64.img,593M)
  ..   - vmd 基础镜像下载中:https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2

  ok   - vmd 基础镜像就绪(/home/udefine-dev/daedalus-fleet-kvm/images/debian-12-genericcloud-amd64.qcow2,334M)
  ..   - vmc 基础镜像下载中:https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2

  ok   - vmc 基础镜像就绪(/home/udefine-dev/daedalus-fleet-kvm/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2,1.2G)

== guest ssh 密钥
  ok   - ssh 密钥:/home/udefine-dev/daedalus-fleet-kvm/keys/id_ed25519

== 共享 PKI(一张 CA + 每台一份 server 证书 + 一张控制面客户端证书)
  ok   - 共享 CA 复用:/home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  ok   - vmu server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  ok   - vmd server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  ok   - vmc server 证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  ok   - 控制面客户端证书复用:/home/udefine-dev/daedalus-fleet-kvm/pki/client.pem

== 宿主 ssh 配置
  ok   - 宿主 ~/.ssh/config 已写入标记块(可 ssh vmu/vmd/vmc)

== 三台 guest
  ok   - vmu:overlay 已建(12G,backing qcow2)
  ok   - vmu:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmu/seed.iso
  ok   - vmu:qemu 已启动(pid 3537293)
  ..   - vmu:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmu:ssh 已通
  ok   - vmu:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 4994aacd5705,mode 644)
  ok   - vmu:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 45599cb75ef0,mode 600)
  ok   - vmu:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmu:/etc/daedalus-agent/config.yaml 已铺设(sha256 1af09b1a8b1b,mode 600)
  ok   - vmu:apt-get update + rsync 已装
  ok   - vmu:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmu/.prepped)
  ok   - vmd:overlay 已建(12G,backing qcow2)
  ok   - vmd:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmd/seed.iso
  ok   - vmd:qemu 已启动(pid 3537553)
  ..   - vmd:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmd:ssh 已通
  ok   - vmd:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 2def8d0f3615,mode 644)
  ok   - vmd:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 00948d302627,mode 600)
  ok   - vmd:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmd:/etc/daedalus-agent/config.yaml 已铺设(sha256 1bd0d896646e,mode 600)
  ok   - vmd:apt-get update + rsync 已装
  ok   - vmd:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmd/.prepped)
  ok   - vmc:overlay 已建(12G,backing qcow2)
  ok   - vmc:NoCloud seed 已造:/home/udefine-dev/daedalus-fleet-kvm/vms/vmc/seed.iso
  ok   - vmc:qemu 已启动(pid 3537875)
  ..   - vmc:known_hosts 里的旧主机密钥已摘除(guest 被重建过)
  ok   - vmc:ssh 已通
  ok   - vmc:/etc/daedalus-agent/tls/agent.pem 已铺设(sha256 82527c9092ae,mode 644)
  ok   - vmc:/etc/daedalus-agent/tls/agent.key 已铺设(sha256 20f7b36513fe,mode 600)
  ok   - vmc:/etc/daedalus-agent/tls/ca.pem 已铺设(sha256 96d539181ed6,mode 644)
  ok   - vmc:/etc/daedalus-agent/config.yaml 已铺设(sha256 57ef3eac2bfe,mode 600)
  ok   - vmc:dnf makecache + 已确保无 rsync / btrfs(SNAPSHOT_CAPABILITY_NONE)
  ok   - vmc:prep 完成(标记 /home/udefine-dev/daedalus-fleet-kvm/vms/vmc/.prepped)

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

== 宿主上的产物
  CA:            /home/udefine-dev/daedalus-fleet-kvm/pki/ca.pem
  控制面客户端:   /home/udefine-dev/daedalus-fleet-kvm/pki/client.pem / /home/udefine-dev/daedalus-fleet-kvm/pki/client.key
  vmu server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmu.pem
  vmd server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmd.pem
  vmc server: /home/udefine-dev/daedalus-fleet-kvm/pki/vmc.pem
  guest 内已预置: /etc/daedalus-agent/config.yaml + tls/{agent.pem,agent.key,ca.pem}
bash tests/fleet/provision-kvm-hosts.sh --up 2>&1  0.10s user 0.06s system 0% cpu 2:51.43 total
EXIT=0
```

(`curl` 的进度条已剪掉,其余原文保留;总耗时 2 分 51 秒,其中下载约 40 秒。)

purge → 重建这一轮之后**再跑一次 `--up`**,仍然是 no-op(虚机复用、配置按 sha256 跳过、
prep 标记命中)——**6.8 秒**:

```text
  ok   - vmu:已在跑(pid 3537293),复用不重建
  ok   - vmu:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 4994aacd5705),跳过
  ok   - vmu:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 45599cb75ef0),跳过
  ok   - vmu:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmu:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 1af09b1a8b1b),跳过
  ok   - vmu:prep 已做过且状态正确,跳过装包
  ok   - vmd:已在跑(pid 3537553),复用不重建
  ok   - vmd:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 2def8d0f3615),跳过
  ok   - vmd:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 00948d302627),跳过
  ok   - vmd:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmd:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 1bd0d896646e),跳过
  ok   - vmd:prep 已做过且状态正确,跳过装包
  ok   - vmc:已在跑(pid 3537875),复用不重建
  ok   - vmc:/etc/daedalus-agent/tls/agent.pem 已是目标内容(sha256 82527c9092ae),跳过
  ok   - vmc:/etc/daedalus-agent/tls/agent.key 已是目标内容(sha256 20f7b36513fe),跳过
  ok   - vmc:/etc/daedalus-agent/tls/ca.pem 已是目标内容(sha256 96d539181ed6),跳过
  ok   - vmc:/etc/daedalus-agent/config.yaml 已是目标内容(sha256 57ef3eac2bfe),跳过
  ok   - vmc:prep 已做过且状态正确,跳过装包
bash tests/fleet/provision-kvm-hosts.sh --up  0.10s user 0.05s system 2% cpu 6.771 total
EXIT=0
```

这一轮之后的 `--status`(三台 `alive=yes`、ssh 可达、转发口在听):

```text

== 宿主:undef-dev(udefine-dev)

== 三台被控机
  vmu  pid=3537293  alive=yes ssh=127.0.0.1:9501(ok) agent=127.0.0.1:9471(fwd=open) guest:9443监听=no
       guest 内: e2e-u24
       -rw------- 1 root root  711 Nov 11 01:33 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:33 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:33 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:33 /etc/daedalus-agent/tls/ca.pem
  vmd  pid=3537553  alive=yes ssh=127.0.0.1:9502(ok) agent=127.0.0.1:9472(fwd=open) guest:9443监听=no
       guest 内: e2e-d12
       -rw------- 1 root root  711 Nov 11 01:34 /etc/daedalus-agent/config.yaml
       -rw------- 1 root root 1704 Nov 11 01:34 /etc/daedalus-agent/tls/agent.key
       -rw-r--r-- 1 root root 1610 Nov 11 01:34 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r-- 1 root root 1919 Nov 11 01:34 /etc/daedalus-agent/tls/ca.pem
  vmc  pid=3537875  alive=yes ssh=127.0.0.1:9503(ok) agent=127.0.0.1:9473(fwd=open) guest:9443监听=no
       guest 内: e2e-c9
       -rw-------. 1 root root  710 Nov 10 20:35 /etc/daedalus-agent/config.yaml
       -rw-------. 1 root root 1704 Nov 10 20:35 /etc/daedalus-agent/tls/agent.key
       -rw-r--r--. 1 root root 1610 Nov 10 20:35 /etc/daedalus-agent/tls/agent.pem
       -rw-r--r--. 1 root root 1919 Nov 10 20:35 /etc/daedalus-agent/tls/ca.pem

== 宿主上的产物
  total 44
  -rw-------. 1 udefine-dev udefine-dev 3268 Nov 11 08:45 ca.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1919 Nov 11 08:45 ca.pem
  -rw-r--r--. 1 udefine-dev udefine-dev   41 Nov 11 08:45 ca.srl
  -rw-------. 1 udefine-dev udefine-dev 1708 Nov 11 08:45 client.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1594 Nov 11 08:45 client.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmc.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmc.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmd.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmd.pem
  -rw-------. 1 udefine-dev udefine-dev 1704 Nov 11 08:45 vmu.key
  -rw-r--r--. 1 udefine-dev udefine-dev 1610 Nov 11 08:45 vmu.pem
  ok   - 三台 guest 全部在跑且 ssh 可达
```

### 8.4 参数边界:错的组合一律非 0 退出,且说清楚原因

```text
$ provision-kvm-hosts.sh --purge-images
  --purge-images 只在 --destroy 时有意义
  -> EXIT=2

$ provision-kvm-hosts.sh --status --purge-images
  --purge-images 只在 --destroy 时有意义
  -> EXIT=2

$ provision-kvm-hosts.sh --up --purge-images
  --purge-images 只在 --destroy 时有意义
  -> EXIT=2

$ provision-kvm-hosts.sh --host
  --host 需要一个 ssh 别名
  -> EXIT=2

$ provision-kvm-hosts.sh --bogus
  未知参数:--bogus
  -> EXIT=2

$ provision-kvm-hosts.sh -h
  #
  -> EXIT=0

```

---

## 9. 真机撞出来并修掉的坑

| # | 现象 | 根因 | 修法 | 钉子(selftest 断言) |
| --- | --- | --- | --- | --- |
| 1 | `qemu: -name vmu: invalid option` | qemu 参数被拼成一个 argv(`-name vmu` 当成单个元素) | 一行一个 argv,逐元素 exec | 「qemu 参数一行一个 argv」「`-name` 与 guest 名分属两个 argv」 |
| 2 | apt 失败却 `EXIT=0`、`.prepped` 还写了 | 函数体在 `\|\|` 列表里被调用时 POSIX 会**关掉 `set -e`**,失败被吞 | 每个可能失败的命令显式 `\|\| return 1` | 「始终失败时最终判失败(不会被静默放过)」 |
| 3 | `apt-get update` 撞 `Could not get lock` | 云镜像自带的 `apt-daily`/`unattended-upgrades`/`dnf-makecache` 与 `cloud-init status --wait` 抢锁 | 15 秒退避重试(最多 8 次)+ `DPkg::Lock::Timeout=120` | 「前两次撞锁后第 3 次成功」 |
| 4 | `REMOTE HOST IDENTIFICATION HAS CHANGED` | guest 重建 ⇒ 新主机密钥,`accept-new` 对"变化"仍然硬失败 | 识别该错误后摘掉旧 known_hosts 条目再重试 | —(真机日志:`known_hosts 里的旧主机密钥已摘除`) |
| 5 | ssh 端口与 agent 端口撞车 | 端口分配未做交叉校验 | 布局校验:两两不同 + 不跨类重复 | 「agent 端口撞 ssh 端口被拒」 |
| 6 | vmu 永远跳过 `apt-get update` | **Ubuntu 云镜像自带 rsync**,只看"现状"就会一直判"不用 prep",而索引是出厂快照 | prep 标记(成功后写)+ 现状复核;标记落在 `vms/<guest>/`,guest 重建即消失 | 「全新 guest 即使自带 rsync 也要 prep 一次」 |
| 7 | `--purge-images` 实际没删镜像 | purge 标志没传进远端 destroy | 把标志作为 destroy 的参数传下去 | —(§8.2 真机输出) |
| 8 | 本地/远端各一份 `ok/bad/step` | 两套实现会漂移,selftest 验的就不是真跑的那份 | 共享库一份,远端程序由本地拼装上传,按 sha256 跳过重传 | 「被测函数确实已定义」+ 整份 selftest |
| 9 | `--status` 汇总不打印失败行 | 局部变量叫 `bad`,把共享库的 `bad()` 函数遮掉了 | 改名 `not_ok` | —(代码审查) |

第 2、6 两条是**最危险的**:一个是"失败被当成成功",一个是"该做的事被永久跳过"。
两者现在都有 selftest 钉子,且都是真机上先看到异常输出才发现的。

---

## 10. 与 `agent-e2e.sh` 的衔接(它需要什么,这里就绪了什么)

`agent-e2e.sh` **未改动**。它跑真机矩阵需要三样东西,本脚本已经把其中两样摆好:

1. **目标机**:`ssh vmu` / `ssh vmd` / `ssh vmc` 从宿主直接可用(§7),免密 sudo 已就绪(§6),
   包索引已刷新(§3/§6 的 prep)。
2. **共享 PKI**:直接拿宿主上的 `pki/` 当控制面 mTLS 客户端凭证。
3. **agent 出货 tarball**:本脚本不负责(它只预置 config + TLS),需要自己打。

一条龙(在 **KVM 宿主上**执行,因为控制面要拨 `127.0.0.1:947x`):

```bash
# 0) 靶场(本脚本负责的部分)
tests/fleet/provision-kvm-hosts.sh --up

# 1) 控制面二进制(agent-e2e 默认找 <repo>/bin/daedalus-host)
just go-build

# 2) agent 出货 tarball(内含 bin/ 与 scripts/;布局照 scripts/install_test.sh)
cd ../daedalus-agent
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
staging="dist/staging-daedalus-agent-$version"
mkdir -p "$staging/bin" "$staging/scripts"
go build -o "$staging/bin/daedalus-agent" ./cmd/daedalus-agent
cp scripts/install.sh scripts/uninstall.sh scripts/postinstall.sh "$staging/scripts/"
tar -czf "dist/daedalus-agent-$version.tar.gz" -C dist "$(basename "$staging")"

# 3) 控制面 mTLS 凭证 = 本脚本造的共享 PKI
export DAEDALUS_FLEET_CLIENT_CERT="$HOME/daedalus-fleet-kvm/pki/client.pem"
export DAEDALUS_FLEET_CLIENT_KEY="$HOME/daedalus-fleet-kvm/pki/client.key"
export DAEDALUS_FLEET_CA="$HOME/daedalus-fleet-kvm/pki/ca.pem"
export DAEDALUS_AGENT_TARBALL="$HOME/daedalus-agent/dist/daedalus-agent-$version.tar.gz"

# 4) 矩阵(注意 --hosts 的地址是 agent 端口,不是 ssh 端口)
cd <daedalus-core>
tests/fleet/agent-e2e.sh \
    --hosts ubuntu=vmu@localhost:9471 debian12=vmd@localhost:9472 rhel9=vmc@localhost:9473
```

`--hosts` 的语法是 `key=<ssh别名>[@<可拨地址>]`:给 `地址:端口` 就原样拨,
给裸地址则补 `:9443`。这里刻意用 `localhost:<947x>` —— 控制面在宿主上,拨
`localhost` 正好命中 server 证书的 SAN `DNS:localhost`,而 `:947x` 是宿主侧的转发口。

---

## 11. 复现命令一览

```bash
# 本机(不需要 KVM / 不需要网络)
bash tests/fleet/provision-kvm-hosts.sh --selftest

# 真机
bash tests/fleet/provision-kvm-hosts.sh --up                 # 幂等拉起
bash tests/fleet/provision-kvm-hosts.sh --status             # 看状态
bash tests/fleet/provision-kvm-hosts.sh --destroy            # 停 + 清 vms/,保留镜像/PKI
bash tests/fleet/provision-kvm-hosts.sh --destroy --purge-images   # 连基础镜像一起删

# 换宿主 / 换工作目录名
DAEDALUS_KVM_HOST=<ssh别名> DAEDALUS_KVM_DIR=<目录名> bash tests/fleet/provision-kvm-hosts.sh --up
```
