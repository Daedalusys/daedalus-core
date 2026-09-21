// 回滚计划生成器(todo 14)。
//
// 职责切分:本文件只做**排序与筛选**——逆序排列、剔除无逆可执行的步骤。
// "怎么逆"是适配器领域(service.set 的单元文件恢复 + ActiveState 反动词
// 在 todo 22 实现),tx 层不理解、也不得理解任何适配器语义;计划步骤里的
// BeforeState 保持适配器写入的原始 JSON,原样持久化、原样交还执行者。
package tx

// BuildRollbackPlan 由正向步骤列表生成逆序回滚计划:
//   - 逆序:最后施加的副作用最先解除(LIFO 补偿事务语义);
//   - 筛选:只有 BeforeState 非空的步骤才有"有意义的逆"——没有快照就没有
//     可恢复状态,空 BeforeState 的步骤(纯读探测、无副作用声明)直接剔除;
//   - 纯函数:不修改入参(逐步值拷贝),可安全对任意步骤切片调用。
//
// 调用点:mark(applied) 固化计划(todo 15 的 rollback 子命令从日志读取
// RollbackPlan.Steps,按序交给适配器逆向执行)。
func BuildRollbackPlan(steps []Step) RollbackPlan {
	plan := RollbackPlan{}
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if len(s.BeforeState) == 0 {
			continue
		}
		plan.Steps = append(plan.Steps, s)
	}
	return plan
}
