package main

// adapters_test.go —— 测试专用适配器(仅 _test 期注册, 生产注册表恒空)。
//
//   - noop:       Propose 返回非空 before/after(使其进入回滚计划), Apply/Rollback 均成功;
//   - failapply:  Propose 非空 before, Apply 恒失败(Returncode 1 + Error)—— 供
//                 "失败步 → MarkFailed + 部分回滚"测试钉桩;
//   - nobefore:   Propose 返回**空** before —— 该步不进回滚计划(BuildRollbackPlan 依
//                 BeforeState 非空筛选), 钉"无快照即无逆"的筛选面。
//
// 用 init 注册: go test 加载本文件即生效, 而 `go build` 生产二进制永不包含它们。

import (
	"context"
	"encoding/json"

	"github.com/daedalus-os/daedalus/core/internal/tx"
)

func init() {
	RegisterAdapter("noop", noopAdapter{})
	RegisterAdapter("failapply", failApplyAdapter{})
	RegisterAdapter("nobefore", noBeforeAdapter{})
}

type noopAdapter struct{}

func (noopAdapter) Propose(_ context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	return json.RawMessage(`{"before":"snapshot"}`), json.RawMessage(`{"after":` + string(args) + `}`), nil
}
func (noopAdapter) Apply(_ context.Context, _ tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 0, Stdout: "noop ok"}
}
func (noopAdapter) Rollback(_ context.Context, _ tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 0, Stdout: "noop rb"}
}

type failApplyAdapter struct{}

func (failApplyAdapter) Propose(_ context.Context, _ json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	return json.RawMessage(`{"before":"snapshot"}`), json.RawMessage(`{}`), nil
}
func (failApplyAdapter) Apply(_ context.Context, s tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 1, Error: "boom"}
}
func (failApplyAdapter) Rollback(_ context.Context, _ tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 0, Stdout: "failapply rb"}
}

type noBeforeAdapter struct{}

func (noBeforeAdapter) Propose(_ context.Context, _ json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	return nil, json.RawMessage(`{"after":"x"}`), nil // before 空 → 不进回滚计划
}
func (noBeforeAdapter) Apply(_ context.Context, _ tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 0}
}
func (noBeforeAdapter) Rollback(_ context.Context, _ tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 0}
}
