package main

// policy_set.go —— policy.set 事务适配器(manager-only,A-form)。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析 {name, desired_state, content_ref} → euid==0 守门 → 词表门 →
//     ref 解析(相对路径 + 允许根守卫)→ 读当前 policy 文件(A-form 要求已存在)
//     → 读 ref 文件(loaded)/ 跳过(absent)→ 双快照 {policy_path, previous_content,
//     previous_sha256} / {policy_path, desired_state, source_ref}。
//   - Apply:   三连重校验 → drift 断言(当前 sha256 == before.previous_sha256)
//     → 写盘(loaded=ref 内容 / absent=sentinel)→ 读回 sha256 校验(A5)。
//   - Rollback: 三连重校验 → 把 before.previous_content 写回 + 读回校验。
//
// desired_state 语义面:
//   - loaded = 系统策略应为 content_ref 指向的内容;drift = 当前文件与 Propose 快照不一致.
//   - absent = kill switch;写 SDK DisabledSentinel → policy.Load 返 ErrDisabled → 进程 fail-closed.
//   - absent 与 container 不同:**可逆**(policy 是文件,BeforeState 带旧内容,Rollback 能恢复).
//
// 范围:manager-only(A-form);agent 侧不实现,kill switch 由 fleet 通道分发.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// policyDirCtxKey --policy-dir 旗标的 context 载体键(类型同名于 unitDirCtxKey
// 模式;私有空 struct 绝不用内置类型当键, 防跨包碰撞)。CLI 侧由 commands.go
// 注入;测试通过 withPolicyDir 直接覆写。不改 Adapter 接口签名。
type policyDirCtxKey struct{}

// withPolicyDir 把非空 policy-dir 挂进 ctx;空串原样返回(与 withUnitDir 同款).
func withPolicyDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, policyDirCtxKey{}, dir)
}

// policyDirFrom 读 ctx 内的 --policy-dir 覆写;未设置 → ""(调用方自行回退 CWD).
func policyDirFrom(ctx context.Context) string {
	s, _ := ctx.Value(policyDirCtxKey{}).(string)
	return s
}

// policySetVerbs desired_state 语义标记(不是 action 名——真实动作在 Apply 里按
// 状态 switch)。loaded → "load";absent → "disable"。表外值由 policyStateOK 拒。
var policySetVerbs = map[string]string{
	"loaded": "load",
	"absent": "disable",
}

// policySetArgs 是步骤 args 的线上形态(锁定三键 schema, 未知键拒绝)。
type policySetArgs struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
	ContentRef   string `json:"content_ref"`
}

// policySetBefore Propose 期现状快照: 写盘前的 policy.toml 内容与 sha256;
// absent 与 loaded 都记录(absent 也需旧内容以支持可逆 Rollback).
type policySetBefore struct {
	PolicyPath      string `json:"policy_path"`
	PreviousContent string `json:"previous_content"`
	PreviousSha256  string `json:"previous_sha256"`
}

// policySetAfter 提议的目标态视图: source_ref 仅 loaded 有意义(absent 不留).
type policySetAfter struct {
	PolicyPath   string `json:"policy_path"`
	DesiredState string `json:"desired_state"`
	SourceRef    string `json:"source_ref,omitempty"`
}

// policySetAdapter 无状态空壳: 三段均由本 plan 落地(与 task/container 的 signature 一致).
type policySetAdapter struct{}

// policyStateOK 报告 state 是否属于 v1 policy.set 的 desired_state 集合(两值封闭).
func policyStateOK(state string) bool {
	_, ok := policySetVerbs[state]
	return ok
}

// parsePolicySetArgs 严格解析(DisallowUnknownFields,镜像其它 adapter).
func parsePolicySetArgs(raw json.RawMessage) (policySetArgs, error) {
	var in policySetArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid policy.set args: %w", err)
	}
	if dec.More() {
		return in, errors.New(`invalid policy.set args: JSON 文档后有尾随数据`)
	}
	if in.DesiredState == "" {
		return in, errors.New(`invalid policy.set args: 缺 "desired_state" 字段`)
	}
	return in, nil
}

// Propose: euid 守门 → 词表门 → ref 解析(loaded)→ 当前文件读取(A-form)→ ref 读取(loaded)
// → 双快照序列化。
func (policySetAdapter) Propose(ctx context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parsePolicySetArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if geteuid() != 0 {
		return nil, nil, errors.New(policyScopeRejectMsg)
	}
	if !policyStateOK(in.DesiredState) {
		return nil, nil, fmt.Errorf(`unknown desired_state %q (v1 支持 loaded|absent)`, in.DesiredState)
	}
	content, err := readPolicyFileBytes(policyFilePath)
	if err != nil {
		return nil, nil, err
	}
	if content == nil {
		return nil, nil, fmt.Errorf("policy file not found: %s (A-form 要求 policy.toml 已存在;首次生成归 build)", policyFilePath)
	}
	if detectSentinel(content) {
		return nil, nil, fmt.Errorf("policy is already in absent (sentinel) state; roll back the prior absent Apply first, or use a fresh tx")
	}

	after := policySetAfter{PolicyPath: policyFilePath, DesiredState: in.DesiredState}
	if in.DesiredState == "loaded" {
		if in.ContentRef == "" {
			return nil, nil, errors.New(`invalid policy.set args: loaded 需要 "content_ref" 字段`)
		}
		// ref 解析: 相对路径 + 允许根守卫。baseDir = --policy-dir(若 ctx 注入)或 CWD。
		baseDir := policyDirFrom(ctx)
		if baseDir == "" {
			baseDir = "."
		}
		abs, rerr := policyPathResolve(in.ContentRef, baseDir)
		if rerr != nil {
			return nil, nil, rerr
		}
		refData, rerr := readPolicyFileBytes(abs)
		if rerr != nil {
			return nil, nil, rerr
		}
		if refData == nil {
			return nil, nil, fmt.Errorf("ref file not found: %s", abs)
		}
		after.SourceRef = in.ContentRef
	}

	before, err := json.Marshal(policySetBefore{
		PolicyPath:      policyFilePath,
		PreviousContent: string(content),
		PreviousSha256:  sha256Hex(content),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 before_state 失败: %w", err)
	}
	afterBytes, err := json.Marshal(after)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 after_state 失败: %w", err)
	}
	return before, afterBytes, nil
}

// Apply: 三连重校验 → drift 断言(当前 sha256 == before.previous_sha256)→
// 写盘(loaded=ref 内容 / absent=DisabledSentinel)→ 读回 sha256 校验(A5)。
// drift 失败 / 写回失败 / 任何中间失败均返 rc=1,不修改文件。
func (policySetAdapter) Apply(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parsePolicySetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if geteuid() != 0 {
		return tx.OpResult{Returncode: 1, Error: policyScopeRejectMsg}
	}
	if !policyStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			`unknown desired_state %q (v1 支持 loaded|absent)`, in.DesiredState)}
	}
	if len(step.BeforeState) == 0 {
		return tx.OpResult{Returncode: 1, Error: "apply 缺少 before_state 快照"}
	}
	var before policySetBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("before_state 解析失败: %v", err)}
	}
	if before.PolicyPath != policyFilePath {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			"apply 路径与 Propose 快照不一致: before=%s now=%s", before.PolicyPath, policyFilePath)}
	}

	// drift 断言: 当前文件 sha256 == before.previous_sha256;否则返 rc=1, 不写盘.
	current, err := readPolicyFileBytes(policyFilePath)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if current == nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			"policy file missing at apply (drift detected: present at Propose, gone now): %s", policyFilePath)}
	}
	if sha256Hex(current) != before.PreviousSha256 {
		return tx.OpResult{Returncode: 1, Error: "drift detected: policy file content changed between Propose and Apply (sha256 mismatch)"}
	}

	// 准备写入内容.
	var newContent []byte
	if in.DesiredState == "loaded" {
		baseDir := policyDirFrom(ctx)
		if baseDir == "" {
			baseDir = "."
		}
		abs, rerr := policyPathResolve(in.ContentRef, baseDir)
		if rerr != nil {
			return tx.OpResult{Returncode: 1, Error: rerr.Error()}
		}
		refData, rerr := readPolicyFileBytes(abs)
		if rerr != nil {
			return tx.OpResult{Returncode: 1, Error: rerr.Error()}
		}
		if refData == nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("ref file not found: %s", abs)}
		}
		newContent = refData
	} else { // absent
		newContent = []byte(policy.DisabledSentinel)
	}

	// 写盘 + 读回校验(A5).
	if err := writePolicyFileAtomic(policyFilePath, newContent); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}

	// absent 额外提醒: daemon 看到 sentinel 会 fail-closed; 切到下一步前应该 rollout 或 reload.
	if in.DesiredState == "absent" {
		return tx.OpResult{
			Returncode: 0,
			Stdout:     "WARNING: daedalus services will refuse to start until this is rolled back or re-loaded (policy.Load returns ErrDisabled on sentinel)",
		}
	}
	return tx.OpResult{}
}

// Rollback (Task 5 实现; Task 2 stub).
func (policySetAdapter) Rollback(ctx context.Context, step tx.Step) tx.OpResult {
	return tx.OpResult{Returncode: 1, Error: "policy.set Rollback not implemented (task 5)"}
}
