package main

// container_set.go —— container.set 事务适配器(A-form 严格生命周期管理)。
//
// 三段生命周期(与 adapter.go 的 Adapter 契约对齐):
//   - Propose: 解析 {name, desired_state} → euid==0 拒(rootless-only 守门)→
//     containerStateOK 词表门 → podmanNameGate 名称门 → observeContainerState
//     → 容器是否存在 + desired_state 组合 fail-closed / idempotent 分流 →
//     双快照序列化。无写副作用。
//   - Apply:   三连重校验 → 词表映射: present=空 verb/no-op assert; absent=rm --force;
//     started=必要时 start; stopped=必要时 stop. 返 rc/Stdout/Stderr 原样.
//   - Rollback: 三连重校验 + 快照解析 → 若 before.ContainerID=="" (absent 单向)
//     走 no-op + stdout note(spec G8, 不可逆);否则按 before.StateRunning 表外 + 影
//     → start/stop 恢复(纵深防御).
//
// desired_state 语义面:
//   - A-form 严格: present/started/stopped 三值 要求容器已存在,Propose 不在场即拒;
//     absent 接受不存在(idempotent,快照为空,Apply 也是 no-op).
//   - 词表 = present | absent | started | stopped 四值封闭, containerStateOK 守门.
//   - "absent" 单一不可逆: Apply 跑 rm --force 后无法 auto-recreate; Rollback
//     返 rc=0 + stdout note(让消费方 audit `daedalus_tx_rollback` 行看见事件),
//     **不**调用 podman run(B-form 工具,本 plan 范围外).
//
// euid 守门: 与 service.set 镜像 user-scope-only 反面;但 euid==0 报 rootful 路径
// 风险(共享 quadlet+service.set 协同通道的字面文案).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// containerScopeRejectMsg 三段共用 euid==0 拒串(逐字锁定;测试与
// AGENTS.md "shared 容器走 quadlet+service.set" 描述引用)。
const containerScopeRejectMsg = "container.set requires rootless (euid=0 indicates rootful path; use quadlet + service.set for rootful containers)"

// containerSetVerbs A-form desired_state → podman 子命令的冻结映射:
// present 映射空 verb(no-op), absent=rm, started=start, stopped=stop. 表外
// 由 containerSetArgs 解析前的 containerStateOK 拒; Propose 提前拒,与
// package.set / task.set 同款.
var containerSetVerbs = map[string]string{
	"present": "",
	"absent":  "rm",
	"started": "start",
	"stopped": "stop",
}

// containerSetInverseVerbs 恢复语义: before.StateRunning=true (字符串 "true") →
// start; =false ("false") → stop; 缺失 → no-op + stdout reason(spec 镜 task.set).
var containerSetInverseVerbs = map[string]string{
	"true":  "start",
	"false": "stop",
}

// containerSetArgs 步骤 args 的线上形态(锁定两键 schema, 未知键拒绝).
type containerSetArgs struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
}

// containerSetBefore Propose 期的现状快照:
//   - ContainerName 恒在;
//   - ContainerID / StateRunning / Image / Status 在 idempotent absent 路径
//     上整键缺席(omitempty).
//   - 容器存在时全部填充.
type containerSetBefore struct {
	ContainerName string `json:"container_name"`
	ContainerID   string `json:"container_id,omitempty"`
	StateRunning  *bool  `json:"state_running,omitempty"`
	Image         string `json:"image,omitempty"`
	Status        string `json:"status,omitempty"`
}

// containerSetAfter 提议的目标态视图: verb 为 podman 子命令或空(present).
type containerSetAfter struct {
	Container    string `json:"container"`
	DesiredState string `json:"desired_state"`
	Verb         string `json:"verb,omitempty"`
}

// containerSetAdapter 无状态空壳: 三段已由 Task 4 / Task 5 全填; 当前 Task 仅
// 实现 Propose, Apply/Rollback 暂为 not-implemented stub,跟 task.set Plan-2
// 阶段相同.
type containerSetAdapter struct{}

// containerStateOK 报告 state 是否属于 v1 container.set 的 desired_state 集合
// (A-form 四值封闭).
func containerStateOK(state string) bool {
	_, ok := containerSetVerbs[state]
	return ok
}

// parseContainerSetArgs 严格解析(DisallowUnknownFields 镜像 parseServiceSetArgs).
func parseContainerSetArgs(raw json.RawMessage) (containerSetArgs, error) {
	var in containerSetArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid container.set args: %w", err)
	}
	if dec.More() {
		return in, errors.New(`invalid container.set args: JSON 文档后有尾随数据`)
	}
	if in.DesiredState == "" {
		return in, errors.New(`invalid container.set args: 缺 "desired_state" 字段`)
	}
	return in, nil
}

// Propose (a-c 全段): 三连重校验 → euid gate → 词表门 → 名称门 → observeContainerState
// → 容器是否存在 / desired_state 组合 fail-closed 分流 → 双快照序列化.
func (containerSetAdapter) Propose(ctx context.Context, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	in, err := parseContainerSetArgs(args)
	if err != nil {
		return nil, nil, err
	}
	// euid gate(euid==0 = rootful,本 kind rootless-only).
	if geteuid() == 0 {
		return nil, nil, errors.New(containerScopeRejectMsg)
	}
	if !containerStateOK(in.DesiredState) {
		return nil, nil, fmt.Errorf(`unknown desired_state %q (v1 支持 present|absent|started|stopped)`, in.DesiredState)
	}
	if err := podmanNameGate(in.Name); err != nil {
		return nil, nil, err
	}
	running, id, image, status, exists, err := observeContainerState(ctx, in.Name)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		if in.DesiredState != "absent" {
			// present/started/stopped 在不存在时 = A-form strict 拒
			return nil, nil, fmt.Errorf("container %q not found; create out of v1 scope", in.Name)
		}
		// absent + 不存在 → idempotent(snapshot 全空字段).
		before, _ := json.Marshal(containerSetBefore{ContainerName: in.Name})
		after, _ := json.Marshal(containerSetAfter{
			Container:    in.Name,
			DesiredState: in.DesiredState,
			Verb:         containerSetVerbs[in.DesiredState],
		})
		return before, after, nil
	}
	// 存在时:四状态都放行 Propose;StateRunning 用指针记录 true/false.
	before, err := json.Marshal(containerSetBefore{
		ContainerName: in.Name,
		ContainerID:   id,
		StateRunning:  &running,
		Image:         image,
		Status:        status,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 before_state 失败: %w", err)
	}
	after, err := json.Marshal(containerSetAfter{
		Container:    in.Name,
		DesiredState: in.DesiredState,
		Verb:         containerSetVerbs[in.DesiredState],
	})
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 after_state 失败: %w", err)
	}
	return before, after, nil
}

// Apply: 三连重校验(args / name / euid / 词表)→ 按 desired_state 路由:
//   - present: 二次 inspect 确认仍存在(纵深防御); 存在 → rc=0 no-op; 不存在 → rc=1.
//   - absent:  podman rm --force <name>,rc 原值透传.
//   - started: 若 before.StateRunning==false → podman start; 已 running → no-op.
//   - stopped: 若 before.StateRunning==true → podman stop; 已 stopped → no-op.
// default 不可达(词表门已拦);兜底返 rc=1.
func (containerSetAdapter) Apply(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseContainerSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if geteuid() == 0 {
		return tx.OpResult{Returncode: 1, Error: containerScopeRejectMsg}
	}
	if !containerStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			`unknown desired_state %q (v1 支持 present|absent|started|stopped)`, in.DesiredState)}
	}
	if err := podmanNameGate(in.Name); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}

	switch in.DesiredState {
	case "present":
		_, _, _, _, exists, err := observeContainerState(ctx, in.Name)
		if err != nil {
			return tx.OpResult{Returncode: 1, Error: err.Error()}
		}
		if !exists {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
				"container %q not found at apply (present assert)", in.Name)}
		}
		return tx.OpResult{}

	case "absent":
		res := podmanExec(ctx, "rm", "--force", in.Name)
		if res.Returncode != 0 && res.Error == "" {
			res.Error = fmt.Sprintf("podman rm --force %s 失败: rc=%d %s", in.Name, res.Returncode, res.Stderr)
		}
		return res

	case "started", "stopped":
		// started/stopped 需要 before.StateRunning 判定是否 no-op.
		var before containerSetBefore
		if len(step.BeforeState) == 0 {
			return tx.OpResult{Returncode: 1, Error: "apply 缺少 before_state 快照"}
		}
		if err := json.Unmarshal(step.BeforeState, &before); err != nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("before_state 解析失败: %v", err)}
		}
		if before.StateRunning == nil {
			return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
				"apply %s 需要 before.state_running, 但快照未记录", in.DesiredState)}
		}
		wantRunning := in.DesiredState == "started"
		if *before.StateRunning == wantRunning {
			return tx.OpResult{} // 已处于目标态,no-op
		}
		verb := containerSetVerbs[in.DesiredState]
		res := podmanExec(ctx, verb, in.Name)
		if res.Returncode != 0 && res.Error == "" {
			res.Error = fmt.Sprintf("podman %s %s 失败: rc=%d %s", verb, in.Name, res.Returncode, res.Stderr)
		}
		return res
	}
	return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("unknown desired_state %q", in.DesiredState)}
}

// Rollback: 三连重校验 + 快照解析 + G8 absent 单向不可逆分支 + 双状态恢复.
//   - absent 路径(快照的 ContainerID 空,表明 Propose 期容器不存在):返 rc=0
//     + stdout note,不调 podman(单向不可逆,B-form 才能 recreate).
//   - before.StateRunning == nil: 返 rc=0 + stdout reason("状态未知, skip restore verb").
//   - before.StateRunning 已知: 映射到 start/stop(被 containerSetInverseVerbs 锁),
//     调 podman;rc 原值透传.
func (containerSetAdapter) Rollback(ctx context.Context, step tx.Step) tx.OpResult {
	in, err := parseContainerSetArgs(step.Args)
	if err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if geteuid() == 0 {
		return tx.OpResult{Returncode: 1, Error: containerScopeRejectMsg}
	}
	if !containerStateOK(in.DesiredState) {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			`unknown desired_state %q (v1 支持 present|absent|started|stopped)`, in.DesiredState)}
	}
	if err := podmanNameGate(in.Name); err != nil {
		return tx.OpResult{Returncode: 1, Error: err.Error()}
	}
	if len(step.BeforeState) == 0 {
		return tx.OpResult{Returncode: 1, Error: "rollback 缺少 before_state 快照"}
	}
	var before containerSetBefore
	if err := json.Unmarshal(step.BeforeState, &before); err != nil {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf("before_state 解析失败: %v", err)}
	}

	// (G8) absent 路径 = 单向不可逆, 与 before 详情无关.
	// Apply 调了 rm --force 后,容器在 daedalus 视角下已消失;recreate 需要 run-spec,
	// 不在 v1 范围(B-form plan).本路径返 rc=0 + stdout note,不调 podman.
	// idempotent absent(Propose 期容器不存在)也归此路径(快照空字段,无需区分).
	if in.DesiredState == "absent" {
		return tx.OpResult{Returncode: 0, Stdout: fmt.Sprintf(
			"container %q was removed; recreation requires run-spec not in v1 scope",
			in.Name)}
	}
	// before.StateRunning 缺失(理论上不该发生, 防御性):不发动词.
	if before.StateRunning == nil {
		return tx.OpResult{Returncode: 0, Stdout: fmt.Sprintf(
			"container %q present state unknown (no StateRunning snapshot); skip restore verb",
			in.Name)}
	}
	key := fmt.Sprintf("%t", *before.StateRunning)
	verb, ok := containerSetInverseVerbs[key]
	if !ok {
		return tx.OpResult{Returncode: 1, Error: fmt.Sprintf(
			"unexpected StateRunning 序列化值 %q, 不在 inverse 词表", key)}
	}
	res := podmanExec(ctx, verb, in.Name)
	if res.Returncode != 0 && res.Error == "" {
		res.Error = fmt.Sprintf("podman %s %s 失败: rc=%d %s", verb, in.Name, res.Returncode, res.Stderr)
	}
	return res
}
