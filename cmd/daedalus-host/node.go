package main

// cmdNode 实现 node list / node add:控制面的被控节点名单维护。
// 与 daedalus-tx 的 stdout 纪律一致——机器可读的单行 JSON 是唯一契约。
//
// 控制面 P0.5 不引入常驻监听:名单由 node add 主动向 agent 发起 Register 填充
// (spec §4.4 控制面 outbound、agent inbound)。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// nodeRegisterTimeout 是 node add 单次 Register 的超时。
const nodeRegisterTimeout = 30 * time.Second

// addNode 是 node add 的登记注入缝:生产经 fleet.RegisterNode 主动 Register,
// 测试替换为构造好的节点行(不碰网络)。
var addNode = fleet.RegisterNode

// cmdNode 分派 node 子命令。
func cmdNode(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: daedalus-host node list | node add <addr> --agent-id <id> [--kind agent|atomic]")
		return exitUsage
	}
	switch args[0] {
	case "list":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "daedalus-host node list: 不接受多余参数 %v\n", args[1:])
			return exitUsage
		}
		return cmdNodeList(stdout, stderr)
	case "add":
		return cmdNodeAdd(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "daedalus-host node: 未知子命令 %q\n", args[0])
		return exitUsage
	}
}

// cmdNodeList 渲染 `{"nodes":[...]}`;空名单是 [] 而非 null(下游按数组消费)。
func cmdNodeList(stdout, stderr io.Writer) int {
	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintln(stderr, "node list:", err)
		return exitRuntime
	}
	nodes, err := reg.List()
	if err != nil {
		fmt.Fprintln(stderr, "node list:", err)
		return exitRuntime
	}
	if nodes == nil {
		nodes = []fleet.Node{}
	}
	out, err := json.Marshal(struct {
		Nodes []fleet.Node `json:"nodes"`
	}{Nodes: nodes})
	if err != nil {
		fmt.Fprintln(stderr, "node list:", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, string(out))
	controlPlaneAudit("host_node_list", map[string]string{"count": fmt.Sprint(len(nodes))}, exitOK)
	return exitOK
}

// cmdNodeAdd 向 <addr> 的 agent 发起 Register,把节点行写进 registry。
func cmdNodeAdd(stdout, stderr io.Writer, args []string) int {
	f, err := parseFleetFlags(args, []string{"agent-id", "kind", "distro", "distro-version", "hostname"}, nil)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host node add: %v\n", err)
		return exitUsage
	}
	if len(f.rest) != 1 {
		fmt.Fprintln(stderr, "usage: daedalus-host node add <addr> --agent-id <id> [--kind agent|atomic]"+
			" [--distro <d>] [--distro-version <v>] [--hostname <h>]")
		return exitUsage
	}
	agentID := f.strs["agent-id"]
	if agentID == "" {
		fmt.Fprintln(stderr, "daedalus-host node add: --agent-id 必填")
		return exitUsage
	}
	kindRaw := f.strs["kind"]
	if kindRaw == "" {
		kindRaw = string(objectmodel.MachineKindAgent)
	}
	kind := objectmodel.MachineKind(kindRaw)
	if !kind.Valid() {
		fmt.Fprintf(stderr, "daedalus-host node add: --kind 非法 %q(只接受 agent|atomic)\n", kindRaw)
		return exitUsage
	}

	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintln(stderr, "node add:", err)
		return exitRuntime
	}
	cfg := fleet.DialConfigFromEnv()
	cfg.Addr = f.rest[0]

	ctx, cancel := context.WithTimeout(context.Background(), nodeRegisterTimeout)
	defer cancel()
	node, err := addNode(ctx, cfg, fleet.RegisterOptions{
		AgentID:       agentID,
		Kind:          kind,
		Distro:        f.strs["distro"],
		DistroVersion: f.strs["distro-version"],
		Hostname:      f.strs["hostname"],
	})
	if err != nil {
		fmt.Fprintln(stderr, "node add:", err)
		return exitRuntime
	}
	if err := reg.Put(node); err != nil {
		fmt.Fprintln(stderr, "node add:", err)
		return exitRuntime
	}

	out, err := json.Marshal(struct {
		Node fleet.Node `json:"node"`
	}{Node: node})
	if err != nil {
		fmt.Fprintln(stderr, "node add:", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, string(out))
	controlPlaneAudit("host_node_add", map[string]string{"node_id": node.NodeID, "addr": node.Addr}, exitOK)
	return exitOK
}
