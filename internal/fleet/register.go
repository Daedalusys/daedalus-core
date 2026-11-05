// internal/fleet/register.go:控制面主动向 agent 发起 Register,把结果组装成节点行。
//
// 控制面主动 outbound、agent 常驻 listen(spec §4.4):登记由控制面发起,被控端
// 不开第二个入口。support_level 由 agent 自报的发行版/版本判定;自报缺失或落在
// 矩阵外一律 experimental——分级诚实度:不知道就不打 supported。
package fleet

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// RegisterOptions 是控制面登记一台节点时要写下的字段。
//
// ★ proto 缝:RegisterResponse 只回带 node_id 与 snapshot_capability,**不回带**
// OS 信息(OsInfo 只在 RegisterRequest 上)。故 distro / version / hostname 只能
// 由操作者经 CLI 提供;缺失时 support_level 记 experimental(分级诚实度:不知道
// 就不打 supported),绝不臆造。要让控制面自动学到 OS 信息,需要 proto 在响应侧
// 补字段(归 sdk#28 范围)。
type RegisterOptions struct {
	AgentID       string
	Kind          objectmodel.MachineKind
	Distro        string
	DistroVersion string
	Hostname      string
}

// RegisterNode 向 cfg.Addr 处的 agent 发起 Register,组装出一行 Node。
// 调用方负责用 ctx 或 cfg.CallTimeout 限定超时。
func RegisterNode(ctx context.Context, cfg DialConfig, opts RegisterOptions) (Node, error) {
	if !opts.Kind.Valid() {
		return Node{}, fmt.Errorf("fleet: 节点形态 %q 非法(只接受 atomic|agent)", opts.Kind)
	}
	if opts.AgentID == "" {
		return Node{}, fmt.Errorf("fleet: agent_id 必填")
	}
	client, closer, err := Dial(ctx, cfg)
	if err != nil {
		return Node{}, err
	}
	defer func() { _ = closer() }()

	resp, err := client.Register(ctx, &agentv1.RegisterRequest{AgentId: opts.AgentID})
	if err != nil {
		return Node{}, fmt.Errorf("fleet: Register %s 失败: %w", cfg.Addr, err)
	}
	if resp.GetNodeId() == "" {
		return Node{}, fmt.Errorf("fleet: agent %s 未返回 node_id(注册未完成)", cfg.Addr)
	}

	level, err := SupportLevelFor(opts.Distro, opts.DistroVersion)
	if err != nil {
		// 操作者没给发行版/版本属"信息不足",不是误用:按 experimental 记录,
		// 绝不因为不知道就打 supported。
		level = objectmodel.SupportLevelExperimental
	}

	now := time.Now().Unix()
	return Node{
		NodeID:           resp.GetNodeId(),
		AgentID:          opts.AgentID,
		Kind:             opts.Kind,
		Addr:             cfg.Addr,
		Distro:           opts.Distro,
		DistroVersion:    opts.DistroVersion,
		Hostname:         opts.Hostname,
		SnapshotCap:      normalizeSnapshotCap(resp.GetSnapshotCapability()),
		SupportLevel:     level,
		RegisteredAtUnix: now,
		LastSeenUnix:     now,
	}, nil
}
