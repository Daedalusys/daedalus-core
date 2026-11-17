package fleet

// 本文件钉住心跳预检的三条判据:只有 Ack=true 才算活;Ack=false 是 agent 自认
// 不该接活(按失败处理,不能只看 err==nil);时限取自心跳口径而非工作 RPC 的
// 60s 兜底 —— 后者会把卡死的节点晾到分钟级。
// bufconn + 自签 CA 的装配手法复用 client_test.go,不在这里重造 PKI 助手。

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
)

// ackStub 是按需回 ack 的 agent 桩,并记下收到的 node_id(证明预检报的是目标节点)。
type ackStub struct {
	agentv1.UnimplementedAgentServiceServer
	ack       bool
	seenNode  string
	respondTo chan struct{} // 非 nil 时:收到请求先阻塞到该通道关闭,模拟"连得上但不回"
}

func (s *ackStub) Heartbeat(_ context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	s.seenNode = req.GetNodeId()
	if s.respondTo != nil {
		<-s.respondTo
	}
	return &agentv1.HeartbeatResponse{Ack: s.ack, NextHeartbeatSeconds: 30}, nil
}

// bufconnScheduler 起一个跑在 bufconn 上的双向 TLS 桩,返回指向它的调度器。
// 装配手法与 client_test.go 的 TestDial_SucceedsOverBufconnWithMutualTLS 同源。
func bufconnScheduler(t *testing.T, stub agentv1.AgentServiceServer, timeout time.Duration) *Scheduler {
	t.Helper()
	pki := newTestPKI(t)
	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = lis.Close() })
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsServerConfig(pki, certPoolForTest(t, pki.caPath)))))
	agentv1.RegisterAgentServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	t.Cleanup(overrideDialForBufconn(t, lis))

	return &Scheduler{
		HeartbeatTimeout: timeout,
		dial: DialConfig{
			Addr:       "passthrough:///bufnet",
			ServerName: pki.serverName,
			CAPath:     pki.caPath,
			CertPath:   pki.clientCertPath,
			KeyPath:    pki.clientKeyPath,
		},
	}
}

// heartbeatTarget 造一条指向 bufconn 的 dispatch 目标。
func heartbeatTarget(nodeID string) DispatchTarget {
	n := testNode(nodeID)
	n.Addr = "passthrough:///bufnet"
	return DispatchTarget{Node: n}
}

// TestDialHeartbeat_AckTrueReturnsNil 是正向控制组:Ack=true 的节点预检必须通过。
func TestDialHeartbeat_AckTrueReturnsNil(t *testing.T) {
	stub := &ackStub{ack: true}
	s := bufconnScheduler(t, stub, 5*time.Second)

	if err := s.dialHeartbeat(context.Background(), heartbeatTarget("n1")); err != nil {
		t.Fatalf("Ack=true 的心跳预检必须通过, got %v", err)
	}
	if stub.seenNode != "n1" {
		t.Errorf("心跳报的 node_id = %q, want %q(预检必须报目标节点)", stub.seenNode, "n1")
	}
}

// TestDialHeartbeat_AckFalseIsFailure 钉住 Review Focus:agent 回 Ack=false 说明它
// 自认不该接活(例如正在退出)。只判 err==nil 会把这种节点当活的放行。
func TestDialHeartbeat_AckFalseIsFailure(t *testing.T) {
	stub := &ackStub{ack: false}
	s := bufconnScheduler(t, stub, 5*time.Second)

	err := s.dialHeartbeat(context.Background(), heartbeatTarget("n1"))
	if err == nil {
		t.Fatal("Ack=false 必须判预检失败")
	}
	if !strings.Contains(err.Error(), "n1") {
		t.Errorf("错误文案必须点名节点 id, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "ack=false") {
		t.Errorf("错误文案应讲清 ack=false 这条事实, got %q", err.Error())
	}
}

// TestDialHeartbeat_UsesHeartbeatTimeoutNotCallTimeout 钉住"心跳口径"真的生效:
// 一台连得上但不回的节点(进程卡死 / 磁盘 hang 的常态),预检必须在心跳时限内
// 放弃,而不是晾到工作 RPC 的 60s 兜底。
func TestDialHeartbeat_UsesHeartbeatTimeoutNotCallTimeout(t *testing.T) {
	stub := &ackStub{ack: true, respondTo: make(chan struct{})}
	s := bufconnScheduler(t, stub, 150*time.Millisecond)

	start := time.Now()
	err := s.dialHeartbeat(context.Background(), heartbeatTarget("n1"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("对不回的节点,心跳预检必须超时失败")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("预检耗时 %s:时限没走心跳口径(150ms),退回了工作 RPC 的兜底", elapsed)
	}
	if !strings.Contains(err.Error(), "n1") {
		t.Errorf("错误文案必须点名节点 id, got %q", err.Error())
	}
}
