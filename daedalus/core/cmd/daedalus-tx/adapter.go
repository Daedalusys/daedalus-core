package main

// adapter.go —— 事务适配器注册表(todo 15 落框架, todo 22 注册首个生产适配器)。
//
// 适配器是"怎么改/怎么逆"的领域实现; tx 层(internal/tx)只负责状态机与日志,
// 不理解任何适配器语义(见 internal/tx 包注释)。service.set 适配器在 todo 22
// 落地并在本文件的 init 里 RegisterAdapter —— 生产注册表现含 service.set;
// 测试用的 noop/失败适配器只在 _test.go 里 RegisterAdapter, 绝不进生产二进制。

import (
	"context"
	"encoding/json"

	"github.com/daedalus-os/daedalus/core/internal/tx"
)

// Adapter 是一个事务步骤的执行者。三段生命周期与 tx 状态机对齐:
//   - Propose: 在 proposed 期生成步骤快照(before = 回滚所需原状态, after = 目标状态),
//     不产生任何副作用; 返回的 before/after 由 tx.Step 原样持久化。
//   - Apply:   施加副作用并返回规范化结果(OpResult); 失败以 Returncode!=0 或 Error!= "" 表达。
//   - Rollback: 依据 step.BeforeState 恢复原状态并返回结果。
//
// 约定: before 非空才意味着"有可逆的东西"(BuildRollbackPlan 据此筛选回滚步骤)。
type Adapter interface {
	Propose(ctx context.Context, args json.RawMessage) (before, after json.RawMessage, err error)
	Apply(ctx context.Context, step tx.Step) tx.OpResult
	Rollback(ctx context.Context, step tx.Step) tx.OpResult
}

// registry 是适配器名 → 实现的映射。生产态由 init 注册 service.set(todo 22);
// 测试用的 noop/failapply/nobefore 只在 _test.go 里 RegisterAdapter, 不进生产语义。
var registry = map[string]Adapter{}

func init() {
	// service.set —— 用户域 systemd 单元生命周期适配器(todo 22, 见 service_set.go)。
	RegisterAdapter("service.set", serviceSetAdapter{})
}

// RegisterAdapter 注册一个适配器(重复注册覆盖)。仅供 todo 22 与测试调用。
func RegisterAdapter(name string, a Adapter) { registry[name] = a }

// lookupAdapter 按名取适配器; ok=false 表示未注册(propose 据此 exit 1)。
func lookupAdapter(name string) (Adapter, bool) {
	a, ok := registry[name]
	return a, ok
}
