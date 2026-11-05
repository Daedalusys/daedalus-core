// internal/fleet/scheduler.go:跨机 Plan 的调度。
//
// 逐批 → 逐机 → 逐 intent 下发,按 risk_class × N1/N2/N3 分桶:低危的瞬时失败
// 重试到 policy 的上限,高危的**任何**失败立即暂停整批;任一持久失败即整批 dirty
// 并停住不再前进。目标节点没有回滚点(snapshot capability NONE)时,高危 intent
// 在下发**之前**就被拒。
package fleet

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Daedalusys/daedalus-sdk/policy"
	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

const (
	// EnvClientCert / EnvClientKey / EnvCA 是控制面 mTLS 客户端凭证的路径来源
	// (私钥只从路径读,绝不写进 registry)。
	EnvClientCert = "DAEDALUS_FLEET_CLIENT_CERT"
	EnvClientKey  = "DAEDALUS_FLEET_CLIENT_KEY"
	EnvCA         = "DAEDALUS_FLEET_CA"

	// defaultCallTimeout 是单次 RPC 的兜底超时。
	defaultCallTimeout = 60 * time.Second
	// snapshotCapNone 是 proto 的"无回滚点"枚举名(节点行里以字符串存放)。
	snapshotCapNone = "SNAPSHOT_CAPABILITY_NONE"
)

// Outcome 是单次 intent 落地的结局分桶:N1 成功 / N2 瞬时失败 / N3 持久失败。
type Outcome int

const (
	OutcomeN1 Outcome = iota
	OutcomeN2
	OutcomeN3
	// OutcomeReRegister 是哨兵:agent 回了 FailedPrecondition(node_id 轮换)。
	// 既不是瞬时错误(不能盲目重试),也不是持久失败(不该直接判 dirty)。
	OutcomeReRegister Outcome = 3
)

// String 返回分桶的稳定词表(stdout JSON 契约用)。
func (o Outcome) String() string {
	switch o {
	case OutcomeN1:
		return "success"
	case OutcomeN2:
		return "transient"
	case OutcomeN3:
		return "persistent"
	case OutcomeReRegister:
		return "re_register"
	}
	return "unknown"
}

// ClassifyApply 把一次 ApplyIntent 的回报映射进三桶。纯函数:不碰网络、不写日志。
// 结果缺失(既无响应也无错误)绝不重试,一律 N3。
func ClassifyApply(resp *agentv1.ApplyIntentResponse, err error) Outcome {
	if err != nil {
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Aborted:
			return OutcomeN2
		case codes.FailedPrecondition:
			return OutcomeReRegister
		default:
			return OutcomeN3
		}
	}
	if resp == nil {
		return OutcomeN3
	}
	switch resp.GetResult() {
	case agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS:
		return OutcomeN1
	case agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_TRANSIENT_FAILURE:
		return OutcomeN2
	default:
		// 拒因(无回滚点 / 策略拒)、持久失败与 UNSPECIFIED 同归 N3。
		return OutcomeN3
	}
}

// StepResult 是一条 (intent, node) 的落地记录。
type StepResult struct {
	NodeID   string
	IntentID string
	Outcome  Outcome
	Result   agentv1.ApplyIntentResult
	TxID     string
	Message  string
}

// PlanRun 是一次 Plan 执行的完整记录。
type PlanRun struct {
	Plan      objectmodel.Plan
	Batches   []Batch
	Results   []StepResult
	Status    objectmodel.PlanStatus
	StoppedAt *Batch
}

// DispatchTarget 是一次下发的全部上下文。
type DispatchTarget struct {
	Plan    objectmodel.Plan
	Machine objectmodel.Machine
	Node    Node
	Intent  objectmodel.Intent
	Index   int
}

// Scheduler 驱动一次 Plan。Dispatch / Register / Now 是注入缝:生产用真实
// mTLS 客户端与系统时钟,测试塞 fake 以便断言"发了几次"甚至"一次都没发"。
type Scheduler struct {
	Registry    *Registry
	RetryMax    int
	CallTimeout time.Duration
	Now         func() time.Time
	Dispatch    func(ctx context.Context, t DispatchTarget) (StepResult, error)
	// Register 取回轮换后的权威节点行并刷新 registry;为 nil 时视为
	// "无 re-register 通道",轮换直接判持久失败。
	Register func(ctx context.Context, t DispatchTarget) (Node, error)

	// Policy 是风险判定表的来源(判定结果与 intent 声明取更严的一侧)。
	Policy *policy.Policy
	// dial 是生产下发的凭证配置基座;Addr 每次按目标节点覆盖。
	dial DialConfig
}

// NewScheduler 装配生产调度器:重试上限与风险判定表来自 pol,凭证路径来自环境
// (DAEDALUS_FLEET_CLIENT_CERT / _KEY / DAEDALUS_FLEET_CA)。
func NewScheduler(r *Registry, pol *policy.Policy) *Scheduler {
	s := &Scheduler{
		Registry:    r,
		RetryMax:    RetryMax(pol),
		CallTimeout: defaultCallTimeout,
		Now:         time.Now,
		Policy:      pol,
		dial:        dialConfigFromEnv(),
	}
	s.Dispatch = s.dialDispatch
	s.Register = s.dialRegister
	return s
}

// dialConfigFromEnv 装配基础 DialConfig(Addr 由每次下发按节点覆盖)。
func dialConfigFromEnv() DialConfig {
	return DialConfig{
		CertPath:    os.Getenv(EnvClientCert),
		KeyPath:     os.Getenv(EnvClientKey),
		CAPath:      os.Getenv(EnvCA),
		CallTimeout: defaultCallTimeout,
	}
}

// Run 执行一次 Plan。参数与前置条件(Plan 校验、节点已注册、爆炸半径守恒)
// 任一不满足都直接返回 error——此时还没有任何机器被改过,不产生 dirty 态。
func (s *Scheduler) Run(ctx context.Context, plan objectmodel.Plan) (*PlanRun, error) {
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("fleet: Plan 未通过校验,整批不启动: %w", err)
	}

	machines := make(map[string]objectmodel.Machine, len(plan.Machines))
	nodes := make(map[string]Node, len(plan.Machines))
	for _, m := range plan.Machines {
		n, ok, err := s.Registry.Get(m.MachineID)
		if err != nil {
			return nil, fmt.Errorf("fleet: 读取节点 %s 失败: %w", m.MachineID, err)
		}
		if !ok {
			return nil, fmt.Errorf("fleet: 节点 %s 未注册", m.MachineID)
		}
		machines[m.MachineID] = m
		nodes[m.MachineID] = n
	}

	// 爆炸半径守恒:重算值必须与人审时的声明一致,否则说明机器集被改过。
	computed := ComputeBlastRadius(plan.Intents, plan.Machines)
	if computed.Count != plan.BlastRadius.Count {
		return nil, fmt.Errorf("fleet: blast_radius.count 与目标机数不一致(got %d, want %d),拒绝整批", plan.BlastRadius.Count, computed.Count)
	}
	if computed.MaxRisk != plan.BlastRadius.MaxRisk {
		return nil, fmt.Errorf("fleet: blast_radius.max_risk 与 intents 实际最大风险不一致(got %q, want %q),拒绝整批", plan.BlastRadius.MaxRisk, computed.MaxRisk)
	}

	batches, err := SplitBatches(plan.Machines, plan.Batching)
	if err != nil {
		return nil, err
	}

	run := &PlanRun{Plan: plan, Batches: batches}
	for bi := range batches {
		batch := batches[bi]
		dirty := false
		for _, nodeID := range batch.NodeIDs {
			for i, intent := range plan.Intents {
				res := s.runOne(ctx, DispatchTarget{
					Plan:    plan,
					Machine: machines[nodeID],
					Node:    nodes[nodeID],
					Intent:  intent,
					Index:   i,
				})
				run.Results = append(run.Results, res)
				if res.Outcome == OutcomeN3 {
					dirty = true
					break
				}
			}
			if dirty {
				break
			}
		}
		if dirty {
			run.Status = objectmodel.PlanStatusDirty
			if bi+1 < len(batches) {
				next := batches[bi+1]
				run.StoppedAt = &next
			}
			return run, nil
		}
	}
	run.Status = objectmodel.PlanStatusComplete
	return run, nil
}

// runOne 下发一条 intent,并按分桶决定重试/暂停;总是返回一条 StepResult。
func (s *Scheduler) runOne(ctx context.Context, t DispatchTarget) StepResult {
	risk := s.effectiveRisk(t.Intent)

	// 高危 intent 打到没有回滚点的节点:连 RPC 都不发。
	if risk == objectmodel.RiskClassHigh && t.Node.SnapshotCap == snapshotCapNone {
		return s.fill(t, StepResult{
			Outcome: OutcomeN3,
			Result:  agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_REFUSED_SNAPSHOT_CAPABILITY_NONE,
			Message: fmt.Sprintf("节点 %s 无回滚点(snapshot capability=%s):high-risk intent 拒绝下发——无回滚点 ≠ 可继续",
				t.Node.NodeID, snapshotCapNone),
		})
	}

	// 只有低危允许自动重试(高危任何失败立即暂停)。
	retriesLeft := 0
	if risk == objectmodel.RiskClassLow {
		retriesLeft = s.RetryMax
	}
	reRegistered := false

	for {
		_ = s.markSeen(t.Node.NodeID)
		res, err := s.Dispatch(ctx, t)
		_ = s.markSeen(t.Node.NodeID)
		if err != nil {
			return s.fill(t, StepResult{Outcome: OutcomeN3, Message: fmt.Sprintf("fleet: 下发失败: %v", err)})
		}
		res = s.fill(t, res)

		switch res.Outcome {
		case OutcomeN1:
			return res
		case OutcomeReRegister:
			if reRegistered {
				res.Outcome = OutcomeN3
				res.Message = "fleet: re-register 后 node_id 仍不匹配,判持久失败"
				return res
			}
			// 只重试一次,且不占用 RetryMax 的重试预算。
			reRegistered = true
			fresh, rerr := s.reRegister(ctx, t)
			if rerr != nil {
				res.Outcome = OutcomeN3
				res.Message = fmt.Sprintf("fleet: re-register 失败,判持久失败: %v", rerr)
				return res
			}
			t.Node = fresh
		case OutcomeN2:
			if retriesLeft > 0 {
				retriesLeft--
				continue
			}
			// 重试用尽(或高危根本不许重试)→ 升格为持久失败。
			res.Outcome = OutcomeN3
			return res
		default:
			return res
		}
	}
}

// reRegister 处理 node_id 轮换:取回权威节点行,摘掉旧行再写新行。
func (s *Scheduler) reRegister(ctx context.Context, t DispatchTarget) (Node, error) {
	if s.Register == nil {
		return Node{}, fmt.Errorf("fleet: 未装配 re-register 通道")
	}
	fresh, err := s.Register(ctx, t)
	if err != nil {
		return Node{}, err
	}
	if fresh.NodeID == "" {
		return Node{}, fmt.Errorf("fleet: re-register 返回空 node_id")
	}
	if fresh.NodeID != t.Node.NodeID {
		if err := s.Registry.Delete(t.Node.NodeID); err != nil {
			return Node{}, err
		}
	}
	if err := s.Registry.Put(fresh); err != nil {
		return Node{}, err
	}
	return fresh, nil
}

// effectiveRisk 是 intent 的**生效**风险:策略判定表与 intent 声明取更严的一侧
// ——人审可把 low 显式标 high,反向不成立(判定表说 high 就不能被声明降成 low)。
func (s *Scheduler) effectiveRisk(in objectmodel.Intent) objectmodel.RiskClass {
	if in.RiskClass == objectmodel.RiskClassHigh {
		return objectmodel.RiskClassHigh
	}
	if ClassifyIntent(s.Policy, adapterKind(in.Kind), in.Target) == objectmodel.RiskClassHigh {
		return objectmodel.RiskClassHigh
	}
	return objectmodel.RiskClassLow
}

// adapterKind 把资源 kind 映射到 daedalus-tx 的适配器名,供风险判定表匹配;
// 其余 kind 原样返回,由 ClassifyIntent 的 fail-closed 兜底判 high。
func adapterKind(k objectmodel.Kind) string {
	switch k {
	case objectmodel.KindPackage, objectmodel.KindService:
		return string(k) + ".set"
	}
	return string(k)
}

// fill 补齐 StepResult 的标识字段:NodeID 取当下节点(可能已被 re-register 换成
// 新 id),IntentID 恒为 <plan_id>/<machine_id>/<intent 下标>。
func (s *Scheduler) fill(t DispatchTarget, res StepResult) StepResult {
	res.NodeID = t.Node.NodeID
	res.IntentID = intentID(t)
	return res
}

// intentID 是 intent 的稳定标识:agent 侧照原样回写审计,故必须可复现。
func intentID(t DispatchTarget) string {
	return fmt.Sprintf("%s/%s/%d", t.Plan.ID, t.Machine.MachineID, t.Index)
}

// markSeen 刷新节点的 last_seen。观测是尽力而为的:刷新失败不阻断下发。
func (s *Scheduler) markSeen(nodeID string) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	return s.Registry.MarkSeen(nodeID, now().Unix())
}

// callContext 给一次 RPC 套上超时。
func (s *Scheduler) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.CallTimeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// dialDispatch 是生产下发实现:Dial 到目标节点 → ApplyIntent → 归一到三桶。
// 一次 RPC 只带一条 intent(agent 侧事务通道是单进口的)。
func (s *Scheduler) dialDispatch(ctx context.Context, t DispatchTarget) (StepResult, error) {
	cfg := s.dial
	cfg.Addr = t.Node.Addr
	cfg.CallTimeout = s.CallTimeout

	client, closer, err := Dial(ctx, cfg)
	if err != nil {
		return StepResult{}, err
	}
	defer func() { _ = closer() }()

	callCtx, cancel := s.callContext(ctx)
	defer cancel()

	resp, err := client.ApplyIntent(callCtx, &agentv1.ApplyIntentRequest{
		IntentId:  intentID(t),
		Kind:      string(t.Intent.Kind),
		Target:    t.Intent.Target,
		SpecJson:  string(t.Intent.Spec),
		RiskClass: riskClassToProto(s.effectiveRisk(t.Intent)),
	})
	out := ClassifyApply(resp, err)

	res := StepResult{Outcome: out}
	switch {
	case resp != nil:
		res.Result = resp.GetResult()
		res.TxID = resp.GetTxId()
		res.Message = resp.GetErrorMessage()
	case err != nil:
		res.Message = err.Error()
	}
	return res, nil
}

// dialRegister 是生产 re-register 实现:重新握手取回权威节点行,保留本地已知的
// 自报字段(agent_id / 地址 / 发行版…),只接受控制面权威的 node_id 与 snapshot 能力。
func (s *Scheduler) dialRegister(ctx context.Context, t DispatchTarget) (Node, error) {
	cfg := s.dial
	cfg.Addr = t.Node.Addr
	cfg.CallTimeout = s.CallTimeout

	client, closer, err := Dial(ctx, cfg)
	if err != nil {
		return Node{}, err
	}
	defer func() { _ = closer() }()

	callCtx, cancel := s.callContext(ctx)
	defer cancel()

	resp, err := client.Register(callCtx, &agentv1.RegisterRequest{
		AgentId:         t.Node.AgentID,
		AgentVersion:    t.Node.AgentVersion,
		CertFingerprint: t.Node.CertFingerprint,
	})
	if err != nil {
		return Node{}, err
	}
	if resp.GetNodeId() == "" {
		return Node{}, fmt.Errorf("fleet: Register 未返回 node_id")
	}

	fresh := t.Node
	fresh.NodeID = resp.GetNodeId()
	if cap := resp.GetSnapshotCapability(); cap != agentv1.SnapshotCapability_SNAPSHOT_CAPABILITY_UNSPECIFIED {
		fresh.SnapshotCap = cap.String()
	}
	return fresh, nil
}

// riskClassToProto 把风险词表映射到 proto 枚举(表外值折向 UNSPECIFIED,
// 由 agent 侧按 policy 默认值处置)。
func riskClassToProto(r objectmodel.RiskClass) agentv1.RiskClass {
	switch r {
	case objectmodel.RiskClassLow:
		return agentv1.RiskClass_RISK_CLASS_LOW
	case objectmodel.RiskClassHigh:
		return agentv1.RiskClass_RISK_CLASS_HIGH
	}
	return agentv1.RiskClass_RISK_CLASS_UNSPECIFIED
}
