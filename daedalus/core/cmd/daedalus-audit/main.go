// Command daedalus-audit 是哈希链审计日志的 Go CLI, 与 audit-log.py 的
// 命令行契约(旗标名/默认值/stdout 形态/退出码语义)保持一致, 并额外提供
// `verify` 子命令做全链重算校验。
//
// 用法:
//
//	daedalus-audit --tool <名称> [--identity cli] [--args JSON或原始文本]
//	               [--outcome success|denied|error] [--policy-version 1.0]
//	               [--log-path 路径]
//	daedalus-audit verify [--log-path 路径]
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/daedalus-os/daedalus/core/internal/audit"
	"github.com/daedalus-os/daedalus/core/internal/version"
)

// 退出码与 Python 侧对齐: argparse 用法错误 = 2, 运行期失败 = 1, 成功 = 0。
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// outcomeChoices 对应 argparse 的 choices=["success", "denied", "error"]。
var outcomeChoices = []string{"success", "denied", "error"}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "verify" {
		os.Exit(runVerify(args[1:]))
	}
	os.Exit(runLog(args))
}

// runLog 复刻 audit-log.py main() 的写入口径。
func runLog(argv []string) int {
	fs := flag.NewFlagSet("daedalus-audit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: daedalus-audit [--identity ID] --tool TOOL [--args JSON]\n"+
			"                    [--outcome {success,denied,error}] [--policy-version VER]\n"+
			"                    [--log-path PATH]\n\n"+
			"Daedalus OS Audit Logging CLI (core %s)\n\nFlags:\n", version.Version)
		fs.PrintDefaults()
	}
	identity := fs.String("identity", "cli", "Caller identity (default: cli)")
	tool := fs.String("tool", "", "Tool name called (required)")
	argsRaw := fs.String("args", "{}", "Arguments as JSON string or raw text")
	outcome := fs.String("outcome", "success", "Call outcome (default: success)")
	policyVersion := fs.String("policy-version", audit.DefaultPolicyVersion, "Policy version")
	logPath := fs.String("log-path", audit.DefaultLogPath(), "Path to audit log file")

	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK // -h/--help 与 argparse 一样成功退出
		}
		return exitUsage // 旗标语法错误: argparse 退出码 2
	}

	// required=True 的 --tool: 必须显式出现(空串也算给出, 与 argparse 行为一致)。
	seenTool := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tool" {
			seenTool = true
		}
	})
	if !seenTool {
		fmt.Fprintln(os.Stderr, "usage: daedalus-audit ... (见 --help)")
		fmt.Fprintln(os.Stderr, "daedalus-audit: error: the following arguments are required: --tool")
		return exitUsage
	}
	if !containsChoice(outcomeChoices, *outcome) {
		fmt.Fprintln(os.Stderr, "daedalus-audit: error: argument --outcome: invalid choice: '"+*outcome+
			"' (choose from "+quoteChoices(outcomeChoices)+")")
		return exitUsage
	}

	// --args: 先按 JSON 解析, 失败则整体作为原始字符串(audit-log.py:169-172)。
	var argsVal *audit.Value
	if v, err := audit.ParseValue(*argsRaw); err == nil {
		argsVal = v
	} else {
		argsVal = audit.NewString(*argsRaw)
	}

	record, err := audit.LogAudit(audit.Entry{
		Identity:      *identity,
		Tool:          *tool,
		Args:          argsVal,
		Outcome:       *outcome,
		PolicyVersion: *policyVersion,
		LogPath:       *logPath,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRuntime
	}

	// 成功输出与 Python print(json.dumps(record, indent=2)) 逐字节一致。
	fmt.Println(record.IndentJSON())
	return exitOK
}

// runVerify 处理 `daedalus-audit verify --log-path <file>` 子命令。
func runVerify(argv []string) int {
	fs := flag.NewFlagSet("daedalus-audit verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logPath := fs.String("log-path", audit.DefaultLogPath(), "Path to audit log file")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	n, err := audit.Verify(*logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRuntime
	}
	fmt.Printf("verified %d entries; hash chain intact\n", n)
	return exitOK
}

func containsChoice(choices []string, v string) bool {
	for _, c := range choices {
		if c == v {
			return true
		}
	}
	return false
}

// quoteChoices 输出 argparse 风格 "'success', 'denied', 'error'"。
func quoteChoices(choices []string) string {
	out := ""
	for i, c := range choices {
		if i > 0 {
			out += ", "
		}
		out += "'" + c + "'"
	}
	return out
}
