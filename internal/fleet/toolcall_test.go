package fleet_test

// 本文件钉住控制面只读工具调用面的两个契约:结局分类(ClassifyToolCall)与
// 取节点 → 建连 → 调 RPC → 关连接 的同一条路径(ToolCaller)。建连走注入缝指向
// bufconn 假 server,与 client_test.go 同一手法,不占真实端口。

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
)

// TestClassifyToolCall 钉住四种结局的判定口径:节点拒(codes.PermissionDenied /
// codes.FailedPrecondition)、节点不可达(codes.Unavailable / codes.DeadlineExceeded)、
// 工具自身失败(插件自报 IsError 或其它错误码)。既无响应也无错误不是成功。
func TestClassifyToolCall(t *testing.T) {
	cases := []struct {
		name string
		resp *agentv1.CallToolResponse
		err  error
		want fleet.ToolCallOutcome
	}{
		{"成功", &agentv1.CallToolResponse{IsError: false}, nil, fleet.ToolCallOK},
		{"插件自报错误", &agentv1.CallToolResponse{IsError: true, ErrorMessage: "越界"}, nil, fleet.ToolCallToolFailed},
		{"节点拒了", nil, status.Error(codes.PermissionDenied, "不在白名单"), fleet.ToolCallDenied},
		{"节点没配置", nil, status.Error(codes.FailedPrecondition, "未配置"), fleet.ToolCallDenied},
		{"节点不可达", nil, status.Error(codes.Unavailable, "连接被拒"), fleet.ToolCallNodeUnreachable},
		{"节点超时", nil, status.Error(codes.DeadlineExceeded, "调用超时"), fleet.ToolCallNodeUnreachable},
		{"其它错误码", nil, status.Error(codes.Internal, "插件进程崩溃"), fleet.ToolCallToolFailed},
		{"既无响应也无错误", nil, nil, fleet.ToolCallToolFailed},
	}
	for _, c := range cases {
		if got := fleet.ClassifyToolCall(c.resp, c.err); got != c.want {
			t.Errorf("%s: ClassifyToolCall = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestToolCaller_ListTools 钉住「节点报什么就返回什么」:工具清单的过滤
// (白名单 ∩ 插件自报只读)是节点侧的职责,控制面不二次判定。
func TestToolCaller_ListTools(t *testing.T) {
	want := []agentv1.ToolDescriptor{
		{PluginId: "daedalus.shell", ToolName: "shell_exec", Description: "受控命令执行", InputSchemaJson: `{"type":"object"}`, ReadOnly: true},
		{PluginId: "daedalus.svc", ToolName: "service_query", ReadOnly: true},
	}
	stub := &toolStub{tools: []*agentv1.ToolDescriptor{&want[0], &want[1]}}
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	caller := &fleet.ToolCaller{Registry: reg, Dial: bufconnDial(t, stub)}

	got, err := caller.ListTools(context.Background(), "lab-a")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("工具数 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].GetPluginId() != want[i].GetPluginId() || got[i].GetToolName() != want[i].GetToolName() || got[i].GetReadOnly() != want[i].GetReadOnly() {
			t.Errorf("第 %d 个工具 = %s/%s(只读=%v), want %s/%s(只读=%v)", i,
				got[i].GetPluginId(), got[i].GetToolName(), got[i].GetReadOnly(),
				want[i].GetPluginId(), want[i].GetToolName(), want[i].GetReadOnly())
		}
	}
}

// TestToolCaller_CallTool_PassesArgumentsVerbatim 钉住参数透传:网关/巡检不解析、
// 不改写入参,节点侧才拿得到原始 JSON。
func TestToolCaller_CallTool_PassesArgumentsVerbatim(t *testing.T) {
	stub := &toolStub{}
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	caller := &fleet.ToolCaller{Registry: reg, Dial: bufconnDial(t, stub)}

	const args = `{"command":"systemctl","args":["--failed","--no-pager"]}`
	resp, err := caller.CallTool(context.Background(), "lab-a", "daedalus.shell", "shell_exec", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallOK {
		t.Fatalf("ClassifyToolCall = %v, want %v", got, fleet.ToolCallOK)
	}
	if stub.callReq.GetArgumentsJson() != args {
		t.Errorf("服务端收到的 arguments_json = %q, want %q", stub.callReq.GetArgumentsJson(), args)
	}
	if stub.callReq.GetPluginId() != "daedalus.shell" || stub.callReq.GetToolName() != "shell_exec" {
		t.Errorf("服务端收到的定位 = %s/%s", stub.callReq.GetPluginId(), stub.callReq.GetToolName())
	}
}

// TestToolCaller_CallTool_PluginSelfReportedError 钉住「插件报错不等于节点拒」:
// RPC 成功但 IsError=true 时必须归到工具失败,不能算 denied / unreachable。
func TestToolCaller_CallTool_PluginSelfReportedError(t *testing.T) {
	stub := &toolStub{callResp: &agentv1.CallToolResponse{IsError: true, ErrorMessage: "路径越界"}}
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	caller := &fleet.ToolCaller{Registry: reg, Dial: bufconnDial(t, stub)}

	resp, err := caller.CallTool(context.Background(), "lab-a", "daedalus.fs", "read_file", `{"path":"/etc/shadow"}`)
	if err != nil {
		t.Fatalf("CallTool 不应把插件业务错误包成 RPC 错误: %v", err)
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallToolFailed {
		t.Errorf("ClassifyToolCall = %v, want %v", got, fleet.ToolCallToolFailed)
	}
}

// TestToolCaller_CallTool_DeniedByNode 钉住节点拒的归类:错误码原样透出,
// 由 ClassifyToolCall 判成 denied(不是不可达,也不是工具失败)。
func TestToolCaller_CallTool_DeniedByNode(t *testing.T) {
	stub := &toolStub{callErr: status.Error(codes.PermissionDenied, "工具不在远程调用白名单")}
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	caller := &fleet.ToolCaller{Registry: reg, Dial: bufconnDial(t, stub)}

	resp, err := caller.CallTool(context.Background(), "lab-a", "daedalus.fs", "write_file", `{}`)
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallDenied {
		t.Errorf("ClassifyToolCall = %v, want %v(err=%v)", got, fleet.ToolCallDenied, err)
	}
}

// TestToolCaller_UnregisteredNodeIsUnreachable 钉住注册表未命中:节点不存在时
// 不建连、不调 RPC,错误必须带上 Unavailable 语义,让调用方归到「节点不可达」。
func TestToolCaller_UnregisteredNodeIsUnreachable(t *testing.T) {
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	dialed := false
	caller := &fleet.ToolCaller{
		Registry: reg,
		Dial: func(context.Context, string) (agentv1.AgentServiceClient, func() error, error) {
			dialed = true
			return nil, nil, errors.New("不该被调用")
		},
	}

	resp, err := caller.CallTool(context.Background(), "lab-b", "daedalus.fs", "read_file", `{}`)
	if dialed {
		t.Error("节点未注册时不得建连")
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("ClassifyToolCall = %v, want %v(err=%v)", got, fleet.ToolCallNodeUnreachable, err)
	}
}

// TestToolCaller_DialFailureIsUnreachable 钉住建连失败的口径:底层错误原文保留
// (运维要看得到是证书还是端口),分类上是「节点不可达」。
func TestToolCaller_DialFailureIsUnreachable(t *testing.T) {
	reg := newToolTestRegistry(t, "lab-a", "10.0.0.5:9443")
	caller := &fleet.ToolCaller{
		Registry: reg,
		Dial: func(context.Context, string) (agentv1.AgentServiceClient, func() error, error) {
			return nil, nil, errors.New("fleet: CAPath 为空:拒绝无 CA 连接(fail-closed)")
		},
	}

	resp, err := caller.CallTool(context.Background(), "lab-a", "daedalus.fs", "read_file", `{}`)
	if err == nil {
		t.Fatal("建连失败必须返回错误")
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("ClassifyToolCall = %v, want %v", got, fleet.ToolCallNodeUnreachable)
	}
}

// TestToolCaller_CallTimeoutBounded 钉住单次调用的时限:节点卡住时不能把整轮巡检
// 拖到调用方自己的超时,CallTimeout 必须真的套到 RPC 上。
func TestToolCaller_CallTimeoutBounded(t *testing.T) {
	stub := &blockingToolStub{release: make(chan struct{})}
	defer close(stub.release)
	reg := newToolTestRegistry(t, "lab-a", "passthrough:///bufnet")
	caller := &fleet.ToolCaller{Registry: reg, Dial: bufconnDial(t, stub), CallTimeout: 50 * time.Millisecond}

	start := time.Now()
	_, err := caller.CallTool(context.Background(), "lab-a", "daedalus.shell", "shell_exec", `{}`)
	if err == nil {
		t.Fatal("节点卡住必须超时返回错误")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("超时没生效:耗时 %v", elapsed)
	}
	if got := fleet.ClassifyToolCall(nil, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("ClassifyToolCall = %v, want %v(err=%v)", got, fleet.ToolCallNodeUnreachable, err)
	}
}

// ---------------- 测试夹具 ----------------

// toolStub 是只实现工具调用面的假 agent;其余 RPC 走 Unimplemented。
type toolStub struct {
	agentv1.UnimplementedAgentServiceServer
	tools    []*agentv1.ToolDescriptor
	callReq  *agentv1.CallToolRequest
	callResp *agentv1.CallToolResponse
	callErr  error
}

func (s *toolStub) ListTools(context.Context, *agentv1.ListToolsRequest) (*agentv1.ListToolsResponse, error) {
	return &agentv1.ListToolsResponse{Tools: s.tools, AuditSeq: 7}, nil
}

func (s *toolStub) CallTool(_ context.Context, req *agentv1.CallToolRequest) (*agentv1.CallToolResponse, error) {
	s.callReq = req
	if s.callErr != nil {
		return nil, s.callErr
	}
	if s.callResp != nil {
		return s.callResp, nil
	}
	return &agentv1.CallToolResponse{ContentJson: `{"ok":true}`, AuditSeq: 8}, nil
}

// blockingToolStub 的 CallTool 一直挂到 release 关闭(或请求 ctx 结束),
// 用来证明调用方设的时限真的在 RPC 上生效。
type blockingToolStub struct {
	agentv1.UnimplementedAgentServiceServer
	release chan struct{}
}

func (*blockingToolStub) ListTools(context.Context, *agentv1.ListToolsRequest) (*agentv1.ListToolsResponse, error) {
	return &agentv1.ListToolsResponse{}, nil
}

func (s *blockingToolStub) CallTool(ctx context.Context, _ *agentv1.CallToolRequest) (*agentv1.CallToolResponse, error) {
	select {
	case <-s.release:
		return &agentv1.CallToolResponse{}, nil
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// bufconnDial 起一个 bufconn 假 server 并返回 ToolCaller 的建连注入缝。
func bufconnDial(t *testing.T, agent agentv1.AgentServiceServer) func(context.Context, string) (agentv1.AgentServiceClient, func() error, error) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, agent)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	return func(ctx context.Context, _ string) (agentv1.AgentServiceClient, func() error, error) {
		conn, err := grpc.DialContext(ctx, "passthrough:///bufnet",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}))
		if err != nil {
			return nil, nil, err
		}
		return agentv1.NewAgentServiceClient(conn), conn.Close, nil
	}
}

// newToolTestRegistry 造一个只含单个节点的注册表(nodeID 的地址由调用方给)。
func newToolTestRegistry(t *testing.T, nodeID, addr string) *fleet.Registry {
	t.Helper()
	t.Setenv(fleet.FleetDirEnv, t.TempDir())
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Put(fleet.Node{NodeID: nodeID, AgentID: nodeID, Addr: addr}); err != nil {
		t.Fatalf("Put %s: %v", nodeID, err)
	}
	return reg
}
