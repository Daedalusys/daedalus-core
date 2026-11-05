package main

// plan 子命令的测试:submit 的输入校验与落盘、status 的 stdout 契约(空集是 []、
// dirty 时给本地恢复命令)、advance 的人在环恢复语义与用法闸。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// TestPlanSubmit_ReadsJSONFile 证明 submit 读文件 → 校验 → 执行 → 落盘 → 打印状态。
// 测试环境没有可连的 agent,故下发必然失败、状态是 dirty —— 这正是要断言的分桶。
func TestPlanSubmit_ReadsJSONFile(t *testing.T) {
	root := setupFleetCLI(t)
	seedNode(t, testNodeRow("n1", objectmodel.SupportLevelSupported))
	plan := testPlan(t, "p-1", []objectmodel.Machine{testMachine("n1")}, []objectmodel.Intent{lowIntent()}, 1)

	code, out, errOut := runCapture(t, "plan", "submit", planFile(t, plan))
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	got := decodePlanStatus(t, out)
	if got.PlanID != "p-1" {
		t.Errorf("plan_id = %q, want p-1", got.PlanID)
	}
	if got.MaxRisk != string(objectmodel.RiskClassLow) {
		t.Errorf("max_risk = %q, want low", got.MaxRisk)
	}
	if got.Status != string(objectmodel.PlanStatusDirty) {
		t.Errorf("status = %q, want dirty(无可连 agent,下发必然失败)", got.Status)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results 数 = %d, want 1", len(got.Results))
	}
	if got.Results[0].IntentID != "p-1/n1/0" {
		t.Errorf("intent_id = %q, want p-1/n1/0", got.Results[0].IntentID)
	}
	if _, found, err := lastPlanRecord(root, "p-1"); err != nil || !found {
		t.Errorf("运行记录未落盘: found=%v err=%v", found, err)
	}
}

// TestPlanSubmit_RejectsMissingFile 证明缺文件是用法错误(2)且点名路径。
func TestPlanSubmit_RejectsMissingFile(t *testing.T) {
	setupFleetCLI(t)
	missing := filepath.Join(t.TempDir(), "nope.json")
	code, _, errOut := runCapture(t, "plan", "submit", missing)
	if code != exitUsage {
		t.Errorf("退出码 = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "nope.json") {
		t.Errorf("stderr 应点名路径: %s", errOut)
	}
}

// TestPlanSubmit_RejectsInvalidPlan 证明 Plan.Validate 的报错原样透出为用法错误。
func TestPlanSubmit_RejectsInvalidPlan(t *testing.T) {
	setupFleetCLI(t)
	// max_risk 声明 low,但 intent 是 high:违反 Plan 的等式。
	body := []byte(`{"id":"p-2","intents":[{"kind":"package","target":"nginx","risk_class":"high"}],` +
		`"machines":[{"machine_id":"n1","kind":"agent","support_level":"supported"}],` +
		`"batching":{"strategy":"count","size":1},"blast_radius":{"count":1,"max_risk":"low"}}`)
	code, _, errOut := runCapture(t, "plan", "submit", writeTempFile(t, "bad.json", body))
	if code != exitUsage {
		t.Errorf("退出码 = %d, want %d(stderr=%s)", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "blast_radius.max_risk") {
		t.Errorf("报错应原样透出 Validate 的字段名: %s", errOut)
	}
}

// TestPlanStatus_UnknownID 证明未知 plan-id 是运行期失败(1)而不是用法错误(2)。
func TestPlanStatus_UnknownID(t *testing.T) {
	setupFleetCLI(t)
	code, _, errOut := runCapture(t, "plan", "status", "no-such-plan")
	if code != exitRuntime {
		t.Errorf("退出码 = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(errOut, "no-such-plan") {
		t.Errorf("stderr 应点名 plan-id: %s", errOut)
	}
}

// TestPlanStatus_RendersEmptyArraysNotNull 钉住 RF5:batches / results / recovery
// 为空时必须是 [] 而不是 null(下游 jq 与 shell 脚本按数组消费)。
func TestPlanStatus_RendersEmptyArraysNotNull(t *testing.T) {
	root := setupFleetCLI(t)
	plan := testPlan(t, "p-3", []objectmodel.Machine{testMachine("n1")}, []objectmodel.Intent{lowIntent()}, 1)
	seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
		PlanID: "p-3", Status: string(objectmodel.PlanStatusComplete), MaxRisk: string(objectmodel.RiskClassLow),
	}})

	code, out, errOut := runCapture(t, "plan", "status", "p-3")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	for _, want := range []string{`"batches":[]`, `"results":[]`, `"recovery":[]`} {
		if !strings.Contains(out, want) {
			t.Errorf("空集必须是 %s, got %s", want, out)
		}
	}
}

// TestPlanStatus_RendersRecoveryCommandsOnDirty 证明 dirty 且已有成功落地的节点时,
// 每台给一条**本地**恢复命令;非 dirty 一律空。
func TestPlanStatus_RendersRecoveryCommandsOnDirty(t *testing.T) {
	results := []planResult{
		{NodeID: "n1", IntentID: "p-4/n1/0", Outcome: "success", Result: "success", TxID: "tx-abc"},
		{NodeID: "n2", IntentID: "p-4/n2/0", Outcome: "persistent", Result: "persistent_failure", Message: "boom"},
	}
	cases := []struct {
		status   objectmodel.PlanStatus
		wantReco int
	}{
		{objectmodel.PlanStatusDirty, 1},
		{objectmodel.PlanStatusComplete, 0},
		{objectmodel.PlanStatusPaused, 0},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			root := setupFleetCLI(t)
			plan := testPlan(t, "p-4", []objectmodel.Machine{testMachine("n1"), testMachine("n2")},
				[]objectmodel.Intent{lowIntent()}, 1)
			seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
				PlanID: "p-4", Status: string(tc.status), MaxRisk: string(objectmodel.RiskClassLow),
				Results: results,
			}})

			code, out, errOut := runCapture(t, "plan", "status", "p-4")
			if code != exitOK {
				t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
			}
			got := decodePlanStatus(t, out)
			if len(got.Recovery) != tc.wantReco {
				t.Fatalf("recovery 数 = %d, want %d(%+v)", len(got.Recovery), tc.wantReco, got.Recovery)
			}
			if tc.wantReco == 1 {
				if got.Recovery[0].NodeID != "n1" || got.Recovery[0].TxID != "tx-abc" {
					t.Errorf("恢复线索应指向成功落地的 n1/tx-abc: %+v", got.Recovery[0])
				}
				if got.Recovery[0].Command != "daedalus-tx rollback tx-abc" {
					t.Errorf("command = %q, want 逐字的本地命令", got.Recovery[0].Command)
				}
			}
		})
	}
}

// TestPlanAdvance_SkipNodeRemovesItFromPendingBatch 证明 --skip 把人已接受的节点
// 从待跑批次摘掉、把它的结果标成 skipped-by-human,并落一行新记录。
func TestPlanAdvance_SkipNodeRemovesItFromPendingBatch(t *testing.T) {
	root := setupFleetCLI(t)
	plan := testPlan(t, "p-6", []objectmodel.Machine{testMachine("n1"), testMachine("n2")},
		[]objectmodel.Intent{lowIntent()}, 1)
	seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
		PlanID: "p-6", Status: string(objectmodel.PlanStatusDirty), MaxRisk: string(objectmodel.RiskClassLow),
		Batches: []fleet.Batch{{Index: 0, NodeIDs: []string{"n1"}}, {Index: 1, NodeIDs: []string{"n2"}}},
		Results: []planResult{
			{NodeID: "n1", IntentID: "p-6/n1/0", Outcome: "success", Result: "success", TxID: "tx-1"},
			{NodeID: "n2", IntentID: "p-6/n2/0", Outcome: "persistent", Result: "persistent_failure", Message: "boom"},
		},
	}})

	code, out, errOut := runCapture(t, "plan", "advance", "p-6", "--skip", "n2")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	got := decodePlanStatus(t, out)
	for _, b := range got.Batches {
		for _, id := range b.NodeIDs {
			if id == "n2" {
				t.Errorf("被 skip 的节点仍留在待跑批次里: %+v", got.Batches)
			}
		}
	}
	skipped := false
	for _, r := range got.Results {
		if r.NodeID != "n2" {
			continue
		}
		skipped = true
		if !strings.HasPrefix(r.Message, "skipped-by-human:") {
			t.Errorf("n2 的结果未标成人已接受: %q", r.Message)
		}
	}
	if !skipped {
		t.Error("人已接受不等于抹掉记录:n2 的结果必须仍在")
	}
	if got.Status != string(objectmodel.PlanStatusComplete) {
		t.Errorf("status = %q, want complete(没有待跑节点了)", got.Status)
	}

	// append-only:人介入一次就多一行记录。
	rec, found, err := lastPlanRecord(root, "p-6")
	if err != nil || !found {
		t.Fatalf("续跑记录未落盘: found=%v err=%v", found, err)
	}
	if rec.Run.Status != got.Status {
		t.Errorf("落盘状态 = %q, want %q", rec.Run.Status, got.Status)
	}
}

// TestPlanAdvance_ForceWithoutDirtyIsUsageError 证明只有 dirty/paused 的 Plan
// 才需要人介入;两种恢复参数也不能同时给或缺。
func TestPlanAdvance_ForceWithoutDirtyIsUsageError(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"complete 的 Plan 不许 force", []string{"p-5", "--force"}},
		{"complete 的 Plan 不许 skip", []string{"p-5", "--skip", "n1"}},
		{"两种恢复参数同时给", []string{"p-5", "--force", "--skip", "n1"}},
		{"两种恢复参数都不给", []string{"p-5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupFleetCLI(t)
			plan := testPlan(t, "p-5", []objectmodel.Machine{testMachine("n1")}, []objectmodel.Intent{lowIntent()}, 1)
			seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
				PlanID: "p-5", Status: string(objectmodel.PlanStatusComplete), MaxRisk: string(objectmodel.RiskClassLow),
			}})
			code, _, _ := runCapture(t, append([]string{"plan", "advance"}, tc.args...)...)
			if code != exitUsage {
				t.Errorf("退出码 = %d, want %d", code, exitUsage)
			}
		})
	}
}

// TestPlanRunPersistenceRoundTrip 证明 submit 落的记录就是 status 读到的同一份。
func TestPlanRunPersistenceRoundTrip(t *testing.T) {
	setupFleetCLI(t)
	seedNode(t, testNodeRow("n1", objectmodel.SupportLevelSupported))
	plan := testPlan(t, "p-7", []objectmodel.Machine{testMachine("n1")}, []objectmodel.Intent{lowIntent()}, 1)

	code, out, errOut := runCapture(t, "plan", "submit", planFile(t, plan))
	if code != exitOK {
		t.Fatalf("submit 退出码 = %d(stderr=%s)", code, errOut)
	}
	submitted := decodePlanStatus(t, out)

	code, out, errOut = runCapture(t, "plan", "status", "p-7")
	if code != exitOK {
		t.Fatalf("status 退出码 = %d(stderr=%s)", code, errOut)
	}
	fromDisk := decodePlanStatus(t, out)

	if fromDisk.PlanID != submitted.PlanID || fromDisk.Status != submitted.Status ||
		len(fromDisk.Results) != len(submitted.Results) {
		t.Errorf("落盘/读回不一致:\n submitted %+v\n from disk %+v", submitted, fromDisk)
	}
}

// TestPlanAdvance_RotationTolerantPending 钉住 node_id 轮换后的续跑判定:
// 结果的 node_id 可能已经被 re-register 换成新值,认机器必须看 intent_id 里的
// machine_id;否则已成功的机器会被当成待跑而被重跑,并因旧 node_id 已从 registry
// 摘掉而整个 advance 失败(退出 1)。
func TestPlanAdvance_RotationTolerantPending(t *testing.T) {
	root := setupFleetCLI(t)
	// 待跑的那台必须在 registry 里(真实流程里登记过);轮换过的 n1 已不在。
	seedNode(t, testNodeRow("n2", objectmodel.SupportLevelSupported))
	plan := testPlan(t, "p-8", []objectmodel.Machine{testMachine("n1"), testMachine("n2")},
		[]objectmodel.Intent{lowIntent()}, 1)
	seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
		PlanID: "p-8", Status: string(objectmodel.PlanStatusDirty), MaxRisk: string(objectmodel.RiskClassLow),
		Results: []planResult{
			// n1 已成功,但 node_id 是轮换后的新值。
			{NodeID: "n1-new", IntentID: "p-8/n1/0", Outcome: "success", Result: "success", TxID: "tx-1"},
			// n2 从未成功 → 待跑(测试环境无可连 agent,必然失败)。
			{NodeID: "n2", IntentID: "p-8/n2/0", Outcome: "persistent", Result: "persistent_failure", Message: "boom"},
		},
	}})

	code, out, errOut := runCapture(t, "plan", "advance", "p-8", "--force")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	got := decodePlanStatus(t, out)
	seen := 0
	for _, r := range got.Results {
		if strings.HasSuffix(r.IntentID, "/n1/0") {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("n1 的结果数 = %d, want 1(轮换后不得被当成待跑而重跑)", seen)
	}
}

// TestPlanAdvance_DoesNotRerunPartiallySucceededMachine 钉住"不重复应用":
// 一台机部分成功时不整机重跑(objectmodel.Plan 的 intents 作用于所有目标机,
// 整机重跑会把已成功的 intent 再应用一遍并产生重复 intent_id);它保持 dirty,
// 等人在控制面显式 skip 或本地 rollback。
func TestPlanAdvance_DoesNotRerunPartiallySucceededMachine(t *testing.T) {
	root := setupFleetCLI(t)
	intents := []objectmodel.Intent{
		lowIntent(),
		{Kind: objectmodel.KindService, Target: "nginx.service", RiskClass: objectmodel.RiskClassLow},
	}
	plan := testPlan(t, "p-9", []objectmodel.Machine{testMachine("n1")}, intents, 1)
	seedPlanRecord(t, root, planRecord{Plan: plan, Run: planStatusOutput{
		PlanID: "p-9", Status: string(objectmodel.PlanStatusDirty), MaxRisk: string(objectmodel.RiskClassLow),
		Results: []planResult{
			{NodeID: "n1", IntentID: "p-9/n1/0", Outcome: "success", Result: "success", TxID: "tx-a"},
			{NodeID: "n1", IntentID: "p-9/n1/1", Outcome: "persistent", Result: "persistent_failure", Message: "boom"},
		},
	}})

	code, out, errOut := runCapture(t, "plan", "advance", "p-9", "--force")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	got := decodePlanStatus(t, out)
	first := 0
	for _, r := range got.Results {
		if r.IntentID == "p-9/n1/0" {
			first++
		}
	}
	if first != 1 {
		t.Errorf("p-9/n1/0 的结果数 = %d, want 1(已成功的 intent 不得被再应用一次)", first)
	}
	if got.Status != string(objectmodel.PlanStatusDirty) {
		t.Errorf("status = %q, want dirty(半成功未被接受,不能报 complete)", got.Status)
	}
}
