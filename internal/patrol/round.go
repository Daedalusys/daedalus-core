// round.go 编排一轮巡检:清单 → 网关 → AI 客户端 → 分类 → 发信 → 状态。
//
// 只读纪律:节点侧只暴露只读工具,AI 客户端唯一的写出口是网关的 report_finding;
// 本文件不发起任何修复动作。
//
// 失败也是发现:节点不可达、AI 客户端失败或超时、清单解析失败都产出一条巡检级发现
// (SelfCheckItemID)并照常走分类与投递 —— 静默的空轮次会让人以为机器健康。
package patrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SelfCheckItemID 是巡检自身的失败项。它不是清单项:网关只收清单内 id,
	// 这一条由编排层直接追加。
	SelfCheckItemID = "patrol-self-check"

	// defaultNodeTimeout 是一轮(含 AI 客户端)的兜底时限;patrol.toml 可覆盖。
	defaultNodeTimeout = 10 * time.Minute

	findingsFileName = "findings.jsonl"
	promptFileName   = "prompt.txt"
	aiStdoutFileName = "ai-client.stdout.log"
	aiStderrFileName = "ai-client.stderr.log"
	// promptPlaceholder 让 [patrol.ai_client].command 自行决定提示词放在哪个参数上;
	// 没有占位符时提示词追加为最后一个位置参数(opencode run 的提示词就是位置参数)。
	promptPlaceholder = "{prompt}"
	roundTimeLayout   = "20060102T150405Z"
	// roundDirAttempts 是同一秒内重跑的目录后缀尝试次数(定时器 30 分钟一轮,
	// 撞秒只可能来自人工重跑)。
	roundDirAttempts = 10
	tokenBytes       = 32
	// roundDirMode / roundFileMode:轮次目录里有本轮令牌与工具返回原文,同机不可读。
	roundDirMode  = 0o700
	roundFileMode = 0o600
)

// RunConfig 是一轮巡检的全部输入。Zero 值不可用:除 NodeTimeout 外都必填。
type RunConfig struct {
	// ChecklistPath 是 patrol.toml 路径;Runner 自己读并按 NodeID 合并清单。
	ChecklistPath string
	// StatePath 是跨轮状态文件(<state 目录>/<node>.json)。
	StatePath string
	// RoundRoot 是轮次目录根,一轮一个子目录。
	RoundRoot string
	NodeID    string
	// AICommand 是 AI 客户端命令;命令里可写 {prompt} 占位符。
	AICommand []string
	// NodeTimeout 是一轮的整体时限,零值回落 defaultNodeTimeout。
	NodeTimeout time.Duration
}

// Runner 跑一轮巡检。Fleet 是节点调用面(fleet.ToolCaller 满足它),
// Mail 是投递面(mailer.Sender 满足它);两者都可以为 nil,但那样这一轮只落盘不发信。
type Runner struct {
	Config RunConfig
	Fleet  Fleet
	Mail   Sender
	// Now 是时钟注入缝;零值用 time.Now。
	Now func() time.Time

	// runAI 是 AI 客户端的执行缝;零值走真实子进程(execAI)。
	runAI aiClientRunner
}

// aiClientRun 是一次 AI 客户端执行需要的全部上下文。
type aiClientRun struct {
	// ConfigPath 是生成的客户端配置;通过 OPENCODE_CONFIG 交给客户端。
	ConfigPath string
	// Prompt 是本轮提示词。
	Prompt string
	// Dir 是轮次目录:客户端的工作目录,stdout/stderr 也落这里。
	Dir string
}

// aiClientRunner 执行 AI 客户端并等它退出。
type aiClientRunner func(ctx context.Context, spec aiClientRun) error

// roundFailure 是本轮没跑完的原因:摘要进邮件,err 决定退出码。
type roundFailure struct {
	summary string
	cause   error
}

// Run 跑一轮,返回相对上一轮的变化。error 非 nil 表示本轮没跑完(退出码据此非 0),
// 但发现、邮件与状态照常落 —— 失败本身也要送到人手里。
func (r *Runner) Run(ctx context.Context) (Changes, error) {
	if err := r.validate(); err != nil {
		return Changes{}, err
	}

	checklist, err := LoadChecklist(r.Config.ChecklistPath, r.Config.NodeID)
	if err != nil {
		dir, dirErr := r.createRoundDir()
		if dirErr != nil {
			return Changes{}, errors.Join(err, dirErr)
		}
		return r.finish(ctx, dir, []Finding{selfCheckFinding(r.Config.NodeID, "清单解析失败", err)}, err)
	}

	items := EnabledItems(checklist.Items)
	if len(items) == 0 {
		// 没有清单项就没有一轮巡检:不起网关、不拉客户端、不发信。
		return Changes{}, nil
	}

	dir, err := r.createRoundDir()
	if err != nil {
		return Changes{}, err
	}
	findings, failure := r.inspect(ctx, dir, items)
	if failure != nil {
		findings = appendSelfCheck(findings, selfCheckFinding(r.Config.NodeID, failure.summary, failure.cause))
		return r.finish(ctx, dir, findings, failure.cause)
	}
	return r.finish(ctx, dir, findings, nil)
}

// inspect 起网关、跑客户端、收本轮发现。节点不可达时不再拉客户端:对着空工具面
// 下结论只会产出无证据的噪音。
func (r *Runner) inspect(ctx context.Context, dir string, items []Item) ([]Finding, *roundFailure) {
	buf := &findingBuffer{}
	g := NewGateway(r.Config.NodeID, items, r.Fleet, buf)
	defer func() { _ = g.Close() }()

	token, err := newRoundToken()
	if err != nil {
		return nil, &roundFailure{"本轮令牌生成失败", err}
	}
	g.SetToken(token)

	if err := g.Prepare(); err != nil {
		return nil, &roundFailure{"节点不可达,本轮没有拿到工具面", err}
	}

	srv, endpoint, err := serveGateway(g)
	if err != nil {
		return nil, &roundFailure{"网关监听失败", err}
	}
	defer func() { _ = srv.Close() }()

	cfgPath, err := WriteAIClientConfig(dir, endpoint, token)
	if err != nil {
		return nil, &roundFailure{"生成 AI 客户端配置失败", err}
	}
	prompt := buildPrompt(r.Config.NodeID, items)
	if err := os.WriteFile(filepath.Join(dir, promptFileName), []byte(prompt), roundFileMode); err != nil {
		return nil, &roundFailure{"写本轮提示词失败", err}
	}

	runCtx, cancel := context.WithTimeout(ctx, r.nodeTimeout())
	defer cancel()
	spec := aiClientRun{ConfigPath: cfgPath, Prompt: prompt, Dir: dir}
	runErr := r.aiRunner()(runCtx, spec)

	// 先封网关再停监听:客户端已退出,这里只为消掉迟到的请求 —— 快照必须在
	// 不可能再有 Append 之后取,否则一轮发现会漏掉或与快照竞争。
	_ = g.Close()
	_ = srv.Close()
	if runErr != nil {
		return buf.snapshot(), &roundFailure{"AI 客户端失败", runErr}
	}
	return buf.snapshot(), nil
}

// finish 是本轮的唯一收尾:落发现 → 读回 → 分类 → 发信 → 写状态。
// 投递失败不写状态:下一轮拿旧状态再算一次,邮件才有重发机会。
func (r *Runner) finish(ctx context.Context, dir string, findings []Finding, roundErr error) (Changes, error) {
	path := filepath.Join(dir, findingsFileName)
	if err := WriteFindings(path, findings); err != nil {
		return Changes{}, err
	}
	stored, err := ReadFindings(path)
	if err != nil {
		return Changes{}, err
	}
	prev, err := LoadState(r.Config.StatePath)
	if err != nil {
		return Changes{}, err
	}
	changes := Classify(prev, stored)
	if err := r.notify(ctx, changes); err != nil {
		return changes, err
	}
	if err := SaveState(r.Config.StatePath, ActiveFindings(stored)); err != nil {
		return changes, err
	}
	return changes, roundErr
}

// notify 走统一的 Notify:空变化不发信,那条判断只该有一处。
func (r *Runner) notify(ctx context.Context, changes Changes) error {
	if r.Mail == nil {
		return nil
	}
	if err := Notify(changes, func(subject, body string) error {
		return r.Mail.Send(ctx, subject, body)
	}); err != nil {
		return fmt.Errorf("发送巡检邮件失败: %w", err)
	}
	return nil
}

// serveGateway 在回环上开一个随机端口,由本进程持有监听(网关不做任何进程的父进程)。
func serveGateway(g *Gateway) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("监听 127.0.0.1 失败: %w", err)
	}
	srv := &http.Server{
		Handler:           g.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	// 监听地址是 127.0.0.1:<port>;令牌走 Authorization 头,不拼进 URL(会进日志)。
	return srv, "http://" + ln.Addr().String() + "/mcp", nil
}

// aiRunner 返回本轮使用的执行器:测试注入优先,否则走真实子进程。
func (r *Runner) aiRunner() aiClientRunner {
	if r.runAI != nil {
		return r.runAI
	}
	return r.execAI
}

// execAI 拉起 AI 客户端:不经 shell,stdout/stderr 落轮次目录便于事后核对。
func (r *Runner) execAI(ctx context.Context, spec aiClientRun) error {
	argv := expandPrompt(r.Config.AICommand, spec.Prompt)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = mergeEnv(os.Environ(), "OPENCODE_CONFIG", spec.ConfigPath)

	stdout, err := os.OpenFile(filepath.Join(spec.Dir, aiStdoutFileName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, roundFileMode)
	if err != nil {
		return fmt.Errorf("创建 AI 客户端 stdout 日志失败: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(filepath.Join(spec.Dir, aiStderrFileName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, roundFileMode)
	if err != nil {
		return fmt.Errorf("创建 AI 客户端 stderr 日志失败: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("AI 客户端 %s 运行失败: %w", argv[0], err)
	}
	return nil
}

// expandPrompt 把提示词交给客户端:命令里写了 {prompt} 就替换,否则追加为最后一个参数。
func expandPrompt(argv []string, prompt string) []string {
	out := make([]string, len(argv))
	found := false
	for i, a := range argv {
		if strings.Contains(a, promptPlaceholder) {
			out[i] = strings.ReplaceAll(a, promptPlaceholder, prompt)
			found = true
			continue
		}
		out[i] = a
	}
	if !found {
		out = append(out, prompt)
	}
	return out
}

// mergeEnv 覆盖一个环境变量。直接 append 会留下两条同名项,exec 传下去后
// libc 取第一条,新值反而看不见。
func mergeEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

func newRoundToken() (string, error) {
	var raw [tokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("读取随机源失败: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// buildPrompt 把清单项拼成提示词。tools 是提示级建议,不是强制白名单 ——
// 强制「某一项只能用某几个工具」需要额外协议,本轮不做。
func buildPrompt(nodeID string, items []Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "对节点 %q 做一轮只读巡检。只观察、只报告,不要执行任何修复动作。\n\n", nodeID)
	fmt.Fprintf(&b, "可用工具:名字以 %s_ 开头的节点工具(只读),以及 report_finding(交回发现)。\n", nodeToolPrefix(nodeID))
	b.WriteString("每条清单项必须且只能调用一次 report_finding:status 用 problem 表示有问题、ok 表示看过没问题。\n")
	b.WriteString("problem 必须带 evidence:tool 填你调用的工具名,output 逐字复制本轮工具返回的一段原文。\n\n")
	for i, it := range items {
		fmt.Fprintf(&b, "%d. item_id = %s(基线严重级 %s", i+1, it.ID, it.Severity)
		if len(it.Tools) > 0 {
			fmt.Fprintf(&b, ",建议工具:%s", strings.Join(it.Tools, ", "))
		}
		fmt.Fprintf(&b, ")\n   %s\n", it.Prompt)
	}
	return b.String()
}

// selfCheckFinding 把巡检自身的失败变成一条发现。证据是对端错误的原文
// (巡检自己的事实,不是模型的转述),因此不过网关那道「工具返回子串」校验。
func selfCheckFinding(nodeID, summary string, cause error) Finding {
	return Finding{
		Node:     nodeID,
		ItemID:   SelfCheckItemID,
		Status:   StatusProblem,
		Severity: SeverityWarning,
		Summary:  summary + ": " + cause.Error(),
		Evidence: []Evidence{{Tool: "patrol", Output: cause.Error()}},
	}
}

// appendSelfCheck 保证巡检级发现唯一:清单里若恰好有同名项(清单规则允许这个 id),
// 巡检自身的失败优先 —— 同一项一轮只能有一条发现,否则整轮落盘失败。
func appendSelfCheck(findings []Finding, self Finding) []Finding {
	out := make([]Finding, 0, len(findings)+1)
	for _, f := range findings {
		if f.ItemID != self.ItemID {
			out = append(out, f)
		}
	}
	return append(out, self)
}

// findingBuffer 是网关的发现收集器:网关保证 Append 串行,一轮结束后一次性写盘。
type findingBuffer struct {
	findings []Finding
}

func (b *findingBuffer) Append(f Finding) error {
	b.findings = append(b.findings, f)
	return nil
}

func (b *findingBuffer) snapshot() []Finding {
	return append([]Finding(nil), b.findings...)
}

func (r *Runner) validate() error {
	if !ValidNodeID(r.Config.NodeID) {
		return fmt.Errorf("巡检节点 id %q 非法(只允许字母、数字与 . _ -)", r.Config.NodeID)
	}
	for _, side := range []struct {
		name  string
		value string
	}{
		{"清单文件路径", r.Config.ChecklistPath},
		{"状态文件路径", r.Config.StatePath},
		{"轮次目录根", r.Config.RoundRoot},
	} {
		if strings.TrimSpace(side.value) == "" {
			return fmt.Errorf("巡检配置缺少%s", side.name)
		}
	}
	if len(r.Config.AICommand) == 0 {
		return errors.New("巡检配置缺少 [patrol.ai_client].command")
	}
	return nil
}

// createRoundDir 建本轮目录 <RoundRoot>/<node>-<ts>;同一秒重跑时加序号后缀。
func (r *Runner) createRoundDir() (string, error) {
	if err := os.MkdirAll(r.Config.RoundRoot, roundDirMode); err != nil {
		return "", fmt.Errorf("创建轮次目录根 %s 失败: %w", r.Config.RoundRoot, err)
	}
	base := filepath.Join(r.Config.RoundRoot, r.Config.NodeID+"-"+r.now().UTC().Format(roundTimeLayout))
	for i := 0; i < roundDirAttempts; i++ {
		dir := base
		if i > 0 {
			dir = fmt.Sprintf("%s-%d", base, i+1)
		}
		err := os.Mkdir(dir, roundDirMode)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("创建轮次目录 %s 失败: %w", dir, err)
		}
	}
	return "", fmt.Errorf("轮次目录 %s 已存在且加序号仍冲突", base)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) nodeTimeout() time.Duration {
	if r.Config.NodeTimeout <= 0 {
		return defaultNodeTimeout
	}
	return r.Config.NodeTimeout
}
