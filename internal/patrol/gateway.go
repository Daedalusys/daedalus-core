// gateway.go 是本轮巡检对 AI 客户端的唯一接口面:节点只读工具的聚合与
// report_finding 的信任边界。
//
// 两条不变式:远程调用面只含只读工具(节点白名单 ∩ 插件自报只读,本文件只做二次剔除、
// 不判定放行);发现里的证据必须是本轮工具返回原文的子串 —— 模型没有改写证据的权力。
package patrol

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	sdkversion "github.com/Daedalusys/daedalus-sdk/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reportFindingTool 是模型唯一的写出口名。它不带节点前缀,免得和节点工具撞名。
const reportFindingTool = "report_finding"

// Fleet 是网关取节点工具清单与调工具的面;控制面实现见 internal/fleet.ToolCaller。
type Fleet interface {
	ListTools(ctx context.Context, nodeID string) ([]agentv1.ToolDescriptor, error)
	CallTool(ctx context.Context, nodeID, pluginID, toolName, argumentsJSON string) (*agentv1.CallToolResponse, error)
}

// FindingSink 收本轮的结构化发现。落盘时机与形状由调用方决定。
type FindingSink interface {
	Append(finding Finding) error
}

// recordedCall 是一次工具返回的留底,供 report_finding 校验证据是否原文。
type recordedCall struct {
	tool   string
	output string
}

// Gateway 把一台节点的只读工具与 report_finding 聚合成一轮巡检的 MCP 服务。
//
// 生命周期:NewGateway 只装配依赖 → Prepare 拉工具清单并建服务 → Handler 交给
// HTTP server → Close 收摊。Prepare 失败后不重试:同一轮里节点不会自己活过来,
// 失败原因原样交给调用方变成一条发现。
type Gateway struct {
	nodeID string
	items  map[string]struct{}
	fleet  Fleet
	sink   FindingSink

	mu     sync.Mutex
	ready  bool
	closed bool
	err    error
	stream http.Handler

	callsMu sync.RWMutex
	calls   []recordedCall
}

// NewGateway 装配一轮网关。items 是本轮清单(只需其 id 集合,用来判定
// report_finding 的 item_id 是否在清单里),不碰网络。
func NewGateway(nodeID string, items []Item, fleet Fleet, sink FindingSink) *Gateway {
	allowed := make(map[string]struct{}, len(items))
	for _, it := range items {
		allowed[it.ID] = struct{}{}
	}
	return &Gateway{nodeID: nodeID, items: allowed, fleet: fleet, sink: sink}
}

// Prepare 拉取节点可远程调用的工具并建好本轮 MCP 服务。
//
// 失败原样返回:巡检必须把它变成一条发现(节点不可达、没开远程面都算发现),
// 而不是静默开一个没有工具的轮次 —— 那样模型只会回「看不到问题」。
func (g *Gateway) Prepare(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prepareLocked(ctx)
}

// prepareLocked 是 Prepare 的实现,调用方持有 g.mu。
func (g *Gateway) prepareLocked(ctx context.Context) error {
	if g.ready {
		return nil
	}
	if g.err != nil {
		return g.err
	}
	tools, err := g.fleet.ListTools(ctx, g.nodeID)
	if err != nil {
		g.err = fmt.Errorf("取节点 %s 的可远程调用工具失败: %w", g.nodeID, err)
		return g.err
	}
	srv, err := g.buildServer(tools)
	if err != nil {
		g.err = err
		return err
	}
	g.stream = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	g.ready = true
	return nil
}

// buildServer 把节点工具与 report_finding 注册到同一个 MCP server。
//
// 工具清单的来源是节点侧(白名单 ∩ 插件自报只读);这里再剔一遍非只读描述符,
// 属于纵深防御而不是策略判定 —— 写工具永不可远程调用,两道闸是「与」。
func (g *Gateway) buildServer(tools []agentv1.ToolDescriptor) (*mcp.Server, error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "daedalus-patrol", Version: sdkversion.Version}, nil)

	seen := make(map[string]string, len(tools))
	for i := range tools {
		// 取址遍历:描述符内含 proto 内部互斥量,值拷贝会被 go vet 拦下。
		d := &tools[i]
		if !d.GetReadOnly() {
			continue
		}
		name := exposedToolName(g.nodeID, d.GetToolName())
		if len(name) > 128 {
			return nil, fmt.Errorf("节点 %s 的工具 %q 加前缀后名字超过 128 字符(%q)", g.nodeID, d.GetToolName(), name)
		}
		if prev, ok := seen[name]; ok {
			// 静默覆盖会让模型以为调的是 A 实际去了 B。撞名是节点侧配置问题,
			// 让本轮巡检失败比给出一个错的工具面安全。
			return nil, fmt.Errorf("节点 %s 的工具 %q 与 %q 暴露成同名 %q", g.nodeID, prev, d.GetToolName(), name)
		}
		seen[name] = d.GetToolName()

		desc := d.GetDescription()
		if strings.TrimSpace(desc) == "" {
			desc = fmt.Sprintf("节点 %s 的只读工具 %s", g.nodeID, d.GetToolName())
		}
		srv.AddTool(&mcp.Tool{
			Name:        name,
			Description: desc,
			InputSchema: inputSchema(d.GetInputSchemaJson()),
			// 节点已经判过只读,这里把结论告诉客户端,让它自己也能守住边界。
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, g.nodeToolHandler(d.GetPluginId(), d.GetToolName()))
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        reportFindingTool,
		Description: g.reportDescription(),
	}, g.handleReportFinding)

	return srv, nil
}

// inputSchema 优先原样转交节点给的 schema,让模型看到插件真实的参数形状;
// 节点没给或给的不是对象 schema 时退成宽松对象 —— 参数怎么判是插件自己的事。
func inputSchema(raw string) any {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil && m["type"] == "object" {
		return m
	}
	return map[string]any{"type": "object", "additionalProperties": true}
}

// exposedToolName 给节点工具加节点前缀。节点 id 与工具名都允许 . - 之类字符,
// 统一归一成下划线,保证名字形状稳定、不依赖客户端对特殊字符的容忍度。
func exposedToolName(nodeID, tool string) string {
	return sanitizeToolNamePart(nodeID) + "_" + sanitizeToolNamePart(tool)
}

// sanitizeToolNamePart 把非 [A-Za-z0-9_] 的字符换成下划线。
func sanitizeToolNamePart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// nodeToolHandler 把一次 MCP 工具调用转成节点 RPC。参数原样透传,失败一律变成
// IsError 的工具结果:模型要看见原因才好自己纠正,MCP 层不该把节点故障抛成协议错。
func (g *Gateway) nodeToolHandler(pluginID, toolName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := g.checkOpen(); err != nil {
			return errorResult(err), nil
		}

		var args string
		if req != nil && req.Params != nil {
			args = string(req.Params.Arguments)
		}
		resp, err := g.fleet.CallTool(ctx, g.nodeID, pluginID, toolName, args)

		var text string
		isErr := false
		switch {
		case err != nil:
			isErr = true
			text = fmt.Sprintf("调用节点 %s 的工具 %s 失败: %v", g.nodeID, toolName, err)
		case resp == nil:
			isErr = true
			text = fmt.Sprintf("节点 %s 的工具 %s 没有返回内容", g.nodeID, toolName)
		default:
			isErr = resp.GetIsError()
			text = nodeText(resp.GetContentJson())
			if msg := resp.GetErrorMessage(); msg != "" {
				text = joinText(text, msg)
			}
			if strings.TrimSpace(text) == "" {
				text = "工具没有返回内容"
			}
		}

		// 记的就是模型看到的这份文本:证据校验比的因此是同一份字符串。
		g.RecordCall(toolName, text)
		return &mcp.CallToolResult{
			IsError: isErr,
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil
	}
}

// nodeText 把节点的 content_json 归一成模型看到的那段文本。
//
// 节点侧编码 content_json 的实现不在本仓,形状没有可对照的单一事实源:
// 按 MCP content 数组(MCP 的原始形态)为主,单个 content 对象与裸 JSON 字符串为辅,
// 都对不上就原样当作文本。只取文本项 —— 本轮不消费非文本内容。
func nodeText(raw string) string {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err == nil && len(items) > 0 {
		parts := make([]string, 0, len(items))
		for _, it := range items {
			if it.Type == "text" {
				parts = append(parts, it.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}

	var single struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &single); err == nil && single.Type == "text" {
		return single.Text
	}

	var quoted string
	if err := json.Unmarshal([]byte(raw), &quoted); err == nil {
		return quoted
	}
	return raw
}

// joinText 把工具输出与错误说明拼成一段;空的那一段不留空行。
// 调用点只在错误说明非空时进来,所以只需处理输出为空的情形。
func joinText(out, msg string) string {
	if strings.TrimSpace(out) == "" {
		return msg
	}
	return out + "\n" + msg
}

// reportFindingInput 是 report_finding 的入参形状。
type reportFindingInput struct {
	ItemID   string          `json:"item_id" jsonschema:"本轮清单里的清单项 id。"`
	Status   string          `json:"status" jsonschema:"problem 表示这一项有问题;ok 表示这一轮看过且没问题。"`
	Severity string          `json:"severity" jsonschema:"严重级,取 info、warning、critical 之一。"`
	Summary  string          `json:"summary" jsonschema:"一句话说清现象与影响,写给人看。"`
	Evidence []evidenceInput `json:"evidence,omitempty" jsonschema:"支撑这条结论的工具调用原文。"`
}

// evidenceInput 是一条证据的入参形状。
type evidenceInput struct {
	Tool       string `json:"tool" jsonschema:"产出这段输出的工具名。"`
	Input      string `json:"input,omitempty" jsonschema:"这次工具调用的入参 JSON。"`
	Output     string `json:"output" jsonschema:"必须原样复制工具返回原文的一段,不能改写。"`
	ReturnCode int    `json:"returncode,omitempty" jsonschema:"工具返回码,没有就留 0。"`
}

// handleReportFinding 是模型唯一的写出口。三条硬校验全在这里,
// 通过后落盘并回一句「记上了」;任何一条不过都回工具错误,让模型自己改。
func (g *Gateway) handleReportFinding(_ context.Context, _ *mcp.CallToolRequest, in reportFindingInput) (*mcp.CallToolResult, any, error) {
	if err := g.checkOpen(); err != nil {
		return nil, nil, err
	}
	finding, err := g.validateFinding(in)
	if err != nil {
		return nil, nil, err
	}
	if err := g.sink.Append(finding); err != nil {
		return nil, nil, fmt.Errorf("落盘发现失败: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("已记录 %s 的发现:%s", finding.ItemID, finding.Status),
		}},
	}, nil, nil
}

// validateFinding 执行 report_finding 的硬校验并构造发现。
func (g *Gateway) validateFinding(in reportFindingInput) (Finding, error) {
	if _, ok := g.items[in.ItemID]; !ok {
		// 清单外的 id 会让跨轮去重与邮件分组失效,直接把本轮清单摊给模型看。
		return Finding{}, fmt.Errorf("item_id %q 不在本轮清单里(清单:%s)", in.ItemID, strings.Join(g.itemIDs(), ", "))
	}
	if !validSeverity(in.Severity) {
		return Finding{}, fmt.Errorf("severity %q 不在 {%s, %s, %s} 里",
			in.Severity, SeverityInfo, SeverityWarning, SeverityCritical)
	}
	// 状态词表同样封闭:拼错的状态在分类里会被当成「有问题」,白白造出假告警。
	if in.Status != StatusProblem && in.Status != StatusOK {
		return Finding{}, fmt.Errorf("status %q 不在 {%s, %s} 里", in.Status, StatusProblem, StatusOK)
	}
	if strings.TrimSpace(in.Summary) == "" {
		return Finding{}, fmt.Errorf("summary 不能为空:邮件要说明这条发现是什么")
	}

	evidence := make([]Evidence, 0, len(in.Evidence))
	for i, e := range in.Evidence {
		if !g.recordedOutput(e.Output) {
			return Finding{}, fmt.Errorf("evidence[%d] 的 output 不是本轮工具返回原文的子串:证据只能原样引用工具输出,不能是模型自己写的文本(本轮记录过的工具:%s)",
				i, strings.Join(orDash(g.recordedTools()), ", "))
		}
		evidence = append(evidence, Evidence{
			Tool:       e.Tool,
			Input:      e.Input,
			Output:     e.Output,
			ReturnCode: e.ReturnCode,
		})
	}

	return Finding{
		Node:     g.nodeID,
		ItemID:   in.ItemID,
		Status:   in.Status,
		Severity: in.Severity,
		Summary:  in.Summary,
		Evidence: evidence,
	}, nil
}

// RecordCall 记一次工具返回,供 report_finding 校验证据。记的是模型看到的那份文本。
func (g *Gateway) RecordCall(tool, output string) {
	g.callsMu.Lock()
	defer g.callsMu.Unlock()
	g.calls = append(g.calls, recordedCall{tool: tool, output: output})
}

// recordedOutput 判断一段文本是否出现在本轮某次工具返回里。
//
// 空串是所有文本的子串,不能算证据 —— 否则「不写证据」就能绕过校验。
func (g *Gateway) recordedOutput(output string) bool {
	if output == "" {
		return false
	}
	g.callsMu.RLock()
	defer g.callsMu.RUnlock()
	for _, c := range g.calls {
		if strings.Contains(c.output, output) {
			return true
		}
	}
	return false
}

// recordedTools 返回本轮记录过返回的工具名(去重排序),用于证据不匹配时的提示。
func (g *Gateway) recordedTools() []string {
	g.callsMu.RLock()
	defer g.callsMu.RUnlock()
	seen := make(map[string]struct{}, len(g.calls))
	for _, c := range g.calls {
		seen[c.tool] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// orDash 让空列表在提示里读起来不是空白。
func orDash(items []string) []string {
	if len(items) == 0 {
		return []string{"无"}
	}
	return items
}

// itemIDs 返回本轮清单的 id(排序后),用于错误提示。
func (g *Gateway) itemIDs() []string {
	ids := make([]string, 0, len(g.items))
	for id := range g.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// reportDescription 是 report_finding 的工具说明。清单 id 直接写进去 ——
// 这是模型判断「该报哪一项」的唯一提示。
func (g *Gateway) reportDescription() string {
	return fmt.Sprintf("记录一条巡检发现。item_id 必须是本轮清单里的 id(%s);"+
		"severity 取 info / warning / critical;evidence 里每条 output 必须原样复制本轮工具返回原文的一段,"+
		"不能改写或转述。", strings.Join(g.itemIDs(), ", "))
}

// Handler 返回本轮 MCP 服务。调用方负责监听地址与令牌(见 TokenMiddleware),
// 网关自己只关心协议。
//
// 首次请求会懒加载工具清单(与 Prepare 同一条路径):调用方没显式 Prepare 时也不会
// 静默开一个没有工具的轮次,取清单失败直接回 503 并带上原因。
func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.Prepare(r.Context()); err != nil {
			http.Error(w, fmt.Sprintf("巡检网关未就绪: %v", err), http.StatusServiceUnavailable)
			return
		}
		g.mu.Lock()
		stream := g.stream
		g.mu.Unlock()
		stream.ServeHTTP(w, r)
	})
}

// Close 收摊:此后到达的工具调用与发现一律拒,可重复调用。
//
// 一轮巡检的尾声是「客户端退出 → 收 findings → 发信」;迟到的调用若被受理,
// 会把已经算过变化的发现塞进下一轮。
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return nil
}

// checkOpen 拒掉收摊之后的一切调用。
func (g *Gateway) checkOpen() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("本轮巡检已收摊,不再受理工具调用")
	}
	return nil
}

// errorResult 把一句话包成工具错误结果。
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// TokenMiddleware 给网关套上本轮令牌校验:请求必须带 `Authorization: Bearer <token>`。
//
// 令牌由巡检二进制每轮新生成并只告诉本轮 AI 客户端;只走请求头不进 URL ——
// URL 会落进访问日志。令牌为空一律拒(fail-closed):漏配令牌不能退化成「谁都能连」。
func TokenMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 认证方案按 RFC 大小写不敏感;令牌本身按字节比,且用常数时间比较。
		scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if token == "" || !ok || !strings.EqualFold(scheme, "Bearer") ||
			subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "未授权:本轮令牌不匹配", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
