// Package controller 的契约类型(v2 controller runtime 的线上契约,改动即破坏)。
//
// 本文件只含纯类型(信封为 SDK 别名)与 JSON tag,零函数体/方法/构造器/Validate
// ——校验归 v2 runtime,预留包 v1 不接线。序列化形态由 types_test.go 的金样往返
// 测试逐字节锁定(键序 = 字段声明序),任何漂移必须先改测试再改类型。
package controller

import (
	"context"
	"time"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// Object 是 v2 controller runtime 调和循环消费的顶层契约对象。
// 形状的唯一事实源在 SDK objectmodel(插件仓要 import,本包是 internal 不可达),
// 此处别名保持 v1 线上契约与金样字节形态不变。
type (
	Object    = objectmodel.Object
	Metadata  = objectmodel.Metadata
	Status    = objectmodel.Status
	Condition = objectmodel.Condition
)

// Result 是动词处理器的返回契约(requeue_after_ns 为非负纳秒数,零值抑制)。
type Result struct {
	Requeue      bool          `json:"requeue"`
	RequeueAfter time.Duration `json:"requeue_after_ns,omitempty"`
}

// Op 是标准动词文法的线协议 token(小写;VISION.md §6 与本常量表逐字对应,
// 冲突时以本文件为准并修文——单向规范性,防第二事实源)。
//
// 注意区分:begin/propose 不进动词表——事务生命周期原语(begin→propose→apply→
// rollback)不是资源操作,由 daedalus-tx 子命令承载;本表只收资源操作动词。
type Op string

const (
	OpQuery    Op = "query"    // 观测面·读单实例(对应 service.query 等工具)
	OpList     Op = "list"     // 观测面·读集合(对应 service.list)
	OpSet      Op = "set"      // 声明面·写:驱动向 desired_state(v1 真实现仅 service 经 daedalus-tx)
	OpDelete   Op = "delete"   // 声明面·写:语义 = desired_state 达 DesiredStateAbsent;v1 无任何 delete 适配器实现
	OpApply    Op = "apply"    // 执行面·事务提交(已实现:daedalus-tx apply 子命令,逐字一致)
	OpRollback Op = "rollback" // 执行面·事务回滚(已实现:daedalus-tx rollback,逐字一致)
	OpStatus   Op = "status"   // 执行面·事务状态(已实现:daedalus-tx status,逐字一致)
)

// DesiredStateAbsent 是 delete 动词在 desired_state 词汇中的哨兵值;
// 各 kind 的删除语义词汇归 provider 领域,本层只钉 token。
const DesiredStateAbsent = "absent"

// ReconcileFunc 是 v2 controller runtime 的调和回调:Watch 推来事件或
// 兜底触发时调用一次。v1 本包零调用点——本类型是 VISION.md §5 parity 表
// "契约缝·零运行时" 行所指的缝,不提供任何默认实现。
type ReconcileFunc func(ctx context.Context, obj Object) (Result, error)

// AdmissionOp 是写动词的准入操作 token(取值即 OpSet/OpDelete 两枚的字符串)。
type AdmissionOp string

// Decision 是准入决定。
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// AdmissionFunc 是 v2 动态准入链的缝。框架注释锁定:v1 静态准入 = policy.toml
// 单实现(fail-closed);本类型是 v2 动态链的插入点,不是对现状的第二事实源
// 宣称——policy.toml 的强制值今天、明天都由 internal/policy 独家承载。
type AdmissionFunc func(op AdmissionOp, obj Object) Decision
