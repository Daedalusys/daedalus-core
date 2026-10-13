package main

// task_set.go —— task.set 事务适配器。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析 {name, desired_state} → 词表门(封闭三值)→ normalizeTimerName
//     → resolveUnitFile + guardUnitPath(系统域逐字拒)→ readUnitFileSnapshot
//     → 文件不存在即拒(v1 三值全部需要文件存在,fail-closed)→ observeTimerEnabledState
//     → 双快照序列化。无副作用:不跑 systemctl、不写文件。
//   - Apply:   三连重校验(纵深防御)→ 词表映射:enabled→enable / disabled→disable
//     / absent→observe+maybe disable+rm+daemon-reload。disable 失败即返回 rc=1,
//     **不**继续 rm(任务 4 在 TestTaskSet_Apply_Absent_DisableFails_DoesNotRemove 钉死)。
//   - Rollback: 三连重校验 + 快照 unit_file 守卫 → 文件内容漂移恢复(写后 daemon-reload)→
//     按 before.enabled_state 恢复 enabled 态;file_content 为 nil 时**不删除**当前文件
//     (A3 非适配器所建文件);is-enabled 表外值不发动词,stdout 记原因。
//
// desired_state 语义面:
//   - 封闭三值 enabled|disabled|absent(任务 2 词表门,后续 plan 引入 cron 替代形态
//     会加 "created" 档,届时拆分 taskSetVerbs 即可);
//   - Propose 提前拒(同 package.set): 表外值即退;不"先提议后拒绝"中转语义;
//   - 文件缺失 → Propose 拒,统一错误串 "task file not found: <path>(task.set 只管理
//     已存在的 timer;创建走 cron 替代 plan)"。
//
// 逆动词的语义选择: enabled→enable / disabled→disable 两档恢复性动词映射;
// static / linked / masked 等 systemd 表外值不发动词(reason 记 stdout,与 service.set
// 同款尽力恢复语义)。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// taskSetVerbs (词表): desired_state → systemd --user 动词的冻结映射。
// 表外值由 taskSetArgs 解析前的 taskStateOK 拒;Propose 提前拒,与 package.set 同款。
var taskSetVerbs = map[string]string{
	"enabled":  "enable",
	"disabled": "disable",
	"absent":   "remove",
}

// taskSetInverseVerbs (恢复语义): enabled→enable / disabled→disable。
// 表外状态(static/linked/masked/空/未知)不发动词,stdout 记原因——单动词无法可靠复原
// 中间态。
var taskSetInverseVerbs = map[string]string{
	"enabled":  "enable",
	"disabled": "disable",
}

// taskFileMissingMsg 是 Propose 在文件缺失时的统一拒串(任务 3 的 review focus 决策:三值
// 一致拒)。逐字锁定,消费方在测试里逐字比对。
const taskFileMissingMsg = "task file not found: %s (task.set 只管理已存在的 timer;创建走 cron 替代 plan)"

// taskSetArgs 是步骤 args 的线上形态(锁定两键 schema, 未知键拒绝)。
type taskSetArgs struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
}

// taskSetBefore 是 Propose 期的现状快照: timer_name 与 unit_file 恒在; file_content
// 在文件缺失时整键缺席(*string + omitempty); enabled_state 恒在(Propose 已读到 systemd)。
type taskSetBefore struct {
	TimerName    string  `json:"timer_name"`
	UnitFile     string  `json:"unit_file"`
	FileContent  *string `json:"file_content,omitempty"`
	EnabledState string  `json:"enabled_state"`
}

// taskSetAfter 提议的目标态视图: verb 为映射后的 systemd 动词(enabled|disabled|absent);
// enabled|disabled 对应 enable/disable;absent 用 "remove" 标记语义差异(Apply 据此走
// observe+disable+rm+daemon-reload 复合流程)。
type taskSetAfter struct {
	Timer        string `json:"timer"`
	DesiredState string `json:"desired_state"`
	Verb         string `json:"verb,omitempty"`
}

// taskSetAdapter 无状态空壳: 生命周期三段已全部落地;RegisterAdapter 进 registry。
type taskSetAdapter struct{}

// taskStateOK 报告 state 是否属于 v1 task.set 的 desired_state 集合(封闭三值)。
func taskStateOK(state string) bool {
	_, ok := taskSetVerbs[state]
	return ok
}

// parseTaskSetArgs 严格解析(DisallowUnknownFields, 镜像 service_set.go)。
func parseTaskSetArgs(raw json.RawMessage) (taskSetArgs, error) {
	var in taskSetArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid task.set args: %w", err)
	}
	if dec.More() {
		return in, errors.New(`invalid task.set args: JSON 文档后有尾随数据`)
	}
	if in.DesiredState == "" {
		return in, errors.New(`invalid task.set args: 缺 "desired_state" 字段`)
	}
	return in, nil
}

// Propose (a-e 全段): 解析 → 词表门 → 名称门(隐式 .timer 后缀)→ resolveUnitFile +
// guardUnitPath → 文件快照 → **三值文件存在性门**(任务 3 广义化扩展)→ enabled_state 观测 →
// 双快照序列化。
func (taskSetAdapter) Propose(ctx context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parseTaskSetArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if !taskStateOK(in.DesiredState) {
		return nil, nil, fmt.Errorf(`unknown desired_state %q (v1 支持 enabled|disabled|absent)`, in.DesiredState)
	}
	timer, err := normalizeTimerName(in.Name)
	if err != nil {
		return nil, nil, err
	}
	target, err := resolveUnitFile(ctx, timer)
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
	if content == nil {
		// 三值一致 fail-closed(任务 3 / Review Focus #1 决策):
		// enabled 在无文件时 systemctl enable 会失败;disabled 也会因 unit 不存在而失败;
		// absent 在无文件时无任何副作用,语义上"已 absent",但 Propose 必须有可逆快照
		// 才能生成 rollback 计划,无论 desired_state 哪个值。
		return nil, nil, fmt.Errorf(taskFileMissingMsg, target)
	}
	enabled, err := observeTimerEnabledState(ctx, timer)
	if err != nil {
		return nil, nil, err
	}
	before, err := json.Marshal(taskSetBefore{
		TimerName:    timer,
		UnitFile:     target,
		FileContent:  content,
		EnabledState: enabled,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 before_state 失败: %w", err)
	}
	after, err := json.Marshal(taskSetAfter{Timer: timer, DesiredState: in.DesiredState, Verb: taskSetVerbs[in.DesiredState]})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 after_state 失败: %w", err)
	}
	return before, after, nil
}

// Apply (d-f): 三连重校验 → 词表映射 → 直发 systemd → absent 走复合流程(任务 4 钉)。
// disable 失败即整体失败,后续 rm **不**执行。
func (taskSetAdapter) Apply(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseTaskSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if !taskStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			`unknown desired_state %q (v1 支持 enabled|disabled|absent)`, in.DesiredState)}
	}
	timer, err := normalizeTimerName(in.Name)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	target, err := resolveUnitFile(ctx, timer)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := guardUnitPath(ctx, target); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	verb, ok := taskSetVerbs[in.DesiredState]
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("unknown desired_state %q", in.DesiredState)}
	}

	switch in.DesiredState {
	case "enabled", "disabled":
		res := systemctlUser(ctx, verb, timer)
		if res.Returncode != 0 && res.Error == "" {
			res.Error = fmt.Sprintf("systemctl --user %s %s 失败: rc=%d %s", verb, timer, res.Returncode, res.Stderr)
		}
		return res

	case "absent":
		// 复合流程:observe → 必要时 disable → rm → daemon-reload。
		// 任一步失败即整体失败,**不**继续 rm(任务 4 review focus #2)。
		var agg tx.OpResult
		currentEnabled, err := observeTimerEnabledState(ctx, timer)
		if err != nil {
			agg.Returncode = 1
			agg.Error = fmt.Sprintf("absent 前置观测失败: %v", err)
			return agg
		}
		agg.Stdout += fmt.Sprintf("[is-enabled %s] %s\n", timer, currentEnabled)
		if currentEnabled == "enabled" {
			dr := systemctlUser(ctx, "disable", timer)
			agg.Stdout += fmt.Sprintf("[systemctl --user disable %s] rc=%d\n", timer, dr.Returncode)
			if dr.Returncode != 0 || dr.Error != "" {
				agg.Returncode = 1
				agg.Error = fmt.Sprintf("absent 的 disable 失败, 未继续 rm: %s",
					firstNonEmpty(dr.Error, dr.Stderr))
				agg.Stderr = dr.Stderr
				return agg
			}
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			agg.Returncode = 1
			agg.Error = fmt.Sprintf("absent 删除 timer 文件失败: %v", err)
			return agg
		}
		rl := systemctlUser(ctx, "daemon-reload")
		agg.Stdout += fmt.Sprintf("[systemctl --user daemon-reload] rc=%d\n", rl.Returncode)
		if rl.Returncode != 0 || rl.Error != "" {
			agg.Returncode = 1
			agg.Error = fmt.Sprintf("daemon-reload 失败: %s", firstNonEmpty(rl.Error, rl.Stderr))
			agg.Stderr = rl.Stderr
			return agg
		}
		return agg
	}
	// unreachable due to taskStateOK 守卫
	return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("unknown desired_state %q", in.DesiredState)}
}

// Rollback (g-h): 三连重校验 + 快照 unit_file 守卫 → file_content 漂移恢复(写后
// daemon-reload)→ enabled_state 恢复(表外值不发动词)。
func (taskSetAdapter) Rollback(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseTaskSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if !taskStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			`unknown desired_state %q (v1 支持 enabled|disabled|absent)`, in.DesiredState)}
	}
	timer, err := normalizeTimerName(in.Name)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	target, err := resolveUnitFile(ctx, timer)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if err := guardUnitPath(ctx, target); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if len(step.BeforeState) == 0 {
		return tx.OpResult{Returncode: 1, Error: "rollback 缺少 before_state 快照"}
	}
	var before taskSetBefore
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

	// 恢复文件(仅在漂移时): file_content 非 nil 且当前内容 ≠ 快照 → restoreUnitFile;
	// 文件 content 为 nil(Propose 时本就不存在)而当前文件存在 → **不删除**(A3
	// 非适配器所建文件,stdout 记原因)。
	if before.FileContent != nil {
		cur, rerr := readUnitFileSnapshot(before.UnitFile)
		if rerr != nil {
			return tx.OpResult{Returncode: 1, Error: rerr.Error()}
		}
		if cur == nil || *cur != *before.FileContent {
			if _, werr := restoreUnitFile(before.UnitFile, *before.FileContent); werr != nil {
				return tx.OpResult{Returncode: 1, Error: werr.Error()}
			}
			dr := systemctlUser(ctx, "daemon-reload")
			agg.Stdout += fmt.Sprintf("[systemctl --user daemon-reload] rc=%d\n", dr.Returncode)
			if dr.Returncode != 0 || dr.Error != "" {
				agg.Returncode = 1
				agg.Error = fmt.Sprintf("daemon-reload 失败, 未施加恢复动词: %s",
					firstNonEmpty(dr.Error, dr.Stderr))
				agg.Stderr = dr.Stderr
				return agg
			}
		}
	} else {
		// file_content 为 nil 且当前文件存在 → 不删(任务 5 / Review Focus #3)。
		if cur, rerr := readUnitFileSnapshot(before.UnitFile); rerr == nil && cur != nil {
			agg.Stdout += fmt.Sprintf(
				"file_content 在提议时为 nil(snapshot 记录无原文件), 当前文件存在 %s, 跳过删除(非适配器所建)\n",
				before.UnitFile)
		}
	}

	verb, ok := taskSetInverseVerbs[before.EnabledState]
	if !ok {
		agg.Stdout += fmt.Sprintf(
			"enabled_state=%q 无恢复动词映射, 跳过 systemctl 动作(尽力恢复语义)\n",
			before.EnabledState)
		return agg
	}
	vr := systemctlUser(ctx, verb, timer)
	agg.Stdout += fmt.Sprintf("[systemctl --user %s %s] rc=%d\n", verb, timer, vr.Returncode)
	if vr.Stderr != "" {
		agg.Stderr += vr.Stderr + "\n"
	}
	if vr.Returncode != 0 || vr.Error != "" {
		agg.Returncode = 1
		agg.Error = fmt.Sprintf("恢复动词 %s 失败: %s", verb, firstNonEmpty(vr.Error, vr.Stderr))
	}
	return agg
}
