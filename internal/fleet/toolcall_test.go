// 本文件钉住远程只读工具调用的归类契约:成功 / 节点拒绝 / 节点不可达 /
// 插件自报失败四态各自对应哪些 gRPC code 与响应字段。
// 未注册的节点不得发起建连:它本来就是「不可达」,不该在这次巡检里制造噪声连接。
package fleet_test

import (
	"context"
	"errors"
	"net"
	"testing"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
)

// TestClassifyToolCall 表驱动钉住四态映射:响应字段与 gRPC code 各自归到哪一态。
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
		{"节点超时", nil, status.Error(codes.DeadlineExceeded, "超时"), fleet.ToolCallNodeUnreachable},
		{"无法归类的失败", nil, status.Error(codes.Internal, "协议错"), fleet.ToolCallToolFailed},
	}
	for _, c := range cases {
		if got := fleet.ClassifyToolCall(c.resp, c.err); got != c.want {
			t.Errorf("%s: ClassifyToolCall = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestToolCaller_UnknownNodeIsUnreachable 钉住「取不到节点 → 不可达」:
// 注册表里没有这台机器时,一个建连动作都不许发出去。
func TestToolCaller_UnknownNodeIsUnreachable(t *testing.T) {
	t.Setenv(fleet.FleetDirEnv, t.TempDir())
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	dialed := false
	tc := &fleet.ToolCaller{
		Registry: reg,
		Dial: func(context.Context, string) (agentv1.AgentServiceClient, func() error, error) {
			dialed = true
			return nil, nil, errors.New("不该发生的建连")
		},
	}

	tools, err := tc.ListTools(context.Background(), "not-registered")
	if err == nil {
		t.Fatal("未注册节点必须报错")
	}
	if dialed {
		t.Error("未注册节点不得发起建连")
	}
	if tools != nil {
		t.Errorf("失败时工具集必须为 nil, got %v", tools)
	}
	if got := fleet.ClassifyToolCall(nil, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("ListTools 失败归类 = %v, want %v", got, fleet.ToolCallNodeUnreachable)
	}

	resp, err := tc.CallTool(context.Background(), "not-registered", "daedalus.shell", "shell_exec", "{}")
	if err == nil {
		t.Fatal("未注册节点必须报错")
	}
	if dialed {
		t.Error("未注册节点不得发起建连")
	}
	if resp != nil {
		t.Errorf("失败时响应必须为 nil, got %v", resp)
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("CallTool 失败归类 = %v, want %v", got, fleet.ToolCallNodeUnreachable)
	}
}

// TestToolCaller_CallsOverBufconn 走一遍完整调用路径:注册表里有这台机器 →
// ListTools 拿到工具集 → CallTool 的 arguments_json 原样到达假 server →
// 插件自报业务错误归到 tool_failed;建连失败归到 node_unreachable。
func TestToolCaller_CallsOverBufconn(t *testing.T) {
	t.Setenv(fleet.FleetDirEnv, t.TempDir())
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Put(fleet.Node{NodeID: "n1", Addr: "bufnet"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	agent := &bufconnAgent{tools: []*agentv1.ToolDescriptor{
		{PluginId: "daedalus.shell", ToolName: "shell_exec", Description: "执行只读命令", ReadOnly: true},
	}}
	dial := startBufconnAgent(t, agent)
	tc := &fleet.ToolCaller{Registry: reg, Dial: dial}

	tools, err := tc.ListTools(context.Background(), "n1")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].GetPluginId() != "daedalus.shell" || tools[0].GetToolName() != "shell_exec" {
		t.Fatalf("工具集 = %v, want daedalus.shell/shell_exec", tools)
	}

	args := `{"command":"uptime","args":[]}`
	resp, err := tc.CallTool(context.Background(), "n1", "daedalus.shell", "shell_exec", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(agent.arguments) != 1 || agent.arguments[0] != args {
		t.Errorf("arguments_json 未原样到达节点: %q, want %q", agent.arguments, args)
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallOK {
		t.Errorf("成功调用归类 = %v, want %v", got, fleet.ToolCallOK)
	}

	agent.isError = true
	agent.errorMessage = "越界"
	resp, err = tc.CallTool(context.Background(), "n1", "daedalus.shell", "shell_exec", args)
	if err != nil {
		t.Fatalf("插件业务错误不该变成 RPC 错误: %v", err)
	}
	if got := fleet.ClassifyToolCall(resp, err); got != fleet.ToolCallToolFailed {
		t.Errorf("插件自报错误归类 = %v, want %v", got, fleet.ToolCallToolFailed)
	}

	// 建连失败:够不着这台机器,归到 node_unreachable(不是 denied,也不是工具失败)。
	failing := &fleet.ToolCaller{
		Registry: reg,
		Dial: func(context.Context, string) (agentv1.AgentServiceClient, func() error, error) {
			return nil, nil, errors.New("连接被拒")
		},
	}
	if _, err := failing.ListTools(context.Background(), "n1"); err == nil {
		t.Fatal("建连失败必须报错")
	} else if got := fleet.ClassifyToolCall(nil, err); got != fleet.ToolCallNodeUnreachable {
		t.Errorf("建连失败归类 = %v, want %v", got, fleet.ToolCallNodeUnreachable)
	}
}

// bufconnAgent 是只实现 ListTools / CallTool 的假 agent server。
type bufconnAgent struct {
	agentv1.UnimplementedAgentServiceServer
	tools        []*agentv1.ToolDescriptor
	arguments    []string
	isError      bool
	errorMessage string
}

func (a *bufconnAgent) ListTools(context.Context, *agentv1.ListToolsRequest) (*agentv1.ListToolsResponse, error) {
	return &agentv1.ListToolsResponse{Tools: a.tools, AuditSeq: 1}, nil
}

func (a *bufconnAgent) CallTool(_ context.Context, req *agentv1.CallToolRequest) (*agentv1.CallToolResponse, error) {
	a.arguments = append(a.arguments, req.GetArgumentsJson())
	return &agentv1.CallToolResponse{
		IsError:      a.isError,
		ErrorMessage: a.errorMessage,
		ContentJson:  `{"stdout":"up 3 days"}`,
		AuditSeq:     2,
	}, nil
}

// startBufconnAgent 起一个内存管道上的假 agent server,返回注入用的 Dial 缝。
// 管道不出进程,故这里用明文凭证即可(生产路径的 mTLS 由集成测试覆盖)。
func startBufconnAgent(t *testing.T, agent agentv1.AgentServiceServer) func(ctx context.Context, addr string) (agentv1.AgentServiceClient, func() error, error) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, agent)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return func(ctx context.Context, _ string) (agentv1.AgentServiceClient, func() error, error) {
		conn, err := grpc.DialContext(ctx, "passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, err
		}
		return agentv1.NewAgentServiceClient(conn), conn.Close, nil
	}
}
