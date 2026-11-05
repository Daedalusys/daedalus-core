// internal/fleet/batching.go:把 Plan 的目标机切成子批,并重算爆炸半径。
//
// count 按台数切块;importance 按 support_level 分两批(supported 先、experimental 后,
// 某档为空则不产生空批);custom 在 P0.5 显式未实现(报错而非静默退化成某一种)。
package fleet

import (
	"fmt"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// Batch 是一个待跑子批:Index 自 0 连续递增,NodeIDs 为该批的 node_id 列表。
type Batch struct {
	Index   int      `json:"index"`
	NodeIDs []string `json:"node_ids"`
}

// SplitBatches 按 b 把 machines 切成子批。
func SplitBatches(machines []objectmodel.Machine, b objectmodel.Batching) ([]Batch, error) {
	switch b.Strategy {
	case objectmodel.BatchingCount:
		if b.Size <= 0 {
			return nil, fmt.Errorf("fleet: batching.strategy=count 需 size>0, got %d", b.Size)
		}
		return chunkBySize(machines, b.Size), nil
	case objectmodel.BatchingImportance:
		return chunkByImportance(machines), nil
	case objectmodel.BatchingCustom:
		return nil, fmt.Errorf("fleet: batching.strategy=custom 未实现(P0.5 只做 count/importance)")
	}
	return nil, fmt.Errorf("fleet: batching.strategy 表外值 %q(P0.5 只做 count/importance)", b.Strategy)
}

// chunkBySize 按台数顺序切块。
func chunkBySize(machines []objectmodel.Machine, size int) []Batch {
	out := []Batch{}
	for start := 0; start < len(machines); start += size {
		end := start + size
		if end > len(machines) {
			end = len(machines)
		}
		out = append(out, Batch{Index: len(out), NodeIDs: machineIDs(machines[start:end])})
	}
	return out
}

// chunkByImportance 先 supported 后 experimental;某档为空则不产生空批。
func chunkByImportance(machines []objectmodel.Machine) []Batch {
	var supported, experimental []objectmodel.Machine
	for _, m := range machines {
		if m.SupportLevel == objectmodel.SupportLevelSupported {
			supported = append(supported, m)
		} else {
			experimental = append(experimental, m)
		}
	}
	out := []Batch{}
	for _, group := range [][]objectmodel.Machine{supported, experimental} {
		if len(group) == 0 {
			continue
		}
		out = append(out, Batch{Index: len(out), NodeIDs: machineIDs(group)})
	}
	return out
}

// machineIDs 取一批机器的 node_id 列表。
func machineIDs(machines []objectmodel.Machine) []string {
	out := make([]string, 0, len(machines))
	for _, m := range machines {
		out = append(out, m.MachineID)
	}
	return out
}

// ComputeBlastRadius 重算爆炸半径的声明值:台数 = 目标机数,按 intent 的资源 kind
// 计数,最大风险 = intents 的最大风险。MaxDowntime 不由本函数推断(人审在提交时给),
// 故留零值。
func ComputeBlastRadius(intents []objectmodel.Intent, machines []objectmodel.Machine) objectmodel.BlastRadius {
	breakdown := make(map[string]int, len(intents))
	for _, in := range intents {
		breakdown[string(in.Kind)]++
	}
	return objectmodel.BlastRadius{
		Count:         len(machines),
		KindBreakdown: breakdown,
		MaxRisk:       objectmodel.Plan{Intents: intents}.MaxRisk(),
	}
}
