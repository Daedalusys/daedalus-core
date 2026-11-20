# MarkSeen 热路径去掉整表读 —— 400 台证据(core#184,2026-11-20)

> **状态**:已执行。门禁 6 条全绿(命令与退出码见 §1),改前/改后对照见 §2。
> **驱动**:`internal/fleet/{registry,scheduler}.go` 两处改动 + 新增
> `seen_hotpath_test.go`(3 例)/ `seen_bench_test.go`(3 基准)。
> **控制面**:`daedalus-core` `perf/fleet-markseen-hotpath`(基线 `origin/main` = `b7e413b`)。
> **环境**:8 × Intel i7-3632QM(2.20GHz,2012 年移动 CPU),`encoding/json` 约 8.3 µs/次 decode。
> **TMPDIR**:`/mnt/cache/tmp`(fsync ≈ 5 ms),两侧口径一致。

## 1. Gate 命令原文与退出码

```bash
$ gofmt -l internal/fleet/
# EXIT=0(无输出)

$ go build ./...
# EXIT=0

$ go vet ./internal/fleet/
# EXIT=0(无输出)

$ go test ./internal/fleet/ -count=1
ok  	github.com/Daedalusys/daedalus-core/internal/fleet	2.520s
# EXIT=0

$ go test ./... -count=1
# EXIT=0(全部 ok;tests/integration 17.929s)

$ go test ./tests/integration -run 'TestFleetScale_MarkSeenDoesNotRewriteRegistry' -count=1 -v
    fleet_scale_test.go:439: LASTSEEN 200 节点:nodes.jsonl sha256=c5d50db2…(未变), mtime=…(未变), seen.log 400 行
--- PASS: TestFleetScale_MarkSeenDoesNotRewriteRegistry (3.59s)
# EXIT=0
```

## 2. 数字

### 2.1 400 台一次 `Run`(同一基准夹具,`-benchtime=5x -count=3`,报中位数)

| 口径 | 改前(`main` detached worktree) | 改后(本分支) | 变化 |
| --- | --- | --- | --- |
| `BenchmarkScheduler_Run_400Nodes` | 5.92 / 6.36 / 5.85 s(**5.92 s**) | 81.7 / 96.6 / 100.6 ms(**96.6 ms**) | **−98.4%** |
| `BenchmarkRegistry_MarkSeen_400`(公共 API,仍读表) | 8.20 / 6.74 / 6.36 ms/op | 8.37 / 7.29 / 7.51 ms/op | 不变(预期) |
| `BenchmarkRegistry_MarkSeenKnown_400`(热路径) | —(不存在) | 71.4 / 62.5 / 62.9 µs/op | 比 `MarkSeen` 低 **≈107×** |

### 2.2 200 台、无网络成本(`internal/fleet` 既有测试的日志口径)

`TestScheduler_RunReadsRegistryOnce/Run_不写身份表` 的 200 台 fake-dispatch Run,同一条命令
`go test ./internal/fleet -run 'TestScheduler_RunReadsRegistryOnce' -count=3 -v`:

| | 改前(2026-11-18 证据原文) | 改后(本次 3 跑) |
| --- | --- | --- |
| 200 台 1 条 intent | 927 ms / 1.867 s / 1.536 s | **18 ms / 41 ms / 24 ms** |

这一条最干净:没有网络、没有 fsync,量到的就是注册表侧成本。

### 2.3 200 台端到端(真 mTLS 桩 + 心跳预检 + ApplyIntent)

| | 改前 | 改后 |
| --- | --- | --- |
| `TestFleetScale_MarkSeenDoesNotRewriteRegistry` | 5.20 s(2026-11-18)/ 5.59 s(本次改前复测) | **3.41 s / 3.59 s / 3.66 s** |

**剩下的 3.5 s 不是注册表** —— 是 200 次串行 mTLS 建连 + 200 次心跳预检 + 200 次
ApplyIntent。注册表侧那 0.9–1.9 s(§2.2 的改前数字)已经归零。

## 3. 改动与不变量

- `Registry.markSeenKnown(nodeID, unix, all)`:`appendSeen` + `compactSeenIfNeeded`,
  **不调 `loadBase()`**。契约写死:调用方必须已持有身份表快照并校验过该节点已注册
  —— `Scheduler.Run` 本来就在整批开始前 `List()` 一次、对每台机做过「未注册→整批拒绝」。
- `Registry.MarkSeen`:**未注册必须报错**这条对外 fail-fast 一字未动,只把末尾的
  `compactSeen` 换成 `compactSeenIfNeeded`。
- `compactSeenIfNeeded`:先 `Stat` 侧车;`size < seenCompactThreshold(len(all))` 直接返回。
  论证:每行至少 1 字节 ⇒ 行数 ≤ 字节数,故 `size < 阈值` 时必然 `lines < 阈值`,
  **不可能漏压实**。真压实时仍走原 `compactSeen`(同阈值、同 `loadSeen → temp → fsync → rename`)。
- 不动的不变量:`nodes.jsonl` 原子替换、`seen.log` 不 fsync、读侧容忍末尾半截行、
  压实原子性、`List`/`Get` 折叠语义。

## 4. 残余(如实记)

1. **公共 `MarkSeen` 仍读一遍身份表** —— 它是对外入口,谁都能调,必须自己校验。
   `§2.1` 的 `MarkSeen_400` 数字前后不变就是证据:本次只把调度热路径切到已知变体。
2. **压实判定仍在热路径上做一次 `Stat`** —— O(1),但每台机每次下发会有一次 syscall。
   可以接受;要再省就得在 Registry 上挂内存计数(会引入并发语义),本次不做。
3. **re-register 轮换**:`runOne` 在节点换 id 后把新 id 复制进本轮快照副本
   (不改调用方切片),否则压实会把轮换节点刚落下的观测挤掉 —— 只影响 last_seen
   新鲜度,不影响身份。

## 5. 局限

- 单机口径(2.20GHz 老 CPU),绝对秒数按 §0 的 decode 成本换算读;结论是**量纲**:
  2n 次整表读 → 0。
- `Scheduler_Run_400Nodes` 改前无法用 `-benchtime=100x`(约 10 分钟/次),两侧统一用 `5x`。
- 200 台端到端测试的极差可达 ±1 s,故给原始值与中位数,不给"最好一次"。
