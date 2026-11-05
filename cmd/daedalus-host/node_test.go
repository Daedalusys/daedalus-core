package main

// node 子命令的测试:单行 JSON 契约、support_level 分级诚实度、node add 的
// 节点形态校验。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// TestNodeList_EmptyRegistryRendersEmptyArray 钉住空名单是 [] 而非 null。
func TestNodeList_EmptyRegistryRendersEmptyArray(t *testing.T) {
	setupFleetCLI(t)
	code, out, errOut := runCapture(t, "node", "list")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	if got := strings.TrimSpace(out); got != `{"nodes":[]}` {
		t.Errorf("空名单 stdout = %q, want {\"nodes\":[]}", got)
	}
}

// TestNodeList_RendersSupportLevel 钉住 node list 必须带 support_level 且取值
// 落在 {supported, experimental}(分级诚实度)。
func TestNodeList_RendersSupportLevel(t *testing.T) {
	setupFleetCLI(t)
	seedNode(t, testNodeRow("n-b", objectmodel.SupportLevelExperimental))
	seedNode(t, testNodeRow("n-a", objectmodel.SupportLevelSupported))

	code, out, errOut := runCapture(t, "node", "list")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	var got struct {
		Nodes []fleet.Node `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout 不是合法 JSON: %v(%s)", err, out)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("节点数 = %d, want 2", len(got.Nodes))
	}
	if got.Nodes[0].NodeID != "n-a" || got.Nodes[1].NodeID != "n-b" {
		t.Errorf("顺序应为 node_id 升序, got %q,%q", got.Nodes[0].NodeID, got.Nodes[1].NodeID)
	}
	for _, n := range got.Nodes {
		if n.SupportLevel != objectmodel.SupportLevelSupported && n.SupportLevel != objectmodel.SupportLevelExperimental {
			t.Errorf("节点 %s 的 support_level = %q,不在 {supported, experimental} 内", n.NodeID, n.SupportLevel)
		}
	}
}

// TestNodeAdd_WritesRegistryAndSupportLevel 证明 node add 把登记结果落进
// registry,并把支持分级一并算好(缺失发行版不得打 supported)。
func TestNodeAdd_WritesRegistryAndSupportLevel(t *testing.T) {
	root := setupFleetCLI(t)
	orig := addNode
	addNode = func(_ context.Context, cfg fleet.DialConfig, opts fleet.RegisterOptions) (fleet.Node, error) {
		if cfg.Addr != "10.0.0.11:9443" {
			t.Errorf("DialConfig.Addr = %q, want 10.0.0.11:9443", cfg.Addr)
		}
		if opts.Kind != objectmodel.MachineKindAgent {
			t.Errorf("Kind = %q, want agent(默认)", opts.Kind)
		}
		level, err := fleet.SupportLevelFor(opts.Distro, opts.DistroVersion)
		if err != nil {
			t.Fatalf("SupportLevelFor: %v", err)
		}
		return fleet.Node{
			NodeID: "n1", AgentID: opts.AgentID, Kind: opts.Kind, Addr: cfg.Addr,
			Distro: opts.Distro, DistroVersion: opts.DistroVersion, Hostname: opts.Hostname,
			SnapshotCap:      "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME",
			SupportLevel:     level,
			RegisteredAtUnix: 1700000000, LastSeenUnix: 1700000000,
		}, nil
	}
	t.Cleanup(func() { addNode = orig })

	code, out, errOut := runCapture(t, "node", "add", "10.0.0.11:9443",
		"--agent-id", "a1", "--distro", "ubuntu", "--distro-version", "24.04")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0(stderr=%s)", code, errOut)
	}
	var got struct {
		Node fleet.Node `json:"node"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout 不是合法 JSON: %v(%s)", err, out)
	}
	if got.Node.SupportLevel != objectmodel.SupportLevelSupported {
		t.Errorf("support_level = %q, want supported", got.Node.SupportLevel)
	}
	if got.Node.SnapshotCap != "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME" {
		t.Errorf("snapshot_capability = %q, want BTRFS_SUBVOLUME", got.Node.SnapshotCap)
	}

	// 同一行必须已经落进 registry(同根,list 能读到)。
	code, listOut, _ := runCapture(t, "node", "list")
	if code != exitOK {
		t.Fatalf("node list 退出码 = %d", code)
	}
	if !strings.Contains(listOut, `"node_id":"n1"`) {
		t.Errorf("registry 里没有刚登记的节点(根=%s): %s", root, listOut)
	}
}

// TestNodeAdd_RejectsUnknownKind 证明节点形态表外值被拒在用法层(不下发 RPC)。
func TestNodeAdd_RejectsUnknownKind(t *testing.T) {
	setupFleetCLI(t)
	called := false
	orig := addNode
	addNode = func(context.Context, fleet.DialConfig, fleet.RegisterOptions) (fleet.Node, error) {
		called = true
		return fleet.Node{}, nil
	}
	t.Cleanup(func() { addNode = orig })

	code, _, _ := runCapture(t, "node", "add", "10.0.0.11:9443", "--agent-id", "a1", "--kind", "server")
	if code != exitUsage {
		t.Errorf("退出码 = %d, want %d(用法错误)", code, exitUsage)
	}
	if called {
		t.Error("非法 --kind 不得发起 Register")
	}
}
