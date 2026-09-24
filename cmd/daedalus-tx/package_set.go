package main

// package_set.go —— package.set 事务适配器:快照类型、边界解析、Propose 读快照
// 与 Apply/Rollback 通道(进程助手在 package_set_exec.go, 注册在 adapter.go)。
//
// 关键不变量: desired_state 冻结三值 present|absent|latest(表外拒绝); dnf
// history id **不**进 packageSetBefore, 跨步骤传递走 sidecar(缺失/读失败/不可
// 解析一律 error, 不猜 id, fail-closed); 生产调用点必须经五个包级 var(dnfExecFn/
// readDnfHistoryIDFn/writeDnfHistorySidecarFn/rpmQueryFn/geteuid)注入而绝不
// 直调原函数——调用点从第一行起就用 var 名, 测试覆写才生效。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/pkgquery"
)

// packageSetVerbs 显式定义: desired_state → dnf 子命令的冻结映射;
// missing key 由 desiredStateOK 拒。
var packageSetVerbs = map[string]string{
	"present": "install",
	"absent":  "remove",
	"latest":  "upgrade",
}

// 以下函数 var 是 mock 注入缝: 生产调用点一律经 var 名调用, 测试在 _test.go 里
// 覆写 + defer/t.Cleanup 还原。
var (
	dnfExecFn                = dnfExec
	readDnfHistoryIDFn       = readDnfHistoryID
	writeDnfHistorySidecarFn = writeDnfHistorySidecar
	rpmQueryFn               = rpmQuery
	dnfHistoryExistsFn       = dnfHistoryExists
)

// geteuid 是 os.Geteuid 的包级 var 注入缝(函数本身不可 mock,
// 必须包级 var)。root 守门一律经本 var 调用, 绝不直用 os.Geteuid。
var geteuid = os.Geteuid

// txIDCtxKey 镜像 unitguard.go 的 unitDirCtxKey 惯例: 私有空 struct 键型
// (绝不用内置类型当键, 防跨包碰撞)。Adapter 接口签名与 tx.Step 六键线上契约
// 均不动, tx-id 经 ctx 侧路到达 Apply/Rollback。
type txIDCtxKey struct{}

// withTxID 把事务 id 挂进 ctx。唯一写入点是 commands.go 的 cmdApply/cmdRollback
// 两处编排; 空串原样返回(与 withUnitDir 同款防御——调用方语义上永不传空, 传了
// 即视作未透传, 读侧 fail-closed)。
func withTxID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, txIDCtxKey{}, id)
}

// txIDFromCtx 读取 ctx 内透传的事务 id; 未设置/空串 → ok=false。Apply 的
// fail-closed 守门(dnf 副作用之前)与 Rollback 的 sidecar 寻址共用本缝;
// 读不到绝不写空 txID 命名的 sidecar、绝不让 dnf 先跑。
func txIDFromCtx(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(txIDCtxKey{}).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// packageSetArgs 是步骤 args 的线上形态(v1 两键 schema, 未知键拒绝)。
type packageSetArgs struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
}

// packageSetBefore 是 Propose 期的现状快照: installed 恒在; version 未装时
// 空串 + omitempty 整键缺席。已废的 dnf history id 字段绝不
// 在这里 —— adapter 不能回写 step.BeforeState, dnf history id 跨步骤传递走
// sidecar 文件。
type packageSetBefore struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
}

// packageSetAfter 提议的目标态视图: verb 为映射后的 dnf 子命令
// (install|remove|upgrade); 表外 desired_state 由守卫先拒, omitempty
// 只是防御性兜底。
type packageSetAfter struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
	Verb         string `json:"verb,omitempty"`
}

// packageSetAdapter 无状态空壳: 生命周期三段已全部落地(Propose, Apply,
// Rollback); RegisterAdapter 进 registry。
type packageSetAdapter struct{}

// desiredStateOK 报告 state 是否属于 v1 package.set 的 desired_state 集合
// (membership check on packageSetVerbs; 集合外延只随映射表演进)。
func desiredStateOK(state string) bool {
	_, ok := packageSetVerbs[state]
	return ok
}

// parsePackageSetArgs 边界解析(严格镜像 service_set.go 的 parseServiceSetArgs;
// DisallowUnknownFields: 手改注入的多余键即拒)。
func parsePackageSetArgs(raw json.RawMessage) (packageSetArgs, error) {
	var in packageSetArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid package.set args: %w", err)
	}
	if dec.More() {
		return in, errors.New(`invalid package.set args: JSON 文档后有尾随数据`)
	}
	// name 空串**不**在此拒(交给 sanitizePackageName 统一以
	// "Invalid package name or pattern" 形态拒绝 —— 名称门只有一个出口,
	// apply/rollback 侧同字)。
	if in.DesiredState == "" {
		return in, errors.New(`invalid package.set args: 缺 "desired_state" 字段`)
	}
	return in, nil
}

// sanitizePackageName 包名门的唯一出口: (1) 字符集校验走
// pkgquery.IsValidPackageName(单一事实源, 绝不内联白名单 regex 副本);
// (2) post-checks 必需 —— 白名单字符类允许 `.`,
// 光靠 regex 挡不住 "a..b" 路径回溯与 ".hidden" 隐藏文件语义; `/` 与 `\`
// 虽已在字符类外, ContainsAny 仍显式钉住路径分隔符契约(防未来字符类放宽漏口);
// (3) set 域在白名单之外拒通配星号(理由详见函数体内注释)。
// 错误串与 pkgquery.sanitizeQuery 逐字一致。
func sanitizePackageName(name string) error {
	if !pkgquery.IsValidPackageName(name) {
		return fmt.Errorf("Invalid package name or pattern: %s", name)
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("Invalid package name or pattern: %s", name)
	}
	// 查询域字符类含通配星号是 dnf_query 的合法 pattern 面；
	// package.set 的 name 是精确操作对象，glob 展开=批量变更（违背 v1 单段精确
	// 语义与 preview 规模可信性），故 set 域在白名单之外拒通配星号。
	// 加号与冒号无 dnf 展开语义，保留。
	if strings.Contains(name, "*") {
		return fmt.Errorf("Invalid package name or pattern: %s", name)
	}
	return nil
}

// Propose (镜像 serviceSetAdapter.Propose): 解析 → 名称门 → desired_state
// 门 → rpm 现状查询 → 双快照序列化。与 service.set 的关键差异: 表外 desired_state
// 在 Propose 即拒(service.set 放行、Apply 才拒)——package 域 v1 三值封闭, 没有
// "先提议后拒绝"的中间语义。全程零副作用: 不跑 dnf、不写文件、不查 dnf history、
// 不做 root 守门(那是 Apply 的职责); 任何拒绝都发生在 rpm 查询之前,
// rpm 查询自身失败时如实上抛 error, 绝不谎报现状。
func (packageSetAdapter) Propose(ctx context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parsePackageSetArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if err := sanitizePackageName(in.Name); err != nil {
		return nil, nil, err
	}
	if !desiredStateOK(in.DesiredState) {
		return nil, nil, fmt.Errorf(`unknown desired_state %q (v1 支持 present|absent|latest)`, in.DesiredState)
	}
	installed, version, err := rpmQueryFn(ctx, in.Name)
	if err != nil {
		return nil, nil, err
	}
	// before: 未装时 version 为空串, 由 omitempty 消化整键缺席(已废的 dnf history id 字段绝不进这里, 跨步骤传递走 sidecar 文件)。
	before, err := json.Marshal(packageSetBefore{Name: in.Name, Installed: installed, Version: version})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 before_state 失败: %w", err)
	}
	after, err := json.Marshal(packageSetAfter{Name: in.Name, DesiredState: in.DesiredState, Verb: packageSetVerbs[in.DesiredState]})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 after_state 失败: %w", err)
	}
	return before, after, nil
}

// Apply: 三连重校验(args/名称/desired_state, 手改日志纵深防御, 每次独立跑完)→
// root 守门 → txID 透传守门(位于 euid 之后、dnf 直发之前, 拿不到 txID 时绝不先跑
// dnf)→ 动词映射 → dnf 直发 → history 捕获与 sidecar 落盘任一失败都整体判 Apply
// 失败——杜绝"Apply 成功但 Rollback 不可用"。两个 "rollback unavailable" 错误前缀
// 逐字锁定(守门消费)。
// dnf 失败直接透传其 OpResult(rc/Stdout/Stderr/Error 原样), 绝不进 sidecar
// 路径; 成功时 Stdout/Stderr 透传 dnf 输出(service.set Apply 同款惯例)。
func (packageSetAdapter) Apply(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parsePackageSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := sanitizePackageName(in.Name); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if !desiredStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(`unknown desired_state %q (v1 支持 present|absent|latest)`, in.DesiredState)}
	}
	// root 守门: dnf install/remove/upgrade 是系统域写操作, v1 只允许
	// root 经 sudo 走本通道; 经 geteuid var 判定(注入契约, 测试翻非零)。
	if geteuid() != 0 {
		return tx.OpResult{Returncode: 1, Error: "package.set requires root (euid=0); re-run via sudo"}
	}
	// txID 透传守门: sidecar 以 txID+step.Index 命名, 缺失即回滚材料
	// 无处落——在一切 dnf 副作用之前 fail-closed 拒绝, 错误串逐字锁定。
	txID, ok := txIDFromCtx(ctx)
	if !ok {
		return tx.OpResult{Returncode: 1, Error: "rollback unavailable: tx id 未透传"}
	}
	// 三连重校验的第三连(desiredStateOK)已把表外值拒在上方, 本映射查是双保险
	// 防御兜底。
	verb, ok := packageSetVerbs[in.DesiredState]
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("unknown desired_state %q", in.DesiredState)}
	}
	res := dnfExecFn(ctx, verb, "-y", in.Name)
	if res.Returncode != 0 {
		return res
	}
	// 透传本次实发的 verb 与包名: history 捕获经末行
	// Command line 列的相关性校验确认捕获的是自己的事务, 并发窗口内他人
	// 提交的事务会被判"捕获不可信"而 fail-closed。
	historyID, err := readDnfHistoryIDFn(ctx, verb, in.Name)
	if err != nil {
		res.Returncode = 1
		res.Error = fmt.Sprintf("rollback unavailable: dnf history cannot be captured: %v", err)
		return res
	}
	// sidecar 落盘是 Apply 成功的必要条件:
	// 写失败绝不吞错续行返成功——Rollback 全靠这份文件寻址回滚。
	if err := writeDnfHistorySidecarFn(txID, step.Index, historyID); err != nil {
		res.Returncode = 1
		res.Error = fmt.Sprintf("rollback unavailable: dnf history sidecar write failed: %v", err)
		return res
	}
	return res
}

// Rollback: 三连重校验 → root 守门 → txID 透传守门(与 Apply 逐字同串)→ sidecar
// 读取门 → dnf history 探测分流。sidecar 缺失/读失败 → 严格 fail-closed, 不探测、
// 不兜底、绝不猜 history id; history 已被清理时按 desired_state 分流 best-effort:
// present/latest → remove 兜底; absent → **严格 error 且 dnf 物理零调用**——dnf
// remove 对已 absent 包是空操作会误报回滚成功, dnf install 又需要版本信息,
// 强制人工处理。
// 纯消费侧契约: 不写 sidecar、不回写 BeforeState、不重建 history; undo/兜底的
// dnf 结果原样透传(rc!=0 即失败, 不吞)。sidecar 读取门必须排在探测之前
// (顺序即契约)。诊断细节走 OpResult.Stderr, Error 保持逐字。
func (packageSetAdapter) Rollback(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parsePackageSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := sanitizePackageName(in.Name); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if !desiredStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(`unknown desired_state %q (v1 支持 present|absent|latest)`, in.DesiredState)}
	}
	if geteuid() != 0 {
		return tx.OpResult{Returncode: 1, Error: "package.set requires root (euid=0); re-run via sudo"}
	}
	txID, ok := txIDFromCtx(ctx)
	if !ok {
		return tx.OpResult{Returncode: 1, Error: "rollback unavailable: tx id 未透传"}
	}
	// sidecar 读取门: 该文件是 Apply 成功的必要条件产物,
	// 缺失本身即回滚材料不存在——到此为止, 后续任何 dnf 动作都不许发生。
	historyID, err := readDnfHistorySidecar(txID, step.Index)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: "rollback 缺少 dnf history id（sidecar 不存在或不可读）", Stderr: err.Error()}
	}
	exists, err := dnfHistoryExistsFn(ctx, historyID)
	if err != nil {
		// 探测不可靠: 存在性未知即不动 dnf, fail-closed 上抛。
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("回滚前置探测失败（dnf history 状态未知）: %v", err)}
	}
	if exists {
		return dnfExecFn(ctx, "history", "undo", "-y", strconv.FormatInt(historyID, 10))
	}
	switch in.DesiredState {
	case "present", "latest":
		// install/upgrade 路径兜底: 装回来的东西直接卸掉即回到 absent 现状。
		res := dnfExecFn(ctx, "remove", "-y", in.Name)
		if res.Returncode != 0 {
			res.Error = fmt.Sprintf("rollback 依赖的 dnf history 已清理（id=%d），且 remove 兜底失败：%s", historyID, firstNonEmpty(res.Stderr, res.Error))
		}
		return res
	default:
		// absent(及任何理论外值, desiredStateOK 已拦, 双保险方向仍是 fail-closed):
		// 不可逆严格 error, dnf 物理零调用。
		return tx.OpResult{Returncode: 1, Error: "rollback 不可逆：desired_state=absent 的 rollback 在 dnf history 清理后无可靠回滚手段（dnf remove 对已 absent 包是空操作会误报 success，dnf install 又需要版本信息；强制要求人工处理）"}
	}
}
