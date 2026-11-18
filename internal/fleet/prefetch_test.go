package fleet

// 本文件钉住批内预取器(prefetch)的四条性质:
//   1. 只探每台节点的**首个** intent —— 第 2..K 条留给 runOne 现调;
//   2. 拒绝判定(高危 + 无回滚点)先于预检,该对不得被探到;
//   3. Probe==nil 时预取是空操作(既有测试大量直接构造 &Scheduler{});
//   4. ctx 取消不是节点判决,不得进缓存。
// 另有并发上限与"真的并发"的规模断言。

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// lowIntentFor 造一条判定表判低危的 package intent(目标可换,便于一份
// Plan 里放多条不同的 intent)。
func lowIntentFor(target string) objectmodel.Intent {
	return objectmodel.Intent{Kind: objectmodel.KindPackage, Target: target, RiskClass: objectmodel.RiskClassLow}
}

// prefetchFixture 造预取器的入参:Plan、单个批、目标机表与节点行表。
// 节点默认是带 btrfs 快照能力的 agent 节点(预检不会被拒绝判定提前挡掉)。
func prefetchFixture(t *testing.T, nodeIDs []string, intents []objectmodel.Intent, size int) (objectmodel.Plan, Batch, map[string]objectmodel.Machine, map[string]Node) {
	t.Helper()
	machineList := make([]objectmodel.Machine, 0, len(nodeIDs))
	machines := make(map[string]objectmodel.Machine, len(nodeIDs))
	nodes := make(map[string]Node, len(nodeIDs))
	for _, id := range nodeIDs {
		machineList = append(machineList, agentMachine(id))
		machines[id] = agentMachine(id)
		nodes[id] = nodeWithSnapshot(id, snapBtrfs)
	}
	plan := planOf(t, intents, machineList, size)
	return plan, Batch{Index: 0, NodeIDs: append([]string(nil), nodeIDs...)}, machines, nodes
}

// TestPrefetch_OnlyFirstIntentPerNode 钉住预取范围:2 节点 × 2 intent 时
// 只允许探到 (n1,0) 与 (n2,0)。把 K 条 intent 全提前会让第 2..K 条用旧活性
// 下发 —— 中途死掉的节点会被漏检。
func TestPrefetch_OnlyFirstIntentPerNode(t *testing.T) {
	plan, batch, machines, nodes := prefetchFixture(t, []string{"n1", "n2"},
		[]objectmodel.Intent{lowIntent(), lowIntentFor("curl")}, 2)

	var mu sync.Mutex
	seen := map[probeKey]int{}
	s := &Scheduler{Policy: policy.Default(), Probe: func(_ context.Context, d DispatchTarget) error {
		mu.Lock()
		seen[probeKey{NodeID: d.Node.NodeID, Intent: d.Index}]++
		mu.Unlock()
		return nil
	}}

	cache := s.prefetchFirstIntentProbes(context.Background(), plan, batch, machines, nodes)

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []probeKey{{NodeID: "n1", Intent: 0}, {NodeID: "n2", Intent: 0}} {
		if got := seen[want]; got != 1 {
			t.Errorf("探针 %+v 次数 = %d, want 1", want, got)
		}
	}
	for _, bad := range []probeKey{{NodeID: "n1", Intent: 1}, {NodeID: "n2", Intent: 1}} {
		if got := seen[bad]; got != 0 {
			t.Errorf("第 2 条 intent 不得被预取,%+v 被探 %d 次", bad, got)
		}
	}
	if len(cache) != 2 {
		t.Errorf("缓存条目 = %d, want 2(每台节点只有首个 intent)", len(cache))
	}
	if err, ok := cache.lookup("n1", 0); !ok || err != nil {
		t.Errorf("lookup(n1,0) = (%v,%v), want (nil,true)", err, ok)
	}
	if _, ok := cache.lookup("n1", 1); ok {
		t.Error("lookup(n1,1) 不该命中")
	}
}

// TestPrefetch_SkipsRefusedPairs 钉住拒绝判定先于预检:高危 intent 打到无回滚点
// 的节点时,这一对**不得**被预取探到(连预检都白等一个心跳时限,还会把
// "无回滚点"这个真实原因掩盖成"心跳失败")。
func TestPrefetch_SkipsRefusedPairs(t *testing.T) {
	plan, batch, machines, nodes := prefetchFixture(t, []string{"n1"},
		[]objectmodel.Intent{highContainerIntent()}, 1)
	nodes["n1"] = nodeWithSnapshot("n1", snapNone)

	var probes atomic.Int64
	s := &Scheduler{Policy: policy.Default(), Probe: func(context.Context, DispatchTarget) error {
		probes.Add(1)
		return nil
	}}

	cache := s.prefetchFirstIntentProbes(context.Background(), plan, batch, machines, nodes)

	if got := probes.Load(); got != 0 {
		t.Errorf("无回滚点的对不得被预取探到,Probe 被调 %d 次", got)
	}
	if len(cache) != 0 {
		t.Errorf("缓存条目 = %d, want 0", len(cache))
	}
}

// TestPrefetch_NilProbeIsNoop 钉住兼容缝:Probe==nil 表示无预检通道,预取必须是
// 一次空操作(大量既有测试直接构造 &Scheduler{...})。
func TestPrefetch_NilProbeIsNoop(t *testing.T) {
	plan, batch, machines, nodes := prefetchFixture(t, []string{"n1", "n2"},
		[]objectmodel.Intent{lowIntent()}, 2)

	cache := (&Scheduler{Policy: policy.Default()}).prefetchFirstIntentProbes(
		context.Background(), plan, batch, machines, nodes)

	if len(cache) != 0 {
		t.Errorf("Probe==nil 时缓存条目 = %d, want 0", len(cache))
	}
}

// TestPrefetch_DoesNotCacheCanceledProbes 钉住取消的因果:ctx 在执行途中被取消
// (plan 级 10 分钟墙)时,Probe 返回的是 ctx.Err() —— 这不是节点判决,不得进缓存,
// 否则结果里会出现一条"节点 N3"的假因。
func TestPrefetch_DoesNotCacheCanceledProbes(t *testing.T) {
	plan, batch, machines, nodes := prefetchFixture(t, []string{"n1"},
		[]objectmodel.Intent{lowIntent()}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scheduler{Policy: policy.Default(), Probe: func(ctx context.Context, _ DispatchTarget) error {
		return ctx.Err()
	}}

	cache := s.prefetchFirstIntentProbes(ctx, plan, batch, machines, nodes)

	if err, ok := cache.lookup("n1", 0); ok {
		t.Errorf("取消被缓存成节点判决 (err=%v):取消不是节点裁决", err)
	}
}

// TestPrefetch_BoundedConcurrency 钉住两件事:在飞探针数不超过上限,且探针真的
// 并发(串行跑完 200 个 20ms 探针要 4s,并发上限 64 只要 4 轮)。
func TestPrefetch_BoundedConcurrency(t *testing.T) {
	const n = 200
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%03d", i)
	}
	plan, batch, machines, nodes := prefetchFixture(t, ids, []objectmodel.Intent{lowIntent()}, n)

	var inflight, peak atomic.Int64
	s := &Scheduler{Policy: policy.Default(), Probe: func(context.Context, DispatchTarget) error {
		cur := inflight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		return nil
	}}

	start := time.Now()
	cache := s.prefetchFirstIntentProbes(context.Background(), plan, batch, machines, nodes)
	elapsed := time.Since(start)

	if got := peak.Load(); got > probePrefetchConcurrency {
		t.Errorf("在飞探针峰值 = %d, want <= %d", got, probePrefetchConcurrency)
	}
	if len(cache) != n {
		t.Errorf("缓存条目 = %d, want %d", len(cache), n)
	}
	if serial := n * 20 * time.Millisecond; elapsed >= serial/4 {
		t.Errorf("预取耗时 %s,串行会是 %s —— 并发没有生效", elapsed, serial)
	}
}
