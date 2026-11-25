package patrol

// 本文件钉住一轮巡检的编排语义:变化才发信、失败也是发现、空清单什么都不做,
// 以及生成的客户端配置真的把「除本轮网关外全 deny」表达出来了。
// 假 AI 客户端走真实 MCP 连接(从轮次目录里读配置),配置生成、令牌与网关三者
// 一起被钉住,而不是绕过装配直接调内部函数。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
)

const roundTestNode = "lab-a"

// roundFleet 是节点调用面的测试替身:固定工具表、固定返回原文。
type roundFleet struct {
	tools   []agentv1.ToolDescriptor
	listErr error
	output  string

	mu    sync.Mutex
	calls int
}

func (f *roundFleet) ListTools(context.Context, string) ([]agentv1.ToolDescriptor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *roundFleet) CallTool(context.Context, string, string, string, string) (*agentv1.CallToolResponse, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	blocks, err := json.Marshal([]map[string]string{{"type": "text", "text": f.output}})
	if err != nil {
		return nil, err
	}
	return &agentv1.CallToolResponse{ContentJson: string(blocks)}, nil
}

func serviceTool() agentv1.ToolDescriptor {
	return agentv1.ToolDescriptor{
		PluginId:        "daedalus.service",
		ToolName:        "service_list",
		Description:     "列出 systemd 单元状态",
		InputSchemaJson: `{"type":"object"}`,
		ReadOnly:        true,
	}
}

// mailSpy 记录投递;err 用来模拟 SMTP 故障。
type mailSpy struct {
	mu       sync.Mutex
	subjects []string
	bodies   []string
	err      error
}

func (m *mailSpy) Send(_ context.Context, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.subjects = append(m.subjects, subject)
	m.bodies = append(m.bodies, body)
	return nil
}

func (m *mailSpy) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subjects)
}

func (m *mailSpy) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		return ""
	}
	return m.bodies[len(m.bodies)-1]
}

// aiReport 是要交给网关的一条发现;evidence 为真时先从节点工具取一段原文。
type aiReport struct {
	itemID   string
	status   string
	severity string
	summary  string
	evidence bool
}

// bearerTransport 让测试客户端按配置里的 Authorization 头访问网关 ——
// 与生产客户端读同一份配置、走同一条令牌中间件。
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// newFakeAIClient 用真实 MCP 客户端跑一轮:配置从轮次目录读,工具名与放行规则
// 都在这里被核对,任何一处漂移都会让本轮测试失败。
func newFakeAIClient(t *testing.T, nodeID string, reports []aiReport) aiClientRunner {
	t.Helper()
	return func(ctx context.Context, spec aiClientRun) error {
		cfg, err := readAIClientConfig(spec.ConfigPath)
		if err != nil {
			t.Fatalf("读本轮客户端配置失败: %v", err)
		}
		server, ok := cfg.MCP.Servers[aiServerName]
		if !ok {
			t.Fatalf("客户端配置里没有 MCP server %q", aiServerName)
		}
		token, ok := strings.CutPrefix(server.Headers["Authorization"], "Bearer ")
		if !ok || token == "" {
			t.Fatalf("MCP server 没带本轮令牌: %+v", server.Headers)
		}

		session, err := mcp.NewClient(&mcp.Implementation{Name: "patrol-round-test", Version: "0"}, nil).
			Connect(ctx, &mcp.StreamableClientTransport{
				Endpoint:             server.URL,
				HTTPClient:           &http.Client{Transport: bearerTransport{token: token}},
				DisableStandaloneSSE: true,
				MaxRetries:           -1,
			}, nil)
		if err != nil {
			t.Fatalf("连接本轮网关失败: %v", err)
		}
		defer func() { _ = session.Close() }()

		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("列工具失败: %v", err)
		}
		allow := cfg.Permissions[len(cfg.Permissions)-1]
		for _, tool := range listed.Tools {
			if !ruleMatches(allow.Action, ActionName(aiServerName, tool.Name)) {
				t.Errorf("工具 %q 的动作名 %q 不在放行规则 %q 里", tool.Name, ActionName(aiServerName, tool.Name), allow.Action)
			}
		}
		if got := ActionName(aiServerName, reportFindingTool); got != "patrol-node_report_finding" {
			t.Errorf("report_finding 的动作名 = %q,期望 patrol-node_report_finding", got)
		}

		nodeTool := nodeToolPrefix(nodeID) + "_service_list"
		for _, rep := range reports {
			args := map[string]any{
				"item_id":  rep.itemID,
				"status":   rep.status,
				"severity": rep.severity,
				"summary":  rep.summary,
			}
			if rep.evidence {
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: nodeTool})
				if err != nil {
					t.Fatalf("调用节点工具 %s 失败: %v", nodeTool, err)
				}
				text := textOf(t, res)
				args["evidence"] = []map[string]any{{"tool": nodeTool, "output": text}}
			}
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: reportFindingTool, Arguments: args})
			if err != nil || res.IsError {
				t.Fatalf("report_finding(%s) 失败: err=%v result=%s", rep.itemID, err, textOf(t, res))
			}
		}
		return nil
	}
}

// textOf 取 MCP 结果里的文本内容。
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

// readAIClientConfig 解析生成的客户端配置。
type parsedAIClientConfig struct {
	Permissions []struct {
		Action   string `json:"action"`
		Resource string `json:"resource"`
		Effect   string `json:"effect"`
	} `json:"permissions"`
	MCP struct {
		Servers map[string]struct {
			Type     string            `json:"type"`
			URL      string            `json:"url"`
			OAuth    bool              `json:"oauth"`
			CodeMode bool              `json:"codemode"`
			Headers  map[string]string `json:"headers"`
		} `json:"servers"`
	} `json:"mcp"`
	Model      string          `json:"model"`
	SmallModel string          `json:"small_model"`
	Provider   json.RawMessage `json:"provider"`
}

func readAIClientConfig(path string) (parsedAIClientConfig, error) {
	var cfg parsedAIClientConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ruleMatches 只支持一个 * 通配:测试只用来钉 patrol-node_* 这类前缀规则。
func ruleMatches(pattern, action string) bool {
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return pattern == action
	}
	return strings.HasPrefix(action, pattern[:i]) && strings.HasSuffix(action, pattern[i+1:])
}

// writeChecklist 落一份清单夹具并返回路径。
func writeChecklist(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "patrol.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写清单夹具失败: %v", err)
	}
	return path
}

const twoItemChecklist = `
[patrol]
node_timeout = "10m"

[[checklist.item]]
id = "disk-health"
prompt = "磁盘空间、inode 与挂载点有哪些健康问题迹象?"
severity = "warning"

[[checklist.item]]
id = "log-health"
prompt = "系统日志里有没有值得报的异常?"
severity = "info"
`

// roundDir 找出本轮目录(只有一个轮次时)。
func roundDir(t *testing.T, root string) string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, roundTestNode+"-*"))
	if err != nil {
		t.Fatalf("找轮次目录失败: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("期望 1 个轮次目录,实际 %v", dirs)
	}
	return dirs[0]
}

func TestRunner_RoundClassifiesAndSendsOnlyChanges(t *testing.T) {
	// 屏蔽宿主全局客户端配置:这条用例只钉「无全局配置」分支。
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	mail := &mailSpy{}
	fleet := &roundFleet{tools: []agentv1.ToolDescriptor{serviceTool()}, output: "nginx.service failed\n"}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, twoItemChecklist),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			AICommand:     []string{"opencode", "run"},
			NodeTimeout:   30 * time.Second,
		},
		Fleet: fleet,
		Mail:  mail,
		Now:   func() time.Time { return now },
	}
	r.runAI = newFakeAIClient(t, roundTestNode, []aiReport{
		{itemID: "disk-health", status: StatusProblem, severity: SeverityWarning, summary: "根分区余量 3%", evidence: true},
		{itemID: "log-health", status: StatusOK, severity: SeverityInfo, summary: "日志无异常"},
	})

	changes, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("第一轮不该失败: %v", err)
	}
	if len(changes.Added) != 1 || changes.Added[0].ItemID != "disk-health" {
		t.Fatalf("第一轮的新增不对: %+v", changes)
	}
	if got := mail.count(); got != 1 {
		t.Fatalf("第一轮应发 1 封邮件,实际 %d", got)
	}
	if body := mail.lastBody(); !strings.Contains(body, "nginx.service failed") {
		t.Errorf("证据原文必须进邮件:\n%s", body)
	}

	// 第二轮:同样的发现 → 持续项,不再发信。
	r.Now = func() time.Time { return now.Add(30 * time.Minute) }
	changes, err = r.Run(context.Background())
	if err != nil {
		t.Fatalf("第二轮不该失败: %v", err)
	}
	if n := len(changes.Added) + len(changes.Worsened) + len(changes.Recovered); n != 0 {
		t.Fatalf("第二轮不该有变化: %+v", changes)
	}
	if len(changes.Ongoing) != 1 {
		t.Fatalf("第二轮应把同一发现归为持续: %+v", changes)
	}
	if got := mail.count(); got != 1 {
		t.Fatalf("同一发现第二轮不该重复发信,实际累计 %d 封", got)
	}

	// 状态文件只留活跃项:ok 项不进去,下一轮再出问题才算新增。
	st, err := LoadState(r.Config.StatePath)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if _, ok := st["disk-health"]; !ok || len(st) != 1 {
		t.Fatalf("状态应只含 disk-health: %+v", st)
	}
}

func TestRunner_NodeUnreachableBecomesFinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, twoItemChecklist),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			AICommand:     []string{"opencode", "run"},
		},
		Fleet: &roundFleet{listErr: status.Error(codes.Unavailable, "fleet: 节点 lab-a 未注册")},
		Mail:  mail,
	}
	// 节点不可达时不该再拉起 AI 客户端:朝空工具面下结论只会产出无证据的噪音。
	r.runAI = func(context.Context, aiClientRun) error {
		t.Error("节点不可达时不该拉起 AI 客户端")
		return nil
	}

	changes, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("节点不可达必须让本轮失败(退出码非 0)")
	}
	if len(changes.Added) != 1 || changes.Added[0].ItemID != SelfCheckItemID {
		t.Fatalf("巡检级发现没产出: %+v", changes)
	}
	if got := mail.count(); got != 1 {
		t.Fatalf("巡检失败也必须进邮件,实际 %d 封", got)
	}
	body := mail.lastBody()
	if !strings.Contains(body, SelfCheckItemID) || !strings.Contains(body, "未注册") {
		t.Errorf("邮件必须说清是哪一步失败:\n%s", body)
	}

	// 发现照常落盘:证据可核对。
	findings, err := ReadFindings(filepath.Join(roundDir(t, r.Config.RoundRoot), findingsFileName))
	if err != nil {
		t.Fatalf("读发现文件失败: %v", err)
	}
	if len(findings) != 1 || findings[0].ItemID != SelfCheckItemID {
		t.Fatalf("落盘的发现不对: %+v", findings)
	}

	// 第二轮仍然不可达 → 持续项,不再重复打扰。
	r.Now = func() time.Time { return time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC) }
	changes, err = r.Run(context.Background())
	if err == nil {
		t.Fatal("第二轮仍然不可达,必须继续报失败")
	}
	if len(changes.Ongoing) != 1 {
		t.Fatalf("同一失败第二轮应归为持续: %+v", changes)
	}
	if got := mail.count(); got != 1 {
		t.Fatalf("持续失败不该重复发信,实际累计 %d 封", got)
	}
}

func TestRunner_EmptyChecklistDoesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, "[patrol]\nnode_timeout = \"10m\"\n"),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			AICommand:     []string{"opencode", "run"},
		},
		Mail: mail,
	}
	r.runAI = func(context.Context, aiClientRun) error {
		t.Error("清单为空时不该拉起 AI 客户端")
		return nil
	}

	changes, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("空清单不是错误: %v", err)
	}
	if len(changes.Added)+len(changes.Worsened)+len(changes.Recovered)+len(changes.Ongoing) != 0 {
		t.Fatalf("空清单不该产出变化: %+v", changes)
	}
	if mail.count() != 0 {
		t.Fatalf("空清单不该发信,实际 %d 封", mail.count())
	}
	// 没有清单项就没有这一轮:连轮次目录都不建。
	if _, err := os.Stat(r.Config.RoundRoot); !os.IsNotExist(err) {
		t.Fatalf("空清单不该创建轮次目录,stat err=%v", err)
	}
}

func TestRunner_ChecklistParseFailureBecomesFinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			// 未知键:清单解析 fail-closed。
			ChecklistPath: writeChecklist(t, "[patrol]\nbogus_key = 1\n"),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			AICommand:     []string{"opencode", "run"},
		},
		Mail: mail,
	}
	r.runAI = func(context.Context, aiClientRun) error {
		t.Error("清单解析失败时不该拉起 AI 客户端")
		return nil
	}

	changes, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("清单解析失败必须让本轮失败")
	}
	if len(changes.Added) != 1 || changes.Added[0].ItemID != SelfCheckItemID {
		t.Fatalf("巡检级发现没产出: %+v", changes)
	}
	if !strings.Contains(mail.lastBody(), "清单") {
		t.Errorf("邮件必须点明清单解析失败:\n%s", mail.lastBody())
	}
}

func TestRunner_AIClientFailureBecomesFinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, twoItemChecklist),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			// 真实子进程路径:/bin/false 以非零退出。
			AICommand:   []string{"/bin/false"},
			NodeTimeout: 30 * time.Second,
		},
		Fleet: &roundFleet{tools: []agentv1.ToolDescriptor{serviceTool()}, output: "ok\n"},
		Mail:  mail,
	}

	changes, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("AI 客户端失败必须让本轮失败")
	}
	if len(changes.Added) != 1 || changes.Added[0].ItemID != SelfCheckItemID {
		t.Fatalf("巡检级发现没产出: %+v", changes)
	}
	if body := mail.lastBody(); !strings.Contains(body, "AI 客户端失败") {
		t.Errorf("邮件必须点明 AI 客户端失败:\n%s", body)
	}
	// 客户端日志照常落盘,方便事后核对它到底说了什么。
	if _, err := os.Stat(filepath.Join(roundDir(t, r.Config.RoundRoot), aiStderrFileName)); err != nil {
		t.Errorf("AI 客户端 stderr 日志缺失: %v", err)
	}
}

func TestRunner_AIClientTimeoutBecomesFinding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, twoItemChecklist),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			AICommand:     []string{"/bin/sleep", "10"},
			NodeTimeout:   120 * time.Millisecond,
		},
		Fleet: &roundFleet{tools: []agentv1.ToolDescriptor{serviceTool()}, output: "ok\n"},
		Mail:  mail,
	}

	start := time.Now()
	changes, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("AI 客户端超时必须让本轮失败")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("超时没有被执行时限截断: 耗时 %s", elapsed)
	}
	if len(changes.Added) != 1 || changes.Added[0].ItemID != SelfCheckItemID {
		t.Fatalf("巡检级发现没产出: %+v", changes)
	}
	if !strings.Contains(mail.lastBody(), "AI 客户端失败") {
		t.Errorf("邮件必须点明 AI 客户端失败:\n%s", mail.lastBody())
	}
}

func TestRunner_ExecPassesPromptAndWritesRoundDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	mail := &mailSpy{}
	r := &Runner{
		Config: RunConfig{
			ChecklistPath: writeChecklist(t, twoItemChecklist),
			StatePath:     filepath.Join(root, "state", roundTestNode+".json"),
			RoundRoot:     filepath.Join(root, "round"),
			NodeID:        roundTestNode,
			// 占位符替换:提示词作为参数交给客户端,不走 shell。
			AICommand:   []string{"/bin/echo", "start", "{prompt}"},
			NodeTimeout: 10 * time.Second,
		},
		Fleet: &roundFleet{tools: []agentv1.ToolDescriptor{serviceTool()}, output: "ok\n"},
		Mail:  mail,
	}

	changes, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("echo 正常退出的一轮不该失败: %v", err)
	}
	if len(changes.Added)+len(changes.Worsened)+len(changes.Recovered)+len(changes.Ongoing) != 0 {
		t.Fatalf("没报发现的一轮不该有变化: %+v", changes)
	}

	dir := roundDir(t, r.Config.RoundRoot)
	stdout, err := os.ReadFile(filepath.Join(dir, aiStdoutFileName))
	if err != nil {
		t.Fatalf("读客户端 stdout 失败: %v", err)
	}
	for _, want := range []string{"start", "item_id = disk-health", "item_id = log-health"} {
		if !strings.Contains(string(stdout), want) {
			t.Errorf("客户端参数里缺少 %q:\n%s", want, stdout)
		}
	}
	for _, name := range []string{promptFileName, aiConfigName, findingsFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("轮次目录缺少 %s: %v", name, err)
		}
	}
	// 配置里有本轮令牌:必须只有属主可读。
	info, err := os.Stat(filepath.Join(dir, aiConfigName))
	if err != nil {
		t.Fatalf("stat 客户端配置失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != aiConfigMode {
		t.Errorf("客户端配置权限 = %o,期望 %o", perm, aiConfigMode)
	}
}

func TestActionName_RuleMatchesEveryTool(t *testing.T) {
	cases := []struct{ server, tool, want string }{
		{"patrol-node", "report_finding", "patrol-node_report_finding"},
		{"patrol-node", "lab_a_service_list", "patrol-node_lab_a_service_list"},
		{"patrol-node", "lab_a_service.query", "patrol-node_lab_a_service_query"},
		{"patrol_node", "x-y", "patrol_node_x-y"},
		{"node.with/slash", "z", "node_with_slash_z"},
	}
	for _, c := range cases {
		if got := ActionName(c.server, c.tool); got != c.want {
			t.Errorf("ActionName(%q,%q) = %q,期望 %q", c.server, c.tool, got, c.want)
		}
	}

	// 网关可能暴露的工具名形态:report_finding、节点前缀、插件消歧、序号后缀。
	tools := []string{
		"report_finding",
		"lab_a_service_list",
		"lab_a_daedalus_service_service_query",
		"lab_a_shell_exec_2",
	}
	for _, tool := range tools {
		if !ruleMatches(allowRule(), ActionName(aiServerName, tool)) {
			t.Errorf("放行规则 %q 命中不了工具 %q 的动作名 %q", allowRule(), tool, ActionName(aiServerName, tool))
		}
	}
	// 下划线形态一条也命中不了:这是「规则必须保留 -」的回归钉子。
	if ruleMatches("patrol_node_*", ActionName(aiServerName, "report_finding")) {
		t.Errorf("patrol_node_* 不该命中 patrol-node_report_finding")
	}
}

func TestWriteAIClientConfig_DenyAllThenAllowGateway(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	path, err := WriteAIClientConfig(dir, "http://127.0.0.1:4242/mcp", "round-token")
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("配置没落在轮次目录: %s", path)
	}
	cfg, err := readAIClientConfig(path)
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	if len(cfg.Permissions) != 2 {
		t.Fatalf("期望两条规则(全 deny + 放行本轮),实际 %+v", cfg.Permissions)
	}
	if first := cfg.Permissions[0]; first.Action != "*" || first.Resource != "*" || first.Effect != "deny" {
		t.Errorf("第一条必须是全局 deny: %+v", first)
	}
	if second := cfg.Permissions[1]; second.Action != "patrol-node_*" || second.Effect != "allow" {
		t.Errorf("第二条必须放行本轮网关: %+v", second)
	}
	server := cfg.MCP.Servers[aiServerName]
	if server.Type != "remote" || server.URL != "http://127.0.0.1:4242/mcp" || server.OAuth || server.CodeMode {
		t.Errorf("MCP server 条目不对: %+v", server)
	}
	if got := server.Headers["Authorization"]; got != "Bearer round-token" {
		t.Errorf("Authorization = %q", got)
	}
	// 没有全局配置时不该凭空写模型字段:模型由命令行 --model 或全局配置决定。
	if cfg.Model != "" || cfg.SmallModel != "" || len(cfg.Provider) != 0 {
		t.Errorf("无全局配置时不该有模型字段: %+v", cfg)
	}
}

func TestWriteAIClientConfig_InheritsGlobalModel(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	globalDir := filepath.Join(base, "opencode")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("建全局配置目录失败: %v", err)
	}
	global := `{"model":"aigw-main/code-deep","small_model":"aigw-sub/code-fast","provider":{"aigw-main":{"apiKey":"from-env"}}}`
	if err := os.WriteFile(filepath.Join(globalDir, "opencode.json"), []byte(global), 0o600); err != nil {
		t.Fatalf("写全局配置失败: %v", err)
	}

	path, err := WriteAIClientConfig(t.TempDir(), "http://127.0.0.1:1/mcp", "tok")
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	cfg, err := readAIClientConfig(path)
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	if cfg.Model != "aigw-main/code-deep" || cfg.SmallModel != "aigw-sub/code-fast" {
		t.Errorf("模型字段没继承: %+v", cfg)
	}
	if len(cfg.Provider) == 0 || !strings.Contains(string(cfg.Provider), "aigw-main") {
		t.Errorf("provider 没继承: %s", cfg.Provider)
	}
}

func TestWriteAIClientConfig_RejectsBrokenGlobalConfig(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	globalDir := filepath.Join(base, "opencode")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("建全局配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "opencode.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("写全局配置失败: %v", err)
	}

	if _, err := WriteAIClientConfig(t.TempDir(), "http://127.0.0.1:1/mcp", "tok"); err == nil {
		t.Fatal("全局配置损坏时必须失败,不能静默生成一份没有模型的配置")
	}
}

func TestMergeEnv_OverridesDuplicate(t *testing.T) {
	got := mergeEnv([]string{"A=1", "OPENCODE_CONFIG=/old", "B=2"}, "OPENCODE_CONFIG", "/new")
	var hits []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			hits = append(hits, kv)
		}
	}
	if len(hits) != 1 || hits[0] != "OPENCODE_CONFIG=/new" {
		t.Fatalf("同名变量必须被覆盖成一条: %v", got)
	}
	if !strings.Contains(strings.Join(got, ","), "A=1") || !strings.Contains(strings.Join(got, ","), "B=2") {
		t.Fatalf("其余变量不能丢: %v", got)
	}
}

func TestBuildPrompt_CarriesItemsAndToolPrefix(t *testing.T) {
	got := buildPrompt("lab-a", []Item{
		{ID: "disk-health", Prompt: "磁盘健康吗?", Severity: SeverityWarning, Tools: []string{"service_list"}},
		{ID: "log-health", Prompt: "日志有异常吗?", Severity: SeverityInfo},
	})
	for _, want := range []string{"lab-a", "lab_a_", "item_id = disk-health", "磁盘健康吗?", "建议工具:service_list", "item_id = log-health"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示词缺少 %q:\n%s", want, got)
		}
	}
}

func TestRunner_RejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  RunConfig
	}{
		{"节点 id 非法", RunConfig{ChecklistPath: "x", StatePath: "y", RoundRoot: "z", NodeID: "../etc", AICommand: []string{"a"}}},
		{"缺清单路径", RunConfig{StatePath: "y", RoundRoot: "z", NodeID: "lab-a", AICommand: []string{"a"}}},
		{"缺状态路径", RunConfig{ChecklistPath: "x", RoundRoot: "z", NodeID: "lab-a", AICommand: []string{"a"}}},
		{"缺轮次目录", RunConfig{ChecklistPath: "x", StatePath: "y", NodeID: "lab-a", AICommand: []string{"a"}}},
		{"缺客户端命令", RunConfig{ChecklistPath: "x", StatePath: "y", RoundRoot: "z", NodeID: "lab-a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Runner{Config: c.cfg}
			if _, err := r.Run(context.Background()); err == nil {
				t.Fatalf("%s 时必须报错", c.name)
			}
		})
	}
}

func TestSelfCheckFinding_CarriesErrorText(t *testing.T) {
	f := selfCheckFinding("lab-a", "节点不可达", fmt.Errorf("fleet: 节点 lab-a 未注册"))
	if f.ItemID != SelfCheckItemID || f.Status != StatusProblem {
		t.Fatalf("巡检级发现的形状不对: %+v", f)
	}
	if len(f.Evidence) != 1 || !strings.Contains(f.Evidence[0].Output, "未注册") {
		t.Fatalf("证据必须是错误原文: %+v", f.Evidence)
	}
	// 清单里若恰好有同名项,巡检自身的失败优先,避免同一轮两条同 id 落盘失败。
	got := appendSelfCheck([]Finding{{ItemID: SelfCheckItemID, Node: "lab-a"}, {ItemID: "disk-health"}}, f)
	if len(got) != 2 || got[len(got)-1].Summary != f.Summary {
		t.Fatalf("追加巡检级发现后形状不对: %+v", got)
	}
}
