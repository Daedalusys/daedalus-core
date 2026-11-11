package fleet

// 本文件钉住失败分桶与暂停语义:ApplyIntentResult / gRPC code → N1(成功)/
// N2(瞬时)/ N3(持久)的纯映射,re-register 单次重试,以及低位重试上限与
// 高危"任何失败立即暂停"的分岔。
// Dispatch 是注入缝:测试塞 fake,从而断言"根本没发 RPC"与"发了几次"。

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

const (
	snapBtrfs = "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME"
	snapNone  = "SNAPSHOT_CAPABILITY_NONE"
	// snapUnspecifiedForTest 是 proto3 零值(agent 没探测到 / 没填)的枚举名。
	snapUnspecifiedForTest = "SNAPSHOT_CAPABILITY_UNSPECIFIED"
)

// fakeDispatcher 按脚本回应 Dispatch,并记录每次调用的目标;fake.reply 的 n
// 是该 (node, intent) 的第几次调用(0 起)。
type fakeDispatcher struct {
	calls []DispatchTarget
	reply func(d DispatchTarget, n int) (StepResult, error)
}

func (f *fakeDispatcher) dispatch(_ context.Context, d DispatchTarget) (StepResult, error) {
	n := 0
	for _, c := range f.calls {
		// 按 (machine, intent 下标) 计数:node_id 会在 re-register 后轮换,
		// 用它做键会把同一条 intent 的第二次调用误当成第一次。
		if c.Machine.MachineID == d.Machine.MachineID && c.Index == d.Index {
			n++
		}
	}
	f.calls = append(f.calls, d)
	if f.reply == nil {
		return StepResult{
			Outcome: OutcomeN1,
			Result:  agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS,
			TxID:    "tx-ok",
		}, nil
	}
	return f.reply(d, n)
}

// newRegistryWithNodes 建一个只含给定节点的注册表(t.Setenv 隔离)。
func newRegistryWithNodes(t *testing.T, nodes ...Node) *Registry {
	t.Helper()
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	for _, n := range nodes {
		if err := r.Put(n); err != nil {
			t.Fatalf("Put %s: %v", n.NodeID, err)
		}
	}
	return r
}

// nodeWithSnapshot 造一台指定 snapshot 能力的 agent 节点。
func nodeWithSnapshot(nodeID, snapshotCap string) Node {
	n := testNode(nodeID)
	n.SnapshotCap = snapshotCap
	return n
}

// agentMachine 造一台 agent 形态的目标机。
func agentMachine(nodeID string) objectmodel.Machine {
	return objectmodel.Machine{
		MachineID:    nodeID,
		Kind:         objectmodel.MachineKindAgent,
		SupportLevel: objectmodel.SupportLevelSupported,
	}
}

// lowIntent 是判定表判低危的 intent(package.set + 非内核包)。
func lowIntent() objectmodel.Intent {
	return objectmodel.Intent{Kind: objectmodel.KindPackage, Target: "nginx", RiskClass: objectmodel.RiskClassLow}
}

// highIntent 是声明为高危的 intent。
func highIntent() objectmodel.Intent {
	return objectmodel.Intent{
		Kind:      objectmodel.KindPackage,
		Target:    "kernel-core",
		RiskClass: objectmodel.RiskClassHigh,
	}
}

// planOf 造一个能过 Validate 的 Plan(批大小由 size 给出)。
func planOf(t *testing.T, intents []objectmodel.Intent, machines []objectmodel.Machine, size int) objectmodel.Plan {
	t.Helper()
	intentList := make([]objectmodel.Intent, len(intents))
	copy(intentList, intents)
	machineList := make([]objectmodel.Machine, len(machines))
	copy(machineList, machines)
	p := objectmodel.Plan{
		ID:       "0123456789abcdef",
		Intents:  intentList,
		Machines: machineList,
		Batching: objectmodel.Batching{Strategy: objectmodel.BatchingCount, Size: size},
		BlastRadius: objectmodel.BlastRadius{
			Count:       len(machineList),
			MaxRisk:     objectmodel.Plan{Intents: intentList}.MaxRisk(),
			MaxDowntime: 30 * time.Second,
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("测试夹具 Plan 不合法: %v", err)
	}
	return p
}

// TestScheduler_RefusesDispatchWhenSnapshotNone 钉住 N1 前置拒绝:目标节点没有
// 回滚点时,高危 intent 必须在下发**之前**被拒(一个 RPC 都不发)。
func TestScheduler_RefusesDispatchWhenSnapshotNone(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapNone))
	plan := planOf(t, []objectmodel.Intent{highIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("高危 intent 遇无回滚点节点不得发 RPC,Dispatch 被调 %d 次", len(fake.calls))
	}
	if len(run.Results) != 1 {
		t.Fatalf("Results 数 = %d, want 1", len(run.Results))
	}
	if run.Results[0].Outcome != OutcomeN3 {
		t.Errorf("Outcome = %v, want %v", run.Results[0].Outcome, OutcomeN3)
	}
	if !strings.Contains(strings.ToLower(run.Results[0].Message), "snapshot") {
		t.Errorf("拒绝文案应点名 snapshot 能力, got %q", run.Results[0].Message)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusDirty)
	}
}

// TestClassifyApply_Table 钉住 ApplyIntentResult × gRPC code 到三桶的纯映射。
func TestClassifyApply_Table(t *testing.T) {
	results := []struct {
		result agentv1.ApplyIntentResult
		want   Outcome
	}{
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS, OutcomeN1},
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_REFUSED_SNAPSHOT_CAPABILITY_NONE, OutcomeN3},
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_REFUSED_POLICY, OutcomeN3},
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_PERSISTENT_FAILURE, OutcomeN3},
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_TRANSIENT_FAILURE, OutcomeN2},
		{agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_UNSPECIFIED, OutcomeN3},
	}
	for _, tc := range results {
		t.Run("result="+tc.result.String(), func(t *testing.T) {
			if got := ClassifyApply(&agentv1.ApplyIntentResponse{Result: tc.result}, nil); got != tc.want {
				t.Errorf("ClassifyApply(%v, nil) = %v, want %v", tc.result, got, tc.want)
			}
		})
	}

	codesTable := []struct {
		code codes.Code
		want Outcome
	}{
		{codes.Unavailable, OutcomeN2},
		{codes.DeadlineExceeded, OutcomeN2},
		{codes.Aborted, OutcomeN2},
		{codes.FailedPrecondition, OutcomeReRegister},
		{codes.PermissionDenied, OutcomeN3},
		{codes.Internal, OutcomeN3},
		{codes.Unknown, OutcomeN3},
	}
	for _, tc := range codesTable {
		t.Run("code="+tc.code.String(), func(t *testing.T) {
			got := ClassifyApply(nil, status.Error(tc.code, "boom"))
			if got != tc.want {
				t.Errorf("ClassifyApply(nil, %v) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}

	t.Run("无响应且无错误", func(t *testing.T) {
		if got := ClassifyApply(nil, nil); got != OutcomeN3 {
			t.Errorf("响应缺失 = %v, want %v(结果缺失绝不重试)", got, OutcomeN3)
		}
	})
}

// TestClassifyApply_FailedPreconditionNeedsReRegister 钉住 re-register 哨兵值
// 与它的语义:node_id 轮换不是持久失败,但也不能当瞬时错误盲目重试。
func TestClassifyApply_FailedPreconditionNeedsReRegister(t *testing.T) {
	if OutcomeReRegister != 3 {
		t.Errorf("OutcomeReRegister = %d, want 3", OutcomeReRegister)
	}
	got := ClassifyApply(nil, status.Error(codes.FailedPrecondition, "node_id mismatch"))
	if got != OutcomeReRegister {
		t.Errorf("FailedPrecondition = %v, want %v", got, OutcomeReRegister)
	}
}

// TestScheduler_NodeIDMismatchReRegistersOnce 证明 node_id 轮换触发一次
// re-register + 重试一次,成功后 registry 里只剩新的 node_id。
func TestScheduler_NodeIDMismatchReRegistersOnce(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{reply: func(_ DispatchTarget, n int) (StepResult, error) {
		if n == 0 {
			return StepResult{Outcome: OutcomeReRegister, Message: "node_id 轮换"}, nil
		}
		return StepResult{Outcome: OutcomeN1, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS, TxID: "tx-9"}, nil
	}}
	registers := 0
	s := &Scheduler{
		Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch,
		Register: func(_ context.Context, d DispatchTarget) (Node, error) {
			registers++
			fresh := d.Node
			fresh.NodeID = "n1-new"
			fresh.LastSeenUnix = 1700000999
			return fresh, nil
		},
	}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Errorf("Dispatch 次数 = %d, want 2(首发 + re-register 后重试一次)", len(fake.calls))
	}
	if registers != 1 {
		t.Errorf("Register 次数 = %d, want 1", registers)
	}
	if run.Results[0].Outcome != OutcomeN1 {
		t.Errorf("Outcome = %v, want %v", run.Results[0].Outcome, OutcomeN1)
	}
	nodes, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "n1-new" {
		t.Errorf("registry = %+v, want 只剩 n1-new", nodes)
	}
}

// TestScheduler_NodeIDMismatchTwiceIsPersistent 证明连续两次 mismatch 才是 N3。
func TestScheduler_NodeIDMismatchTwiceIsPersistent(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{reply: func(_ DispatchTarget, _ int) (StepResult, error) {
		return StepResult{Outcome: OutcomeReRegister, Message: "node_id 轮换"}, nil
	}}
	s := &Scheduler{
		Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch,
		Register: func(_ context.Context, d DispatchTarget) (Node, error) { return d.Node, nil },
	}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Errorf("Dispatch 次数 = %d, want 2(只重试一次)", len(fake.calls))
	}
	if run.Results[0].Outcome != OutcomeN3 {
		t.Errorf("Outcome = %v, want %v", run.Results[0].Outcome, OutcomeN3)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusDirty)
	}
}

// TestScheduler_LowRiskTransientRetriesUpToThree 证明低危瞬时失败重试到上限,
// 用尽后转 N3 + dirty。
func TestScheduler_LowRiskTransientRetriesUpToThree(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{reply: func(_ DispatchTarget, _ int) (StepResult, error) {
		return StepResult{Outcome: OutcomeN2, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_TRANSIENT_FAILURE}, nil
	}}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 4 {
		t.Errorf("Dispatch 次数 = %d, want 4(1 次首发 + 3 次重试)", len(fake.calls))
	}
	if run.Results[0].Outcome != OutcomeN3 {
		t.Errorf("Outcome = %v, want %v(重试用尽)", run.Results[0].Outcome, OutcomeN3)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusDirty)
	}
}

// TestScheduler_LowRiskTransientThenSuccess 证明低位瞬时失败后成功即继续,不 dirty。
func TestScheduler_LowRiskTransientThenSuccess(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{reply: func(_ DispatchTarget, n int) (StepResult, error) {
		if n < 2 {
			return StepResult{Outcome: OutcomeN2, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_TRANSIENT_FAILURE}, nil
		}
		return StepResult{Outcome: OutcomeN1, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS, TxID: "tx-3"}, nil
	}}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 3 {
		t.Errorf("Dispatch 次数 = %d, want 3", len(fake.calls))
	}
	if run.Results[0].Outcome != OutcomeN1 {
		t.Errorf("Outcome = %v, want %v", run.Results[0].Outcome, OutcomeN1)
	}
	if run.Status != objectmodel.PlanStatusComplete {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusComplete)
	}
}

// TestScheduler_HighRiskAnyFailurePausesImmediately 钉住 G9:高危的**任何**失败
// 立即暂停,一次重试都不做。
func TestScheduler_HighRiskAnyFailurePausesImmediately(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{highIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{reply: func(_ DispatchTarget, _ int) (StepResult, error) {
		return StepResult{Outcome: OutcomeN2, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_TRANSIENT_FAILURE}, nil
	}}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("高危不得重试,Dispatch 次数 = %d, want 1", len(fake.calls))
	}
	if run.Results[0].Outcome != OutcomeN3 {
		t.Errorf("Outcome = %v, want %v", run.Results[0].Outcome, OutcomeN3)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusDirty)
	}
}

// TestScheduler_HighRiskSuccessDoesNotPause 证明高危成功照常推进。
func TestScheduler_HighRiskSuccessDoesNotPause(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{highIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("Dispatch 次数 = %d, want 1", len(fake.calls))
	}
	if run.Status != objectmodel.PlanStatusComplete {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusComplete)
	}
}

// TestScheduler_BatchBoundaryStopsOnDirty 证明某批出 N3 后,后续批一个 RPC 都不发。
func TestScheduler_BatchBoundaryStopsOnDirty(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs), nodeWithSnapshot("n2", snapBtrfs))
	machines := []objectmodel.Machine{agentMachine("n1"), agentMachine("n2")}
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, machines, 1)
	fake := &fakeDispatcher{reply: func(d DispatchTarget, _ int) (StepResult, error) {
		if d.Node.NodeID == "n1" {
			return StepResult{Outcome: OutcomeN3, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_PERSISTENT_FAILURE}, nil
		}
		return StepResult{Outcome: OutcomeN1, Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS, TxID: "tx-n2"}, nil
	}}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("dirty 后不得前进,Dispatch 次数 = %d, want 1", len(fake.calls))
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Errorf("Status = %q, want %q", run.Status, objectmodel.PlanStatusDirty)
	}
	if run.StoppedAt == nil || run.StoppedAt.Index != 1 {
		t.Errorf("StoppedAt = %+v, want 指向批 1", run.StoppedAt)
	}
}

// TestScheduler_IntentIDIsStable 钉住 intent_id 的格式:<plan_id>/<machine_id>/<intent 下标>。
// agent 侧照原样回写审计,格式变了就对不上账。
func TestScheduler_IntentIDIsStable(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "0123456789abcdef/n1/0"; run.Results[0].IntentID != want {
		t.Errorf("IntentID = %q, want %q", run.Results[0].IntentID, want)
	}
}

// TestScheduler_StepResultCarriesNodeAndIntentKind 钉住运行记录的两个新字段:
// 恢复线索要按节点形态分支(agent 与 atomic 的还原手段不同),记录里必须带得出
// 节点 kind 与 intent kind,否则读侧重算时无从判断。
func TestScheduler_StepResultCarriesNodeAndIntentKind(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := run.Results[0]
	if res.NodeKind != objectmodel.MachineKindAgent {
		t.Errorf("NodeKind = %q, want %q(取注册节点的 Kind)", res.NodeKind, objectmodel.MachineKindAgent)
	}
	if res.IntentKind != string(objectmodel.KindPackage) {
		t.Errorf("IntentKind = %q, want %q", res.IntentKind, objectmodel.KindPackage)
	}
}

// TestScheduler_UnregisteredNodeFailsClosed 证明目标机不在 registry 时整批不启动。
func TestScheduler_UnregisteredNodeFailsClosed(t *testing.T) {
	r := newRegistryWithNodes(t)
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("ghost")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	if _, err := s.Run(context.Background(), plan); err == nil {
		t.Fatal("未注册节点必须拒绝整批")
	}
	if len(fake.calls) != 0 {
		t.Errorf("未注册节点不得发 RPC,Dispatch 次数 = %d", len(fake.calls))
	}
}

// TestScheduler_MarksSeenAroundDispatch 证明每次 dispatch 前后都会刷新 last_seen。
func TestScheduler_MarksSeenAroundDispatch(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapBtrfs))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	s := &Scheduler{
		Registry: r, RetryMax: 3, Policy: policy.Default(), Dispatch: (&fakeDispatcher{}).dispatch,
		Now: func() time.Time { return time.Unix(1700000123, 0) },
	}
	if _, err := s.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	n, ok, err := r.Get("n1")
	if err != nil || !ok {
		t.Fatalf("Get n1: ok=%v err=%v", ok, err)
	}
	if n.LastSeenUnix != 1700000123 {
		t.Errorf("LastSeenUnix = %d, want 1700000123", n.LastSeenUnix)
	}
}

// TestScheduler_RefusesHighRiskWhenSnapshotCapabilityUnknown 钉住 RF1 的 fail-closed
// 边界:节点没有**确证**有回滚点时(空值 / proto 零值 UNSPECIFIED / NONE),高危
// intent 一律不发 RPC —— "不知道有没有回滚点"必须按"没有"处理。
func TestScheduler_RefusesHighRiskWhenSnapshotCapabilityUnknown(t *testing.T) {
	for _, cap := range []string{"", snapUnspecifiedForTest, snapNone} {
		t.Run("cap="+cap, func(t *testing.T) {
			r := newRegistryWithNodes(t, nodeWithSnapshot("n1", cap))
			plan := planOf(t, []objectmodel.Intent{highIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
			fake := &fakeDispatcher{}
			s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

			run, err := s.Run(context.Background(), plan)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(fake.calls) != 0 {
				t.Errorf("回滚点未确证不得发 RPC,Dispatch 被调 %d 次", len(fake.calls))
			}
			if run.Status != objectmodel.PlanStatusDirty {
				t.Errorf("Status = %q, want dirty", run.Status)
			}
		})
	}
}

// TestScheduler_LowRiskStillRunsOnUnknownSnapshotCapability 是上面那条的反面控制组:
// 能力未知只挡高危,不该把低危也一起挡死(否则整张 Plan 无法推进)。
func TestScheduler_LowRiskStillRunsOnUnknownSnapshotCapability(t *testing.T) {
	r := newRegistryWithNodes(t, nodeWithSnapshot("n1", snapUnspecifiedForTest))
	plan := planOf(t, []objectmodel.Intent{lowIntent()}, []objectmodel.Machine{agentMachine("n1")}, 1)
	fake := &fakeDispatcher{}
	s := &Scheduler{Registry: r, RetryMax: 3, Now: time.Now, Policy: policy.Default(), Dispatch: fake.dispatch}

	run, err := s.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("低危 intent 应正常下发,Dispatch 次数 = %d, want 1", len(fake.calls))
	}
	if run.Status != objectmodel.PlanStatusComplete {
		t.Errorf("Status = %q, want complete", run.Status)
	}
}
