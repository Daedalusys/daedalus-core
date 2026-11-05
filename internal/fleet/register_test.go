package fleet

// 本文件用 bufconn 起一个内存 agent 桩,验证 RegisterNode 的装配与 support_level
// 判定(分级诚实度:操作者未给发行版 → experimental,绝不打 supported)。

import (
	"context"
	"testing"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// registerStub 是只实现 Register 的 agent 桩。
type registerStub struct {
	agentv1.UnimplementedAgentServiceServer
	nodeID   string
	snapshot agentv1.SnapshotCapability
}

func (s registerStub) Register(_ context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if req.GetAgentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id 必填")
	}
	return &agentv1.RegisterResponse{NodeId: s.nodeID, SnapshotCapability: s.snapshot}, nil
}

// TestRegisterNode_AssemblesNodeAndSupportLevel 证明 RegisterNode 把控制面权威
// 字段(node_id / snapshot capability)与操作者给出的 OS 事实组装成一行节点,
// 并按 support_level 矩阵定级;未给发行版时记 experimental。
func TestRegisterNode_AssemblesNodeAndSupportLevel(t *testing.T) {
	cases := []struct {
		name         string
		distro       string
		version      string
		wantSupport  objectmodel.SupportLevel
		wantHostname string
	}{
		{"矩阵内发行版", "ubuntu", "24.04", objectmodel.SupportLevelSupported, "host-a"},
		{"矩阵内 distro 的矩阵外版本", "debian", "13", objectmodel.SupportLevelExperimental, "host-a"},
		{"矩阵外发行版", "fedora", "42", objectmodel.SupportLevelExperimental, "host-a"},
		{"操作者未给发行版", "", "", objectmodel.SupportLevelExperimental, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pki := newTestPKI(t)
			lis := bufconn.Listen(1024 * 1024)
			defer lis.Close()
			srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsServerConfig(pki, certPoolForTest(t, pki.caPath)))))
			agentv1.RegisterAgentServiceServer(srv, registerStub{
				nodeID:   "node-authoritative",
				snapshot: agentv1.SnapshotCapability_SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME,
			})
			go func() { _ = srv.Serve(lis) }()
			defer srv.Stop()

			restore := overrideDialForBufconn(t, lis)
			defer restore()

			node, err := RegisterNode(context.Background(), DialConfig{
				Addr:       "passthrough:///bufnet",
				ServerName: pki.serverName,
				CAPath:     pki.caPath,
				CertPath:   pki.clientCertPath,
				KeyPath:    pki.clientKeyPath,
			}, RegisterOptions{
				AgentID:       "a1",
				Kind:          objectmodel.MachineKindAgent,
				Distro:        tc.distro,
				DistroVersion: tc.version,
				Hostname:      tc.wantHostname,
			})
			if err != nil {
				t.Fatalf("RegisterNode: %v", err)
			}
			if node.NodeID != "node-authoritative" {
				t.Errorf("NodeID = %q, want 控制面权威值 node-authoritative", node.NodeID)
			}
			if node.SnapshotCap != "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME" {
				t.Errorf("SnapshotCap = %q, want BTRFS_SUBVOLUME", node.SnapshotCap)
			}
			if node.SupportLevel != tc.wantSupport {
				t.Errorf("SupportLevel = %q, want %q", node.SupportLevel, tc.wantSupport)
			}
			if node.AgentID != "a1" || node.Kind != objectmodel.MachineKindAgent {
				t.Errorf("自报字段错: agent_id=%q kind=%q", node.AgentID, node.Kind)
			}
			if node.RegisteredAtUnix == 0 || node.LastSeenUnix == 0 {
				t.Errorf("时间戳必须落库: registered=%d last_seen=%d", node.RegisteredAtUnix, node.LastSeenUnix)
			}
		})
	}
}

// TestRegisterNode_RejectsBadKind 证明节点形态非法时不下发任何 RPC。
func TestRegisterNode_RejectsBadKind(t *testing.T) {
	if _, err := RegisterNode(context.Background(), DialConfig{}, RegisterOptions{
		AgentID: "a1",
		Kind:    objectmodel.MachineKind("bogus"),
	}); err == nil {
		t.Fatal("非法节点形态必须被拒")
	}
	if _, err := RegisterNode(context.Background(), DialConfig{}, RegisterOptions{
		Kind: objectmodel.MachineKindAgent,
	}); err == nil {
		t.Fatal("空 agent_id 必须被拒")
	}
}

// TestRegisterNode_NormalizesUnspecifiedSnapshotCapability 钉住 proto 的显式约定:
// Register 响应没带 snapshot 能力(UNSPECIFIED)时按 NONE 落库——留成 UNSPECIFIED
// 会让调度器的 RF1 前置拒绝失效("没探测到"被当成"有回滚点")。
func TestRegisterNode_NormalizesUnspecifiedSnapshotCapability(t *testing.T) {
	pki := newTestPKI(t)
	lis := bufconn.Listen(1024 * 1024)
	defer lis.Close()
	// 零值 = UNSPECIFIED:agent 没有填这个字段。
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsServerConfig(pki, certPoolForTest(t, pki.caPath)))))
	agentv1.RegisterAgentServiceServer(srv, registerStub{nodeID: "n1"})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	defer overrideDialForBufconn(t, lis)()

	node, err := RegisterNode(context.Background(), DialConfig{
		Addr:       "passthrough:///bufnet",
		ServerName: pki.serverName,
		CAPath:     pki.caPath,
		CertPath:   pki.clientCertPath,
		KeyPath:    pki.clientKeyPath,
	}, RegisterOptions{AgentID: "a1", Kind: objectmodel.MachineKindAgent})
	if err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	if node.SnapshotCap != "SNAPSHOT_CAPABILITY_NONE" {
		t.Errorf("SnapshotCap = %q, want SNAPSHOT_CAPABILITY_NONE(proto 要求 UNSPECIFIED 按 none 处理)", node.SnapshotCap)
	}
}
