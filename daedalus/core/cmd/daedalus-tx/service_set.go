package main

// service_set.go —— service.set 事务适配器(todo 22, 计划 todo 22 (a)-(f) 条款)。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析单元文件路径(直在调用用户进程内, **不**经 daedalus-service ——
//     它是系统域 DynamicUser 服务, 读不到用户私有单元文件)→ UNCONDITIONAL 守卫 →
//     快照文件内容 + ActiveState(argv 直发 systemctl --user show), 零副作用。
//   - Apply:   重解析重守卫(手改日志纵深防御)→ desired_state 动词映射 →
//     `systemctl --user <verb> <unit>`。v1 生命周期动词**从不改单元文件**。
//   - Rollback:重解析重守卫(含快照 unit_file)→ 仅当快照内容与当前实况有漂移时
//     恢复文件(恢复写之后、逆动词之前必跑 daemon-reload, (f) 条款)→
//     按 BeforeState.active_state 施加恢复性动词。
//
// desired_state 语义面(计划 review round 1 钉):
//   - v1 动词集 started|stopped|restarted|enabled|disabled(映射见 serviceSetVerbs);
//   - "reload" 在 Propose 即逐字拒绝 "desired_state reload not supported in v1"
//     (copilot 分类器按 danger 归类属 todo 24, 本适配器只负责拒绝执行);
//   - 其余未知值 Propose **放行**(快照照常), Apply 拒绝 → apply exit 1 +
//     拒绝原因落该步 OpResult(plan todo 22 Failure QA 钉死该形态)。
//
// 逆动词的语义选择(计划 (e) 括注 "active→stop, inactive→start" 与其
// Happy QA "ActiveState 必须被恢复" 互相矛盾 —— 括注映射是把状态推向相反值,
// 永远无法"恢复")。本实现采**恢复语义 active→start / inactive→stop / 其余不发
// 动词**(与 todo 22 正文 "re-applies the prior ActiveState" 及门控测试断言一致),
// 该选择在代码注释与 DoneClaim 双登记。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daedalus-os/daedalus/core/internal/tx"
)

// systemctlUserBinary 是 `systemctl --user` 的绝对路径注入缝(家族模式同
// cmd/daedalus-service 的 systemctlBinary): 生产态固定 /usr/bin/systemctl,
// argv 直发无 $PATH 查找, 测试覆写指向 t.TempDir() 下的 shell 夹具。
var systemctlUserBinary = "/usr/bin/systemctl"

// systemctlUserTimeout 单次 systemctl 调用兜底超时(30s, 与 shellpolicy/service 同值)。
const systemctlUserTimeout = 30 * time.Second

// serviceSetVerbs (d) 条款: desired_state → systemctl --user 动词的冻结映射表。
var serviceSetVerbs = map[string]string{
	"started":   "start",
	"stopped":   "stop",
	"restarted": "restart",
	"enabled":   "enable",
	"disabled":  "disable",
}

// serviceSetInverseVerbs (e) 条款(恢复语义, 见文件头"语义选择"注释):
// 回滚动词把单元恢复到 BeforeState.active_state 观测到的值; 表外状态
// (activating/deactivating/failed/空)不发动词 —— 单动词无法可靠复原中间态。
var serviceSetInverseVerbs = map[string]string{
	"active":   "start",
	"inactive": "stop",
}

// serviceSetArgs 是步骤 args 的线上形态(计划钉死的两键 schema, 未知键拒绝)。
type serviceSetArgs struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
}

// serviceSetBefore (c) 条款快照: unit_file 恒在; file_content 单元缺失时整个
// 键缺席(*string + omitempty, 与"存在但为空文件"可区分); active_state 恒在。
type serviceSetBefore struct {
	UnitFile    string  `json:"unit_file"`
	FileContent *string `json:"file_content,omitempty"`
	ActiveState string  `json:"active_state"`
}

// serviceSetAfter 提议的目标态视图(未知 desired_state 时 verb 键缺席)。
type serviceSetAfter struct {
	Unit         string `json:"unit"`
	DesiredState string `json:"desired_state"`
	Verb         string `json:"verb,omitempty"`
}

// serviceSetAdapter 无状态: 每段生命周期都从 step.Args/BeforeState + ctx 重新
// 推导一切(日志可被手改, 守卫与校验每一次都独立跑完)。
type serviceSetAdapter struct{}

// parseServiceSetArgs 边界解析(DisallowUnknownFields: 手改注入的多余键即拒)。
func parseServiceSetArgs(raw json.RawMessage) (serviceSetArgs, error) {
	var in serviceSetArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid service.set args: %w", err)
	}
	if dec.More() {
		return in, errors.New(`invalid service.set args: JSON 文档后有尾随数据`)
	}
	// name 空串**不**在此拒(交给 normalizeUnitName 统一以 "invalid unit name"
	// 形态拒绝 —— 名称门只有一个出口, apply/rollback 侧同字)。
	if in.DesiredState == "" {
		return in, errors.New(`invalid service.set args: 缺 "desired_state" 字段`)
	}
	return in, nil
}

// Propose (a)(b)(c)(f): 校验 → 守卫 → 文件快照 → ActiveState 观测 → 双快照。
// 任何拒绝都发生在读文件与执行 systemctl 之前。
func (serviceSetAdapter) Propose(ctx context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parseServiceSetArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if in.DesiredState == "reload" {
		return nil, nil, errors.New("desired_state reload not supported in v1")
	}
	unit, err := normalizeUnitName(in.Name)
	if err != nil {
		return nil, nil, err
	}
	target, err := resolveUnitFile(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	if err := guardUnitPath(ctx, target); err != nil {
		return nil, nil, err
	}
	content, err := readUnitFileSnapshot(target)
	if err != nil {
		return nil, nil, err
	}
	active, err := observeActiveState(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	before, err := json.Marshal(serviceSetBefore{UnitFile: target, FileContent: content, ActiveState: active})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 before_state 失败: %w", err)
	}
	after, err := json.Marshal(serviceSetAfter{Unit: unit, DesiredState: in.DesiredState, Verb: serviceSetVerbs[in.DesiredState]})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 after_state 失败: %w", err)
	}
	return before, after, nil
}

// Apply (d): 参数/名称/路径三连重校验(守卫在动词映射与执行之前)→ 动词直发。
// v1 生命周期动词从不触碰单元文件 —— apply 无任何写点。
func (serviceSetAdapter) Apply(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseServiceSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	unit, err := normalizeUnitName(in.Name)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	target, err := resolveUnitFile(ctx, unit)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := guardUnitPath(ctx, target); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	verb, ok := serviceSetVerbs[in.DesiredState]
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			"unknown desired_state %q (v1 支持 started|stopped|restarted|enabled|disabled, reload 拒绝)", in.DesiredState)}
	}
	res := systemctlUser(ctx, verb, unit)
	if res.Returncode != 0 && res.Error == "" {
		res.Error = fmt.Sprintf("systemctl --user %s %s 失败: rc=%d %s", verb, unit, res.Returncode, res.Stderr)
	}
	return res
}

// Rollback (e)(f): 三连重校验 + 快照 unit_file 守卫 → 漂移恢复(写后有 reload)
// → 按 before.active_state 施加恢复动词。恢复写只在"快照有内容且当前不同/缺失"
// 时发生; 快照无内容(提议时文件本就不存在)而当前存在 → 不删除非适配器所建文件。
func (serviceSetAdapter) Rollback(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseServiceSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	unit, err := normalizeUnitName(in.Name)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	target, err := resolveUnitFile(ctx, unit)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := guardUnitPath(ctx, target); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if len(step.BeforeState) == 0 {
		return tx.OpResult{Returncode: 1, Error: "rollback 缺少 before_state 快照"}
	}
	var before serviceSetBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("before_state 解析失败: %v", err)}
	}
	if before.UnitFile == "" || !strings.HasPrefix(before.UnitFile, "/") {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("before_state.unit_file 非绝对路径: %q", before.UnitFile)}
	}
	if err := guardUnitPath(ctx, before.UnitFile); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}

	var agg tx.OpResult
	// (e) 恢复机制(v1 唯一写点): 快照内容存在且当前实况有漂移 → 逐字写回。
	if before.FileContent != nil {
		cur, rerr := readUnitFileSnapshot(before.UnitFile)
		if rerr != nil {
			return tx.OpResult{Returncode: 1, Error: rerr.Error()}
		}
		if cur == nil || *cur != *before.FileContent {
			if _, werr := restoreUnitFile(before.UnitFile, *before.FileContent); werr != nil {
				return tx.OpResult{Returncode: 1, Error: werr.Error()}
			}
			// (f) 发生过恢复写 → systemd 缓存已过期, daemon-reload 必须先于动词。
			dr := systemctlUser(ctx, "daemon-reload")
			agg.Stdout += fmt.Sprintf("[systemctl --user daemon-reload] rc=%d\n", dr.Returncode)
			if dr.Returncode != 0 || dr.Error != "" {
				agg.Returncode = 1
				agg.Error = fmt.Sprintf("daemon-reload 失败, 未施加恢复动词: %s", firstNonEmpty(dr.Error, dr.Stderr))
				agg.Stderr = dr.Stderr
				return agg
			}
		}
	}
	// 恢复性动词: 表外状态不发动词, 原因记入 OpResult.Stdout(尽力恢复语义)。
	verb, ok := serviceSetInverseVerbs[before.ActiveState]
	if !ok {
		agg.Stdout += fmt.Sprintf("active_state=%q 无恢复动词映射, 跳过 systemctl 动作(尽力恢复语义)", before.ActiveState)
		return agg
	}
	vr := systemctlUser(ctx, verb, unit)
	agg.Stdout += fmt.Sprintf("[systemctl --user %s %s] rc=%d\n", verb, unit, vr.Returncode)
	if vr.Stderr != "" {
		agg.Stderr += vr.Stderr + "\n"
	}
	if vr.Returncode != 0 || vr.Error != "" {
		agg.Returncode = 1
		agg.Error = fmt.Sprintf("恢复动词 %s 失败: %s", verb, firstNonEmpty(vr.Error, vr.Stderr))
	}
	return agg
}
