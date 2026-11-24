// internal/fleet/toolcall.go:控制面到节点的只读工具调用适配。
//
// 硬线:放行判定只在节点侧做(策略白名单 ∩ 插件自报 ReadOnlyHint,两道闸是
// 「与」),控制面不自行判只读,也不替插件校验参数 —— 入参原样透传,插件自己的
// shellpolicy / pathguard 仍然生效。一次调用不重试:只读失败由调用方决定。
package fleet

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToolCallOutcome 是一次远程工具调用的归类结果。
type ToolCallOutcome int

const (
	// ToolCallOK 表示 RPC 成功且插件没有报业务错误。
	ToolCallOK ToolCallOutcome = iota
	// ToolCallDenied 表示节点侧拒绝放行:未配置调用面、不在白名单、或插件自报可写。
	ToolCallDenied
	// ToolCallNodeUnreachable 表示够不着节点或插件:未注册、建连失败、插件起不来。
	ToolCallNodeUnreachable
	// ToolCallToolFailed 表示插件自己报的业务错误,以及归不出确定归因的失败。
	ToolCallToolFailed
)

// String 返回稳定词表(巡检报告与日志按它消费)。
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

// ClassifyToolCall 把一次工具调用的回报映射进四态。纯函数:不碰网络、不写日志。
// 口径与节点侧的错误码一一对应:PermissionDenied / FailedPrecondition 是放行
// 判定拒的,Unavailable / DeadlineExceeded 是够不着节点或插件,其余一律按工具
// 失败处理(归不出因就不当成成功)。
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
	if resp.GetIsError() {
		return ToolCallToolFailed
	}
	return ToolCallOK
}

// ToolCaller 向注册表里的节点发起只读工具调用。Registry 负责 node_id → Addr
// 的解析,Dial 是建连注入缝(生产由 NewToolCaller 装配 mTLS 凭证)。
type ToolCaller struct {
	Registry *Registry
	Dial     func(ctx context.Context, addr string) (agentv1.AgentServiceClient, func() error, error)
	// CallTimeout 是单次 RPC 的时限;<=0 时回落 defaultCallTimeout,与工作 RPC
	// 同一口径。活性心跳的时限不套到这里 —— 列工具与调工具都是工作 RPC。
	CallTimeout time.Duration
}

// NewToolCaller 装配生产用的 ToolCaller:mTLS 凭证来自环境变量(与调度器同一
// 来源),Addr 按目标节点覆盖。
func NewToolCaller(r *Registry) *ToolCaller {
	base := DialConfigFromEnv()
	return &ToolCaller{
		Registry:    r,
		CallTimeout: defaultCallTimeout,
		Dial: func(ctx context.Context, addr string) (agentv1.AgentServiceClient, func() error, error) {
			cfg := base
			cfg.Addr = addr
			return Dial(ctx, cfg)
		},
	}
}

// ListTools 列出节点上允许被远程调用的工具(白名单 ∩ 只读由节点判定)。
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
	tools := make([]agentv1.ToolDescriptor, 0, len(resp.GetTools()))
	for _, tool := range resp.GetTools() {
		if tool == nil {
			continue
		}
		// 逐字段重建而非解引用拷贝:proto 消息内含互斥量,按值拷贝会破锁。
		tools = append(tools, agentv1.ToolDescriptor{
			PluginId:        tool.GetPluginId(),
			ToolName:        tool.GetToolName(),
			Description:     tool.GetDescription(),
			InputSchemaJson: tool.GetInputSchemaJson(),
			ReadOnly:        tool.GetReadOnly(),
		})
	}
	return tools, nil
}

// CallTool 远程调用节点上的一个工具。argumentsJSON 原样透传给插件,是否放行
// 由节点侧判定 —— 本方法既不解析也不改写参数。
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

// connect 解析节点并建连。取不到节点、地址缺失、建连失败一律归成 Unavailable
// (对调用方而言都是「够不着这台机器」);注册表读不出来是控制面自己的问题,
// 按普通错误返回,不冒充节点不可达。
func (c *ToolCaller) connect(ctx context.Context, nodeID string) (agentv1.AgentServiceClient, func() error, error) {
	if c.Registry == nil {
		return nil, nil, fmt.Errorf("fleet: ToolCaller.Registry 为空")
	}
	node, ok, err := c.Registry.Get(nodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("fleet: 读取注册表失败: %w", err)
	}
	if !ok {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 节点 %s 未注册", nodeID)
	}
	if node.Addr == "" {
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 节点 %s 没有地址", nodeID)
	}
	if c.Dial == nil {
		return nil, nil, fmt.Errorf("fleet: ToolCaller.Dial 为空")
	}
	client, closer, err := c.Dial(ctx, node.Addr)
	if err != nil {
		// 凭证缺失与连接被拒都表现为「这台机器够不着」;原始原因保留在文案里,
		// 不吞掉建连失败的细节。
		return nil, nil, status.Errorf(codes.Unavailable, "fleet: 连接节点 %s 失败: %v", nodeID, err)
	}
	if closer == nil {
		closer = func() error { return nil }
	}
	return client, closer, nil
}

// callContext 给一次 RPC 套上时限。
func (c *ToolCaller) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.CallTimeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	return context.WithTimeout(ctx, timeout)
}
