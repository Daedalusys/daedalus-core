// finding.go 定义发现模型与跨轮变化四分类。
//
// 身份 invariant:一条清单项在同一节点上同一时刻只有一条活跃发现,身份 = (node, item_id);
// 去重与邮件分组都靠它,不靠模型措辞。
package patrol

import "sort"

// 发现状态词表。problem 是「这一项有问题」,ok 是「这一轮看过,没问题」。
const (
	StatusProblem = "problem"
	StatusOK      = "ok"
)

// Evidence 是一次工具调用的原文证据。Input 是调用参数的 JSON 文本、Output 是工具返回原文;
// 证据只接受原文,模型没有改写权 —— 否则「证据」就成了模型自己的转述。
type Evidence struct {
	Tool       string `json:"tool"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	ReturnCode int    `json:"returncode"`
}

// Finding 是一轮巡检里某个清单项的发现。
type Finding struct {
	Node     string     `json:"node"`
	ItemID   string     `json:"item_id"`
	Status   string     `json:"status"`
	Severity string     `json:"severity"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Changes 是一轮巡检相对上一轮的变化。前三类才是「变化」,持续项不进邮件。
type Changes struct {
	Added     []Finding `json:"added"`
	Worsened  []Finding `json:"worsened"`
	Recovered []Finding `json:"recovered"`
	Ongoing   []Finding `json:"ongoing"`
}

// Classify 把本轮发现与上一轮的活跃状态比对成四类。
//
// prev 的键是 item_id(状态文件按节点分文件存放);两侧都带 node 时节点不同即不同身份,
// 因此跨节点混用同一个 map 也不会把两台机器的同名项串成一条。
// 本轮状态除了明确的 ok 都按「有问题」处理(fail-closed:未知状态不能算正常);
// 恢复条目带上一轮的发现 —— 邮件要说明「之前报的是什么,现在没了」。
//
// cur 的 item_id 必须唯一:唯一性由 ReadFindings 在入口拒重复来保证,本函数不做
// 二次归并 —— 静默归并会把「上游把同一项报了两遍」这个真问题藏起来。
func Classify(prev map[string]Finding, cur []Finding) Changes {
	var ch Changes
	matched := make(map[string]struct{}, len(cur))

	for _, f := range cur {
		old, ok := activePrev(prev, f)
		switch {
		case !isProblem(f):
			// 本轮判正常:上一轮有活跃问题才算恢复,否则这一项本来就没变化。
			if ok {
				ch.Recovered = append(ch.Recovered, old)
				matched[f.ItemID] = struct{}{}
			}
		case !ok:
			ch.Added = append(ch.Added, f)
		case severityRank(f.Severity) > severityRank(old.Severity):
			ch.Worsened = append(ch.Worsened, f)
			matched[f.ItemID] = struct{}{}
		default:
			// 严重级持平或下调都归持续;下调以条目自身的严重级呈现,不算恶化也不算恢复。
			ch.Ongoing = append(ch.Ongoing, f)
			matched[f.ItemID] = struct{}{}
		}
	}

	// 上一轮的活跃项本轮压根没出现(模型没再报它)同样算恢复。
	// 按 id 排序,保证同一份输入产出同一份结果。
	ids := make([]string, 0, len(prev))
	for id := range prev {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := matched[id]; ok {
			continue
		}
		if !isProblem(prev[id]) {
			continue
		}
		ch.Recovered = append(ch.Recovered, prev[id])
	}
	return ch
}

// ActiveFindings 从一轮的发现里挑出活跃项(状态不是 ok),键是 item_id。
// 这是落进状态文件、供下一轮比对的形状:ok 项不进状态,它们下轮再出问题算新增。
//
// cur 的 item_id 必须唯一(由 ReadFindings 保证):这里的后写覆盖只是取最后一条,
// 不是归并策略 —— 重复在入口就被拒了。
func ActiveFindings(cur []Finding) map[string]Finding {
	st := make(map[string]Finding, len(cur))
	for _, f := range cur {
		if isProblem(f) {
			st[f.ItemID] = f
		}
	}
	return st
}

// isProblem 判断一条发现是否表示「有问题」。只有明确的 ok 算正常:
// 未知/缺失状态一律按有问题处理,避免把异常静默吞掉。
func isProblem(f Finding) bool { return f.Status != StatusOK }

// activePrev 取上一轮同一身份的活跃发现。历史状态里状态是 ok 的项没有活跃问题。
func activePrev(prev map[string]Finding, cur Finding) (Finding, bool) {
	old, ok := prev[cur.ItemID]
	if !ok || !isProblem(old) {
		return Finding{}, false
	}
	if old.Node != "" && cur.Node != "" && old.Node != cur.Node {
		return Finding{}, false
	}
	return old, true
}

// severityRank 把严重级映射成可比较的序。表外值排在 info 之下:
// 坏掉的严重级不会凭空造出「恶化」告警。
func severityRank(s string) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}
