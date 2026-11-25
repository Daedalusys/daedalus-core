// gateway.go:一台机器一轮巡检的 MCP 网关。
//
// 网关把节点侧的只读工具面原样转给 AI 客户端,并只收一个自带工具 report_finding;
// 发现的三道硬校验(清单内 item_id、合法 severity、证据为工具原文子串)在这里,
// 因为模型是唯一能编造结论的一方。网关不持有监听地址与令牌判定 —— Handler() 返回
// 可被调用方套中间件的 http.Handler。
package patrol

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	sdkversion "github.com/Daedalusys/daedalus-sdk/version"
)

const (
	// gatewayName 是网关在 MCP 握手时报出的实现名,便于客户端侧辨认。
	gatewayName = "daedalus-patrol-gateway"
	// reportFindingTool 是网关自带的唯一工具:模型只能通过它交发现。
	reportFindingTool = "report_finding"
	// maxToolNameLen 是 MCP 工具名上限;节点 id 与工具名都是外部输入,不能假设够短。
	maxToolNameLen = 128
	// maxToolNameSuffix 是去重序号上限(2..max-1):节点导出上千个同名工具不现实。
	maxToolNameSuffix = 1000
)

// objectSchema 是节点没给 schema 或给了坏 schema 时的兜底:收下任何参数,
// 参数照旧原样透传,由插件自己校验。
const objectSchema = `{"type":"object"}`

// falseHint 给 ToolAnnotations 的 *bool 字段取址(SDK 不导出取址助手)。
var falseHint = false

// ErrNoRemoteTools 表示节点可达但没有暴露任何可远程调用的只读工具:
// 策略白名单为空,或插件都没自报 ReadOnlyHint。这种节点「本轮查不了任何东西」,
// 必须当失败处理 —— 让模型对着空工具面逐项报 ok,等于把「没查」写成「健康」。
var ErrNoRemoteTools = errors.New("节点没有暴露任何可远程调用的只读工具")

// Fleet 是网关用到的节点调用面;fleet.ToolCaller 直接满足它。
// 放行判定(远程白名单 ∩ 插件自报只读)在节点侧已经判过,网关不再判第二遍。
type Fleet interface {
	ListTools(ctx context.Context, nodeID string) ([]agentv1.ToolDescriptor, error)
	CallTool(ctx context.Context, nodeID, pluginID, toolName, argumentsJSON string) (*agentv1.CallToolResponse, error)
}

// FindingSink 收本轮发现。是否落盘、落成什么形状由调用方决定。
//
// 并发契约:网关保证 Append 是串行调用(一次只有一条在写),实现方不需要自己加锁;
// 但一个 sink 不能被多个网关共用 —— 串行化只在单个网关内成立。
type FindingSink interface {
	Append(finding Finding) error
}

// Gateway 是一轮巡检对一台机器开出的 MCP 服务。
//
// 生命周期 NewGateway → (SetToken) → Handler → Close;证据池只在内存里。
type Gateway struct {
	nodeID string
	items  map[string]struct{}
	fleet  Fleet
	sink   FindingSink

	// token 由 SetToken 在 Handler 之前设定,空串表示不校验。
	token string

	mu       sync.Mutex
	records  []callRecord
	reported map[string]struct{}
	closed   bool

	// sinkMu 只罩住 sink.Append:证据池与去重表用 mu,两把锁互不嵌套。
	sinkMu sync.Mutex

	once     sync.Once
	server   *mcp.Server
	declared []agentv1.ToolDescriptor
	toolErr  error
	// exposed 是本轮真正暴露给模型的节点工具数。它是「有没有观测面」的唯一判据:
	// 声明了工具但全被过滤掉(非只读、重名、名字空)同样等于零工具。
	exposed int
}

// callRecord 是一份网关交给客户端的工具返回原文;证据只能是其中某一份的子串。
type callRecord struct {
	tool   string
	output string
}

// NewGateway 用本轮清单装配网关:清单只判 item_id 是否合法,不构成工具白名单。
func NewGateway(nodeID string, items []Item, fleet Fleet, sink FindingSink) *Gateway {
	ids := make(map[string]struct{}, len(items))
	for _, it := range items {
		ids[it.ID] = struct{}{}
	}
	return &Gateway{
		nodeID:   nodeID,
		items:    ids,
		fleet:    fleet,
		sink:     sink,
		reported: make(map[string]struct{}, len(items)),
	}
}

// SetToken 设定本轮令牌,必须早于 Handler 调用。空串表示不校验,
// 只应出现在测试或单机演练里 —— 生产监听面由巡检二进制决定。
func (g *Gateway) SetToken(token string) { g.token = token }

// CallCount 返回本轮已记录的节点工具调用次数。一轮一次都没调,说明没有任何观测:
// 那些「ok」结论不是查出来的,是编出来的。
func (g *Gateway) CallCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.records)
}

// RecordCall 记一次工具返回供证据校验:记的必须是客户端看到的那份原文。
func (g *Gateway) RecordCall(tool, output string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.records = append(g.records, callRecord{tool: tool, output: output})
}

// Prepare 取一次节点工具清单(幂等),把失败原因交回调用方 —— 节点不可达本身
// 就该记成一条发现,静默空手开局会让「这台机器这轮没查」消失。
func (g *Gateway) Prepare() error {
	g.ensureServer()
	if g.toolErr != nil {
		return g.toolErr
	}
	if g.exposed == 0 {
		return fmt.Errorf("%w:节点 %s(策略白名单为空,或插件都没自报只读)",
			ErrNoRemoteTools, g.nodeID)
	}
	return nil
}

// Handler 返回本轮 MCP 端点。设了令牌就套上令牌中间件,没设就是裸端点。
func (g *Gateway) Handler() http.Handler {
	srv := g.ensureServer()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	if g.token == "" {
		return h
	}
	return TokenAuth(h, g.token)
}

// Close 封住本轮(之后的调用一律回错),监听面与会话归调用方的 http.Server。
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.records = nil
	return nil
}

// TokenAuth 要求请求带本轮令牌(X-Patrol-Token 或 Authorization: Bearer)。
// 独立成中间件,巡检二进制挂监听时自己套一层;测试也可以绕开它直连 Handler。
func TokenAuth(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasToken(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing or invalid patrol token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func hasToken(r *http.Request, token string) bool {
	got := r.Header.Get("X-Patrol-Token")
	if got == "" {
		if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = v
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// reportInput 是 report_finding 的入参。节点不在入参里:模型报的是清单项与依据,
// 哪台机器由网关盖章,模型没有指认权。
type reportInput struct {
	ItemID   string          `json:"item_id" jsonschema:"id of the checklist item this finding is about"`
	Status   string          `json:"status" jsonschema:"problem when something is wrong, ok when the item was checked and is fine"`
	Severity string          `json:"severity" jsonschema:"one of info, warning, critical"`
	Summary  string          `json:"summary" jsonschema:"one-line summary for the operator"`
	Evidence []evidenceInput `json:"evidence,omitempty" jsonschema:"tool outputs backing this finding"`
}

type evidenceInput struct {
	Tool       string `json:"tool" jsonschema:"tool name as exposed to you"`
	Input      string `json:"input,omitempty" jsonschema:"arguments you passed, as JSON text"`
	Output     string `json:"output" jsonschema:"verbatim substring of a tool output from this round"`
	ReturnCode int    `json:"returncode,omitempty" jsonschema:"tool return code if it reports one"`
}

// ensureServer 装配一次 MCP 服务器:工具清单、工具表、证据池共用同一个生命周期,
// 客户端中途重连也拿到同一份。
func (g *Gateway) ensureServer() *mcp.Server {
	g.once.Do(func() { g.server = g.buildServer() })
	return g.server
}

func (g *Gateway) buildServer() *mcp.Server {
	switch {
	case g.fleet == nil:
		g.toolErr = errors.New("网关未装配 Fleet,本轮没有节点工具")
	default:
		tools, err := g.fleet.ListTools(context.Background(), g.nodeID)
		switch {
		case err != nil:
			g.toolErr = fmt.Errorf("拉取节点 %s 的工具清单失败: %w", g.nodeID, err)
		case len(tools) == 0:
			g.toolErr = fmt.Errorf("%w:节点 %s(策略白名单为空,或插件都没自报只读)",
				ErrNoRemoteTools, g.nodeID)
		default:
			g.declared = tools
		}
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: gatewayName, Version: sdkversion.Version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        reportFindingTool,
		Description: "Report the finding for exactly one checklist item. Every evidence output must be a verbatim substring of a tool output returned in this round.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseHint, OpenWorldHint: &falseHint},
	}, g.reportFinding)

	used := map[string]struct{}{reportFindingTool: {}}
	// 按下标取址而不是 range 值:proto 消息内含互斥量,值拷贝会连锁复制(vet copylocks)。
	for i := range g.declared {
		d := &g.declared[i]
		if d.ToolName == "" {
			continue
		}
		// 只读是节点侧判过的,这里不再判第二遍策略;但传输契约上「ListTools 只回
		// 只读工具」本身要成立 —— 节点侧哪天回归了,core 不能替写工具背书。
		// 这是一道自检,不是第二事实源:被跳过的工具在节点侧本来就调不通。
		if !d.ReadOnly {
			continue
		}
		exposed := uniqueToolName(g.toolPrefix(), d.PluginId, d.ToolName, used)
		if exposed == "" {
			continue
		}
		used[exposed] = struct{}{}
		g.exposed++
		srv.AddTool(&mcp.Tool{
			Name:        exposed,
			Description: fmt.Sprintf("Read-only tool %s.%s on node %s. %s", d.PluginId, d.ToolName, g.nodeID, d.Description),
			InputSchema: nodeInputSchema(d.InputSchemaJson),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &falseHint, OpenWorldHint: &falseHint},
		}, g.nodeToolHandler(exposed, d))
	}
	return srv
}

// reportFinding 是唯一的写入口:校验不过就不进 sink,并把原因回给模型自己纠正。
func (g *Gateway) reportFinding(_ context.Context, _ *mcp.CallToolRequest, in reportInput) (*mcp.CallToolResult, any, error) {
	if g.isClosed() {
		return errorResult("本轮巡检已结束,不再接收发现"), nil, nil
	}
	f := Finding{
		Node:     g.nodeID,
		ItemID:   in.ItemID,
		Status:   in.Status,
		Severity: in.Severity,
		Summary:  in.Summary,
	}
	for _, e := range in.Evidence {
		f.Evidence = append(f.Evidence, Evidence{Tool: e.Tool, Input: e.Input, Output: e.Output, ReturnCode: e.ReturnCode})
	}
	if err := g.checkFinding(f); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if err := g.commit(f); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("发现已记录: %s/%s", g.nodeID, f.ItemID)}},
	}, nil, nil
}

// checkFinding 是三道硬校验的唯一实现,顺序固定:先身份、再严重级、最后证据。
// status 不设白名单:下游把非 ok 一律当「有问题」,空值自然会 fail-closed 到告警侧,
// 而在这层多加一道词表只会让模型在没有结论时被迫二选一。
func (g *Gateway) checkFinding(f Finding) error {
	if _, ok := g.items[f.ItemID]; !ok {
		return fmt.Errorf("item_id %q 不在本轮清单里(可选: %s)", f.ItemID, strings.Join(g.sortedItemIDs(), ", "))
	}
	if !validSeverity(f.Severity) {
		return fmt.Errorf("severity %q 不在 {%s, %s, %s} 里", f.Severity, SeverityInfo, SeverityWarning, SeverityCritical)
	}
	// 「有问题」必须带证据:空数组不是「没有异议」,是把这个结论的举证责任放空了。
	// ok 项允许零证据 —— 它说的是「看过,没问题」,本来就没有可贴的异常输出。
	if f.Status != StatusOK && len(f.Evidence) == 0 {
		return fmt.Errorf("status %q 的发现必须至少带一条 evidence:结论要有工具返回原文撑着", f.Status)
	}
	for i, ev := range f.Evidence {
		// 空白串同样是任何文本的子串:不单独拒掉就等于「零证据也能过」。
		if strings.TrimSpace(ev.Output) == "" {
			return fmt.Errorf("evidence[%d].output 为空:证据必须是工具返回的原文", i)
		}
		tools, names := g.recordedTools()
		if _, ok := tools[ev.Tool]; !ok {
			return fmt.Errorf("evidence[%d].tool %q 不是本轮调用过的工具(本轮记录过: %s)",
				i, ev.Tool, toolNames(names))
		}
		if !g.recorded(ev.Output) {
			return fmt.Errorf("evidence[%d].output 不是本轮任何一次工具返回的子串(本轮记录过: %s)",
				i, toolNames(names))
		}
	}
	return nil
}

// commit 落一条发现。同一项一轮只收一条,保证交出去的东西满足下游唯一性 invariant。
func (g *Gateway) commit(f Finding) error {
	if g.sink == nil {
		return errors.New("网关未装配发现收集器")
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("本轮巡检已结束,不再接收发现")
	}
	if _, dup := g.reported[f.ItemID]; dup {
		g.mu.Unlock()
		return fmt.Errorf("item_id %q 本轮已经报过:同一项一轮只能有一条发现", f.ItemID)
	}
	g.reported[f.ItemID] = struct{}{}
	g.mu.Unlock()

	// 串行化只在 Append 上:调用方(整文件读-改-写)不需要自己再加锁,而校验与去重
	// 不必排在磁盘 IO 后面。
	g.sinkMu.Lock()
	err := g.sink.Append(f)
	g.sinkMu.Unlock()
	if err != nil {
		// 收集失败时撤掉标记:本轮还能靠重报成功,而不是把这一项永远锁在「已报」。
		g.mu.Lock()
		delete(g.reported, f.ItemID)
		g.mu.Unlock()
		return fmt.Errorf("记录发现失败: %w", err)
	}
	return nil
}

// nodeToolHandler 把一次 MCP 工具调用转成一次节点 RPC。
//
// 失败一律如实回 IsError:巡检的结论建立在「工具到底看到了什么」上,
// 把失败抹平成空输出会让模型照着空结果下判断。
func (g *Gateway) nodeToolHandler(exposed string, d *agentv1.ToolDescriptor) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if g.isClosed() {
			return errorResult("本轮巡检已结束,不再调用节点工具"), nil
		}
		if g.fleet == nil {
			return errorResult("网关未装配 Fleet,无法调用节点工具"), nil
		}
		var args string
		if req.Params != nil {
			// 参数原样透传:白名单与路径规则由节点插件自己判,网关不做第二层改写。
			args = string(req.Params.Arguments)
		}
		resp, err := g.fleet.CallTool(ctx, g.nodeID, d.PluginId, d.ToolName, args)

		fail := func(text string) (*mcp.CallToolResult, error) {
			g.RecordCall(exposed, text)
			return errorResult(text), nil
		}
		switch {
		case err != nil:
			return fail(fmt.Sprintf("调用节点 %s 的工具 %s 失败: %v", g.nodeID, exposed, err))
		case resp == nil:
			return fail(fmt.Sprintf("节点 %s 的工具 %s 返回空结果(RPC 既无响应也无错误)", g.nodeID, exposed))
		case resp.GetIsError():
			detail := resp.GetErrorMessage()
			if detail == "" {
				detail = unwrapContentJSON(resp.GetContentJson())
			}
			if detail == "" {
				detail = "插件报告错误但没有给出原因"
			}
			return fail(fmt.Sprintf("节点 %s 的工具 %s 报告错误: %s", g.nodeID, exposed, detail))
		}
		text := unwrapContentJSON(resp.GetContentJson())
		g.RecordCall(exposed, text)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

// recorded 判断某段文本是否是本轮某次工具返回的子串。子串而非相等:
// 模型的转述里会夹带自己的话,但被引用的那一段必须逐字来自工具。
func (g *Gateway) recorded(output string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.records {
		if strings.Contains(r.output, output) {
			return true
		}
	}
	return false
}

// recordedTools 返回本轮记录过的工具名:集合用于校验 evidence.tool,列表用于错误文案。
func (g *Gateway) recordedTools() (map[string]struct{}, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := make(map[string]struct{}, len(g.records))
	for _, r := range g.records {
		set[r.tool] = struct{}{}
	}
	return set, slices.Sorted(maps.Keys(set))
}

// toolNames 把空集合也渲染成一句人话,错误文案不留空白。
func toolNames(names []string) string {
	if len(names) == 0 {
		return "(无,本轮还没调用过任何工具)"
	}
	return strings.Join(names, ", ")
}

func (g *Gateway) isClosed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

func (g *Gateway) sortedItemIDs() []string { return slices.Sorted(maps.Keys(g.items)) }

// toolPrefix 是节点工具名前缀:客户端只看得到名字,前缀是它辨认「哪台机器」的唯一线索。
func (g *Gateway) toolPrefix() string { return nodeToolPrefix(g.nodeID) }

// nodeToolPrefix 把节点 id 压成工具名前缀。提示词里要告诉客户端「工具以哪个前缀开头」,
// 与网关暴露工具用的是同一份规则。
func nodeToolPrefix(nodeID string) string {
	var b strings.Builder
	for _, r := range nodeID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "node"
	}
	return b.String()
}

// uniqueToolName 处理同名工具:先补插件段,仍有冲突就加序号。序号先让出位数再拼,
// 否则满长 base 每次截断都回到自己;用尽返回空串由调用方跳过 —— 少一个工具好过两个
// 不同工具顶同一个名字(调用会打到错的插件上)。
func uniqueToolName(prefix, pluginID, toolName string, used map[string]struct{}) string {
	base := clipName(prefix + "_" + sanitizeName(toolName))
	if _, clash := used[base]; !clash {
		return base
	}
	base = clipName(prefix + "_" + sanitizeName(strings.ReplaceAll(pluginID, ".", "_")) + "_" + sanitizeName(toolName))
	if _, clash := used[base]; !clash {
		return base
	}
	for i := 2; i < maxToolNameSuffix; i++ {
		suffix := fmt.Sprintf("_%d", i)
		candidate := clipNameN(base, maxToolNameLen-len(suffix)) + suffix
		if _, clash := used[candidate]; !clash {
			return candidate
		}
	}
	return ""
}

// sanitizeName 把标识符压进 MCP 工具名允许的字符集(字母、数字与 _ - .)。
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// clipName 截到 MCP 工具名上限。截断后仍可能重名,重名由调用方的序号兜底。
func clipName(name string) string { return clipNameN(name, maxToolNameLen) }

// clipNameN 按给定上限截断;名字里的非 ASCII 已被压成 "_",不会截断在多字节字符中间。
func clipNameN(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	return name[:limit]
}

// unwrapContentJSON 把节点回的 MCP content 信封拆成模型能读的原文。
//
// 真实链路上 ContentJson 是 [{"type":"text","text":"..."}]:原样交给模型会让多行原文以转义
// 形式出现,模型照抄的真换行反而不是它的子串。只有全是 text 块才拼接,否则退回整个信封。
func unwrapContentJSON(contentJSON string) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil || len(blocks) == 0 {
		return contentJSON
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type != "text" {
			return contentJSON
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}

// nodeInputSchema 用节点给的对象 schema,其余(空、坏 JSON、非 object)退回空对象:
// 坏 schema 不该让整轮巡检起不来。
func nodeInputSchema(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return json.RawMessage(objectSchema)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return json.RawMessage(objectSchema)
	}
	if t, _ := m["type"].(string); t != "object" {
		return json.RawMessage(objectSchema)
	}
	return json.RawMessage(raw)
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
