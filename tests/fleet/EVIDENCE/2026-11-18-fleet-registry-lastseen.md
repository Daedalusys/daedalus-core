# 节点注册表 last_seen 与整表写解耦 —— 200 台规模证据(core#165,2026-11-18)

> **状态**:已执行。5 条 gate 全绿(命令与退出码见 §1),新旧对照墙时见 §3。
> **驱动**:`internal/fleet/{registry,seen,scheduler}.go` 三处改动 + 新增
> `tests/integration/fleet_scale_test.go::TestFleetScale_MarkSeenDoesNotRewriteRegistry`。
> **控制面**:`daedalus-core` `p2/registry-lastseen`(基线 `origin/main` = `7ca4d2c`)。
> **被控端**:**假 agent** —— 进程内 200 个 loopback 监听 + 真双向 TLS 的 gRPC 桩
> (与 `2026-11-16-fleet-scale-200.md` 同一套夹具,一条未改)。
> **策略**:`policy.Default()`;批策略 `count` size=50。
> **环境**:8 × Intel i7-3632QM(2.20GHz,2012 年的移动 CPU)。这台机器的
> `encoding/json` 实测 **8.3 µs/次 decode**(单条 Node 行),比现代核慢一个量级 ——
> 下面所有绝对秒数都要按这个口径读(§5 说明了它为什么重要)。
> **TMPDIR**:每个数字都标了口径;`t.TempDir()` 落在 `TMPDIR` 下,`/tmp` 的 fsync
> ≈ 0.7 ms,`/mnt/cache/tmp` ≈ 5 ms。两个口径都测了(§3)。

## 1. Gate 命令原文与退出码

```bash
$ go test ./internal/fleet -count=1 -race
ok  	github.com/Daedalusys/daedalus-core/internal/fleet	15.642s
# EXIT=0

$ go test ./cmd/daedalus-host -count=1
ok  	github.com/Daedalusys/daedalus-core/cmd/daedalus-host	0.476s
# EXIT=0

$ go test ./tests/integration -count=1 -short
ok  	github.com/Daedalusys/daedalus-core/tests/integration	0.183s
# EXIT=0

$ go test ./tests/integration -run 'TestFleetScale_MarkSeenDoesNotRewriteRegistry' -count=1 -v
=== RUN   TestFleetScale_MarkSeenDoesNotRewriteRegistry
    fleet_scale_test.go:439: LASTSEEN 200 节点:nodes.jsonl sha256=c4bd1b12a4fd168f7a531684dc6d5b6c3418ae46f4ca0a779a4359f8e35a8748(未变), mtime=2026-11-18 16:06:40.860759067 +0800 CST(未变), seen.log 400 行
--- PASS: TestFleetScale_MarkSeenDoesNotRewriteRegistry (5.20s)
PASS
ok  	github.com/Daedalusys/daedalus-core/tests/integration	5.210s
# EXIT=0

$ go vet ./internal/fleet
# EXIT=0(无输出)
```

> 四条 `go test` 与 `go vet` 的 `TMPDIR=/tmp`。`nodes.jsonl` 的 sha256 每次跑都不同
> (假 agent 的监听端口与 `registered_at_unix` 每次都变),所以**断言的是"前后相等"**,
> 不是某个固定值。

## 2. 结构判据:`nodes.jsonl` 一次都没被重写

`TestFleetScale_MarkSeenDoesNotRewriteRegistry`:200 台、1 条低危 `package.set`、
count size=50(4 批)。一次 Run 里 `markSeen` 被调 **400 次**(每台下发前后各一次),
改前是 400 次「整表读 + 整表写 + fsync」,改后是 400 次纯追加。

| 断言 | 改前(`origin/main`) | 改后(本分支) |
| --- | --- | --- |
| `nodes.jsonl` 内容 sha256 前后一致 | ❌ 变了 | ✅ 未变 |
| `nodes.jsonl` mtime 前后一致 | ❌ 变了 | ✅ 未变 |
| `seen.log` 行数 ≥ 200 | 文件不存在(0) | ✅ **400 行**(= 2 × 200) |
| `status=complete` + 200 条 `success` + 每台 1 次 `ApplyIntent` | ✅(行为本来就没问题) | ✅ |

改前那一跑(独立 detached worktree,把本分支的测试文件拷过去跑,不污染树):

```bash
$ git worktree add -q --detach /mnt/cache/tmp/opencode/scale-before origin/main
$ # 把 p2/registry-lastseen 的 tests/integration/fleet_scale_test.go 拷进该 worktree(仅测量夹具)
$ TMPDIR=/tmp go test ./tests/integration -run 'TestFleetScale_MarkSeenDoesNotRewriteRegistry' -count=1 -v
    fleet_scale_test.go:410: nodes.jsonl 被重写:内容 sha256 2af156d996f5810454ca060c8131e78af36ad172dcc7985b4991255d8ae89aba → 392540be62ab0b94bee077a60b8d7dd6dfb02bf700c2395c73d1765688cc619b
--- FAIL: TestFleetScale_MarkSeenDoesNotRewriteRegistry (6.96s)
FAIL	github.com/Daedalusys/daedalus-core/tests/integration	6.969s
# EXIT=1(这是"改前必红"的证据)
```

## 3. 墙时:改前 / 改后

协议(两边逐字同一条命令,同一个 `TMPDIR` 口径;改前用独立 detached worktree,
**不用 `git stash`**):

```bash
# 改后(本分支)
$ TMPDIR=/tmp go test ./tests/integration -run 'TestFleetScale_200Nodes_AllSucceed' -count=1 -v
    fleet_scale_test.go:102: SCALE 200 节点:耗时 4.618s / 4.194s / 3.889s,返回 JSON 47637 字节,批次 4,结果 200 条

# 改前(origin/main @ 7ca4d2c)
$ git worktree add -q --detach /mnt/cache/tmp/opencode/scale-before origin/main
$ cd /mnt/cache/tmp/opencode/scale-before   # go.work 用绝对路径(本地测量夹具,不入库)
$ TMPDIR=/tmp go test ./tests/integration -run 'TestFleetScale_200Nodes_AllSucceed' -count=1 -v
    fleet_scale_test.go:101: SCALE 200 节点:耗时 6.413s / 5.448s / 5.999s,返回 JSON 47637 字节,批次 4,结果 200 条
```

| 口径 | 改前(3 跑) | 改后(3 跑) | 中位数变化 |
| --- | --- | --- | --- |
| `TMPDIR=/tmp`(fsync ≈ 0.7 ms) | 6.413 / 5.448 / 5.999(**5.999**) | 4.618 / 4.194 / 3.889(**4.194**) | **−30%** |
| `TMPDIR=/mnt/cache/tmp`(fsync ≈ 5 ms) | 6.941 / 7.862 / 6.067(**6.941**) | 4.417 / 4.250 / 4.142(**4.250**) | **−39%** |

> 历史基线(2026-11-16 证据)是 **5.512 s** —— 与本次改前实测同量级,说明改前那棵树
> 复现了历史行为。这台机器抖动大(单跑极差可达 ±1 s),所以给的是 3 跑原始值与中位数,
> 不给"最好一次"。

**剩下的 4.2 s 花在哪**(本机口径,`TMPDIR=/tmp`):

| 成分 | 量 | 依据 |
| --- | --- | --- |
| 注册表侧(400 次 `MarkSeen`,每次读一遍 200 行身份表) | **0.9–1.9 s** | `go test ./internal/fleet -run 'TestScheduler_RunReadsRegistryOnce/Run_不写身份表' -count=1 -v` → `200 节点 1 条 intent:Run 耗时 927ms / 1.867s / 1.536s(fake dispatch)` |
| 网络侧(200 次串行 mTLS 建连 + 200 次心跳预检 + 200 次 ApplyIntent) | **≈ 2.5–3 s** | 上面两个数相减;未被本次改动触碰 |

## 4. 残余风险(**本次不解决**,如实记)

1. **`MarkSeen` 仍会读一遍身份表** —— 只用做“未注册节点必须报错”的 fail-fast 校验
   (`Registry.MarkSeen` → `loadBase`)。这是 **O(n) 纯读、无 fsync**,不再整表重写。
   但它仍然是**二次**的:一次 Plan 有 2n 次 `MarkSeen`,每次都要重放 n 行身份表。
   在本分支用一次性夹具独立复测三档(`TMPDIR=/tmp`,同一台机器):

   | 节点数 n | 2n 次 `MarkSeen` 合计 | 每次 |
   | --- | --- | --- |
   | 200 | 0.93 s | 2.32 ms |
   | 400 | 4.81 s | 6.02 ms |
   | 800 | 18.37 s | 11.48 ms |

   单次耗时随 n 近似线性(2.32 → 6.02 → 11.48 ms),合计随 n **二次**(0.93 → 4.81 → 18.37 s)。
   200 台时它占改后一次 Run(4.19 s)的 **22%**,尚可接受;400 台在本机口径下已经吃掉 4.8 s。
   **这是本次刻意留下的**:计划只把“整表写 + fsync”挪走,纯读路径没动。
2. **`List()` / `Get()` 仍整表读**(每次调用都重放整个 `nodes.jsonl`,再折叠 `seen.log`);
   `node list`、`Scheduler.Run` 的取数各读一次,已从"每台机一次"收敛成"每批一次"。
3. **再往上的触发器**:按上面的实测曲线,**单批 ≈ 400 台**(本机口径)就足以让 `MarkSeen`
   的纯读吃掉 4.8 s,800 台 18.4 s;更快的 CPU 按比例下移,但量纲仍是二次。另一条触发线是
   注册表落在**网络盘 / 慢盘**(`TMPDIR=/mnt/cache/tmp` 那一列已看到 fsync 成本消掉后收益
   放大到 −39%)。到那时才需要内存索引 / write-behind —— 那必须重新回答“崩溃时丢什么”,
   并重新定义多写者语义;本次刻意不做。
   还有一条更便宜的候选:`runOne` 拿到的节点来自 `Run` **已经校验过**的注册表查询,
   `markSeen` 的“未注册”校验在热路径上是重复的 —— 一个内部 unchecked 变体就能把 2n 次
   整表读降到 0,同时保留 API 边界上的 fail-fast。
4. **`seen.log` 的 durability 是刻意降级的**:追加不 fsync,崩溃只丢最近的 last_seen
   增量,节点身份(`nodes.jsonl`,临时文件 → fsync → rename)不受影响。不要"顺手补个 Sync":
   那等于把每次下发都拖回等盘的老路,也是本次要消掉的东西。
5. **`seen.log` 的写者假定是单控制面进程**(与 `nodes.jsonl` 同口径)。`O_APPEND` 单行
   写让并发追加不至于写坏行,但 `Put`/`Delete` 的"折叠 + 清空"与并发 `MarkSeen` 之间
   有一个窗口:清空可能吃掉刚落的一条观测(只影响 last_seen 的新鲜度,不影响身份)。
   多控制面 HA 不在本次范围(#165 之外)。

## 5. 未覆盖(如实记)

- **没有 200 台真机**:与 #156 同口径,假 agent 足够量控制面规模行为。
- **没有测 1000 台**:只测了 200 / 400 / 800 三档(§4.1 的表);1000 台按二次曲线外推。
- **没有测"压实"在生产规模下的时机**:`seen.log` 的压实阈值是
  `max(4 × 节点数, 1024)`(200 台时 = 1024 行,一次 Run 400 行 → 不触发);
  压实行为由 `internal/fleet/seen_test.go::TestSeen_Compaction*` 在单测里钉住,
  200 台规模下未观察到压实发生。
- **没测并发写**:单进程串行下发,与现状一致。

## 6. 与计划的偏差

| 计划写的 | 实际 | 原因 |
| --- | --- | --- |
| Task 1 Step 5 的 `git add` 只列 `internal/fleet/{seen.go,seen_test.go,registry.go}` | 同一 commit 里还加了 `cmd/daedalus-host/node_test.go` | Task 1 Step 1 明确要求那条跨包用例(Review Focus #6),漏列是笔误 |
| 只给了 `seenCompactFactor = 4` 与 `max(4×节点数, 1024)` | 另加了 `seenCompactFloor = 1024`、`seenCompactThreshold(nodes)`、`countSeenLines()` | 阈值口径需要"节点数"这个入参;`countSeenLines` 只数换行不解析 JSON(热路径上不必为判定压实而反序列化整个侧车) |
| `Put`/`Delete` = … → `store()` → `resetSeen()`(未说错误处理) | `resetSeen` 的错误照常返回 | 身份写已提交但侧车清不掉,是必须让调用方知道的文件系统故障(残留只会让 last_seen 变旧,不会复活节点) |
| 计划说"200 台下 ≈90 ms/次 Run" | 本机 fake-dispatch 实测 0.9–1.9 s | 8.3 µs/decode × 8 万次;现代核上就是 ~90 ms。见 §4.1 |
| Task 2 的测试只钉"文案 + 不写身份表" | 额外在测试里打印 Run 墙时 | 让 §4 的残余风险有一个可复算的命令,而不是转述 |
