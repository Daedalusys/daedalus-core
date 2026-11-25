// round.go 编排一轮巡检:清单 → 网关 → AI 客户端 → 分类 → 发信 → 状态。
//
// 只读纪律:节点侧只暴露只读工具,AI 客户端唯一的写出口是网关的 report_finding。
// 失败也是发现:节点不可达、客户端失败或超时、清单解析失败、成功退出却没交齐清单项,
// 都产出一条巡检级发现(SelfCheckItemID)并照常投递 —— 静默的空轮次会让人以为机器健康。
package patrol

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SelfCheckItemID 是巡检自身的失败项。它不是清单项:网关只收清单内 id,
	// 这一条由编排层直接追加。
	SelfCheckItemID = "patrol-self-check"

	// defaultNodeTimeout 是 AI 客户端的兜底执行时限;patrol.toml 可覆盖。
	defaultNodeTimeout = 10 * time.Minute
	// shutdownTimeout 是收摊时等 handler 结束的时限(客户端已退出,正常毫秒级)。
	shutdownTimeout = 10 * time.Second

	findingsFileName = "findings.jsonl"
	promptFileName   = "prompt.txt"
	aiStdoutFileName = "ai-client.stdout.log"
	aiStderrFileName = "ai-client.stderr.log"
	// stateErrorFileName 记录本轮遇到的状态文件损坏与备份路径:轮次目录是这一轮
	// 唯一的事后现场,坏状态被重置的原因必须能在这里查到。
	stateErrorFileName = "state-error.txt"
	// promptPlaceholder 让 [patrol.ai_client].command 自行决定提示词放在哪个参数上;
	// 没有占位符时提示词追加为最后一个位置参数(opencode run 的提示词就是位置参数)。
	promptPlaceholder = "{prompt}"
	roundTimeLayout   = "20060102T150405Z"
	roundDirAttempts  = 10
	tokenBytes        = 32

	// 轮次目录里有本轮令牌与工具返回原文,同机不可读。
	roundDirMode  = 0o700
	roundFileMode = 0o600
)

// errIncompleteRound 标记「客户端正常退出,但本轮发现没覆盖全部启用项」。
var errIncompleteRound = errors.New("本轮发现没有覆盖全部启用的清单项")

// errNoToolCalls 标记「客户端正常退出,但一次节点工具都没调」:没有观测就没有结论。
var errNoToolCalls = errors.New("本轮没有成功调用任何节点工具")

// roundKeep 是每节点保留的轮次目录数(30 分钟一轮约等于 25 小时)。轮次目录里
// 有生成的客户端配置(可能内联 provider 凭据),不设上限会让密钥无限期留存。
const roundKeep = 50

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
	// NodeTimeout 只约束 AI 客户端的执行时长,零值回落 defaultNodeTimeout;
	// 网关起停与投递不在其内(前者在本机,后者有 SMTP 连接时限)。
	NodeTimeout time.Duration
}

// Runner 跑一轮巡检。Fleet 是节点调用面(fleet.ToolCaller 满足它),
// Mail 是投递面(mailer.Sender 满足它);两者都可以为 nil,那样这一轮只落盘不发信。
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

// Run 跑一轮,返回相对上一轮的变化。error 非 nil 表示本轮没跑完,或有需要人处理的
// 异常(例如状态文件损坏,已备份并重置),但发现、邮件与状态照常落 —— 失败本身也要
// 送到人手里。
func (r *Runner) Run(ctx context.Context) (Changes, error) {
	if err := r.validate(); err != nil {
		return Changes{}, err
	}

	checklist, err := LoadChecklist(r.Config.ChecklistPath, r.Config.NodeID)
	if err != nil {
		return r.roundFailed(ctx, "清单解析失败", err)
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
	findings, toolCalls, failure := r.inspect(ctx, dir, items)
	if failure == nil {
		// 成功轮也要核对覆盖:少报一项会被四分类当成「恢复」,那是假恢复。
		// 缺项与「一次都没调」可以同时成立,两条都要说 —— 只报缺项会把
		// 「本轮没有任何观测」这个更关键的事实盖掉。
		var reasons []string
		var causes []error
		if missing := missingItems(items, findings); len(missing) > 0 {
			reasons = append(reasons, fmt.Sprintf("AI 客户端没交齐全清单项(缺:%s)", strings.Join(missing, ", ")))
			causes = append(causes, errIncompleteRound)
		}
		if toolCalls == 0 {
			reasons = append(reasons, "AI 客户端一次节点工具都没成功调用,本轮没有任何观测")
			causes = append(causes, errNoToolCalls)
		}
		if len(reasons) > 0 {
			failure = &roundFailure{summary: strings.Join(reasons, "; "), cause: errors.Join(causes...)}
		}
	}
	if failure == nil {
		return r.finish(ctx, dir, findings, nil, nil)
	}
	self := selfCheckFinding(r.Config.NodeID, failure.summary, failure.cause)
	findings = appendSelfCheck(findings, self)
	return r.finish(ctx, dir, findings, &self, failure.cause)
}

// roundFailed 把巡检自身的失败收成一条发现,并走同一条收尾路径。
func (r *Runner) roundFailed(ctx context.Context, summary string, cause error) (Changes, error) {
	dir, err := r.createRoundDir()
	if err != nil {
		return Changes{}, errors.Join(cause, err)
	}
	self := selfCheckFinding(r.Config.NodeID, summary, cause)
	return r.finish(ctx, dir, []Finding{self}, &self, cause)
}

// inspect 起网关、跑客户端、收本轮发现。节点不可达时不再拉客户端:对着空工具面
// 下结论只会产出无证据的噪音。
func (r *Runner) inspect(ctx context.Context, dir string, items []Item) ([]Finding, int, *roundFailure) {
	buf := &findingBuffer{}
	g := NewGateway(r.Config.NodeID, items, r.Fleet, buf)
	defer func() { _ = g.Close() }()

	token, err := newRoundToken()
	if err != nil {
		return nil, 0, &roundFailure{"本轮令牌生成失败", err}
	}
	g.SetToken(token)

	if err := g.Prepare(); err != nil {
		summary := "节点不可达,本轮没有拿到工具面"
		if errors.Is(err, ErrNoRemoteTools) {
			summary = "节点没有可远程调用的只读工具,本轮无法观测"
		}
		return nil, 0, &roundFailure{summary, err}
	}

	srv, endpoint, serveErr, err := serveGateway(g)
	if err != nil {
		return nil, 0, &roundFailure{"网关监听失败", err}
	}
	defer func() { _ = srv.Close() }()

	cfgPath, err := WriteAIClientConfig(dir, endpoint, token)
	if err != nil {
		return nil, 0, &roundFailure{"生成 AI 客户端配置失败", err}
	}
	prompt := buildPrompt(r.Config.NodeID, items)
	if err := os.WriteFile(filepath.Join(dir, promptFileName), []byte(prompt), roundFileMode); err != nil {
		return nil, 0, &roundFailure{"写本轮提示词失败", err}
	}

	runCtx, cancel := context.WithTimeout(ctx, r.nodeTimeout())
	defer cancel()
	runErr := r.aiRunner()(runCtx, aiClientRun{ConfigPath: cfgPath, Prompt: prompt, Dir: dir})

	// 收摊顺序:等 handler 收尾 → 封网关(拒新发现)→ 取快照。客户端已退出,
	// Shutdown 正常毫秒级返回,独立时限只防迟到的长连接。
	shutCtx, shutCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	_ = srv.Shutdown(shutCtx)
	shutCancel()
	// 计数必须在 Close 之前取:Close 会把证据池(连带调用记录)清掉。
	calls := g.CallCount()
	_ = g.Close()
	serveFailure := <-serveErr

	if runErr != nil {
		return buf.snapshot(), calls, &roundFailure{"AI 客户端失败", runErr}
	}
	if serveFailure != nil && !errors.Is(serveFailure, http.ErrServerClosed) {
		return buf.snapshot(), calls, &roundFailure{"网关监听中断", fmt.Errorf("http.Serve: %w", serveFailure)}
	}
	return buf.snapshot(), calls, nil
}

// finish 是本轮的唯一收尾:落发现 → 读回 → 分类 → 发信 → 写状态。
//
// self 非 nil 表示失败轮:本轮没有观测,不能把「本轮没出现」当成恢复,状态也不覆盖
// 上一轮的活跃项。投递失败一律不写状态 —— 下一轮重算才能重发。状态文件损坏不走失败
// 轮:备份原件后按「没有基线」照常分类,不让一次损坏把巡检永久卡死。
func (r *Runner) finish(ctx context.Context, dir string, findings []Finding, self *Finding, roundErr error) (Changes, error) {
	path := filepath.Join(dir, findingsFileName)

	prev, err := LoadState(r.Config.StatePath)
	stateReset := err != nil
	if stateReset {
		// 坏状态不能让这台机器永久停摆:原件改名留证,本轮按「没有基线」继续。
		// 代价是这一轮的活跃项全会被算成新增 —— 宁可多报一遍,也不能因为一次
		// 损坏就再不巡检;备份文件、轮次目录里的说明与退出码一起把异常留在审计里。
		backup, backupErr := quarantineState(r.Config.StatePath, r.now())
		if backupErr != nil {
			return Changes{}, errors.Join(roundErr, err, backupErr)
		}
		audit := fmt.Sprintf("%s 巡检状态损坏,已备份为 %s,本轮按无基线处理:%v\n",
			r.now().UTC().Format(time.RFC3339), backup, err)
		if writeErr := os.WriteFile(filepath.Join(dir, stateErrorFileName), []byte(audit), roundFileMode); writeErr != nil {
			// 留不下审计文件也要把这一轮巡检跑完,但不能吞掉写失败。
			err = errors.Join(err, writeErr)
		}
		prev = map[string]Finding{}
		note := "巡检状态损坏,已备份为 " + filepath.Base(backup) + ",本轮按无基线处理"
		if self == nil {
			findings = appendSelfCheck(findings, selfCheckFinding(r.Config.NodeID, note, err))
		} else {
			self.Summary += ";" + note
			findings = appendSelfCheck(findings, *self)
		}
		roundErr = errors.Join(roundErr, err)
	}

	if err := WriteFindings(path, findings); err != nil {
		return Changes{}, errors.Join(roundErr, err)
	}
	stored, err := ReadFindings(path)
	if err != nil {
		return Changes{}, errors.Join(roundErr, err)
	}

	var changes Changes
	var next map[string]Finding
	if self != nil {
		changes = failureChanges(prev, *self)
		next = failureState(prev, *self)
	} else {
		changes = Classify(prev, stored)
		next = ActiveFindings(stored)
		if stateReset {
			// 状态损坏是本轮的一次性事故,已经报过;它不是节点健康基线的一部分,
			// 不落进状态,免得下一轮平白多一封「恢复 patrol-self-check」的邮件。
			delete(next, SelfCheckItemID)
		}
	}
	if err := r.notify(ctx, changes); err != nil {
		return changes, errors.Join(roundErr, err)
	}
	if err := SaveState(r.Config.StatePath, next); err != nil {
		return changes, errors.Join(roundErr, err)
	}
	return changes, roundErr
}

// failureChanges 只保留「上一轮活跃项仍活跃」与「self-check 若是新发现」。
// 失败轮没有观测,把缺席算成恢复会让同一封邮件既报新增又报全部旧问题恢复。
func failureChanges(prev map[string]Finding, self Finding) Changes {
	var ch Changes
	for _, id := range slices.Sorted(maps.Keys(prev)) {
		ch.Ongoing = append(ch.Ongoing, prev[id])
	}
	if _, ok := prev[self.ItemID]; !ok {
		ch.Added = append(ch.Added, self)
	}
	return ch
}

// failureState 保留上一轮活跃项并合入 self-check(同名覆盖):下一轮成功时
// 旧问题仍是持续,不会又变成新增;self-check 消失才算恢复。
func failureState(prev map[string]Finding, self Finding) map[string]Finding {
	next := make(map[string]Finding, len(prev)+1)
	maps.Copy(next, prev)
	next[self.ItemID] = self
	return next
}

// missingItems 返回清单里没有任何发现覆盖的 item_id(清单顺序)。网关已拒重复,
// 所以只查缺项。
func missingItems(items []Item, findings []Finding) []string {
	seen := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		seen[f.ItemID] = struct{}{}
	}
	var missing []string
	for _, it := range items {
		if _, ok := seen[it.ID]; !ok {
			missing = append(missing, it.ID)
		}
	}
	return missing
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

// serveGateway 在回环上开随机端口;返回的 channel 收到 Serve 的结局(缓冲 1)。
func serveGateway(g *Gateway) (*http.Server, string, <-chan error, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", nil, fmt.Errorf("监听 127.0.0.1 失败: %w", err)
	}
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	// 令牌走 Authorization 头,不拼进 URL(会进日志)。
	return srv, "http://" + ln.Addr().String() + "/mcp", done, nil
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

// buildPrompt 把清单项拼成提示词。tools 是提示级建议,不是强制白名单。
func buildPrompt(nodeID string, items []Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "对节点 %q 做一轮只读巡检。只观察、只报告,不要执行任何修复动作。\n\n", nodeID)
	fmt.Fprintf(&b, "可用工具:名字以 %s_ 开头的节点工具(只读),以及 report_finding(交回发现)。\n", nodeToolPrefix(nodeID))
	b.WriteString("每条清单项必须且只能调用一次 report_finding:status 用 problem 表示有问题、ok 表示看过没问题。\n")
	fmt.Fprintf(&b, "problem 必须带 evidence:tool 填你调用的工具名(会话里看到的 %s_<名字>,或去掉 %s_ 前缀的 <名字>,两种都认),output 逐字复制本轮工具返回的一段原文。\n\n", aiServerName, aiServerName)
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
// 摘要压成一行:它要拼进邮件的 bullet,而 errors.Join 的 Error() 用换行连接各条
// cause,原样拼进去会把一条发现撕成两段。证据侧保留原文 —— findings.jsonl 是 JSON,
// 换行会被转义,不存在断行问题。
func selfCheckFinding(nodeID, summary string, cause error) Finding {
	return Finding{
		Node:     nodeID,
		ItemID:   SelfCheckItemID,
		Status:   StatusProblem,
		Severity: SeverityWarning,
		Summary:  summary + ": " + oneLine(cause.Error()),
		Evidence: []Evidence{{Tool: "patrol", Output: cause.Error()}},
	}
}

// appendSelfCheck 保证巡检级发现唯一:清单里若恰好有同名项,巡检自身的失败优先。
func appendSelfCheck(findings []Finding, self Finding) []Finding {
	out := make([]Finding, 0, len(findings)+1)
	for _, f := range findings {
		if f.ItemID != self.ItemID {
			out = append(out, f)
		}
	}
	return append(out, self)
}

// findingBuffer 是网关的发现收集器。网关保证 Append 串行,收摊时仍可能与迟到的
// handler 交错 —— 加锁让快照与 Append 不可能并发。
type findingBuffer struct {
	mu       sync.Mutex
	findings []Finding
}

func (b *findingBuffer) Append(f Finding) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.findings = append(b.findings, f)
	return nil
}

func (b *findingBuffer) snapshot() []Finding {
	b.mu.Lock()
	defer b.mu.Unlock()
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
			pruneRoundDirs(r.Config.RoundRoot, r.Config.NodeID, roundKeep)
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("创建轮次目录 %s 失败: %w", dir, err)
		}
	}
	return "", fmt.Errorf("轮次目录 %s 已存在且加序号仍冲突", base)
}

// pruneRoundDirs 只保留最近 keep 个本节点的轮次目录。目录名以 UTC 时间戳结尾,
// 字典序即时间序;别的节点的现场不碰。清理失败不影响本轮 —— 留一堆旧目录比
// 因为清理失败停掉巡检轻得多。
func pruneRoundDirs(root, nodeID string, keep int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && isRoundDirName(e.Name(), nodeID) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sortRoundDirs(names)
	for _, name := range names[:len(names)-keep] {
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

// sortRoundDirs 按 (时间戳, 序号) 排序。纯字典序会把同秒的 -10 排到 -2 前面,
// 清理时先删掉的就不是最旧的那批。
func sortRoundDirs(names []string) {
	slices.SortFunc(names, func(a, b string) int {
		ta, sa := roundDirKey(a)
		tb, sb := roundDirKey(b)
		if c := strings.Compare(ta, tb); c != 0 {
			return c
		}
		return cmp.Compare(sa, sb)
	})
}

// roundDirKey 从已通过 isRoundDirName 的目录名里取 (时间戳, 序号)。
func roundDirKey(name string) (string, int) {
	rest, seq := name, 1
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		if n, err := strconv.Atoi(rest[i+1:]); err == nil {
			seq, rest = n, rest[:i]
		}
	}
	if len(rest) < len("20060102T150405Z") {
		return rest, seq
	}
	return rest[len(rest)-len("20060102T150405Z"):], seq
}

// isRoundDirName 核对 name 是否真是本节点的轮次目录:<node>-<UTC 时间戳>[-<序号>]。
// 只看字符串前缀不够 —— 节点 A 的 id 可能是节点 B 的 id 加一段后缀,
// 「A-」会连 B 的现场一起算进来,清理时就把别人的证据删了。
func isRoundDirName(name, nodeID string) bool {
	rest, ok := strings.CutPrefix(name, nodeID+"-")
	if !ok {
		return false
	}
	const stampLen = len("20060102T150405")
	if len(rest) < stampLen+1 || rest[stampLen] != 'Z' {
		return false
	}
	if _, err := time.Parse("20060102T150405", rest[:stampLen]); err != nil {
		return false
	}
	tail := rest[stampLen+1:]
	if tail == "" {
		return true
	}
	seq, ok := strings.CutPrefix(tail, "-")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(seq)
	return err == nil && n >= 2 && n <= roundDirAttempts
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
