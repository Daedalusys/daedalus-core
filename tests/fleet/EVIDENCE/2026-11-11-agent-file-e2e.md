# agent `file` 类 intent 真机验收证据 — ext4 / xfs(2026-11-11)

> 执行入口:`tests/fleet/agent-file-e2e.sh`(本轮新增;**未改** `agent-e2e.sh`)。
> **判定独立于被验对象**:`plan submit` 的 `status` / `result` / `tx_id` 一律不作判据,
> 只采信 ssh 到目标机上读出来的文件事实(存在性 / 类型 / mode / sha256 / `cat -A` 原文)。
> 本文件每一段都是**真实输出的原文粘贴**。

- **执行日期**:2026-11-11
- **执行人**:orca worker(`core#44` Task 2:file 类真机验收)
- **控制面**:`daedalus-core` @ `a634896` + 本轮新增的 `tests/fleet/agent-file-e2e.sh`
- **被控端**:`daedalus-agent` @ `6bee8f5`(出货包 `daedalus-agent-6bee8f5.tar.gz`)
- **靶场**:`Masterwet_Server` 上常驻的三台 KVM guest(与 `2026-11-11-agent-e2e.md` 同一批)
- **为什么单独一个驱动**:spec §7 要求 file intent 在 **ext4 与 xfs** 上各跑一次;
  `agent-e2e.sh` 只跑 package,而 file 类的判据(内容逐字节 / mode)与 package 类
  ("包管理器说装上了")完全不同,不应混在一条判定里。

---

## 0. 结论速览

| 腿 | 文件系统 | 目标 | 期望 | 结果 |
| --- | --- | --- | --- | --- |
| ext4 低危 | ext4(`vmu`) | `/tmp/daedalus-e2e-file.conf` | 存在 / 内容逐字相符 / mode 0644 | **PASS**:`644 13 regular file`,`e2e-file-vmu$`,sha256 `4bac3ef4…` |
| xfs 低危 | xfs(`vmc`) | `/tmp/daedalus-e2e-file.conf` | 同上 | **PASS**:`644 13 regular file`,`e2e-file-vmc$`,sha256 `68bd26d1…` |
| 高危路径 | ext4(`vmu`) | `/etc/daedalus-e2e.conf` | **应当放行**(file 有内建单文件快照),存在 + 内容相符 | **PASS**:`644 13 regular file`,`e2e-file-etc$`,sha256 `3d6ebc2f…` |
| 收尾 | — | 三台测试文件 | 删干净 | **PASS**:6 条 `rc=0`,事后 `stat` 均 `No such file or directory` |
| 汇总 | — | — | — | **`agent-file-e2e: 14 通过 / 0 失败`**,退出码 `0` |

---

## 1. 命令与退出码

```console
# 1) 驱动同步到 KVM 宿主(仓里新增的文件;agent tarball 与 daedalus-host 已按另一份证据同步)
$ rsync -az tests/fleet/agent-file-e2e.sh Masterwet_Server:~/daedalus-e2e/core/tests/fleet/
rsync rc=0

# 2) 真机矩阵(地址取 agent 转发口 9471-9473,不是 ssh 口;localhost 命中证书 SAN)
$ ssh Masterwet_Server 'cd ~/daedalus-e2e/core && \
    DAEDALUS_FLEET_CLIENT_CERT=$HOME/daedalus-fleet-kvm/pki/client.pem \
    DAEDALUS_FLEET_CLIENT_KEY=$HOME/daedalus-fleet-kvm/pki/client.key \
    DAEDALUS_FLEET_CA=$HOME/daedalus-fleet-kvm/pki/ca.pem \
    DAEDALUS_FLEET_DIR=$HOME/daedalus-e2e/fleet-file \
    bash tests/fleet/agent-file-e2e.sh --hosts \
      ubuntu=vmu@localhost:9471 debian12=vmd@localhost:9472 rhel9=vmc@localhost:9473'
FILE_E2E_EXIT=0
```

退出码 `0`。下文 §2 是这一次运行的 stdout 全文。

---

## 2. 真机矩阵输出(全文原文)

```text
fleet 根目录:/home/udefine-dev/daedalus-e2e/fleet-file

== 被控机基础信息(目标机自己的 /etc/os-release 与根文件系统类型)
  ok   - ubuntu(vmu): ID=ubuntu VERSION_ID=24.04 根文件系统=ext4
  ok   - debian12(vmd): ID=debian VERSION_ID=12 根文件系统=ext4
  ok   - rhel9(vmc): ID=centos VERSION_ID=9 根文件系统=xfs

== 登记被控机(daedalus-host node add → 拿 node_id)
  ok   - ubuntu: node add localhost:9471(agent_id=e2e-u24) → node_id=e2e-u24-17d49a73 support_level=supported
    node add: {"node":{"node_id":"e2e-u24-17d49a73","agent_id":"e2e-u24","kind":"agent","addr":"localhost:9471","distro":"ubuntu","distro_version":"24.04","kernel":"","arch":"","hostname":"vmu","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794366191,"last_seen_unix":1794366191}}
  ok   - debian12: node add localhost:9472(agent_id=e2e-d12) → node_id=e2e-d12-555983eb support_level=supported
    node add: {"node":{"node_id":"e2e-d12-555983eb","agent_id":"e2e-d12","kind":"agent","addr":"localhost:9472","distro":"debian","distro_version":"12","kernel":"","arch":"","hostname":"vmd","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"supported","cert_fingerprint":"","registered_at_unix":1794366191,"last_seen_unix":1794366191}}
  ok   - rhel9: node add localhost:9473(agent_id=e2e-c9) → node_id=e2e-c9-9b93893c support_level=experimental
    node add: {"node":{"node_id":"e2e-c9-9b93893c","agent_id":"e2e-c9","kind":"agent","addr":"localhost:9473","distro":"centos","distro_version":"9","kernel":"","arch":"","hostname":"vmc","agent_version":"","snapshot_capability":"SNAPSHOT_CAPABILITY_NONE","support_level":"experimental","cert_fingerprint":"","registered_at_unix":1794366191,"last_seen_unix":1794366191}}

== 选腿:ext4 与 xfs 各一条低危腿
  [腿] ext4-ubuntu: target=/tmp/daedalus-e2e-file.conf content=  e   2   e   -   f   i   l   e   -   v   m   u  \n
    [基线] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      stat: cannot statx '/tmp/daedalus-e2e-file.conf': No such file or directory
    [基线] 不存在
    [计划] {
  "id": "agent-file-e2e-ext4-ubuntu",
  "intents": [
    {
      "kind": "file",
      "target": "/tmp/daedalus-e2e-file.conf",
      "spec": {
        "content": "e2e-file-vmu\n"
      },
      "expected_diff": "",
      "risk_class": "low"
    }
  ],
  "machines": [
    {
      "machine_id": "e2e-u24-17d49a73",
      "kind": "agent",
      "support_level": "supported"
    }
  ],
  "batching": {
    "strategy": "count",
    "size": 1
  },
  "blast_radius": {
    "count": 1,
    "kind_breakdown": {
      "file": 1
    },
    "max_downtime_ns": 30000000000,
    "max_risk": "low"
  }
}
    [控制面自报(exit=0,不作判据)] {"plan_id":"agent-file-e2e-ext4-ubuntu","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-u24-17d49a73"]}],"results":[{"node_id":"e2e-u24-17d49a73","intent_id":"agent-file-e2e-ext4-ubuntu/e2e-u24-17d49a73/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-agent-file-e2e-ext4-ubuntu/e2e-u24-17d49a73/0","message":""}],"recovery":[]}
    [独立核实] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      644 13 regular file
    [独立核实] $ sudo sha256sum /tmp/daedalus-e2e-file.conf
      4bac3ef40a559b8723775de0750f6bcb2361063b7b55c26a63d651f63af572fc
    [独立核实] $ sudo cat -A /tmp/daedalus-e2e-file.conf
      e2e-file-vmu$
  ok   - ext4-ubuntu: 目标机上 /tmp/daedalus-e2e-file.conf 存在且是普通文件
  ok   - ext4-ubuntu: 目标机上 /tmp/daedalus-e2e-file.conf 内容逐字节相符(sha256 4bac3ef40a559b8723775de0750f6bcb2361063b7b55c26a63d651f63af572fc)
  ok   - ext4-ubuntu: 目标机上 /tmp/daedalus-e2e-file.conf mode=644
  [腿] xfs-rhel9: target=/tmp/daedalus-e2e-file.conf content=  e   2   e   -   f   i   l   e   -   v   m   c  \n
    [基线] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      stat: cannot statx '/tmp/daedalus-e2e-file.conf': No such file or directory
    [基线] 不存在
    [计划] {
  "id": "agent-file-e2e-xfs-rhel9",
  "intents": [
    {
      "kind": "file",
      "target": "/tmp/daedalus-e2e-file.conf",
      "spec": {
        "content": "e2e-file-vmc\n"
      },
      "expected_diff": "",
      "risk_class": "low"
    }
  ],
  "machines": [
    {
      "machine_id": "e2e-c9-9b93893c",
      "kind": "agent",
      "support_level": "experimental"
    }
  ],
  "batching": {
    "strategy": "count",
    "size": 1
  },
  "blast_radius": {
    "count": 1,
    "kind_breakdown": {
      "file": 1
    },
    "max_downtime_ns": 30000000000,
    "max_risk": "low"
  }
}
    [控制面自报(exit=0,不作判据)] {"plan_id":"agent-file-e2e-xfs-rhel9","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-c9-9b93893c"]}],"results":[{"node_id":"e2e-c9-9b93893c","intent_id":"agent-file-e2e-xfs-rhel9/e2e-c9-9b93893c/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-agent-file-e2e-xfs-rhel9/e2e-c9-9b93893c/0","message":""}],"recovery":[]}
    [独立核实] $ sudo stat -c '%a %s %F' /tmp/daedalus-e2e-file.conf
      644 13 regular file
    [独立核实] $ sudo sha256sum /tmp/daedalus-e2e-file.conf
      68bd26d1023eb8ffb150f9a7fb221e8cc95378e3d1667d1dff8f2958b9498f30
    [独立核实] $ sudo cat -A /tmp/daedalus-e2e-file.conf
      e2e-file-vmc$
  ok   - xfs-rhel9: 目标机上 /tmp/daedalus-e2e-file.conf 存在且是普通文件
  ok   - xfs-rhel9: 目标机上 /tmp/daedalus-e2e-file.conf 内容逐字节相符(sha256 68bd26d1023eb8ffb150f9a7fb221e8cc95378e3d1667d1dff8f2958b9498f30)
  ok   - xfs-rhel9: 目标机上 /tmp/daedalus-e2e-file.conf mode=644

== 高危路径腿:/etc/daedalus-e2e.conf(file 在 agent 节点上有内建单文件快照,应放行)
  [腿] etc-ubuntu: target=/etc/daedalus-e2e.conf content=  e   2   e   -   f   i   l   e   -   e   t   c  \n
    [基线] $ sudo stat -c '%a %s %F' /etc/daedalus-e2e.conf
      stat: cannot statx '/etc/daedalus-e2e.conf': No such file or directory
    [基线] 不存在
    [计划] {
  "id": "agent-file-e2e-etc-ubuntu",
  "intents": [
    {
      "kind": "file",
      "target": "/etc/daedalus-e2e.conf",
      "spec": {
        "content": "e2e-file-etc\n"
      },
      "expected_diff": "",
      "risk_class": "low"
    }
  ],
  "machines": [
    {
      "machine_id": "e2e-u24-17d49a73",
      "kind": "agent",
      "support_level": "supported"
    }
  ],
  "batching": {
    "strategy": "count",
    "size": 1
  },
  "blast_radius": {
    "count": 1,
    "kind_breakdown": {
      "file": 1
    },
    "max_downtime_ns": 30000000000,
    "max_risk": "low"
  }
}
    [控制面自报(exit=0,不作判据)] {"plan_id":"agent-file-e2e-etc-ubuntu","status":"complete","max_risk":"low","batches":[{"index":0,"node_ids":["e2e-u24-17d49a73"]}],"results":[{"node_id":"e2e-u24-17d49a73","intent_id":"agent-file-e2e-etc-ubuntu/e2e-u24-17d49a73/0","node_kind":"agent","intent_kind":"file","outcome":"success","result":"success","tx_id":"tx-agent-file-e2e-etc-ubuntu/e2e-u24-17d49a73/0","message":""}],"recovery":[]}
    [独立核实] $ sudo stat -c '%a %s %F' /etc/daedalus-e2e.conf
      644 13 regular file
    [独立核实] $ sudo sha256sum /etc/daedalus-e2e.conf
      3d6ebc2fe683575b569cb76623e9157c1371c804c33293f758a6b3076072eeab
    [独立核实] $ sudo cat -A /etc/daedalus-e2e.conf
      e2e-file-etc$
  ok   - etc-ubuntu: 目标机上 /etc/daedalus-e2e.conf 存在且是普通文件
  ok   - etc-ubuntu: 目标机上 /etc/daedalus-e2e.conf 内容逐字节相符(sha256 3d6ebc2fe683575b569cb76623e9157c1371c804c33293f758a6b3076072eeab)
  info - etc-ubuntu: /etc/daedalus-e2e.conf mode=644(本腿只判存在与内容)

== 收尾:删掉本驱动写下的文件(高危路径那条必须删)
  ubuntu: $ sudo rm -f /etc/daedalus-e2e.conf → rc=0
  ubuntu: $ sudo rm -f /tmp/daedalus-e2e-file.conf → rc=0
  debian12: $ sudo rm -f /etc/daedalus-e2e.conf → rc=0
  debian12: $ sudo rm -f /tmp/daedalus-e2e-file.conf → rc=0
  rhel9: $ sudo rm -f /etc/daedalus-e2e.conf → rc=0
  rhel9: $ sudo rm -f /tmp/daedalus-e2e-file.conf → rc=0

agent-file-e2e: 14 通过 / 0 失败
```

**矩阵最后一行(原文)**:`agent-file-e2e: 14 通过 / 0 失败`,退出码 `0`。

> 注:`cat -A` 的 `$` 是行尾标记 ⇒ 文件内容恰好是 `<字符串>` + **一个** `\n`,13 字节,
> 与计划里 `"content": "e2e-file-vmu\n"` 逐字节一致(sha256 是更强的说法)。

---

## 3. 目标机侧旁证(不是判据,是解释"为什么能放行")

### 3.1 单文件快照真的留了

`file` 类的回滚点是 agent 内建的纯 Go copy 后端,快照落在 `data_dir/snapshots/`。
本轮三条腿的目标文件在 apply **之前都不存在**,所以快照是 `absent` 标记(23/28 字节):

```text
===== vmu: 快照目录(file 类的单文件回滚点)
total 12
drwx------ 2 root root 4096 Nov 11 03:03 .
drwxr-xr-x 6 root root 4096 Nov 11 03:03 ..
-rw------- 1 root root   23 Nov 11 03:03 0.snap.absent

===== vmc: 快照目录
total 4
drwxr-xr-x. 2 root root 27 Nov 10 22:03 .
drwxr-xr-x. 2 root root 62 Nov 10 22:03 ..
-rw------- 1 root root   28 Nov 10 22:03 0.snap.absent
```

### 3.2 agent 审计链(哈希链)记了这三条 apply

```text
$ ssh vmu "sudo tail -2 /var/lib/daedalus-agent/audit/chain.jsonl"
{"args": {"kind": "file", "target": "/tmp/daedalus-e2e-file.conf", "tx_id": "tx-agent-file-e2e-ext4-ubuntu/e2e-u24-17d49a73/0"}, "entry_hash": "c551e078e915b789caa517ae461e8d20fdcbaa144230e8910ccbc7e60ed3cc09", "identity": "agent-file-e2e-ext4-ubuntu/e2e-u24-17d49a73/0", "outcome": "ok", "policy_version": "1.0", "prev_hash": "e80638640b684033f65ff74f3e4de2704807fb03935e0c3a77dd95ca38f97e42", "timestamp": "2026-11-11T03:03:11.462437+00:00", "tool": "apply_intent.success"}
{"args": {"kind": "file", "target": "/etc/daedalus-e2e.conf", "tx_id": "tx-agent-file-e2e-etc-ubuntu/e2e-u24-17d49a73/0"}, "entry_hash": "9abc2ced6cbf5dfd0e3e8aa9042e6da35b92be96601d977ee24a44159365ddc9", "identity": "agent-file-e2e-etc-ubuntu/e2e-u24-17d49a73/0", "outcome": "ok", "policy_version": "1.0", "prev_hash": "c551e078e915b789caa517ae461e8d20fdcbaa144230e8910ccbc7e60ed3cc09", "timestamp": "2026-11-11T03:03:12.554711+00:00", "tool": "apply_intent.success"}

$ ssh vmc "sudo tail -1 /var/lib/daedalus-agent/audit/chain.jsonl"
{"args": {"kind": "file", "target": "/tmp/daedalus-e2e-file.conf", "tx_id": "tx-agent-file-e2e-xfs-rhel9/e2e-c9-9b93893c/0"}, "entry_hash": "feb3a322b6ffae390816869a0442d8ba855e34ff4ad225de3e3afce1a2df7ddd", "identity": "agent-file-e2e-xfs-rhel9/e2e-c9-9b93893c/0", "outcome": "ok", "policy_version": "1.0", "prev_hash": "3120962fe9e59e282c5953b942e5d05ce4679d5c7d2a45279c23fbe0300abb9f", "timestamp": "2026-11-11T03:03:12.042328+00:00", "tool": "apply_intent.success"}
```

### 3.3 高危路径为什么放行(代码路径,不是运行时日志)

`/etc/daedalus-e2e.conf` 这条腿在计划里声明 `risk_class: "low"`,但控制面的
`effectiveRisk` 会取"声明 vs 判定表"更严的一侧:

- `internal/fleet/risk.go` 的 `file.set` 分支按 `high_risk_path_prefixes` 判,
  `policy.toml [fleet] high_risk_path_prefixes = ["/etc", "/boot", "/usr", "/var/lib"]`
  ⇒ `/etc/daedalus-e2e.conf` **运行时是 high**(`internal/fleet/risk_test.go`
  的 `TestClassifyIntent_FileKindUsesPathTable` 把这条映射钉住)。
- 高危闸门 `runOne` 只在"高危 **且** 该 kind 在该节点上没有回滚点"时拒;
  `intentHasRollbackPoint(node{kind:agent}, KindFile)` 走 `objectmodel.HasRollbackPoint(file)`
  ⇒ **有**,故照发(`daedalus-core@a634896` 就是修这个)。
- agent 侧 `apply_intent.go` 同样按 kind 判:`PointFor("file") = PointFile` ⇒ 高危也不拒。

故这条腿的期望是**放行并真写**,实测也是这样(§2)。`plan submit` 的 `max_risk` 显示 `low`
是因为它是**声明值**(`Plan.MaxRisk()` 只看 `intents[].risk_class`),不是运行时判定值 ——
判定值目前不进 stdout 契约,也不进审计 args。

---

## 4. 收尾复核(测试文件确实删干净了)

```console
$ ssh vmu "sudo stat -c '%a %s' /etc/daedalus-e2e.conf 2>&1 || true"
stat: cannot statx '/etc/daedalus-e2e.conf': No such file or directory
$ ssh vmu "sudo stat -c '%a %s' /tmp/daedalus-e2e-file.conf 2>&1 || true"
stat: cannot statx '/tmp/daedalus-e2e-file.conf': No such file or directory
$ ssh vmc "sudo stat -c '%a %s' /tmp/daedalus-e2e-file.conf 2>&1 || true"
stat: cannot statx '/tmp/daedalus-e2e-file.conf': No such file or directory
```

---

## 5. 本轮踩到并修掉的驱动缺陷(都是**本驱动**的,不是产品的)

### 5.1 `set -e` + 探"文件不存在"⇒ 整轮静默中断

第一版把"基线探针"写成 `stat_out="$(remote_file_stat ...)"`,而目标文件**本来就不存在**,
`stat` 退出 1 ⇒ `set -e` 直接终止。更糟的是 `trap ... ERR` **没有打印任何线索** ——
bash 的 ERR trap 默认**不进入函数体**,要在函数里的失败点上看到线索必须 `set -E`(errtrace)。

- 现象:日志停在 `[腿] ext4-ubuntu: ...` 这一行,退出码 1,没有任何后续输出
  (`bash -x` 定位到 `+ stat_out='stat: cannot statx ...'` 之后即断)。
- 处置:(1) `set -E`;(2) 探针函数末尾 `|| true` —— "文件不存在"是本驱动的**正常**步骤,
  不该按意外失败处理。
- 这一条在真机上才暴露;本地 `bash -n` 与空注册表预检都过不了它。

### 5.2 命令替换会吃掉尾随换行

第一版内容用 `"$(printf 'e2e-file-%s\n' "$alias")"` 生成 —— 命令替换**剥掉尾随换行**,
期望内容会变成 `e2e-file-vmu`(12 字节),而计划里写的是 13 字节的 `e2e-file-vmu\n`,
两边判据会互相打架。

- 处置:改用 `$'e2e-file-'"${alias}"$'\n'` 直接赋值(赋值不剥尾随换行),并用
  `od -c` 把内容形状打进日志(`[腿] ... content= e 2 e - ... \n`)。

### 5.3 `stat -c '%F'` 的类型字段是两个词

`regular file` 被空格拆成 `$3` / `$4`,用 `$3 == "regular file"` 判会永远不相等(空文件还是
`regular empty file`)。处置:改用正则 `^[0-7]+[[:space:]]+[0-9]+[[:space:]]+regular`。

---

## 6. 与期望不符之处 / 未验证项

- **高危路径腿的"运行时判定为 high"没有一行运行时输出作证**:控制面的 `effectiveRisk`
  结果不进 `plan submit` 的 stdout,也不进 agent 审计 args。本文件的依据是
  `policy.toml` 的 `high_risk_path_prefixes` + `internal/fleet/risk_test.go` 的钉死映射 +
  `runOne` 的闸门代码路径(§3.3),不是运行时日志。要拿到运行时证据需要产品侧新增输出,
  本轮不越界改产品。
- **`file` 类的回滚本身(把快照拷回原路径)未演练**:本轮只验了"放行 + 真写 + 快照点存在",
  没有制造一个 dirty Plan 去走人工恢复。agent 侧的回滚由 `internal/tx` 单测覆盖。
- **只跑了一次**:三条腿各跑一轮,重复投递幂等(同一份内容再写一次)未单独跑。
- **目标文件都选在"原本不存在"的路径上**:覆盖已存在文件时的 `mode 保留` 语义
  (agent 的 `targetWriteMode` 对已存在文件保留原 mode)本轮未在真机验证。
