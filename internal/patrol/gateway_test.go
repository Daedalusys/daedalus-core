package patrol_test

// 本文件钉住巡检网关的两条信任边界:节点只读工具的参数透传与返回记录,
// 以及 report_finding 的三条硬校验(item_id 在清单内、severity 合法、
// evidence 是工具原文的子串)。测试走真的 Streamable HTTP + MCP client,
// 不直接调 handler —— 端口协议形状也是契约的一部分。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// fakeFleet 是 patrol.Fleet 的替身:回放预置的工具清单与结局,同时记下每次调用的入参。
type fakeFleet struct {
	tools   []agentv1.ToolDescriptor
	listErr error
	resp    *agentv1.CallToolResponse
	callErr error

	mu    sync.Mutex
	calls []fleetCall
}

// fleetCall 是一次 CallTool 的入参快照。
type fleetCall struct {
	nodeID   string
	pluginID string
	toolName string
	args     string
}

func (f *fakeFleet) ListTools(context.Context, string) ([]agentv1.ToolDescriptor, error) {
	return f.tools, f.listErr
}

func (f *fakeFleet) CallTool(_ context.Context, nodeID, pluginID, toolName, argumentsJSON string) (*agentv1.CallToolResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fleetCall{nodeID, pluginID, toolName, argumentsJSON})
	f.mu.Unlock()
	return f.resp, f.callErr
}

// call 取第 i 次调用;越界直接失败,免得断言静默跳过。
func (f *fakeFleet) call(t *testing.T, i int) fleetCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.calls) {
		t.Fatalf("只发生了 %d 次工具调用,取不到第 %d 次", len(f.calls), i)
	}
	return f.calls[i]
}

// recordingSink 是 patrol.FindingSink 的替身:收下每条发现,便于断言「拒的时候什么都没落」。
type recordingSink struct {
	err error

	mu       sync.Mutex
	findings []patrol.Finding
}

func (s *recordingSink) Append(f patrol.Finding) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findings = append(s.findings, f)
	return nil
}

// all 返回已收下的发现快照。
func (s *recordingSink) all() []patrol.Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]patrol.Finding(nil), s.findings...)
}

// shellTool 是一个正常的节点只读工具描述。
func shellTool() agentv1.ToolDescriptor {
	return agentv1.ToolDescriptor{
		PluginId:        "daedalus.shell",
		ToolName:        "shell_exec",
		Description:     "受控命令执行",
		InputSchemaJson: `{"type":"object","properties":{"command":{"type":"string"}}}`,
		ReadOnly:        true,
	}
}

// newGateway 装配一轮巡检的网关。items 默认只有 disk-health 一项。
func newGateway(t *testing.T, fleet patrol.Fleet, sink patrol.FindingSink) *patrol.Gateway {
	t.Helper()
	return patrol.NewGateway("lab-a", []patrol.Item{
		{ID: "disk-health", Prompt: "磁盘健康吗?", Severity: patrol.SeverityWarning, Enabled: true},
	}, fleet, sink)
}

// prepare 拉工具清单;失败直接结束测试 —— 本文件多数用例假定网关已就绪。
func prepare(t *testing.T, g *patrol.Gateway) {
	t.Helper()
	if err := g.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
}

// connect 起一个 httptest 服务并以 MCP client 连上去,测试结束自动收摊。
func connect(t *testing.T, h http.Handler) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return connectURL(t, srv.URL)
}

func connectURL(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "patrol-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool 调一次工具;协议层错误直接失败 —— 工具错误的形状是 IsError,不是 error。
func callTool(t *testing.T, session *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", name, err)
	}
	return res
}

// textOf 拼出工具结果里的文本内容。
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// toolNames 取工具清单里的名字集合。
func toolNames(t *testing.T, session *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	names := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

// findingArgs 造一份合法的 report_finding 入参,evidence 由调用方给。
func findingArgs(evidence ...map[string]any) map[string]any {
	args := map[string]any{
		"item_id":  "disk-health",
		"status":   patrol.StatusProblem,
		"severity": patrol.SeverityWarning,
		"summary":  "根分区剩余 3%",
	}
	if len(evidence) > 0 {
		args["evidence"] = evidence
	}
	return args
}

// TestGateway_Prepare_ExposesOnlyReadOnlyTools 钉住「写工具永不可远程调用」:
// 节点清单里即便混进自报非只读的工具,网关也不暴露;节点工具加节点前缀。
func TestGateway_Prepare_ExposesOnlyReadOnlyTools(t *testing.T) {
	fleet := &fakeFleet{tools: []agentv1.ToolDescriptor{
		shellTool(),
		{PluginId: "daedalus.fs", ToolName: "write_file", ReadOnly: false},
	}}
	g := newGateway(t, fleet, &recordingSink{})
	prepare(t, g)
	session := connect(t, g.Handler())

	names := toolNames(t, session)
	if !names["lab_a_shell_exec"] {
		t.Errorf("缺少带节点前缀的节点工具 lab_a_shell_exec,实际清单:%v", names)
	}
	if !names["report_finding"] {
		t.Errorf("缺少 report_finding,实际清单:%v", names)
	}
	if names["lab_a_write_file"] || names["daedalus_fs_write_file"] {
		t.Errorf("非只读工具不该被暴露,实际清单:%v", names)
	}
}

// TestGateway_Prepare_RejectsBadToolNames 钉住工具名的两道边界:同名工具静默覆盖
// 会让模型以为调的是 A 实际去了 B;名字超过协议上限则客户端直接不认。
func TestGateway_Prepare_RejectsBadToolNames(t *testing.T) {
	cases := []struct {
		name  string
		tools []agentv1.ToolDescriptor
		want  string
	}{
		{
			name: "同名工具",
			tools: []agentv1.ToolDescriptor{
				{PluginId: "daedalus.svc", ToolName: "query", ReadOnly: true},
				{PluginId: "daedalus.pkg", ToolName: "query", ReadOnly: true},
			},
			want: "query",
		},
		{
			name:  "加前缀后超过 128 字符",
			tools: []agentv1.ToolDescriptor{{PluginId: "daedalus.svc", ToolName: strings.Repeat("x", 130), ReadOnly: true}},
			want:  "128",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGateway(t, &fakeFleet{tools: c.tools}, &recordingSink{})
			err := g.Prepare(context.Background())
			if err == nil {
				t.Fatal("坏工具名应当让 Prepare 失败")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误里应点出 %q,实际:%v", c.want, err)
			}
		})
	}
}

// TestGateway_Prepare_ListToolsErrorIsFatal 钉住「节点不可达不是空工具清单」:
// Prepare 原样报错,Handler 拒服务(503),不静默开一个没有工具的轮次。
func TestGateway_Prepare_ListToolsErrorIsFatal(t *testing.T) {
	fleet := &fakeFleet{listErr: fmt.Errorf("节点 lab-a 未注册")}
	g := newGateway(t, fleet, &recordingSink{})

	if err := g.Prepare(context.Background()); err == nil {
		t.Fatal("取工具清单失败时 Prepare 应当报错")
	}

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("未就绪的网关应回 503,实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "未注册") {
		t.Errorf("503 的正文应带上失败原因,实际:%s", body)
	}
}

// TestGateway_NodeTool_PassesArgumentsVerbatim 钉住参数原样透传与返回原文记录:
// 模型拿到的文本就是 RecordCall 记下的文本,证据校验才谈得上「原文」。
func TestGateway_NodeTool_PassesArgumentsVerbatim(t *testing.T) {
	const output = "Filesystem      Size  Used\n/dev/vda1        40G   38G\n"
	fleet := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellTool()},
		resp:  &agentv1.CallToolResponse{ContentJson: fmt.Sprintf(`[{"type":"text","text":%q}]`, output)},
	}
	sink := &recordingSink{}
	g := newGateway(t, fleet, sink)
	prepare(t, g)
	session := connect(t, g.Handler())

	args := map[string]any{"command": "df", "args": []string{"-h"}}
	res := callTool(t, session, "lab_a_shell_exec", args)
	if res.IsError {
		t.Fatalf("正常调用不该是工具错误:%s", textOf(t, res))
	}
	if got := textOf(t, res); got != output {
		t.Errorf("模型拿到的返回 = %q, want %q", got, output)
	}

	call := fleet.call(t, 0)
	if call.nodeID != "lab-a" || call.pluginID != "daedalus.shell" || call.toolName != "shell_exec" {
		t.Errorf("调用目标 = %s/%s/%s, want lab-a/daedalus.shell/shell_exec", call.nodeID, call.pluginID, call.toolName)
	}
	var gotArgs, wantArgs map[string]any
	if err := json.Unmarshal([]byte(call.args), &gotArgs); err != nil {
		t.Fatalf("透传的参数不是合法 JSON(%q): %v", call.args, err)
	}
	_ = json.Unmarshal([]byte(`{"command":"df","args":["-h"]}`), &wantArgs)
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("透传参数 = %v, want %v", gotArgs, wantArgs)
	}

	// 模型引用刚拿到的输出片段 → 通过校验,发现里带上原文。
	res = callTool(t, session, "report_finding", findingArgs(map[string]any{
		"tool": "shell_exec", "input": "df -h", "output": "/dev/vda1        40G   38G", "returncode": 0,
	}))
	if res.IsError {
		t.Fatalf("引用真实工具输出应被接受,却被拒:%s", textOf(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("sink 收到 %d 条发现, want 1", got)
	}
}

// TestGateway_NodeTool_NormalizesContentJSON 钉住 content_json 的归一:模型看到的是
// 文本本身,不是包着它的 JSON —— 否则证据里的换行与引号永远对不上。
func TestGateway_NodeTool_NormalizesContentJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"MCP 内容数组", `[{"type":"text","text":"a\nb"}]`, "a\nb"},
		{"多段内容按行拼接", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"单个内容对象", `{"type":"text","text":"c"}`, "c"},
		{"裸 JSON 字符串", `"d\ne"`, "d\ne"},
		{"非 JSON 裸文本", "df -h\n", "df -h\n"},
		{"只有非文本内容", `[{"type":"image","data":"x"}]`, `[{"type":"image","data":"x"}]`},
		{"空内容也要有话说", "", "工具没有返回内容"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fleet := &fakeFleet{
				tools: []agentv1.ToolDescriptor{shellTool()},
				resp:  &agentv1.CallToolResponse{ContentJson: c.raw},
			}
			g := newGateway(t, fleet, &recordingSink{})
			prepare(t, g)
			session := connect(t, g.Handler())

			res := callTool(t, session, "lab_a_shell_exec", map[string]any{})
			if got := textOf(t, res); got != c.want {
				t.Errorf("模型拿到的文本 = %q, want %q", got, c.want)
			}
		})
	}
}

// TestGateway_NodeTool_CallErrorIsToolError 钉住节点调用失败不伪装成功:
// 原因是工具错误(IsError),不是协议错误,也不是空输出。
func TestGateway_NodeTool_CallErrorIsToolError(t *testing.T) {
	fleet := &fakeFleet{
		tools:   []agentv1.ToolDescriptor{shellTool()},
		callErr: fmt.Errorf("连接节点 lab-a 失败: connection refused"),
	}
	g := newGateway(t, fleet, &recordingSink{})
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "lab_a_shell_exec", map[string]any{"command": "df"})
	if !res.IsError {
		t.Fatalf("节点调用失败应是工具错误,实际:%s", textOf(t, res))
	}
	if text := textOf(t, res); !strings.Contains(text, "connection refused") {
		t.Errorf("工具错误正文应带上节点失败原因,实际:%q", text)
	}
}

// TestGateway_NodeTool_PluginErrorIsToolError 钉住插件自报的业务错误同样透出。
func TestGateway_NodeTool_PluginErrorIsToolError(t *testing.T) {
	fleet := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellTool()},
		resp:  &agentv1.CallToolResponse{IsError: true, ErrorMessage: "命令不在白名单"},
	}
	g := newGateway(t, fleet, &recordingSink{})
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "lab_a_shell_exec", map[string]any{"command": "rm"})
	if !res.IsError {
		t.Fatalf("插件错误应是工具错误,实际:%s", textOf(t, res))
	}
	if text := textOf(t, res); !strings.Contains(text, "命令不在白名单") {
		t.Errorf("工具错误正文应带上插件原因,实际:%q", text)
	}
}

// TestGateway_NodeTool_NoResponseIsToolError 钉住「没响应也没错误」不当成功:
// 协议异常若被当成正常输出,巡检就会拿一份空证据去发信。
func TestGateway_NodeTool_NoResponseIsToolError(t *testing.T) {
	fleet := &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}
	g := newGateway(t, fleet, &recordingSink{})
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "lab_a_shell_exec", map[string]any{"command": "df"})
	if !res.IsError {
		t.Fatalf("没有响应应是工具错误,实际:%s", textOf(t, res))
	}
	if text := textOf(t, res); !strings.Contains(text, "没有返回内容") {
		t.Errorf("工具错误应说明没拿到内容,实际:%q", text)
	}
}

// TestGateway_NodeTool_PluginErrorKeepsContent 钉住插件错误里输出与错误说明都在:
// 模型判断「为什么失败」常常要看那半截输出。
func TestGateway_NodeTool_PluginErrorKeepsContent(t *testing.T) {
	fleet := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellTool()},
		resp: &agentv1.CallToolResponse{
			IsError:      true,
			ContentJson:  `[{"type":"text","text":"df: /mnt/x: 权限不足"}]`,
			ErrorMessage: "命令执行失败",
		},
	}
	g := newGateway(t, fleet, &recordingSink{})
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "lab_a_shell_exec", map[string]any{"command": "df"})
	text := textOf(t, res)
	if !res.IsError || !strings.Contains(text, "权限不足") || !strings.Contains(text, "命令执行失败") {
		t.Errorf("工具结果 = %v %q, want 错误且同时含输出与原因", res.IsError, text)
	}
}

// TestGateway_ReportFinding_RejectsUnknownItemID 钉住第一条硬校验:
// 清单外的 item_id 会让跨轮去重失效,必须当场拒。
func TestGateway_ReportFinding_RejectsUnknownItemID(t *testing.T) {
	sink := &recordingSink{}
	g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
	prepare(t, g)
	session := connect(t, g.Handler())

	args := findingArgs()
	args["item_id"] = "not-in-checklist"
	res := callTool(t, session, "report_finding", args)
	if !res.IsError {
		t.Fatalf("清单外的 item_id 应被拒,实际成功:%s", textOf(t, res))
	}
	if text := textOf(t, res); !strings.Contains(text, "disk-health") {
		t.Errorf("拒因应点出本轮清单里有哪些 id,实际:%q", text)
	}
	if got := sink.all(); len(got) != 0 {
		t.Errorf("被拒的发现不该落盘,实际落了 %d 条", len(got))
	}
}

// TestGateway_ReportFinding_RejectsBadFields 钉住第二条硬校验(severity 词表)与
// 同族的字段校验:status 词表、非空 summary。坏字段进来会让分类与邮件措辞失真。
func TestGateway_ReportFinding_RejectsBadFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value any
		want  string
	}{
		{"severity 不在词表", "severity", "urgent", "critical"},
		{"status 不在词表", "status", "problemm", patrol.StatusOK},
		{"summary 为空", "summary", "   ", "summary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &recordingSink{}
			g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
			prepare(t, g)
			session := connect(t, g.Handler())

			args := findingArgs()
			args[c.field] = c.value
			res := callTool(t, session, "report_finding", args)
			if !res.IsError {
				t.Fatalf("%s 应被拒,实际成功:%s", c.name, textOf(t, res))
			}
			if text := textOf(t, res); !strings.Contains(text, c.want) {
				t.Errorf("拒因应提到 %q,实际:%q", c.want, text)
			}
			if got := sink.all(); len(got) != 0 {
				t.Errorf("被拒的发现不该落盘,实际落了 %d 条", len(got))
			}
		})
	}
}

// TestGateway_ReportFinding_RejectsEvidenceNotFromTools 钉住第三条硬校验:
// 证据只能是本轮工具返回原文的子串(模型转述、编造、空串一律拒)。
func TestGateway_ReportFinding_RejectsEvidenceNotFromTools(t *testing.T) {
	const output = "Filesystem      Size  Used\n/dev/vda1        40G   38G\n"
	cases := []struct {
		name   string
		output string
		record bool
	}{
		{"编造的证据", "磁盘快满了,建议马上清理", true},
		{"空证据", "", true},
		{"本轮没有任何工具调用", output, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &recordingSink{}
			g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
			if c.record {
				g.RecordCall("shell_exec", output)
			}
			prepare(t, g)
			session := connect(t, g.Handler())

			res := callTool(t, session, "report_finding", findingArgs(map[string]any{
				"tool": "shell_exec", "output": c.output,
			}))
			if !res.IsError {
				t.Fatalf("%s 应被拒,实际成功:%s", c.name, textOf(t, res))
			}
			if text := textOf(t, res); !strings.Contains(text, "原文") {
				t.Errorf("拒因应说明证据必须是工具原文,实际:%q", text)
			}
			if got := sink.all(); len(got) != 0 {
				t.Errorf("被拒的发现不该落盘,实际落了 %d 条", len(got))
			}
		})
	}
}

// TestGateway_ReportFinding_AcceptsRealEvidence 钉住放行路径:证据是记录过的一段子串,
// 发现按清单项、节点与原文落到 sink。
func TestGateway_ReportFinding_AcceptsRealEvidence(t *testing.T) {
	sink := &recordingSink{}
	g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
	g.RecordCall("shell_exec", "Filesystem      Size  Used\n/dev/vda1        40G   38G\n")
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "report_finding", findingArgs(map[string]any{
		"tool":       "shell_exec",
		"input":      "df -h",
		"output":     "/dev/vda1        40G   38G",
		"returncode": 0,
	}))
	if res.IsError {
		t.Fatalf("真实证据应被接受,却被拒:%s", textOf(t, res))
	}

	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("sink 收到 %d 条发现, want 1", len(got))
	}
	f := got[0]
	if f.Node != "lab-a" || f.ItemID != "disk-health" || f.Status != patrol.StatusProblem || f.Severity != patrol.SeverityWarning {
		t.Errorf("发现身份 = %s/%s/%s/%s, want lab-a/disk-health/problem/warning", f.Node, f.ItemID, f.Status, f.Severity)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Output != "/dev/vda1        40G   38G" || f.Evidence[0].Tool != "shell_exec" {
		t.Errorf("证据没按原文落盘:%+v", f.Evidence)
	}
}

// TestGateway_ReportFinding_SeverityVocabulary 钉住三个合法严重级都能放行 ——
// 只测一个会让词表里另外两个的拼写错误溜过去。
func TestGateway_ReportFinding_SeverityVocabulary(t *testing.T) {
	for _, severity := range []string{patrol.SeverityInfo, patrol.SeverityWarning, patrol.SeverityCritical} {
		t.Run(severity, func(t *testing.T) {
			sink := &recordingSink{}
			g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
			prepare(t, g)
			session := connect(t, g.Handler())

			args := findingArgs()
			args["severity"] = severity
			res := callTool(t, session, "report_finding", args)
			if res.IsError {
				t.Fatalf("合法严重级 %s 被拒:%s", severity, textOf(t, res))
			}
			if got := sink.all(); len(got) != 1 || got[0].Severity != severity {
				t.Errorf("发现严重级 = %+v, want %s", got, severity)
			}
		})
	}
}

// TestGateway_ReportFinding_SinkErrorSurfaces 钉住落盘失败不吞:模型要看到「没记上」,
// 否则它会以为这一项报完了。
func TestGateway_ReportFinding_SinkErrorSurfaces(t *testing.T) {
	sink := &recordingSink{err: fmt.Errorf("磁盘已满")}
	g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
	prepare(t, g)
	session := connect(t, g.Handler())

	res := callTool(t, session, "report_finding", findingArgs())
	if !res.IsError {
		t.Fatalf("落盘失败应是工具错误,实际成功:%s", textOf(t, res))
	}
	if text := textOf(t, res); !strings.Contains(text, "磁盘已满") {
		t.Errorf("工具错误应带上落盘失败原因,实际:%q", text)
	}
}

// TestGateway_Close_RejectsLateCalls 钉住收摊语义:本轮收摊后迟到的证据与发现都不再受理,
// 免得已经退出的 AI 客户端把东西塞进下一轮。
func TestGateway_Close_RejectsLateCalls(t *testing.T) {
	sink := &recordingSink{}
	g := newGateway(t, &fakeFleet{tools: []agentv1.ToolDescriptor{shellTool()}}, sink)
	prepare(t, g)
	session := connect(t, g.Handler())
	if err := g.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	res := callTool(t, session, "report_finding", findingArgs())
	if !res.IsError {
		t.Fatalf("收摊后的 report_finding 应被拒,实际成功:%s", textOf(t, res))
	}
	if got := sink.all(); len(got) != 0 {
		t.Errorf("收摊后不该再落盘,实际落了 %d 条", len(got))
	}

	res = callTool(t, session, "lab_a_shell_exec", map[string]any{"command": "df"})
	if !res.IsError {
		t.Fatalf("收摊后的节点工具调用应被拒,实际成功:%s", textOf(t, res))
	}
}

// TestTokenMiddleware 钉住本轮令牌校验:缺令牌、错令牌都拒;空令牌 fail-closed ——
// 漏配令牌时绝不能退化成「谁都能连」。
func TestTokenMiddleware(t *testing.T) {
	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(patrol.TokenMiddleware("round-token", next))
	defer srv.Close()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"缺令牌", "", http.StatusUnauthorized},
		{"错令牌", "Bearer other-token", http.StatusUnauthorized},
		{"前缀不对", "round-token", http.StatusUnauthorized},
		{"正确令牌", "Bearer round-token", http.StatusOK},
	}
	for _, c := range cases {
		req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
		if err != nil {
			t.Fatalf("造请求失败: %v", err)
		}
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: 请求失败: %v", c.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: 状态码 = %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}

	if !handlerCalled {
		t.Error("带正确令牌的请求应放行到内部 handler")
	}

	// 空令牌:哪怕请求带了 Authorization 也必须拒。
	emptySRV := httptest.NewServer(patrol.TokenMiddleware("", next))
	defer emptySRV.Close()
	req, _ := http.NewRequest(http.MethodPost, emptySRV.URL, nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("空令牌: 请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("空令牌应 fail-closed 回 401,实际 %d", resp.StatusCode)
	}
}
