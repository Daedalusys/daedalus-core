package main

// commands.go —— 五个子命令的编排(todo 15)。
//
// 分工: 本文件只做"解析 → 调 internal/tx 与适配器 → 盖章 → 打印"的流程;
// 生命周期/日志/回滚计划在 internal/tx, 审计播种在 internal/audit, 盖章链在 stamp.go。
// 一切副作用之前先过 id 形状门(wantIDArgs), 非法 id 绝不触碰日志路径。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/daedalus-os/daedalus/core/internal/tx"
)

// cmdBegin 生成新事务(随机 id + proposed 日志), 盖 step-0 创世 begin 审计, 打印 tx_id。
func cmdBegin(stdout, stderr io.Writer) int {
	t, err := tx.Begin()
	if err != nil {
		return failRuntime(stdout, stderr, fmt.Sprintf("创建事务失败: %v", err))
	}
	// begin 步 0: 传空 prevHash, 由 internal/audit 的 flock 内播种取最近非 tx 记录哈希。
	stampTx("begin", t.ID, 0, "", "success", nil)
	printJSON(stdout, beginDoc{TxID: t.ID})
	return exitOK
}

// cmdPropose 向 proposed 事务追加一个适配器步骤(仅快照, 无副作用), 盖**空 TxID** 外围审计。
func cmdPropose(args []string, stdout, stderr io.Writer) int {
	args, unitDir, ok := extractUnitDir(args, stdout, stderr)
	if !ok {
		return exitUsage
	}
	if len(args) != 3 {
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
	id := args[0]
	if !txIDPattern.MatchString(id) {
		return failUsage(stdout, stderr, fmt.Sprintf("事务 ID 非法: %q (需匹配 ^[a-f0-9]{16}$)", id))
	}
	adapterName, rawArgs := args[1], args[2]

	var stepArgs json.RawMessage
	if !json.Valid([]byte(rawArgs)) {
		return failUsage(stdout, stderr, "args-json 不是合法 JSON")
	}
	stepArgs = json.RawMessage(rawArgs)

	a, ok2 := lookupAdapter(adapterName)
	if !ok2 {
		return failRuntime(stdout, stderr, fmt.Sprintf("未知适配器: %s", adapterName))
	}
	t, code, ok3 := mustLoad(id, stdout, stderr)
	if !ok3 {
		return code
	}
	before, after, perr := a.Propose(withUnitDir(context.Background(), unitDir), stepArgs)
	if perr != nil {
		stampPlain("propose", id, "error")
		return failRuntime(stdout, stderr, fmt.Sprintf("propose 失败: %v", perr))
	}
	if err := t.Append(tx.Step{Adapter: adapterName, Args: stepArgs, BeforeState: before, AfterState: after}); err != nil {
		stampPlain("propose", id, "error")
		return failRuntime(stdout, stderr, fmt.Sprintf("追加步骤失败: %v", err))
	}
	stampPlain("propose", id, "success")
	printJSON(stdout, t)
	return exitOK
}

// cmdApply 依序执行步骤(proposed→applying→applied), 每步盖升序 tx_apply 事务条目;
// 任一步失败 → MarkFailed, 后续步不执行, exit 1。成功后回滚计划固化于日志。
func cmdApply(args []string, stdout, stderr io.Writer) int {
	args, unitDir, okf := extractUnitDir(args, stdout, stderr)
	if !okf {
		return exitUsage
	}
	ctx := withUnitDir(context.Background(), unitDir)
	id, code, ok := wantIDArgs(args, stdout, stderr)
	if !ok {
		return code
	}
	t, code, ok := mustLoad(id, stdout, stderr)
	if !ok {
		return code
	}
	tail := scanTxTail(id)
	prev, nextStep := tail.prevHash, tail.maxStep+1

	if err := t.MarkApplying(); err != nil {
		return failRuntime(stdout, stderr, fmt.Sprintf("无法进入 applying: %v", err))
	}
	for i := range t.Steps {
		step := t.Steps[i]
		res := applyStep(ctx, step)
		t.Steps[i].OpResult = res
		failed := res.Returncode != 0 || res.Error != ""
		outcome := "success"
		if failed {
			outcome = "error"
		}
		h := stampTx("apply", id, nextStep, prev, outcome,
			map[string]any{"step": nextStep, "adapter": step.Adapter})
		if h != "" {
			prev = h // 仅写成功才推进链尾; 丢写则下一步从上一个已写哈希续链(步号可跳, 链不断)
		}
		nextStep++
		if failed {
			if err := t.MarkFailed(); err != nil {
				return failRuntime(stdout, stderr, fmt.Sprintf("标记 failed 失败: %v", err))
			}
			reason := res.Error
			if reason == "" {
				reason = fmt.Sprintf("步骤 %d(%s) returncode=%d", step.Index, step.Adapter, res.Returncode)
			}
			return failRuntime(stdout, stderr, fmt.Sprintf("步骤 %d(%s) 失败: %s", step.Index, step.Adapter, reason))
		}
	}
	if err := t.MarkApplied(); err != nil {
		return failRuntime(stdout, stderr, fmt.Sprintf("标记 applied 失败: %v", err))
	}
	printJSON(stdout, t)
	return exitOK
}

// cmdRollback 逆序施加回滚, 每步盖续号 tx_rollback 事务条目。
//   - applied → 用日志内回滚计划, 执行后置 rolled_back;
//   - failed  → 只回滚"已成功施加"的前缀步骤(partial), 状态机 failed 无合法出边 → 保持 failed;
//   - proposed→ exit 1 nothing to rollback(从未施加)。
func cmdRollback(args []string, stdout, stderr io.Writer) int {
	args, unitDir, okf := extractUnitDir(args, stdout, stderr)
	if !okf {
		return exitUsage
	}
	ctx := withUnitDir(context.Background(), unitDir)
	id, code, ok := wantIDArgs(args, stdout, stderr)
	if !ok {
		return code
	}
	t, code, ok := mustLoad(id, stdout, stderr)
	if !ok {
		return code
	}
	var plan []tx.Step
	switch t.Status {
	case tx.StatusApplied:
		plan = t.RollbackPlan.Steps
	case tx.StatusFailed:
		if i := firstFailed(t.Steps); i >= 0 {
			plan = tx.BuildRollbackPlan(t.Steps[:i]).Steps
		}
	case tx.StatusProposed:
		return failRuntime(stdout, stderr, "nothing to rollback")
	case tx.StatusRolledBack:
		return failRuntime(stdout, stderr, "事务已回滚")
	default: // applying: 半途, 拒绝并发回滚
		return failRuntime(stdout, stderr, "事务正在应用中, 无法回滚")
	}

	tail := scanTxTail(id)
	prev, nextStep := tail.prevHash, tail.maxStep+1
	for _, step := range plan {
		res := rollbackStep(ctx, step)
		outcome := "success"
		if res.Returncode != 0 || res.Error != "" {
			outcome = "error"
		}
		h := stampTx("rollback", id, nextStep, prev, outcome,
			map[string]any{"step": nextStep, "adapter": step.Adapter})
		if h != "" {
			prev = h
		}
		nextStep++
	}
	if t.Status == tx.StatusApplied {
		if err := t.MarkRolledBack(); err != nil {
			return failRuntime(stdout, stderr, fmt.Sprintf("标记 rolled_back 失败: %v", err))
		}
	}
	printJSON(stdout, t)
	return exitOK
}

// cmdStatus 打印事务日志全文, 盖**空 TxID** 外围 status 审计。
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	id, code, ok := wantIDArgs(args, stdout, stderr)
	if !ok {
		return code
	}
	t, code, ok := mustLoad(id, stdout, stderr)
	if !ok {
		return code
	}
	stampPlain("status", id, "success")
	printJSON(stdout, t)
	return exitOK
}

// mustLoad 按 id 载入日志; 未找到 → "transaction <id> not found"(exit 1), 其余错误 exit 1。
func mustLoad(id string, stdout, stderr io.Writer) (*tx.Transaction, int, bool) {
	t, err := tx.Load(id)
	if err != nil {
		if errors.Is(err, tx.ErrNotFound) {
			return nil, failRuntime(stdout, stderr, fmt.Sprintf("transaction %s not found", id)), false
		}
		return nil, failRuntime(stdout, stderr, fmt.Sprintf("载入事务 %s 失败: %v", id, err)), false
	}
	return t, exitOK, true
}

// firstFailed 返回首个失败步(带 BeforeState 快照)的下标; 无则 -1(未施加任何步)。
func firstFailed(steps []tx.Step) int {
	for i, s := range steps {
		if s.OpResult.Returncode != 0 || s.OpResult.Error != "" {
			return i
		}
	}
	return -1
}

// applyStep / rollbackStep 是分派到适配器的窄缝: 适配器缺失视为该步失败(未知适配器),
// 使 apply 能诚实标记 failed 而非 panic。ctx 携带 --unit-dir 覆写(todo 22:
// 旗标只影响 service.set 的解析目录, 其余适配器忽略)。
func applyStep(ctx context.Context, step tx.Step) tx.OpResult {
	a, ok := lookupAdapter(step.Adapter)
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("未知适配器: %s", step.Adapter)}
	}
	return a.Apply(ctx, step)
}

func rollbackStep(ctx context.Context, step tx.Step) tx.OpResult {
	a, ok := lookupAdapter(step.Adapter)
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("未知适配器: %s", step.Adapter)}
	}
	return a.Rollback(ctx, step)
}

// extractUnitDir 从 propose/apply/rollback 的位置参数里摘出 `--unit-dir <path>`
// 旗标(todo 22 CLI 接线): 至多一次、必须携带非空跟随值, 否则报用法错误(exit 2)。
// 返回 (其余位置参数, 覆写目录(未给则 ""))。ok=false 时已打印错误文档。
func extractUnitDir(args []string, stdout, stderr io.Writer) ([]string, string, bool) {
	rest := make([]string, 0, len(args))
	dir := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--unit-dir" {
			rest = append(rest, args[i])
			continue
		}
		if dir != "" {
			failUsage(stdout, stderr, "--unit-dir 至多出现一次")
			return nil, "", false
		}
		if i+1 >= len(args) || args[i+1] == "" {
			failUsage(stdout, stderr, `--unit-dir 需要跟随非空 <path> 参数`)
			return nil, "", false
		}
		dir = args[i+1]
		i++
	}
	return rest, dir, true
}
