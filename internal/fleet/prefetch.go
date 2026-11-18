// internal/fleet/prefetch.go:批内并发的活性预检预取。
//
// 预检的语义是"每条 intent 一次"(挂死节点必须在它自己那条 intent 上被拦下),
// 但串行探完一整批节点会让 N 台"连得上但不回"的节点线性累加心跳时限。预取把
// 批内**节点之间**的预检并发化,把这段墙压回约一个心跳时限;下发本身仍是串行
// 的,顺序与停手语义一概不变。
package fleet

import (
	"context"
	"sync"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// probePrefetchConcurrency 是批内并发预检的探针上限。上限之外的节点排队,
// 免得一批 200 台瞬间开出 200 条 mTLS 连接。
const probePrefetchConcurrency = 64

// probeKey 标识一次预检结果:(节点, intent 下标)。
type probeKey struct {
	NodeID string
	Intent int
}

// probeCache 是批内预检结果缓存。零值(nil)即"无缓存",读取安全。
type probeCache map[probeKey]error

// lookup 查一次预检结果。ok=false 表示没有可用的缓存项,调用方须现场预检。
func (c probeCache) lookup(nodeID string, intent int) (error, bool) {
	err, ok := c[probeKey{NodeID: nodeID, Intent: intent}]
	return err, ok
}

// refusesWithoutRollbackPoint 报告这条 (节点,intent) 会不会在**下发前**被回滚点
// 判定拒掉。预取与 runOne 必须共用这一份判定:预取若自己写一遍,两处一旦漂移
// 就会出现"预取探了注定被拒的对" —— 白等一个心跳时限,还把"无回滚点"这个真实
// 原因掩盖成"心跳失败"。
func (s *Scheduler) refusesWithoutRollbackPoint(t DispatchTarget) bool {
	return s.effectiveRisk(t.Intent) == objectmodel.RiskClassHigh &&
		!intentHasRollbackPoint(t.Node, t.Intent.Kind)
}

// prefetchFirstIntentProbes 并发预取本批每台节点**首个** intent 的预检结果。
//
// 只取首个 intent 是刻意的:把 K 条 intent 的预检全提前,会让第 2..K 条拿着批首
// 那一刻的旧活性去下发 —— 中途死掉的节点被漏检,退化回工作 RPC 的兜底时限。
// 挂死节点在它自己的首个 intent 上照样被拦下(墙因此在批内塌缩),第 2..K 条仍由
// runOne 现调预检,当场判 N3。
//
// 某个探针失败**不**提前取消其余探针:探针集合必须由输入决定。让它随完成时序
// 收缩,同一份输入在不同负载下就会探到不同节点集合,结果与用例都变 flaky。
// 同理,ctx 取消不是节点判决 —— 取消时不写缓存,交给 runOne 现场预检,保留
// "取消 = 一次失败"的旧因果。
func (s *Scheduler) prefetchFirstIntentProbes(ctx context.Context, plan objectmodel.Plan, batch Batch, machines map[string]objectmodel.Machine, nodes map[string]Node) probeCache {
	cache := probeCache{}
	if s.Probe == nil || len(plan.Intents) == 0 || len(batch.NodeIDs) == 0 {
		return cache
	}

	intent := plan.Intents[0]
	sem := make(chan struct{}, probePrefetchConcurrency)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, nodeID := range batch.NodeIDs {
		t := DispatchTarget{
			Plan:    plan,
			Machine: machines[nodeID],
			Node:    nodes[nodeID],
			Intent:  intent,
			Index:   0,
		}
		// 拒绝判定先于预检:注定被拒的对一次都不探。
		if s.refusesWithoutRollbackPoint(t) {
			continue
		}
		wg.Add(1)
		go func(t DispatchTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := s.Probe(ctx, t)
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			cache[probeKey{NodeID: t.Node.NodeID, Intent: t.Index}] = err
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return cache
}
