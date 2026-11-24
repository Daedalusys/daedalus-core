// Command daedalus-patrol 是巡检的 oneshot 入口:读清单、跑一轮、把变化交给通知。
// 本阶段只读清单并打印本轮工作项,网关与 AI 客户端编排在后续阶段接入。
//
//	daedalus-patrol run --node <id>          跑一轮
//	daedalus-patrol checklist [--node <id>]  打印合并后的清单(调试口)
//	daedalus-patrol version                  打印版本
//
// 退出码与 daedalus 家族 CLI 对齐:0 成功、1 运行期、2 用法;巡检只报告不动手。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	sdkversion "github.com/Daedalusys/daedalus-sdk/version"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

// 退出码(与 daedalus-host / daedalus-tx 家族一致)。
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// defaultConfigPath 是镜像内的清单与运行参数单一事实源。
const defaultConfigPath = "/etc/daedalus/patrol.toml"

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

// cmdRun 跑一轮。本阶段只做「读清单 → 打印本轮工作项」:网关、AI 客户端编排与
// 状态比对在后续阶段接进来,这里的用法与退出码契约保持不变。
func cmdRun(argv []string, stdout, stderr io.Writer) int {
	fs, config, node := newFlagSet("run")
	if code, ok := parseFlags(fs, argv, stdout, stderr); !ok {
		return code
	}
	if code, ok := checkNodeID(*node, true, stderr); !ok {
		return code
	}

	cl, err := patrol.LoadChecklist(*config, *node)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-patrol: 读取清单失败: %v\n", err)
		return exitRuntime
	}

	items := enabledItems(cl.Items)
	fmt.Fprintf(stdout, "本轮清单:%s(%d 项)\n", *node, len(items))
	if len(items) == 0 {
		fmt.Fprintln(stdout, "清单为空或全部停用:本轮无事可做")
		return exitOK
	}
	for _, it := range items {
		fmt.Fprintf(stdout, "- %s [%s] %s\n", it.ID, it.Severity, it.Prompt)
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

// enabledItems 过滤掉停用项:清单是配置,跑不跑看开关。
func enabledItems(items []patrol.Item) []patrol.Item {
	out := make([]patrol.Item, 0, len(items))
	for _, it := range items {
		if it.Enabled {
			out = append(out, it)
		}
	}
	return out
}

func usage() string {
	return "daedalus-patrol —— Daedalus 巡检 CLI (core " + sdkversion.Version + ")\n\n" +
		"用法:\n" +
		"  daedalus-patrol run --node <id> [--config <path>]\n" +
		"      跑一轮巡检。本阶段只读清单并打印本轮工作项(网关与 AI 客户端编排在后续阶段接入)。\n" +
		"  daedalus-patrol checklist [--node <id>] [--config <path>]\n" +
		"      打印合并后的清单(全局基线 + 节点专属段),含停用项。\n" +
		"  daedalus-patrol version\n" +
		"      打印版本。\n\n" +
		"旗标:\n" +
		"  --config <path> —— 清单与运行参数文件,默认 " + defaultConfigPath + "。\n" +
		"  --node <id> —— 节点 id;它决定叠哪一段节点专属清单,以及状态按哪个节点落盘。\n"
}
