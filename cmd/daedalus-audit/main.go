// Command daedalus-audit 是哈希链审计日志的 Go CLI, 与 audit-log.py 的
// 命令行契约(旗标名/默认值/stdout 形态/退出码语义)保持一致, 并额外提供
// `verify` 子命令做全链重算校验、`rotate` 子命令做归档轮转与保留清理。
//
// 用法:
//
//	daedalus-audit --tool <名称> [--identity cli] [--args JSON或原始文本]
//	               [--outcome success|denied|error] [--policy-version 1.0]
//	               [--log-path 路径]
//	daedalus-audit verify [--log-path 路径] [--archive-dir 路径]
//	daedalus-audit rotate [--log-path 路径] [--archive-dir 路径] [--force]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/policy"
	"github.com/Daedalusys/daedalus-sdk/version"
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是测试可驱动的 dispatch:优先识别子命令 `verify` / `rotate`,缺省走写入。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "verify":
			return runVerify(args[1:], stdout, stderr)
		case "rotate":
			return runRotate(args[1:], stdout, stderr)
		}
	}
	return runLog(args, stdout, stderr)
}

// runLog 复刻 audit-log.py main() 的写入口径。
func runLog(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daedalus-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: daedalus-audit [--identity ID] --tool TOOL [--args JSON]\n"+
			"                    [--outcome {success,denied|error}] [--policy-version VER]\n"+
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
			return exitOK
		}
		return exitUsage
	}
	seenTool := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tool" {
			seenTool = true
		}
	})
	if !seenTool {
		fmt.Fprintln(stderr, "usage: daedalus-audit ... (见 --help)")
		fmt.Fprintln(stderr, "daedalus-audit: error: the following arguments are required: --tool")
		return exitUsage
	}
	if !containsChoice(outcomeChoices, *outcome) {
		fmt.Fprintln(stderr, "daedalus-audit: error: argument --outcome: invalid choice: '"+*outcome+
			"' (choose from "+quoteChoices(outcomeChoices)+")")
		return exitUsage
	}
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
		fmt.Fprintln(stderr, err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, record.IndentJSON())
	return exitOK
}

// runVerify 校验"活跃 + archive 目录 + prune 账本"的整链。archive 目录缺省
// 取 <dir(log_path)>/archive;若该目录不存在或为空,退化为单文件 Verify(兼容旧日志)。
func runVerify(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daedalus-audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log-path", audit.DefaultLogPath(), "Path to audit log file")
	archiveDir := fs.String("archive-dir", "", "Archive directory (default: <logPath dir>/archive)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	ad := *archiveDir
	if ad == "" {
		ad = filepath.Join(filepath.Dir(*logPath), "archive")
	}
	// 判定是否走 VerifyChain:目录存在且至少有 prune 账本或一个段。
	useChain := false
	if fi, err := os.Stat(ad); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir(ad)
		for _, e := range entries {
			name := e.Name()
			if name == "pruned.jsonl" || strings.Contains(name, ".jsonl.") {
				useChain = true
				break
			}
		}
	}
	var (
		n   int
		err error
	)
	if useChain {
		n, err = audit.VerifyChain(*logPath, ad)
	} else {
		n, err = audit.Verify(*logPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "verified %d entries; hash chain intact\n", n)
	return exitOK
}

// runRotate 从 policy 读阈值,执行 Rotate(归档)+ Prune(保留清理)。
func runRotate(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daedalus-audit rotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log-path", audit.DefaultLogPath(), "Path to active audit log file")
	archiveDir := fs.String("archive-dir", "", "Archive directory (default: <logPath dir>/archive)")
	force := fs.Bool("force", false, "Rotate even if size below threshold")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	// 阈值必来自 policy(fail-closed):缺失拒启。
	pol, err := policy.Load("")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitRuntime
	}
	ad := *archiveDir
	if ad == "" {
		ad = filepath.Join(filepath.Dir(*logPath), "archive")
	}
	rotated, err := audit.Rotate(audit.RotateOptions{
		LogPath:    *logPath,
		ArchiveDir: ad,
		MaxSizeMB:  pol.Audit.MaxSizeMB,
		Force:      *force,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitRuntime
	}
	if _, err := audit.Prune(audit.PruneOptions{
		ArchiveDir: ad,
		RetainDays: pol.Audit.RetainDays,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "rotated=%v\n", rotated)
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
