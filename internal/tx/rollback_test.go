// BuildRollbackPlan 测试:逆序 + 无逆剔除 + 纯函数性,以及 applied 态固化链路。
package tx

import (
	"encoding/json"
	"testing"
)

func TestBuildRollbackPlan_ReversesAndFilters(t *testing.T) {
	// Given: 3 步——步骤 1 无 BeforeState(纯探测,无逆),其余有快照。
	steps := []Step{
		{Index: 0, Adapter: "a", BeforeState: json.RawMessage(`{"s":0}`)},
		{Index: 1, Adapter: "probe-only"},
		{Index: 2, Adapter: "c", BeforeState: json.RawMessage(`{"s":2}`)},
	}

	// When
	plan := BuildRollbackPlan(steps)

	// Then: 逆序(2 先于 0)、无逆步骤被剔除、载荷原样保留。
	if len(plan.Steps) != 2 {
		t.Fatalf("回滚计划应只剩 2 步,实得 %d", len(plan.Steps))
	}
	if plan.Steps[0].Adapter != "c" || plan.Steps[1].Adapter != "a" {
		t.Fatalf("未逆序: %s, %s", plan.Steps[0].Adapter, plan.Steps[1].Adapter)
	}
	if string(plan.Steps[0].BeforeState) != `{"s":2}` {
		t.Fatalf("BeforeState 载荷漂移: %s", plan.Steps[0].BeforeState)
	}

	// 纯函数:入参不被修改(调用方仍可安全复用 steps)。
	if len(steps) != 3 || steps[1].BeforeState != nil {
		t.Fatalf("BuildRollbackPlan 污染了入参: %+v", steps)
	}
}

func TestBuildRollbackPlan_Empty(t *testing.T) {
	if plan := BuildRollbackPlan(nil); len(plan.Steps) != 0 {
		t.Fatalf("空输入应得空计划: %+v", plan.Steps)
	}
	if plan := BuildRollbackPlan([]Step{{Index: 0, Adapter: "no-snap"}}); len(plan.Steps) != 0 {
		t.Fatalf("全无逆输入应得空计划: %+v", plan.Steps)
	}
}

// TestTx_MarkApplied_PersistsRollbackPlan 钉死生命周期与生成器的接线:
// MarkApplied 时计划固化进日志,且持久化形态同样是逆序筛选后的步骤集。
func TestTx_MarkApplied_PersistsRollbackPlan(t *testing.T) {
	useTxDir(t)
	tx := mustBegin(t)
	if err := tx.Append(Step{Adapter: "a", BeforeState: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(Step{Adapter: "probe"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(Step{Adapter: "b", BeforeState: json.RawMessage(`{"n":3}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkApplying(); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkApplied(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.RollbackPlan.Steps) != 2 ||
		loaded.RollbackPlan.Steps[0].Adapter != "b" ||
		loaded.RollbackPlan.Steps[1].Adapter != "a" {
		t.Fatalf("持久化回滚计划未逆序筛选: %+v", loaded.RollbackPlan.Steps)
	}
	// 正向 steps 完整保留(计划是派生视图,不是执行消耗)。
	if len(loaded.Steps) != 3 {
		t.Fatalf("正向步骤被破坏: %d", len(loaded.Steps))
	}
}
