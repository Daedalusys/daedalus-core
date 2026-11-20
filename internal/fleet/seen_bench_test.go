package fleet

// 本文件量 issue #184 的口径:注册表的纯读成本。MarkSeen 每次为"未注册校验"
// 重放整张身份表(400 台 = 400 次 decode),而 Run 在整批开始前已经读过一次并对
// 每台机器做过未注册校验;markSeenKnown 走"调用方已校验"的契约,热路径不读表。
//
// 三个基准用同一套 400 台 fixture,便于改前/改后逐项对比(口径见报告)。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
)

// benchNodeCount 是规模基准的节点数:issue #184 的二次曲线在 400 台已经很陡。
const benchNodeCount = 400

// benchNodeID 是规模基准里的稳定节点标识。
func benchNodeID(i int) string {
	return fmt.Sprintf("m%03d", i)
}

// newBenchRegistry 建一个含 n 台已注册节点的注册表,返回 id 表(下标对齐)。
func newBenchRegistry(b *testing.B, n int) (*Registry, []string) {
	b.Helper()
	b.Setenv(FleetDirEnv, b.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		b.Fatalf("OpenRegistry: %v", err)
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = benchNodeID(i)
		if err := r.Put(testNode(ids[i])); err != nil {
			b.Fatalf("Put %s: %v", ids[i], err)
		}
	}
	return r, ids
}

// BenchmarkRegistry_MarkSeen_400 量 MarkSeen(带未注册校验)的单次成本:
// 每次都要重放 400 行身份表。
func BenchmarkRegistry_MarkSeen_400(b *testing.B) {
	r, ids := newBenchRegistry(b, benchNodeCount)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.MarkSeen(ids[i%len(ids)], 1700000000+int64(i)); err != nil {
			b.Fatalf("MarkSeen: %v", err)
		}
	}
}

// BenchmarkRegistry_MarkSeenKnown_400 是同一夹具上的热路径口径:调用方已持有
// 身份表快照并校验过注册,markSeenKnown 只追加 + 一次 stat。
func BenchmarkRegistry_MarkSeenKnown_400(b *testing.B) {
	r, ids := newBenchRegistry(b, benchNodeCount)
	all, err := r.List()
	if err != nil {
		b.Fatalf("List: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.markSeenKnown(ids[i%len(ids)], 1700000000+int64(i), all); err != nil {
			b.Fatalf("markSeenKnown: %v", err)
		}
	}
}

// benchPlan 造一份与 planOf 同形状的合法 Plan(planOf 收 *testing.T,基准用不了)。
func benchPlan(b *testing.B, n int) objectmodel.Plan {
	b.Helper()
	machines := make([]objectmodel.Machine, n)
	for i := range machines {
		machines[i] = agentMachine(benchNodeID(i))
	}
	p := objectmodel.Plan{
		ID:       "0123456789abcdef",
		Intents:  []objectmodel.Intent{lowIntent()},
		Machines: machines,
		Batching: objectmodel.Batching{Strategy: objectmodel.BatchingCount, Size: 50},
		BlastRadius: objectmodel.BlastRadius{
			Count:       n,
			MaxRisk:     objectmodel.Plan{Intents: []objectmodel.Intent{lowIntent()}}.MaxRisk(),
			MaxDowntime: 30 * time.Second,
		},
	}
	if err := p.Validate(); err != nil {
		b.Fatalf("基准 Plan 不合法: %v", err)
	}
	return p
}

// benchDispatch 是基准用的最小下发实现:不发 RPC,也不做任何记账。
//
// 不用 scheduler_test.go 的 fakeDispatcher:它每次 dispatch 都要扫一遍历史调用
// 来数"第几次",在 400 台 × 多轮迭代下这部分记账本身是 O(n²),会把被测的
// 注册表成本淹掉。基准只关心 Run 的注册表侧开销。
func benchDispatch(context.Context, DispatchTarget) (StepResult, error) {
	return StepResult{
		Outcome: OutcomeN1,
		Result:  agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS,
		TxID:    "tx-bench",
	}, nil
}

// BenchmarkScheduler_Run_400Nodes 是端到端口径:400 台 × 1 条低危 intent,
// 一次 Run 里 800 次 markSeen(不含网络与假 dispatch 的记账成本)。
func BenchmarkScheduler_Run_400Nodes(b *testing.B) {
	r, _ := newBenchRegistry(b, benchNodeCount)
	plan := benchPlan(b, benchNodeCount)
	s := &Scheduler{
		Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(),
		Dispatch: benchDispatch,
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Run(ctx, plan); err != nil {
			b.Fatalf("Run: %v", err)
		}
	}
}
