// Command daedalus-patrol 是巡检的 oneshot 入口:读清单、跑一轮、把变化交给通知。
//
//	daedalus-patrol run --node <id>          跑一轮
//	daedalus-patrol checklist [--node <id>]  打印合并后的清单(调试口)
//	daedalus-patrol version                  打印版本
//
// 退出码与 daedalus 家族 CLI 对齐:0 成功、1 运行期失败、2 用法;巡检只报告不动手。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sdkversion "github.com/Daedalusys/daedalus-sdk/version"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-core/internal/mailer"
	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// 退出码(与 daedalus-host / daedalus-tx 家族一致)。
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

const (
	// defaultConfigPath 是镜像内的清单与运行参数单一事实源。
	defaultConfigPath = "/etc/daedalus/patrol.toml"
	// defaultStateDir / defaultRoundRoot 与单元的 StateDirectory 同根。
	defaultStateDir  = "/var/lib/daedalus-patrol/state"
	defaultRoundRoot = "/var/lib/daedalus-patrol/round"
	// defaultNodeTimeout 是 AI 客户端的兜底执行时限(不含网关起停与投递)。
	defaultNodeTimeout = 10 * time.Minute
	// SMTP 凭据来自 systemd 凭据目录;文件可为空(免密投递是合法形态)。
	credentialsDirEnv      = "CREDENTIALS_DIRECTORY"
	smtpUsernameCredential = "smtp-username"
	smtpPasswordCredential = "smtp-password"
)

// 装配缝的编译期钉子:fleet.ToolCaller 直接满足巡检的节点调用面,不需要适配层。
var _ patrol.Fleet = (*fleet.ToolCaller)(nil)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分派子命令,返回退出码。stdout/stderr 注入以便表驱动测试。
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
	switch sub, args := argv[0], argv[1:]; sub {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return exitOK
	case "run":
		return cmdRun(args, stdout, stderr)
	case "checklist":
		return cmdChecklist(args, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "daedalus-patrol %s\n", sdkversion.Version)
		return exitOK
	default:
		fmt.Fprintf(stderr, "daedalus-patrol: 未知子命令: %s\n\n%s", sub, usage())
		return exitUsage
	}
}

// cmdRun 跑一轮:读配置 → 装配 Runner → 跑 → 按结果给退出码。
//
// 「本轮失败」与「本轮成功但没变化」都要区分开:失败时退出码非 0,由 systemd
// 记录失败状态;没有变化的成功轮次是常态,退出码 0。
func cmdRun(argv []string, stdout, stderr io.Writer) int {
	fs, config, node := newFlagSet("run")
	stateDir := fs.String("state-dir", defaultStateDir, "跨轮状态目录(每节点一个 <node>.json)")
	roundRoot := fs.String("round-root", defaultRoundRoot, "轮次目录根")
	if code, ok := parseFlags(fs, argv, stdout, stderr); !ok {
		return code
	}
	if code, ok := checkNodeID(*node, true, stderr); !ok {
		return code
	}

	cfg, err := patrol.LoadConfig(*config)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: 读取配置失败: %v\n", err)
		return exitRuntime
	}
	timeout, err := nodeTimeout(cfg.Patrol.NodeTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: %v\n", err)
		return exitRuntime
	}
	reg, err := fleet.OpenRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: 打开节点注册表失败: %v\n", err)
		return exitRuntime
	}

	runner := &patrol.Runner{
		Config: patrol.RunConfig{
			ChecklistPath: *config,
			StatePath:     filepath.Join(*stateDir, *node+".json"),
			RoundRoot:     *roundRoot,
			NodeID:        *node,
			AICommand:     cfg.Patrol.AIClient.Command,
			NodeTimeout:   timeout,
		},
		Fleet: fleet.NewToolCaller(reg),
		Mail:  &mailer.Sender{Config: smtpConfig(cfg.Patrol.SMTP)},
	}

	// oneshot 单元被 stop 时让本轮尽快收尾,而不是等 AI 客户端的时限到点。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "daedalus-patrol: 节点 %s 开始一轮巡检\n", *node)
	changes, err := runner.Run(ctx)
	printSummary(stdout, changes)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: 本轮巡检失败: %v\n", err)
		return exitRuntime
	}
	return exitOK
}

// cmdChecklist 打印合并后的清单。停用项也印出来 —— 这个子命令是排配置用的,
// 「为什么这一项没跑」通常就因为它在这里标着停用。
func cmdChecklist(argv []string, stdout, stderr io.Writer) int {
	fs, config, node := newFlagSet("checklist")
	if code, ok := parseFlags(fs, argv, stdout, stderr); !ok {
		return code
	}
	if code, ok := checkNodeID(*node, false, stderr); !ok {
		return code
	}

	cl, err := patrol.LoadChecklist(*config, *node)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: 读取清单失败: %v\n", err)
		return exitRuntime
	}

	scope := "全局"
	if *node != "" {
		scope = *node
	}
	fmt.Fprintf(stdout, "清单:%s(%d 项)\n", scope, len(cl.Items))
	if len(cl.Items) == 0 {
		fmt.Fprintln(stdout, "清单为空:巡检无事可做")
		return exitOK
	}
	for _, it := range cl.Items {
		state := "启用"
		if !it.Enabled {
			state = "停用"
		}
		tools := "全部可远程调用的工具"
		if it.Tools != nil {
			// 显式写 tools = [] 与省略不同:前者是「这一项不建议用任何工具」。
			tools = fmt.Sprintf("%v", it.Tools)
		}
		fmt.Fprintf(stdout, "- %s [%s] %s\n    prompt: %s\n    建议工具: %s\n", it.ID, it.Severity, state, it.Prompt, tools)
	}
	return exitOK
}

// printSummary 把一轮结果打成一行,进 journald 供人对照邮件。
func printSummary(stdout io.Writer, changes patrol.Changes) {
	fmt.Fprintf(stdout, "本轮变化:新增 %d · 恶化 %d · 恢复 %d · 持续 %d\n",
		len(changes.Added), len(changes.Worsened), len(changes.Recovered), len(changes.Ongoing))
}

// nodeTimeout 解析 [patrol].node_timeout;省略用兜底值,写错一律拒。
func nodeTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultNodeTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("巡检配置的 node_timeout %q 非法: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("巡检配置的 node_timeout %q 必须为正", raw)
	}
	return d, nil
}

// smtpConfig 把配置段的投递参数与凭据目录里的凭据拼成投递配置。
// 凭据不进 patrol.toml,也不进单元文件。
func smtpConfig(c patrol.SMTPConfig) mailer.Config {
	return mailer.Config{
		Host:     c.Host,
		Port:     c.Port,
		Username: readCredential(smtpUsernameCredential),
		Password: readCredential(smtpPasswordCredential),
		From:     c.From,
		To:       c.To,
		StartTLS: c.StartTLS,
	}
}

// readCredential 从 systemd 凭据目录读一份凭据;目录未设或文件缺失返回空串
// (免密投递),存在则去掉尾部换行。
func readCredential(name string) string {
	dir := os.Getenv(credentialsDirEnv)
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// newFlagSet 建一份各子命令共用的旗标集。输出丢弃:用法错误由本文件统一措辞,
// 不让 flag 包自己印一遍。
func newFlagSet(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", defaultConfigPath, "清单与运行参数的文件路径")
	node := fs.String("node", "", "节点 id")
	return fs, config, node
}

// checkNodeID 校验 --node。run 必须有节点(没有节点就定位不到状态文件与本机清单),
// checklist 可省(省略即只看全局基线);非空则必须是合法节点 id —— 它会被拼进
// 状态文件路径,含路径分隔符或 .. 的形状在这里就按用法错误拒掉。
func checkNodeID(node string, required bool, stderr io.Writer) (int, bool) {
	if node == "" {
		if required {
			fmt.Fprintf(stderr, "daedalus-patrol: run 需要 --node <节点 id>\n\n%s", usage())
			return exitUsage, false
		}
		return exitOK, true
	}
	if !patrol.ValidNodeID(node) {
		fmt.Fprintf(stderr, "daedalus-patrol: --node 非法:%q(只允许字母、数字与 . _ -)\n\n%s", node, usage())
		return exitUsage, false
	}
	return exitOK, true
}

// parseFlags 解析旗标并归并用法错误。-h/--help 是成功路径(打印用法到 stdout,退出 0)。
func parseFlags(fs *flag.FlagSet, argv []string, stdout, stderr io.Writer) (int, bool) {
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage())
			return exitOK, false
		}
		fmt.Fprintf(stderr, "daedalus-patrol: %s 参数非法: %v\n\n%s", fs.Name(), err, usage())
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "daedalus-patrol: %s 不接受位置参数: %v\n\n%s", fs.Name(), fs.Args(), usage())
		return exitUsage, false
	}
	return exitOK, true
}

func usage() string {
	return "daedalus-patrol —— Daedalus 巡检 CLI (core " + sdkversion.Version + ")\n\n" +
		"用法:\n" +
		"  daedalus-patrol run --node <id> [--config <path>] [--state-dir <dir>] [--round-root <dir>]\n" +
		"      跑一轮巡检:起本轮 MCP 网关、拉起 AI 客户端、把发现变化发邮件。\n" +
		"  daedalus-patrol checklist [--node <id>] [--config <path>]\n" +
		"      打印合并后的清单(全局基线 + 节点专属段),含停用项。\n" +
		"  daedalus-patrol version\n" +
		"      打印版本。\n\n" +
		"旗标:\n" +
		"  --config <path> —— 清单与运行参数文件,默认 " + defaultConfigPath + "。\n" +
		"  --node <id> —— 节点 id;它决定叠哪一段节点专属清单,以及状态按哪个节点落盘。\n" +
		"  --state-dir <dir> —— 跨轮状态目录,默认 " + defaultStateDir + "。\n" +
		"  --round-root <dir> —— 轮次目录根,默认 " + defaultRoundRoot + "。\n"
}
