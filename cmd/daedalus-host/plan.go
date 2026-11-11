// cmdPlan 实现 plan submit / plan status / plan advance —— 控制面唯一的跨机入口。
//
// 退出码:0 提交并执行完成(状态在 JSON 里,complete/dirty 都算"提交成功"),
// 1 运行期失败,2 用法错误。
//
// ★ 恢复边界:proto v1 只有五个 RPC,**没有** rollback RPC,回滚是本地的
// (spec §7.3 C1)。故暂停后的三条人审路径里,skip / force 由控制面实现,
// roll back 只**渲染**每台已成功节点的本地 `daedalus-tx rollback <tx_id>` 命令
// 交给人执行 —— 控制面不代执行远程回滚。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

const (
	// planRecordFile 是运行记录文件名(与 nodes.jsonl 同根,append-only)。
	planRecordFile = "plans.jsonl"
	// planRunTimeout 是 submit/advance 整体执行的兜底超时。
	planRunTimeout = 10 * time.Minute
	// planUsage 是 plan 子命令的用法文本。
	planUsage = "usage: daedalus-host plan submit <plan.json> [--max-downtime <dur>]\n" +
		"       daedalus-host plan status <plan-id>\n" +
		"       daedalus-host plan advance <plan-id> (--skip <node-id> | --force)"
)

// planStatusOutput 是 plan submit/status/advance 的 stdout 契约(单行 JSON,
// 字段名是消费方按字面解析的线上形状)。
type planStatusOutput struct {
	PlanID   string         `json:"plan_id"`
	Status   string         `json:"status"`
	MaxRisk  string         `json:"max_risk"`
	Batches  []fleet.Batch  `json:"batches"`
	Results  []planResult   `json:"results"`
	Recovery []planRecovery `json:"recovery"`
}

// planResult 是单条 (intent, node) 的落地记录。NodeKind / IntentKind 是恢复线索
// 的依据:节点形态决定还原手段(agent 侧没有 daedalus-tx),intent 种类决定给哪种
// 恢复动作。旧记录缺这两个字段时按空值读入,恢复线索退回既有行为。
type planResult struct {
	NodeID     string `json:"node_id"`
	IntentID   string `json:"intent_id"`
	NodeKind   string `json:"node_kind"`
	IntentKind string `json:"intent_kind"`
	Outcome    string `json:"outcome"`
	Result     string `json:"result"`
	TxID       string `json:"tx_id"`
	Message    string `json:"message"`
}

// planRecovery 是 dirty 时的本地恢复线索(控制面不代执行)。
type planRecovery struct {
	NodeID  string `json:"node_id"`
	TxID    string `json:"tx_id"`
	Command string `json:"command"`
}

// planRecord 是 plans.jsonl 的一行:完整 Plan(续跑要用)+ 该次运行的对外形状。
type planRecord struct {
	Plan objectmodel.Plan `json:"plan"`
	Run  planStatusOutput `json:"run"`
}

// fleetFlags 是控制面子命令的极简旗标解析结果。
type fleetFlags struct {
	strs  map[string]string
	bools map[string]bool
	rest  []string
}

// parseFleetFlags 解析控制面子命令的参数:支持 `--name value` 与 `--name=value`
// 出现在**位置参数前后**(用法钉的是 `<addr> --agent-id <id>` 这种形态,标准库
// flag 在遇到首个位置参数即停止解析,用不了)。未知旗标一律拒。
func parseFleetFlags(args []string, strNames, boolNames []string) (fleetFlags, error) {
	isStr := make(map[string]bool, len(strNames))
	for _, n := range strNames {
		isStr[n] = true
	}
	isBool := make(map[string]bool, len(boolNames))
	for _, n := range boolNames {
		isBool[n] = true
	}

	f := fleetFlags{strs: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			f.rest = append(f.rest, arg)
			continue
		}
		name, val, hasVal := strings.TrimPrefix(arg, "--"), "", false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = k, v, true
		}
		switch {
		case isBool[name]:
			if hasVal {
				return f, fmt.Errorf("--%s 不接受取值", name)
			}
			f.bools[name] = true
		case isStr[name]:
			if !hasVal {
				if i+1 >= len(args) {
					return f, fmt.Errorf("--%s 需要取值", name)
				}
				i++
				val = args[i]
			}
			f.strs[name] = val
		default:
			return f, fmt.Errorf("未知旗标 %q", arg)
		}
	}
	return f, nil
}

// cmdPlan 分派 plan 子命令。
func cmdPlan(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, planUsage)
		return exitUsage
	}
	switch args[0] {
	case "submit":
		return cmdPlanSubmit(stdout, stderr, args[1:])
	case "status":
		return cmdPlanStatus(stdout, stderr, args[1:])
	case "advance":
		return cmdPlanAdvance(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "daedalus-host plan: 未知子命令 %q\n\n", args[0])
		fmt.Fprintln(stderr, planUsage)
		return exitUsage
	}
}

// cmdPlanSubmit 读 Plan JSON → 校验 → 执行 → 落运行记录 → 打印状态形状。
func cmdPlanSubmit(stdout, stderr io.Writer, args []string) int {
	f, err := parseFleetFlags(args, []string{"max-downtime"}, nil)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host plan submit: %v\n", err)
		return exitUsage
	}
	if len(f.rest) != 1 {
		fmt.Fprintln(stderr, "usage: daedalus-host plan submit <plan.json> [--max-downtime <dur>]")
		return exitUsage
	}
	path := f.rest[0]
	var maxDowntime time.Duration
	if raw, ok := f.strs["max-downtime"]; ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(stderr, "daedalus-host plan submit: --max-downtime %q 不是合法时长: %v\n", raw, err)
			return exitUsage
		}
		maxDowntime = d
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host plan submit: 读取 %s 失败: %v\n", path, err)
		return exitUsage
	}
	var plan objectmodel.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		fmt.Fprintf(stderr, "daedalus-host plan submit: %s 不是合法 Plan JSON: %v\n", path, err)
		return exitUsage
	}
	if maxDowntime > 0 {
		plan.BlastRadius.MaxDowntime = maxDowntime
	}
	if err := plan.Validate(); err != nil {
		fmt.Fprintf(stderr, "daedalus-host plan submit: %v\n", err)
		return exitUsage
	}

	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintln(stderr, "plan submit:", err)
		return exitRuntime
	}
	pol, err := policy.LoadOrDefault()
	if err != nil {
		fmt.Fprintln(stderr, "plan submit:", err)
		return exitRuntime
	}
	ctx, cancel := planCtx()
	defer cancel()
	run, err := fleet.NewScheduler(reg, pol).Run(ctx, plan)
	if err != nil {
		fmt.Fprintln(stderr, "plan submit:", err)
		return exitRuntime
	}
	out := renderRun(run)
	if err := appendPlanRecord(reg.Root(), planRecord{Plan: plan, Run: out}); err != nil {
		fmt.Fprintln(stderr, "plan submit: 落运行记录失败:", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, mustJSON(out))
	controlPlaneAudit("host_plan_submit", map[string]string{"plan_id": plan.ID, "status": out.Status}, exitOK)
	return exitOK
}

// cmdPlanStatus 打印该 plan-id 的最新一次运行(append-only,最后一行即最新态)。
func cmdPlanStatus(stdout, stderr io.Writer, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: daedalus-host plan status <plan-id>")
		return exitUsage
	}
	planID := args[0]
	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintln(stderr, "plan status:", err)
		return exitRuntime
	}
	rec, found, err := lastPlanRecord(reg.Root(), planID)
	if err != nil {
		fmt.Fprintln(stderr, "plan status:", err)
		return exitRuntime
	}
	if !found {
		// 未知 plan-id 是运行期失败(1),不是用法错误(2)。
		fmt.Fprintf(stderr, "daedalus-host plan status: 未知 plan-id %s\n", planID)
		controlPlaneAudit("host_plan_status", map[string]string{"plan_id": planID}, exitRuntime)
		return exitRuntime
	}
	// recovery 是派生字段:读侧重算,保证无论谁写了这份记录都给出同一条契约。
	rec.Run.Recovery = recoveryFor(rec.Run.Status, rec.Run.Results)
	rec.Run.normalize()
	fmt.Fprintln(stdout, mustJSON(rec.Run))
	controlPlaneAudit("host_plan_status", map[string]string{"plan_id": planID, "status": rec.Run.Status}, exitOK)
	return exitOK
}

// cmdPlanAdvance 是 spec §4.3 的人在环恢复:--skip 接受某节点的持久失败并把它
// 从待跑批次摘掉;--force 不摘任何节点,强行续跑。两者都只对 dirty/paused 有效。
func cmdPlanAdvance(stdout, stderr io.Writer, args []string) int {
	f, err := parseFleetFlags(args, []string{"skip"}, []string{"force"})
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host plan advance: %v\n", err)
		return exitUsage
	}
	skip, force := f.strs["skip"], f.bools["force"]
	if len(f.rest) != 1 || ((skip == "") == !force) {
		fmt.Fprintln(stderr, "usage: daedalus-host plan advance <plan-id> (--skip <node-id> | --force)")
		return exitUsage
	}
	planID := f.rest[0]

	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintln(stderr, "plan advance:", err)
		return exitRuntime
	}
	prev, found, err := lastPlanRecord(reg.Root(), planID)
	if err != nil {
		fmt.Fprintln(stderr, "plan advance:", err)
		return exitRuntime
	}
	if !found {
		fmt.Fprintf(stderr, "daedalus-host plan advance: 未知 plan-id %s\n", planID)
		return exitRuntime
	}
	if prev.Run.Status != string(objectmodel.PlanStatusDirty) && prev.Run.Status != string(objectmodel.PlanStatusPaused) {
		// 非 dirty/paused 的 Plan 不需要人介入:拒绝而不是"顺手再跑一遍"。
		fmt.Fprintf(stderr, "daedalus-host plan advance: plan %s 当前 status=%s,只有 dirty/paused 才需要人介入\n",
			planID, prev.Run.Status)
		controlPlaneAudit("host_plan_advance", map[string]string{"plan_id": planID, "status": prev.Run.Status}, exitUsage)
		return exitUsage
	}

	merged := prev.Run
	if skip != "" {
		if !isPlanMachine(prev.Plan, skip) {
			fmt.Fprintf(stderr, "daedalus-host plan advance: --skip %s 不在该 Plan 的目标机里\n", skip)
			return exitUsage
		}
	}
	merged.Results = markSkippedResults(merged.Results, skip)

	pending := planPendingMachines(prev.Plan, merged.Results)
	if skip != "" {
		pending = removeMachine(pending, skip)
	}

	if len(pending) == 0 {
		if planAllSettled(prev.Plan, merged.Results) {
			merged.Status = string(objectmodel.PlanStatusComplete)
		}
		merged.Batches = []fleet.Batch{}
		merged.Recovery = recoveryFor(merged.Status, merged.Results)
	} else {
		next := prev.Plan
		next.Machines = pending
		next.BlastRadius = fleet.ComputeBlastRadius(next.Intents, pending)
		next.BlastRadius.MaxDowntime = prev.Plan.BlastRadius.MaxDowntime
		next.Status = ""
		if err := next.Validate(); err != nil {
			fmt.Fprintf(stderr, "daedalus-host plan advance: 续跑子 Plan 不合法: %v\n", err)
			return exitUsage
		}
		pol, err := policy.LoadOrDefault()
		if err != nil {
			fmt.Fprintln(stderr, "plan advance:", err)
			return exitRuntime
		}
		ctx, cancel := planCtx()
		defer cancel()
		run, err := fleet.NewScheduler(reg, pol).Run(ctx, next)
		if err != nil {
			fmt.Fprintln(stderr, "plan advance:", err)
			return exitRuntime
		}
		nextOut := renderRun(run)
		// 子跑干净且整张 Plan 都收尾了才算 complete;仍有半成功/未跑的机器就还是 dirty。
		if nextOut.Status == string(objectmodel.PlanStatusComplete) && planAllSettled(prev.Plan, append(merged.Results, nextOut.Results...)) {
			merged.Status = string(objectmodel.PlanStatusComplete)
		} else {
			merged.Status = string(objectmodel.PlanStatusDirty)
		}
		merged.Batches = nextOut.Batches
		merged.Results = append(merged.Results, nextOut.Results...)
		merged.Recovery = recoveryFor(merged.Status, merged.Results)
	}

	if err := appendPlanRecord(reg.Root(), planRecord{Plan: prev.Plan, Run: merged}); err != nil {
		fmt.Fprintln(stderr, "plan advance: 落运行记录失败:", err)
		return exitRuntime
	}
	merged.Recovery = recoveryFor(merged.Status, merged.Results)
	merged.normalize()
	fmt.Fprintln(stdout, mustJSON(merged))
	controlPlaneAudit("host_plan_advance", map[string]string{"plan_id": planID, "status": merged.Status}, exitOK)
	return exitOK
}

// renderRun 把一次运行渲染成 stdout 契约;空集一律 [] 而非 null(下游按数组消费)。
func renderRun(run *fleet.PlanRun) planStatusOutput {
	out := planStatusOutput{
		PlanID:  run.Plan.ID,
		Status:  string(run.Status),
		MaxRisk: string(run.Plan.MaxRisk()),
		Batches: run.Batches,
		Results: []planResult{},
	}
	if out.Batches == nil {
		out.Batches = []fleet.Batch{}
	}
	for _, res := range run.Results {
		out.Results = append(out.Results, planResult{
			NodeID:     res.NodeID,
			IntentID:   res.IntentID,
			NodeKind:   string(res.NodeKind),
			IntentKind: res.IntentKind,
			Outcome:    res.Outcome.String(),
			Result:     protoResultWord(res.Result),
			TxID:       res.TxID,
			Message:    res.Message,
		})
	}
	out.Recovery = recoveryFor(out.Status, out.Results)
	out.normalize()
	return out
}

// normalize 把空集合统一成 [](下游按数组消费:null 会让 jq 与 shell 脚本出错)。
func (o *planStatusOutput) normalize() {
	if o.Batches == nil {
		o.Batches = []fleet.Batch{}
	}
	if o.Results == nil {
		o.Results = []planResult{}
	}
	if o.Recovery == nil {
		o.Recovery = []planRecovery{}
	}
}

// recoveryFor 只在 dirty 且已有成功落地的节点时给出本地恢复线索——控制面
// 不代执行远程回滚(proto v1 没有 rollback RPC,回滚是本地的)。
//
// 线索按**节点形态**分支:daedalus-tx 只装在 atomic 镜像里,给 agent 节点那条
// 命令等于指向一套它根本没有的工具。旧运行记录没有 node_kind(读成空串)按
// atomic 处理,保持既有命令逐字不变。
func recoveryFor(status string, results []planResult) []planRecovery {
	out := []planRecovery{}
	if status != string(objectmodel.PlanStatusDirty) {
		return out
	}
	for _, r := range results {
		if r.Outcome != fleet.OutcomeN1.String() || r.TxID == "" {
			continue
		}
		cmd := "daedalus-tx rollback " + r.TxID
		if r.NodeKind == string(objectmodel.MachineKindAgent) {
			cmd = agentRecoveryCommand(r.IntentKind)
		}
		out = append(out, planRecovery{
			NodeID:  r.NodeID,
			TxID:    r.TxID,
			Command: cmd,
		})
	}
	return out
}

// agentRecoveryCommand 给 agent 节点上的**已落地**变更一条人工恢复线索。
// agent 侧按 kind 划定回滚点:file 有单文件快照可还原,package / service 没有
// 自动还原手段(spec §2),只能指回包管理器 / 服务管理器。
func agentRecoveryCommand(intentKind string) string {
	switch intentKind {
	case string(objectmodel.KindPackage):
		return "在目标机上用包管理器回退(dnf history undo <id> 或 apt-get -f install),细节见 agent 审计日志"
	case string(objectmodel.KindService):
		return "在目标机上用 systemctl 停掉对应单元,细节见 agent 审计日志"
	case string(objectmodel.KindFile):
		// 这条线索只出现在「本节点已成功落地、Plan 因别的节点转 dirty」时,
		// 所以不能说"已还原"——agent 改成功之后并没有回滚,只是 apply 前留了
		// 单文件快照。人要知道的是怎么把它拷回去。
		return "该文件在 apply 前已由 agent 快照(路径见 agent 审计日志),需回退时在目标机上把快照拷回原路径"
	}
	return "该变更在 agent 上没有自动还原手段,细节见 agent 审计日志"
}

// protoResultWord 把 proto 枚举名归一成 SDK 侧冻结的小写词表
// (APPLY_INTENT_RESULT_SUCCESS → success),与 objectmodel 的跨语言契约同源。
func protoResultWord(name interface{ String() string }) string {
	return strings.ToLower(strings.TrimPrefix(name.String(), "APPLY_INTENT_RESULT_"))
}

// markSkippedResults 把该节点名下所有结果标成人已接受。
func markSkippedResults(results []planResult, nodeID string) []planResult {
	if nodeID == "" {
		return results
	}
	out := make([]planResult, 0, len(results))
	for _, r := range results {
		if r.NodeID == nodeID && !strings.HasPrefix(r.Message, "skipped-by-human:") {
			r.Message = "skipped-by-human: " + r.Message
		}
		out = append(out, r)
	}
	return out
}

// machineOfIntentID 取 intent_id(<plan_id>/<machine_id>/<intent 下标>)里的机器段。
// 用 intent_id 而非结果的 node_id 认机器:node_id 会在 re-register 后轮换,
// 而 intent_id 里的 machine_id 恒为 Plan 里的原值。
func machineOfIntentID(intentID string) string {
	parts := strings.Split(intentID, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-2]
}

// planPendingMachines 返回**还没有任何成功落地**的目标机(保持 Plan 原顺序)。
//
// 已经部分成功的机器**不进**待跑集:objectmodel.Plan 的 intents 作用于所有目标机,
// 无法只重跑其中一条,整机重跑会把已成功的 intent 再应用一遍(重复副作用 +
// 重复 intent_id)。这类机器要由人显式 skip 或本地 rollback 收尾。
func planPendingMachines(plan objectmodel.Plan, results []planResult) []objectmodel.Machine {
	successBy := make(map[string]int, len(plan.Machines))
	for _, r := range results {
		if r.Outcome == fleet.OutcomeN1.String() {
			successBy[machineOfIntentID(r.IntentID)]++
		}
	}
	out := []objectmodel.Machine{}
	for _, m := range plan.Machines {
		if successBy[m.MachineID] == 0 {
			out = append(out, m)
		}
	}
	return out
}

// planAllSettled 报告每台目标机都"已收尾":全部 intent 成功,或该机的结果已被
// 人显式接受(skipped-by-human)。有任何一台既没跑完也没被接受 → 还没收尾。
func planAllSettled(plan objectmodel.Plan, results []planResult) bool {
	successes := make(map[string]int, len(plan.Machines))
	accepted := make(map[string]bool, len(plan.Machines))
	for _, r := range results {
		machine := machineOfIntentID(r.IntentID)
		if r.Outcome == fleet.OutcomeN1.String() {
			successes[machine]++
		}
		if strings.HasPrefix(r.Message, "skipped-by-human:") {
			accepted[machine] = true
		}
	}
	for _, m := range plan.Machines {
		if successes[m.MachineID] >= len(plan.Intents) || accepted[m.MachineID] {
			continue
		}
		return false
	}
	return true
}

// removeMachine 从机器列表里摘掉一台(不存在则原样返回)。
func removeMachine(machines []objectmodel.Machine, nodeID string) []objectmodel.Machine {
	out := make([]objectmodel.Machine, 0, len(machines))
	for _, m := range machines {
		if m.MachineID == nodeID {
			continue
		}
		out = append(out, m)
	}
	return out
}

// isPlanMachine 报告 node_id 是否是该 Plan 的目标机之一。
func isPlanMachine(plan objectmodel.Plan, nodeID string) bool {
	for _, m := range plan.Machines {
		if m.MachineID == nodeID {
			return true
		}
	}
	return false
}

// planCtx 给一次 Plan 执行套上兜底超时;调用方必须 defer cancel。
func planCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), planRunTimeout)
}

// appendPlanRecord 追加一行运行记录(append-only:人介入几次就留几行)。
func appendPlanRecord(root string, rec planRecord) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, planRecordFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// lastPlanRecord 取该 plan-id 的最后一行(最新态);文件不存在视为"还没提交过"。
func lastPlanRecord(root, planID string) (planRecord, bool, error) {
	f, err := os.Open(filepath.Join(root, planRecordFile))
	if err != nil {
		if os.IsNotExist(err) {
			return planRecord{}, false, nil
		}
		return planRecord{}, false, err
	}
	defer f.Close()

	var last planRecord
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec planRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// 运行记录损坏即 fail-closed:不能拿半截记录当最新态。
			return planRecord{}, false, fmt.Errorf("运行记录行损坏: %w", err)
		}
		if rec.Plan.ID == planID {
			last = rec
			found = true
		}
	}
	if err := sc.Err(); err != nil {
		return planRecord{}, false, err
	}
	return last, found, nil
}

// mustJSON 渲染单行 JSON(所有字段都是可序列化类型,失败是不可能的编程错误)。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// controlPlaneAudit 落一条控制面审计:与 hostAudit 同一哈希链,args 换成
// plan/node 语义的键值(dir/id 是插件宿主的形状,对控制面不适用)。
func controlPlaneAudit(tool string, fields map[string]string, code int) {
	outcome := "success"
	switch code {
	case exitRuntime:
		outcome = "error"
	case exitUsage:
		outcome = "denied"
	}
	argsJSON := marshalArgs(fields)
	v, err := audit.ParseValue(string(argsJSON))
	if err != nil {
		v = audit.NewString(string(argsJSON))
	}
	_, _ = audit.LogAudit(audit.Entry{
		Identity: hostIdentity,
		Tool:     tool,
		Args:     v,
		Outcome:  outcome,
	})
}
