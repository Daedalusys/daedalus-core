package patrol_test

// 本文件钉住网关的信任核心:report_finding 的三道硬校验(清单内 item_id、
// 合法 severity、证据是工具返回原文的子串)与节点工具调用失败的如实回报。
// 所有断言都走真实的 MCP 客户端与 Streamable HTTP 连接,不直接调 handler 内部函数 ——
// 校验一旦只存在于单元测试里,线上那条路就没人钉。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// 装配缝的编译期钉子:巡检二进制把 *fleet.ToolCaller 直接交给网关,不需要适配层。
var _ patrol.Fleet = (*fleet.ToolCaller)(nil)

const testNode = "lab-a"

// shellToolDesc 造一条节点自报的只读工具;真实链路上「白名单 ∩ ReadOnlyHint」由
// 节点侧判定,网关只照单全收,所以夹具里也必须带 ReadOnly 才符合语义。
func shellToolDesc() agentv1.ToolDescriptor {
	return agentv1.ToolDescriptor{
		PluginId:        "daedalus.shell",
		ToolName:        "shell_exec",
		Description:     "run an allowed command",
		InputSchemaJson: `{"type":"object","properties":{"command":{"type":"string"}}}`,
		ReadOnly:        true,
	}
}

// testItems 是本轮清单;三项同名不同 id,便于 severity 表驱动复用同一个网关。
var testItems = []patrol.Item{
	{ID: "disk-health", Prompt: "看根分区余量", Severity: patrol.SeverityWarning},
	{ID: "service-failed", Prompt: "看失败的单元", Severity: patrol.SeverityWarning},
	{ID: "kernel-panic", Prompt: "看内核报错", Severity: patrol.SeverityCritical},
}

type fakeCall struct {
	nodeID, pluginID, toolName, arguments string
}

// fakeFleet 记录每次调用的参数,便于断言「参数原样透传」。
type fakeFleet struct {
	mu      sync.Mutex
	tools   []agentv1.ToolDescriptor
	listErr error
	resp    *agentv1.CallToolResponse
	callErr error
	calls   []fakeCall
}

func (f *fakeFleet) ListTools(_ context.Context, _ string) ([]agentv1.ToolDescriptor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeFleet) CallTool(_ context.Context, nodeID, pluginID, toolName, arguments string) (*agentv1.CallToolResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{nodeID: nodeID, pluginID: pluginID, toolName: toolName, arguments: arguments})
	f.mu.Unlock()
	if f.callErr != nil {
		return nil, f.callErr
	}
	return f.resp, nil
}

func (f *fakeFleet) lastCall(t *testing.T) fakeCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("节点一次都没被调用")
	}
	return f.calls[len(f.calls)-1]
}

// memSink 是发现收集器;"没有新增 finding" 是硬校验失败时唯一有意义的断言。
// delay 用来放大「两个 Append 同时在写」的窗口,maxParallel 就是网关串行化契约的探针。
type memSink struct {
	mu       sync.Mutex
	findings []patrol.Finding
	err      error

	callsMu     sync.Mutex
	inFlight    int
	maxInFlight int
	delay       time.Duration
}

func (s *memSink) Append(f patrol.Finding) error {
	s.callsMu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.callsMu.Unlock()
	defer func() {
		s.callsMu.Lock()
		s.inFlight--
		s.callsMu.Unlock()
	}()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.findings = append(s.findings, f)
	return nil
}

func (s *memSink) maxParallel() int {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	return s.maxInFlight
}

func (s *memSink) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *memSink) all() []patrol.Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]patrol.Finding(nil), s.findings...)
}

// newGateway 装配网关,并在用例结束时收尾。
func newGateway(t *testing.T, items []patrol.Item, f *fakeFleet, sink *memSink) *patrol.Gateway {
	t.Helper()
	g := patrol.NewGateway(testNode, items, f, sink)
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// connect 用真实 MCP 客户端连上网关的 Streamable HTTP 端点。
func connect(t *testing.T, h http.Handler) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	session, err := mcp.NewClient(&mcp.Implementation{Name: "patrol-test", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           srv.Client(),
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
		}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s 失败(协议层): %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// reportArgs 拼一份 report_finding 入参;evidence 的 output 由调用方给,工具名默认
// 用测试里人工 RecordCall 的 "shell_exec"。
func reportArgs(itemID, severity string, outputs ...string) map[string]any {
	return reportArgsForTool("shell_exec", itemID, severity, outputs...)
}

func reportArgsForTool(tool, itemID, severity string, outputs ...string) map[string]any {
	evidence := make([]map[string]any, 0, len(outputs))
	for _, out := range outputs {
		evidence = append(evidence, map[string]any{
			"tool":       tool,
			"input":      `{"command":"df -h"}`,
			"output":     out,
			"returncode": 0,
		})
	}
	return map[string]any{
		"item_id":  itemID,
		"status":   patrol.StatusProblem,
		"severity": severity,
		"summary":  "根分区余量偏低",
		"evidence": evidence,
	}
}

func TestGateway_ReportFinding_RejectsUnknownItemID(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	res := callTool(t, s, "report_finding", reportArgs("not-in-checklist", patrol.SeverityWarning, "df -h"))

	if !res.IsError {
		t.Fatalf("清单外的 item_id 必须被拒,实际放行:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("被拒的发现不能进 sink,实际进了 %d 条", got)
	}
}

func TestGateway_ReportFinding_RejectsFabricatedEvidence(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\nFilesystem Size Used Avail Use%\n/dev/vda1 40G 3G 35G 9% /\n")

	res := callTool(t, s, "report_finding",
		reportArgs("disk-health", patrol.SeverityWarning, "/dev/vda1 已用 97%,随时写满"))

	if !res.IsError {
		t.Fatalf("工具从未返回过的证据必须被拒,实际放行:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("证据伪造的发现不能进 sink,实际进了 %d 条", got)
	}
}

func TestGateway_ReportFinding_AcceptsRealEvidence(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\nFilesystem Size Used Avail Use%\n/dev/vda1 40G 3G 35G 9% /\n")

	res := callTool(t, s, "report_finding",
		reportArgs("disk-health", patrol.SeverityWarning, "/dev/vda1 40G 3G 35G 9% /"))

	if res.IsError {
		t.Fatalf("原文子串必须放行,实际被拒:%s", resultText(t, res))
	}
	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("期望 sink 收到 1 条发现,实际 %d 条", len(got))
	}
	f := got[0]
	// 节点由网关盖章:模型只报清单项,不能自己指定哪台机器。
	if f.Node != testNode || f.ItemID != "disk-health" || f.Severity != patrol.SeverityWarning {
		t.Fatalf("落库的发现身份不对: %+v", f)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Output != "/dev/vda1 40G 3G 35G 9% /" {
		t.Fatalf("证据原文没保住: %+v", f.Evidence)
	}
}

func TestGateway_ReportFinding_SeverityWhitelist(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "systemctl --failed\n0 loaded units listed.\n")

	bad := callTool(t, s, "report_finding",
		reportArgs("disk-health", "fatal", "systemctl --failed"))
	if !bad.IsError {
		t.Fatalf("表外 severity 必须被拒,实际放行:%s", resultText(t, bad))
	}

	for i, sev := range []string{patrol.SeverityInfo, patrol.SeverityWarning, patrol.SeverityCritical} {
		res := callTool(t, s, "report_finding",
			reportArgs(testItems[i].ID, sev, "0 loaded units listed."))
		if res.IsError {
			t.Fatalf("合法 severity %q 必须放行,实际被拒:%s", sev, resultText(t, res))
		}
	}
	if got := len(sink.all()); got != 3 {
		t.Fatalf("期望 3 条合法发现进 sink,实际 %d 条", got)
	}
}

// Close 之后不再受理:收尾期才到的调用不能写进已经交出去的证据与发现。
func TestGateway_ClosedGatewayRejectsCalls(t *testing.T) {
	f := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellToolDesc()},
		resp:  &agentv1.CallToolResponse{ContentJson: "df -h\n"},
	}
	sink := &memSink{}
	g := newGateway(t, testItems, f, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	if err := g.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	if res := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"df -h"}`)); !res.IsError {
		t.Fatalf("关闭后的节点调用必须被拒:%s", resultText(t, res))
	}
	if res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityWarning, "df -h")); !res.IsError {
		t.Fatalf("关闭后的发现必须被拒:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("关闭后不能写 sink,实际写了 %d 条", got)
	}
}

// 同一项一轮只收一条:发现文件的唯一性 invariant 不能被两遍报告打破。
func TestGateway_ReportFinding_RejectsDuplicateItem(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	if res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityWarning, "df -h")); res.IsError {
		t.Fatalf("第一条发现应当放行:%s", resultText(t, res))
	}
	res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityCritical, "df -h"))
	if !res.IsError {
		t.Fatalf("同一项的第二条发现必须被拒,实际放行:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("期望 sink 里只有 1 条,实际 %d 条", got)
	}
}

// 收集器写失败不能把这一项锁死在「已报」:本轮重报还得能成。
func TestGateway_ReportFinding_SinkFailureAllowsRetry(t *testing.T) {
	sink := &memSink{err: errors.New("磁盘写满")}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	bad := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityWarning, "df -h"))
	if !bad.IsError {
		t.Fatalf("收集器失败必须回报错误:%s", resultText(t, bad))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("收集器失败时 sink 里不该有东西,实际 %d 条", got)
	}

	sink.setErr(nil)
	res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityWarning, "df -h"))
	if res.IsError {
		t.Fatalf("收集器恢复后重报应当成功:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("期望 1 条发现,实际 %d 条", got)
	}
}

// 空串与纯空白都是任何文本的子串 —— 不单独拒掉就是「零证据也能过」的后门。
func TestGateway_ReportFinding_RejectsBlankEvidenceOutput(t *testing.T) {
	for _, blank := range []string{"", " \t ", "\n"} {
		sink := &memSink{}
		g := newGateway(t, testItems, &fakeFleet{}, sink)
		s := connect(t, g.Handler())
		// 记录里确实含这些空白字符:子串判定会放它们过去,只能靠 TrimSpace 拦。
		g.RecordCall("shell_exec", "df -h\n \t \n")

		res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityWarning, blank))
		if !res.IsError {
			t.Fatalf("空白证据 %q 必须被拒,实际放行:%s", blank, resultText(t, res))
		}
		if got := len(sink.all()); got != 0 {
			t.Fatalf("空白证据 %q 的发现不能进 sink,实际进了 %d 条", blank, got)
		}
	}
}

// evidence.tool 是模型自报的,不核对就能把「另一件工具」的原文挂到这条结论下。
func TestGateway_ReportFinding_RejectsUnrecordedTool(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	res := callTool(t, s, "report_finding",
		reportArgsForTool("never_called", "disk-health", patrol.SeverityWarning, "df -h"))
	if !res.IsError {
		t.Fatalf("没调用过的工具名必须被拒,实际放行:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("工具名不实的发现不能进 sink,实际进了 %d 条", got)
	}
}

// 客户端把网关工具显示成 <server>_<网关名>,模型照抄这个名字是最自然的写法;
// 只认网关名会让每轮都白费几次重试(靶场上每轮 4 次),所以两种写法都要认。
func TestGateway_ReportFinding_AcceptsClientVisibleToolName(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	visible := patrol.ActionName("patrol-node", "shell_exec")
	res := callTool(t, s, "report_finding",
		reportArgsForTool(visible, "disk-health", patrol.SeverityWarning, "df -h"))
	if res.IsError {
		t.Fatalf("客户端可见的工具名 %q 应当被接受,实际被拒:%s", visible, resultText(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("带证据的发现应进 sink,实际进了 %d 条", got)
	}
}

// 「有问题」不带证据等于凭空报警:空数组不是「没有异议」,是放空了举证责任。
func TestGateway_ReportFinding_RejectsProblemWithoutEvidence(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())

	res := callTool(t, s, "report_finding", reportArgs("disk-health", patrol.SeverityCritical))
	if !res.IsError {
		t.Fatalf("problem 发现不带证据必须被拒,实际放行:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("无证据的 problem 不能进 sink,实际进了 %d 条", got)
	}
}

func TestGateway_ReportFinding_AllowsOKWithoutEvidence(t *testing.T) {
	sink := &memSink{}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())

	args := reportArgs("disk-health", patrol.SeverityInfo)
	args["status"] = patrol.StatusOK
	res := callTool(t, s, "report_finding", args)
	if res.IsError {
		t.Fatalf("ok 项允许零证据(本来就没有异常输出可贴),实际被拒:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("期望 1 条发现,实际 %d 条", got)
	}
}

func TestGateway_NodeToolExposedAndArgumentsPassThrough(t *testing.T) {
	sink := &memSink{}
	f := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellToolDesc()},
		resp:  &agentv1.CallToolResponse{ContentJson: "df -h\n/dev/vda1 40G 3G 35G 9% /\n", AuditSeq: 7},
	}
	g := newGateway(t, testItems, f, sink)
	s := connect(t, g.Handler())

	var names []string
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("拉工具清单失败: %v", err)
		}
		names = append(names, tool.Name)
	}
	if !slicesContains(names, "report_finding") || !slicesContains(names, "lab_a_shell_exec") {
		t.Fatalf("工具清单缺项: %v", names)
	}

	args := `{"command":"df -h"}`
	res := callTool(t, s, "lab_a_shell_exec", rawJSON(args))
	if res.IsError {
		t.Fatalf("节点调用应当成功:%s", resultText(t, res))
	}
	if got := resultText(t, res); !strings.Contains(got, "/dev/vda1") {
		t.Fatalf("节点返回原文没传回客户端: %q", got)
	}
	if call := f.lastCall(t); call.arguments != args || call.pluginID != "daedalus.shell" || call.toolName != "shell_exec" {
		t.Fatalf("参数没有原样透传: %+v", call)
	}

	// 真调用返回的内容同样进证据池:模型可以引用刚看到的那段原文(工具名用暴露名)。
	report := callTool(t, s, "report_finding",
		reportArgsForTool("lab_a_shell_exec", "disk-health", patrol.SeverityWarning, "/dev/vda1 40G 3G 35G 9%"))
	if report.IsError {
		t.Fatalf("引用节点原文证据必须放行:%s", resultText(t, report))
	}
}

// 真实链路的 ContentJson 是 MCP content 信封;网关必须交解包后的原文,否则模型
// 照抄的真换行不是信封 JSON 的子串(被误拒),邮件里贴出来的也是 \n 转义串。
func TestGateway_NodeToolUnwrapsContentEnvelope(t *testing.T) {
	const inner = "df -h\nFilesystem Size Used Avail Use%\n/dev/vda1 40G 3G 35G 9% /\n"
	envelope, err := json.Marshal([]map[string]string{
		{"type": "text", "text": inner},
		{"type": "text", "text": "(exit 0)\n"},
	})
	if err != nil {
		t.Fatalf("拼信封失败: %v", err)
	}
	sink := &memSink{}
	f := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellToolDesc()},
		resp:  &agentv1.CallToolResponse{ContentJson: string(envelope)},
	}
	g := newGateway(t, testItems, f, sink)
	s := connect(t, g.Handler())

	res := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"df -h"}`))
	if res.IsError {
		t.Fatalf("节点调用应当成功:%s", resultText(t, res))
	}
	if got, want := resultText(t, res), inner+"(exit 0)\n"; got != want {
		t.Fatalf("模型应当看到解包后的原文\n got=%q\nwant=%q", got, want)
	}

	// 证据用真换行的多行原文(不是 JSON 转义形态),必须过。
	line := "/dev/vda1 40G 3G 35G 9% /"
	report := callTool(t, s, "report_finding",
		reportArgsForTool("lab_a_shell_exec", "disk-health", patrol.SeverityWarning, line))
	if report.IsError {
		t.Fatalf("引用解包后的原文必须放行:%s", resultText(t, report))
	}
	finding := sink.all()[0]
	if got := finding.Evidence[0].Output; got != line {
		t.Fatalf("落库的证据应当是解包后的原文,实际 %q", got)
	}
}

// 两条不同项的并发报告都得进 sink:sink 的串行由网关负责(实现方不该自己加锁)。
func TestGateway_ConcurrentReportsBothRecorded(t *testing.T) {
	sink := &memSink{delay: 20 * time.Millisecond}
	g := newGateway(t, testItems, &fakeFleet{}, sink)
	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")

	resCh := make(chan *mcp.CallToolResult, 2)
	callErr := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range []string{"disk-health", "service-failed"} {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()
			res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "report_finding",
				Arguments: reportArgs(item, patrol.SeverityWarning, "df -h"),
			})
			if err != nil {
				callErr <- err
				return
			}
			resCh <- res
		}(item)
	}
	wg.Wait()
	close(resCh)
	close(callErr)

	for err := range callErr {
		t.Fatalf("并发调用在协议层失败: %v", err)
	}
	for res := range resCh {
		if res.IsError {
			t.Fatalf("并发报告不该有一条被拒:%s", resultText(t, res))
		}
	}
	if got := len(sink.all()); got != 2 {
		t.Fatalf("期望 sink 收到 2 条发现,实际 %d 条", got)
	}
	if got := sink.maxParallel(); got != 1 {
		t.Fatalf("sink.Append 被并发调用了(%d 条同时在写):网关必须串行化,实现方不该自己加锁", got)
	}
}

// 满长工具名撞名时,去重必须收敛:截断后不能回到同一个名字(否则序号循环永不结束)。
func TestGateway_DuplicateToolNamesDoNotHang(t *testing.T) {
	// 插件名与工具名同前缀:两级 base 截断后撞成同一个名字,必然落到序号分支。
	longTool := func() agentv1.ToolDescriptor {
		return agentv1.ToolDescriptor{
			PluginId:        strings.Repeat("t", 10),
			ToolName:        strings.Repeat("t", 40),
			InputSchemaJson: `{"type":"object"}`,
			ReadOnly:        true,
		}
	}
	f := &fakeFleet{tools: []agentv1.ToolDescriptor{longTool(), longTool()}}
	g := patrol.NewGateway(strings.Repeat("n", 120), testItems, f, &memSink{})
	t.Cleanup(func() { _ = g.Close() })

	done := make(chan error, 1)
	go func() { done <- g.Prepare() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Prepare 失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("工具名去重没有收敛:满长名字下疑似死循环")
	}
}

func TestGateway_NodeToolRPCCallErrorIsReportedAsError(t *testing.T) {
	f := &fakeFleet{
		tools:   []agentv1.ToolDescriptor{shellToolDesc()},
		callErr: errors.New("节点 lab-a 未注册"),
	}
	g := newGateway(t, testItems, f, &memSink{})
	s := connect(t, g.Handler())

	res := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"df -h"}`))
	if !res.IsError {
		t.Fatalf("RPC 失败不能伪装成成功:%s", resultText(t, res))
	}
	if got := resultText(t, res); !strings.Contains(got, "未注册") {
		t.Fatalf("失败原因必须写进内容: %q", got)
	}
}

func TestGateway_NodeToolBusinessErrorIsReportedAsError(t *testing.T) {
	f := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellToolDesc()},
		resp:  &agentv1.CallToolResponse{IsError: true, ErrorMessage: "command not allowed: rm -rf /"},
	}
	g := newGateway(t, testItems, f, &memSink{})
	s := connect(t, g.Handler())

	res := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"rm -rf /"}`))
	if !res.IsError {
		t.Fatalf("插件业务错误不能伪装成成功:%s", resultText(t, res))
	}
	if got := resultText(t, res); !strings.Contains(got, "command not allowed") {
		t.Fatalf("业务错误原因必须写进内容: %q", got)
	}
}

// 失败文本也在证据池里:否则「工具坏了」这条真实告警拿不出证据,只能改报 ok,
// 下一轮就被四分类算成 Recovered —— 邮件会把「查不了」读成「恢复了」。
func TestGateway_NodeToolFailureTextIsCitableEvidence(t *testing.T) {
	f := &fakeFleet{
		tools: []agentv1.ToolDescriptor{shellToolDesc()},
		resp:  &agentv1.CallToolResponse{IsError: true, ErrorMessage: "systemd is not running"},
	}
	sink := &memSink{}
	g := newGateway(t, testItems, f, sink)
	s := connect(t, g.Handler())

	failure := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"systemctl --failed"}`))
	if !failure.IsError {
		t.Fatalf("插件业务错误不能伪装成成功:%s", resultText(t, failure))
	}
	// 引用模型实际看到的那一整串(含网关前缀),证明「模型看到 == 池里那份」。
	seen := resultText(t, failure)

	report := callTool(t, s, "report_finding",
		reportArgsForTool("lab_a_shell_exec", "service-failed", patrol.SeverityCritical, seen))
	if report.IsError {
		t.Fatalf("失败原文必须能作为证据上报,实际被拒:%s", resultText(t, report))
	}
	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("期望 sink 收到 1 条发现,实际 %d 条", len(got))
	}
	if got[0].Evidence[0].Output != seen {
		t.Fatalf("落库证据应当是模型看到的那串失败文本,实际 %q", got[0].Evidence[0].Output)
	}
}

// 既没响应也没错误是协议异常:最危险的不是报错,而是被当成「工具返回空」。
func TestGateway_NodeToolEmptyResponseIsError(t *testing.T) {
	f := &fakeFleet{tools: []agentv1.ToolDescriptor{shellToolDesc()}}
	g := newGateway(t, testItems, f, &memSink{})
	s := connect(t, g.Handler())

	res := callTool(t, s, "lab_a_shell_exec", rawJSON(`{"command":"df -h"}`))
	if !res.IsError {
		t.Fatalf("空响应必须按错误处理:%s", resultText(t, res))
	}
}

// 拉不到工具清单只说明这台机器这轮没得查,不能顺带把 report_finding 也废掉。
func TestGateway_ListToolsErrorStillAllowsReport(t *testing.T) {
	sink := &memSink{}
	f := &fakeFleet{listErr: errors.New("节点 lab-a 未注册")}
	g := newGateway(t, testItems, f, sink)

	if err := g.Prepare(); err == nil {
		t.Fatal("节点不可达时 Tools() 必须回报错误,便于本轮记一条发现")
	}

	s := connect(t, g.Handler())
	g.RecordCall("shell_exec", "df -h\n")
	res := callTool(t, s, "report_finding",
		reportArgs("disk-health", patrol.SeverityWarning, "df -h"))
	if res.IsError {
		t.Fatalf("清单拉取失败不该连带拒掉发现:%s", resultText(t, res))
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("期望 1 条发现,实际 %d 条", got)
	}
}

func TestGateway_TokenAuth(t *testing.T) {
	g := newGateway(t, testItems, &fakeFleet{}, &memSink{})
	g.SetToken("round-token-1")

	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	// 没有令牌:握不上手。
	client := mcp.NewClient(&mcp.Implementation{Name: "no-token", Version: "0"}, nil)
	if _, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           srv.Client(),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil); err == nil {
		t.Fatal("没带本轮令牌的客户端必须连不进来")
	}

	// 带上令牌:正常握手。
	withToken := &http.Client{Transport: tokenHeader{srv.Client().Transport, "X-Patrol-Token", "round-token-1"}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "with-token", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           withToken,
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
		}, nil)
	if err != nil {
		t.Fatalf("带正确令牌应当握上手: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
}

// tokenHeader 给每个请求加一个头;base 为 nil 时用 DefaultTransport。
type tokenHeader struct {
	base        http.RoundTripper
	header, val string
}

func (h tokenHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := r.Clone(r.Context())
	clone.Header.Set(h.header, h.val)
	return base.RoundTrip(clone)
}

// rawJSON 让参数按原文发出:客户端 marshal 时原样写字节,便于钉住「原样透传」。
type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// 节点可达但一个只读工具都没有(白名单为空 / 插件未自报只读):本轮必须失败。
// 否则模型只剩 report_finding,每一项都能报无证据的 ok,轮次以「全绿」收尾 ——
// 那正是「静默的空轮次让人以为机器健康」。
func TestGateway_PrepareFailsWhenNodeExposesNoTools(t *testing.T) {
	g := newGateway(t, testItems, &fakeFleet{}, &memSink{})
	if err := g.Prepare(); err == nil {
		t.Fatal("零工具节点必须让本轮失败,不能以全绿收尾")
	}
}

// 只读是节点侧的判定;这里只做传输契约自检 —— agent 哪天漏放行一个写工具,
// core 不该替它背书。
func TestGateway_DoesNotExposeNonReadOnlyTools(t *testing.T) {
	f := &fakeFleet{tools: []agentv1.ToolDescriptor{{
		PluginId:        "daedalus.shell",
		ToolName:        "write_file",
		InputSchemaJson: `{"type":"object"}`,
		ReadOnly:        false,
	}}}
	g := newGateway(t, testItems, f, &memSink{})
	if err := g.Prepare(); err == nil {
		t.Fatal("唯一工具是写工具时应按零工具失败")
	}
	s := connect(t, g.Handler())
	listed, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("列工具失败: %v", err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "lab_a_write_file" {
			t.Fatalf("非只读工具不该暴露给模型: %+v", listed.Tools)
		}
	}
}
