// internal/fleet/toolcall.go:控制面到节点的只读工具调用面(ListTools / CallTool)。
//
// 两端契约:控制面只报告、不动手——本文件不提供任何修复动作,也不重试;放行判定
// 全在节点侧(远程调用白名单 ∩ 插件自报 ReadOnlyHint),控制面只把参数原样透传、
// 把结局归类。传输契约见 fleet 控制面设计稿 §4.4。
package fleet

import (
	"context"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToolCallOutcome 是一次工具调用的结局:巡检据此决定这一条算「正常 / 被拒 /
// 节点掉线 / 工具自己错」,四种结局在邮件里的措辞与后续处理都不同。
type ToolCallOutcome int

const (
	// ToolCallOK:节点回了工具的正常输出。
	ToolCallOK ToolCallOutcome = iota
	// ToolCallDenied:节点的只读闸把它拒了(不在白名单 / 插件自报非只读 / 没开远程面)。
	ToolCallDenied
	// ToolCallNodeUnreachable:节点侧的问题——没注册、连不上、超时。
	ToolCallNodeUnreachable
	// ToolCallToolFailed:工具自己失败(插件报业务错误,或 RPC 回了表外错误码)。
	ToolCallToolFailed
)

// String 返回结局的稳定词表(巡检的轮次记录与邮件直接消费)。
func (o ToolCallOutcome) String() string {
	switch o {
	case ToolCallOK:
		return "ok"
	case ToolCallDenied:
		return "denied"
	case ToolCallNodeUnreachable:
		return "node_unreachable"
	case ToolCallToolFailed:
		return "tool_failed"
	}
	return "unknown"
}

// ClassifyToolCall 把一次 CallTool 的回报映射进四种结局。纯函数:不碰网络。
//
// 错误码分组与节点侧的真实语义对齐:节点拒(白名单 / 非只读 / 未配置远程面)回
// PermissionDenied 或 FailedPrecondition;节点掉线回 Unavailable,调用方超时回
// DeadlineExceeded;表外错误码(插件进程崩溃等)算工具失败。
func ClassifyToolCall(resp *agentv1.CallToolResponse, err error) ToolCallOutcome {
	if err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied, codes.FailedPrecondition:
			return ToolCallDenied
		case codes.Unavailable, codes.DeadlineExceeded:
			return ToolCallNodeUnreachable
		default:
			return ToolCallToolFailed
		}
	}
	// 既无响应也无错误是协议异常:绝不能当成功——巡检会把空内容当证据。
	if resp == nil {
		return ToolCallToolFailed
	}
	if resp.GetIsError() {
		return ToolCallToolFailed
	}
	return ToolCallOK
}

// ToolCaller 是巡检链路里控制面唯一碰得到的工具调用面。
//
// 每次调用都重新走 Registry.Get → Dial → RPC → 关连接:节点可能刚重新注册换了
// 地址,长连接会把这层错位藏起来;只读调用频率低,建连成本不重要。
type ToolCaller struct {
	// Registry 是节点名单,目标地址从节点行取。
	Registry *Registry
	// Dial 是建连注入缝:生产走 DialConfigFromEnv() 装配出的 mTLS Dial,
	// 测试走 bufconn 假 server。
	Dial func(ctx context.Context, addr string) (agentv1.AgentServiceClient, func() error, error)
	// CallTimeout 是单次 RPC 的时限;零值回落 defaultCallTimeout(与工作 RPC 同口径)。
	CallTimeout time.Duration
}

// NewToolCaller 装配生产形态:凭证路径来自环境变量,地址按节点行覆盖。
func NewToolCaller(r *Registry) *ToolCaller {
	return &ToolCaller{
		Registry: r,
		Dial: func(ctx context.Context, addr string) (agentv1.AgentServiceClient, func() error, error) {
			cfg := DialConfigFromEnv()
			cfg.Addr = addr
			return Dial(ctx, cfg)
		},
	}
}

// ListTools 取节点允许被远程调用的工具清单。过滤(白名单 ∩ 插件自报只读)由节点侧
// 完成,控制面原样返回——这里再判一次就是第二个策略事实源。
func (c *ToolCaller) ListTools(ctx context.Context, nodeID string) ([]agentv1.ToolDescriptor, error) {
	client, closer, err := c.connect(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = closer() }()

	callCtx, cancel := c.callContext(ctx)
	defer cancel()

	resp, err := client.ListTools(callCtx, &agentv1.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	// proto 的 repeated message 是指针切片,对外的形状用值切片:下游(网关)按值
	// 持有描述符,不必到处判空;空元素直接丢弃(不是合法工具描述符)。
	//
	// 逐字段构造而不是解引用复制:proto 消息内含互斥量,值拷贝会连锁一起复制
	// (go vet copylocks),副本的 proto 内部状态也与原件不一致。
	raw := resp.GetTools()
	tools := make([]agentv1.ToolDescriptor, 0, len(raw))
	for _, d := range raw {
		if d == nil {
			continue
		}
		tools = append(tools, agentv1.ToolDescriptor{
			PluginId:        d.GetPluginId(),
			ToolName:        d.GetToolName(),
			Description:     d.GetDescription(),
			InputSchemaJson: d.GetInputSchemaJson(),
			ReadOnly:        d.GetReadOnly(),
		})
	}
	return tools, nil
}

// CallTool 调一次只读工具;argumentsJSON 原样透传,节点侧按插件自己的策略判。
//
// (响应, nil) 表示 RPC 通了——插件业务错误在响应的 IsError 里,不在 error 里;
// (nil, err) 表示没拿到响应,err 的错误码保留节点语义,交给 ClassifyToolCall 归类。
func (c *ToolCaller) CallTool(ctx context.Context, nodeID, pluginID, toolName, argumentsJSON string) (*agentv1.CallToolResponse, error) {
	client, closer, err := c.connect(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = closer() }()

	callCtx, cancel := c.callContext(ctx)
	defer cancel()

	return client.CallTool(callCtx, &agentv1.CallToolRequest{
		PluginId:      pluginID,
		ToolName:      toolName,
		ArgumentsJson: argumentsJSON,
	})
}

// connect 是 ListTools / CallTool 共用的前段:取节点 → 建连。节点侧的问题(没注册 /
// 没有地址 / 连不上)带 Unavailable 语义,底层原因留在文案里给运维看(证书、端口、
// 地址哪个坏了);装配缺失这类误用报 Internal。
func (c *ToolCaller) connect(ctx context.Context, nodeID string) (agentv1.AgentServiceClient, func() error, error) {
	if c == nil || c.Registry == nil {
		return nil, nil, status.Error(codes.Internal, "fleet: ToolCaller 未装配 Registry")
	}
	node, ok, err := c.Registry.Get(nodeID)
	if err != nil {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 读取节点 %s 失败: %v", nodeID, err)
	}
	if !ok {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 节点 %s 未注册", nodeID)
	}
	if node.Addr == "" {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 节点 %s 没有地址,无法建连", nodeID)
	}
	if c.Dial == nil {
		return nil, nil, status.Error(codes.Internal, "fleet: ToolCaller 未装配 Dial")
	}

	client, closer, err := c.Dial(ctx, node.Addr)
	if err != nil {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 连接节点 %s(%s)失败: %v", nodeID, node.Addr, err)
	}
	if client == nil {
		// Dial 回了非 nil closer 时必须关掉:连接已经建起来却没人用,放着就是泄漏。
		if closer != nil {
			_ = closer()
		}
		return nil, nil, status.Errorf(codes.Internal, "fleet: 连接节点 %s 未产出客户端", nodeID)
	}
	if closer == nil {
		closer = func() error { return nil }
	}
	return client, closer, nil
}

// callContext 给一次 RPC 套上超时(零值回落 defaultCallTimeout)。
func (c *ToolCaller) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.CallTimeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	return context.WithTimeout(ctx, timeout)
}
