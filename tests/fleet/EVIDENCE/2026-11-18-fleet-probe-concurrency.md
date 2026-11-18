# fleet 批内并发预检 —— 规模证据（core#173，2026-11-18）

> **状态**：已执行，**全部用例绿**（`go test ./tests/integration -run TestFleetScale_Hung` → `ok … 7.091s`）。
> **驱动**：新增 `internal/fleet/prefetch.go`（批内并发预取器）+ 接进 `Scheduler.Run`；
> `tests/integration/fleet_scale_test.go` 只**追加**用例与桩字段，既有五条用例逐字未改。
> **执行人**：worker（协调者复核）。
> **口径**：`policy.Default()`；心跳时限在测试里显式压到 `200ms`（生产口径是
> `[fleet].heartbeat_timeout_seconds = 30`，秒粒度压不到 200ms，故直接落在
> `Scheduler.HeartbeatTimeout` 字段上）。被控端仍是进程内 200 个 loopback + 真双向 TLS 的
> gRPC 桩（`scaleAgent`），只新增两个故障开关：`hangHeartbeat`（收请求不回）、
> `failHeartbeat`（立刻答错，用于控制组）。

## 0. 一句话结论

**预检本身不是墙时大头，`ceil(N/64) × 心跳时限` 才是**：200 台全挂死时，并发预取的墙是
`4 波 × 200ms + 握手 ≈ 1.6s`，串行预取是 `200 × 200ms ≈ 40s`（实测 40.872s）。产线口径
（时限 30s）下前者是 ~120s，后者是 100 分钟 —— 后者必然撞满 `planRunTimeout = 10min`。

## 1. 命令原文与退出码

```bash
# 四条 gate（在仓库根执行）
go test ./internal/fleet -count=1 -race                                   # exit 0 → ok  … 2.348s
go test ./tests/integration -count=1 -short                               # exit 0 → ok  … 1.282s
go test ./tests/integration -run 'TestFleetScale_Hung' -count=1 -v -timeout 300s   # exit 0
go vet ./internal/fleet                                                   # exit 0（无输出）

# 证据补充（波次形状与分档）
go test ./tests/integration -run 'TestFleetScale_AllHung' -count=1 -v -timeout 300s                 # exit 0
DAEDALUS_SCALE_N=128 go test ./tests/integration -run 'TestFleetScale_AllHung' …                    # exit 0
DAEDALUS_SCALE_N=64  go test ./tests/integration -run 'TestFleetScale_HungNodes' …                  # exit 0
```

## 2. 用例的 `-v` 输出摘录

```
=== RUN   TestFleetScale_HungNodesDoNotStackHeartbeatWall
    fleet_scale_test.go:506: HUNG 200 节点:末 20 台收请求不回,端到端墙 4.137s(已跑 181 台,status=dirty);
      零下发对照:控制组 879ms → 挂死 941ms(增量 63ms,串行预取会是 19×200ms)
--- PASS: TestFleetScale_HungNodesDoNotStackHeartbeatWall (6.86s)
=== RUN   TestFleetScale_HungHeartbeatDoesNotDispatch
    fleet_scale_test.go:555: HUNG-DISPATCH 2 台:第 2 台收请求不回,ApplyIntent 0 次,健康台 1 次
--- PASS: TestFleetScale_HungHeartbeatDoesNotDispatch (0.22s)
PASS
ok  	github.com/Daedalusys/daedalus-core/tests/integration	7.091s
```

```
=== RUN   TestFleetScale_AllHungPrefetchWallIsWaveShaped
    fleet_scale_test.go:633: ALL-HUNG 200 节点:4 波 × 200ms,墙 1.664s(串行会是 40s),results=1,status=dirty
--- PASS: TestFleetScale_AllHungPrefetchWallIsWaveShaped (2.70s)
PASS
ok  	github.com/Daedalusys/daedalus-core/tests/integration	2.707s
```

```
$ go test ./internal/fleet -count=1 -race
ok  	github.com/Daedalusys/daedalus-core/internal/fleet	2.348s
```

三条用例各自钉的东西：

| 用例 | 判据 |
| --- | --- |
| `TestFleetScale_HungNodesDoNotStackHeartbeatWall` | 末 20 台挂死：前 180 台各一条 N1、第 181 台 N3 且文案含「心跳预检」、整批 dirty；**零下发对照**里多出的 19 台挂死只让墙时多 63ms（串行是 19×200ms） |
| `TestFleetScale_HungHeartbeatDoesNotDispatch` | 挂死那台 `ApplyIntent` 计数 **0**（预检失败不下发） |
| `TestFleetScale_AllHungPrefetchWallIsWaveShaped` | 整批全挂死：墙时 = `ceil(N/64)` 个时限（下界是结构性的：64 槽 × N 个各要一整个时限的探针），不是 N 个 |

## 3. 改前 / 改后对比（单批，零下发，只有预检这一段）

`prefetch.go` 的并发上限就是那个唯一的自变量：把 `probePrefetchConcurrency` 由 `64` 改成 `1`
得到「串行预取」变体（其余一个字节没动，同一台机器、同一份用例、同一个桩）。

| 变体 | 200 台全挂死的墙时 | 128 台全挂死的墙时 | 说明 |
| --- | --- | --- | --- |
| **本方案**：批内并发预取（上限 64） | **1.664s**（4 波 × 200ms + 握手） | **609ms**（2 波 × 200ms） | 轮次由上限决定，与台数无关地塌缩 |
| 串行预取（上限 1） | 40.872s（用例 FAIL） | 26.041s（用例 FAIL） | 探针集合由输入确定 ⇒ 不能提前中止 ⇒ 线性累加 |
| `origin/main`（lazy 串行预检，遇首个 N3 停手） | 951ms（用例 FAIL） | — | 只付 1 个时限：**首个**挂死节点即判脏，后面的根本不探 |

三行都 FAIL 在**同一条口径**上（`origin/main` 与串行变体的差别只在为什么）：

```
# origin/main（951ms 只是"探了第一台就停手"的假象）
fleet_scale_test.go:629: 第 1 台被探 0 次心跳, want 1(探针集合由输入决定)
fleet_scale_test.go:629: 第 2 台被探 0 次心跳, want 1(探针集合由输入决定)   …（1..199 全 0）
# 串行变体（探满了整批，但一轮一个时限）
fleet_scale_test.go:607: 墙时 40.872s, want < 2.8s(4 波 × 200ms,另加 2s 握手余量):串行会是 200×200ms
```

另一条用例上的同一对比（末 20 台挂死，零下发对照的增量）：

| 变体 | 控制组 → 挂死 | 增量 | 判据（< 1s） |
| --- | --- | --- | --- |
| **本方案**（上限 64） | 879ms → 941ms | **+63ms** | PASS |
| 串行预取（上限 1） | 1.519s → 5.974s | **+4.456s**（19×200ms） | **FAIL** |
| `origin/main` | 702ms → 790ms | +88ms | PASS（但同一条用例在"整批都被探到"上 FAIL） |

`origin/main` 那一行恰好说明为什么不能拿"增量"当唯一判据：**串行的旧实现根本不会探到第 2 台
挂死节点**（首个 N3 即停手），所以它在这条上永远是"没超预算"。真正的判据是两条一起：
(a) 整批每台都被探到（探针集合由输入决定）＋ (b) 多出来的挂死节点不累加墙时。

**必须说清的一件事**：`origin/main` 的串行实现一个 Run 最多只付**一个**心跳时限 —— 预检失败即
N3，整批立刻 dirty 并停手，后续挂死节点根本不会被探到。所以「20 台挂死 = 20×30s」不是当前
实现单次 submit 的表现，而是**候选方向 2（先把整批心跳探一遍再串行下发）如果写成串行**的
表现。本方案是候选方向 2 的并发实现：因为「探针集合必须由输入决定」（不能因为某个探针失败
就提前取消其余探针，否则同一份输入在不同负载下会探到不同节点集合，结果变 flaky），整批必须
探满 —— 探满就意味着串行会线性累加，所以并发不是优化选项，而是这条路成立的前提。

**挂死节点在同一个批里的两段墙时**（本方案，200 台，末 20 台挂死）：

| 段 | 200 台 | 64 台 |
| --- | --- | --- |
| 端到端（含 180 台下发：每台一次 mTLS 握手 + 2 次注册表整表 fsync） | 4.137s | 2.054s |
| 其中预检增量（零下发对照：控制组立刻答错 → 测试组前 20 台挂死） | 879ms → 941ms，**+63ms** | 140ms → 249ms，**+109ms** |

端到端的绝对值主要不是预检：200 台 × 2 次整表 `fsync` 才是（同一个实测里量到的另一条规模
问题 `#165`，本方案不碰）。所以判据用**增量**与**波次形状**，不用端到端绝对值。

## 4. 未覆盖（如实记）

1. **产线时限（30s）没有跑**：200 台全挂死在 30s 口径下是 ~120s，跑一次两分钟、且对结论没有
   增量信息，故用 200ms 等比缩放。
2. **没有真机**：仍是进程内假 agent（与 `2026-11-16-fleet-scale-200.md` 同口径）。
3. **批数维度没测**：`size = n`（单批）是刻意的 —— 量的是批内并发，不是批数。
4. **`Registry` 的 O(n²) 整表 fsync（#165）没动**，它仍是端到端墙时的大头。
