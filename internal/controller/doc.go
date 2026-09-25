// Package controller 是 v1.5 契约预留包:只含纯类型与常量,零运行时逻辑、
// 零调用点。v2 的 daedalus-controller runtime 是本包唯一未来的消费者;
// v1 任何代码路径都不得 import 本包(预留即不接线,接线即越权)。
//
// 两轴命名导引(避免 "controller" 一词在两套坐标系里混用):
//   - 插件 type = 打包/分发轴:copilot / capability / controller,
//     描述一个 daedalus-plugin 包是什么角色;
//   - 资源 kind = 对象模型轴(objectmodel 的 7 类封闭枚举):
//     service / package / container / capability / task / transaction / policy,
//     描述 daedalus-tx 事务里被声明/变更的对象是什么。
//
// "capability" 在两轴各出现一次是已记录的同名巧合:插件 type=capability
// 指 fs/shell/pkg/sysinfo 这类能力服务器包,资源 kind=capability 指对象
// 模型里的能力声明,二者互不引用。"transaction" 同理:KindTransaction
// 是资源种类枚举位,tx.Status 是事务生命周期状态,分属两轴。
//
// 本包现在就能落地的理由:宿主 daedalus-host 不新增
// 任何 Type 分支——type=controller 的可接受性完全复用宿主既有的
// type-agnostic 设计:start.go:99 的 buildStartTokens 只按 manifest 的
// Runtime 分派(deno/native),Type 字段从不参与 argv 构造;且宿主零 spawn,
// 宿主零 spawn,绝不成为任何 MCP 服务器的父进程。因此预留 controller
// 类型不需要改动宿主一行代码。
//
// 文件布局:
//   - types.go:契约类型与动词常量(v2 消费者的线上契约,改动即破坏);其中
//     Object/Metadata/Status/Condition 是 SDK objectmodel 信封的别名——信封
//     形状不能留在 internal/(插件仓 import 不到),两处并存即第二事实源;
//   - 对应 VISION.md 的 controller runtime 章节(v2 蓝图,本包是其 v1.5
//     类型锚点)。
package controller
