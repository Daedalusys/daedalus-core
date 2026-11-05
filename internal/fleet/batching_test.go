package fleet

// 本文件钉住批切分:count / importance 两条已实现策略与 custom 的显式未实现。

import (
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// testMachines 造 n 台形态/分级一致的机器,id 形如 m0..m{n-1}。
func testMachines(n int) []objectmodel.Machine {
	out := make([]objectmodel.Machine, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, objectmodel.Machine{
			MachineID:    "m" + string(rune('0'+i)),
			Kind:         objectmodel.MachineKindAgent,
			SupportLevel: objectmodel.SupportLevelSupported,
		})
	}
	return out
}

// TestSplitBatches_Count 证明 count 策略按 Size 切块,末块可不满。
func TestSplitBatches_Count(t *testing.T) {
	batches, err := SplitBatches(testMachines(3), objectmodel.Batching{Strategy: objectmodel.BatchingCount, Size: 2})
	if err != nil {
		t.Fatalf("SplitBatches: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("批数 = %d, want 2", len(batches))
	}
	if batches[0].Index != 0 || strings.Join(batches[0].NodeIDs, ",") != "m0,m1" {
		t.Errorf("批 0 = %+v, want index=0 node_ids=m0,m1", batches[0])
	}
	if batches[1].Index != 1 || strings.Join(batches[1].NodeIDs, ",") != "m2" {
		t.Errorf("批 1 = %+v, want index=1 node_ids=m2", batches[1])
	}
}

// TestSplitBatches_CountRequiresPositiveSize 证明 count 策略下 Size 必须为正。
func TestSplitBatches_CountRequiresPositiveSize(t *testing.T) {
	for _, size := range []int{0, -1} {
		if _, err := SplitBatches(testMachines(2), objectmodel.Batching{Strategy: objectmodel.BatchingCount, Size: size}); err == nil {
			t.Errorf("Size=%d 应被拒", size)
		}
	}
}

// TestSplitBatches_ImportanceSplitsSupportedFirst 证明 importance 把 supported
// 放前、experimental 放后(分级诚实度的落地)。
func TestSplitBatches_ImportanceSplitsSupportedFirst(t *testing.T) {
	machines := []objectmodel.Machine{
		{MachineID: "exp-1", Kind: objectmodel.MachineKindAgent, SupportLevel: objectmodel.SupportLevelExperimental},
		{MachineID: "sup-1", Kind: objectmodel.MachineKindAgent, SupportLevel: objectmodel.SupportLevelSupported},
		{MachineID: "exp-2", Kind: objectmodel.MachineKindAgent, SupportLevel: objectmodel.SupportLevelExperimental},
		{MachineID: "sup-2", Kind: objectmodel.MachineKindAgent, SupportLevel: objectmodel.SupportLevelSupported},
	}
	batches, err := SplitBatches(machines, objectmodel.Batching{Strategy: objectmodel.BatchingImportance})
	if err != nil {
		t.Fatalf("SplitBatches: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("批数 = %d, want 2", len(batches))
	}
	if got := strings.Join(batches[0].NodeIDs, ","); got != "sup-1,sup-2" {
		t.Errorf("首批 = %q, want %q(supported 在前)", got, "sup-1,sup-2")
	}
	if got := strings.Join(batches[1].NodeIDs, ","); got != "exp-1,exp-2" {
		t.Errorf("次批 = %q, want %q(experimental 在后)", got, "exp-1,exp-2")
	}
}

// TestSplitBatches_ImportanceSkipsEmptyBatch 证明某档为空时不产生空批。
func TestSplitBatches_ImportanceSkipsEmptyBatch(t *testing.T) {
	batches, err := SplitBatches(testMachines(2), objectmodel.Batching{Strategy: objectmodel.BatchingImportance})
	if err != nil {
		t.Fatalf("SplitBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("批数 = %d, want 1(全 supported 不产生空批)", len(batches))
	}
	if batches[0].Index != 0 {
		t.Errorf("Index = %d, want 0(索引连续)", batches[0].Index)
	}
}

// TestSplitBatches_CustomIsUnimplemented 证明 custom 策略在 P0.5 显式未实现
// (而非静默退化成某一种)。
func TestSplitBatches_CustomIsUnimplemented(t *testing.T) {
	_, err := SplitBatches(testMachines(2), objectmodel.Batching{Strategy: objectmodel.BatchingCustom})
	if err == nil {
		t.Fatal("custom 策略必须显式报未实现")
	}
	if !strings.Contains(err.Error(), "custom") {
		t.Errorf("报错未点名 custom: %v", err)
	}
}

// TestSplitBatches_UnknownStrategyIsRejected 证明表外策略被拒(fail-closed)。
func TestSplitBatches_UnknownStrategyIsRejected(t *testing.T) {
	if _, err := SplitBatches(testMachines(2), objectmodel.Batching{Strategy: objectmodel.BatchingStrategy("bogus")}); err == nil {
		t.Fatal("表外策略必须被拒")
	}
}

// TestComputeBlastRadius_CountAndBreakdown 证明爆炸半径的台数与按 kind 的分解。
func TestComputeBlastRadius_CountAndBreakdown(t *testing.T) {
	intents := []objectmodel.Intent{
		{Kind: objectmodel.KindPackage, Target: "nginx", RiskClass: objectmodel.RiskClassLow},
		{Kind: objectmodel.KindPackage, Target: "curl", RiskClass: objectmodel.RiskClassLow},
		{Kind: objectmodel.KindService, Target: "nginx.service", RiskClass: objectmodel.RiskClassLow},
	}
	got := ComputeBlastRadius(intents, testMachines(3))
	if got.Count != 3 {
		t.Errorf("Count = %d, want 3", got.Count)
	}
	if got.KindBreakdown["package"] != 2 || got.KindBreakdown["service"] != 1 {
		t.Errorf("KindBreakdown = %v, want package:2 service:1", got.KindBreakdown)
	}
}

// TestComputeBlastRadius_MaxRiskHighWhenAnyHigh 证明任一 intent 高危则整体高危。
func TestComputeBlastRadius_MaxRiskHighWhenAnyHigh(t *testing.T) {
	intents := []objectmodel.Intent{
		{Kind: objectmodel.KindPackage, Target: "nginx", RiskClass: objectmodel.RiskClassLow},
		{Kind: objectmodel.KindPackage, Target: "kernel-core", RiskClass: objectmodel.RiskClassHigh},
	}
	if got := ComputeBlastRadius(intents, testMachines(2)); got.MaxRisk != objectmodel.RiskClassHigh {
		t.Errorf("MaxRisk = %q, want %q", got.MaxRisk, objectmodel.RiskClassHigh)
	}
}
