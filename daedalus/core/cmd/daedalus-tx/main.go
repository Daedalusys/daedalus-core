// Command daedalus-tx 是 Daedalus 的事务原语 CLI(AIOS 对象模型 C3, todo 15)。
//
//	子命令:
//
//	daedalus-tx begin
//	daedalus-tx propose  <tx-id> <adapter> <args-json> [--unit-dir <path>]
//	daedalus-tx apply    <tx-id> [--unit-dir <path>]
//	daedalus-tx rollback <tx-id> [--unit-dir <path>]
//	daedalus-tx status   <tx-id>
//
// stdout 契约(每条子命令**恰一份**机器可读 JSON 文档; todo 26 的 copilot 层
// 解析 CLI stdout, 而非 JSON-RPC):
//   - begin   → {"tx_id":"<16hex>"}
//   - propose/apply/rollback/status → 事务日志全文(id/created_at/status/steps/rollback_plan)
//   - 一切错误 → {"error":"..."} 且退出码非零
//
// 退出码与 daedalus 家族 CLI 对齐(镜像 cmd/daedalus-host/main.go):
// 0 成功, 1 运行期, 2 用法错误。
//
// ★ v1 执行模型(计划 todo 16): daedalus-tx 是**调用用户自己的进程**, 无 systemd
// 单元; 用户域限制由适配器(todo 22)路径守卫无条件强制。本二进制**允许 spawn**
// 子进程(备份/回滚), 与"宿主绝不 spawn"(决策 16)分属不同二进制, 互不违反。
//
// ★ 审计盖章规则(计划 review round 1, 本文件承重): 只有 begin(step 0)/
// apply(步 1..N 升序)/rollback(步 N+1..)的事务条目携带 Entry.TxID/TxStep;
// propose 与 status 发出**空 TxID** 条目(tx-id 只在 args 里)。identity=daedalus-tx,
// tool=daedalus_tx_<sub>。begin 的 tx_prev_hash 由 internal/audit 的 flock 内播种
// (见 audit.LogAudit todo 15 外科改动); apply/rollback 的步链由本 CLI 扫描审计日志
// 取该事务上一条 in-tx 记录 entry_hash 显式串接(跨进程续链)。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/daedalus-os/daedalus/core/internal/version"
)

// 退出码(与 daedalus-host/daedalus-audit 家族一致)。
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// txIdentity 写入审计日志的调用者身份。
const txIdentity = "daedalus-tx"

// txIDPattern 与 internal/tx 的形状门同源: crypto/rand 8 字节小写十六进制。
// CLI 在触碰任何日志/盘之前先校验位置参数(CLI 侧防线, 与 tx.Load 双保险)。
var txIDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分派子命令, 返回退出码。stdout/stderr 注入以便测试(镜像 host 模式)。
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
	sub, args := argv[0], argv[1:]
	switch sub {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return exitOK
	case "begin":
		if len(args) != 0 {
			return failUsage(stdout, stderr, "begin 不接受参数")
		}
		return cmdBegin(stdout, stderr)
	case "propose":
		return cmdPropose(args, stdout, stderr)
	case "apply":
		return cmdApply(args, stdout, stderr)
	case "rollback":
		return cmdRollback(args, stdout, stderr)
	case "status":
		return cmdStatus(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "daedalus-tx: 未知子命令: %s\n\n", sub)
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
}

// wantIDArgs 校验 <id 型子命令> 的位置参数: 恰好 1 个且匹配 tx-id 形状。
// 返回 (id, exitCode, ok)。ok=false 时 exitCode ∈ {exitUsage}; 形状非法在
// **触碰日志路径之前**即拒(round-2 fold 路径安全: 非法 id 绝不参与拼路径/落盘)。
func wantIDArgs(args []string, stdout, stderr io.Writer) (string, int, bool) {
	if len(args) != 1 {
		fmt.Fprint(stderr, usage())
		return "", exitUsage, false
	}
	id := args[0]
	if !txIDPattern.MatchString(id) {
		return "", failUsage(stdout, stderr,
			fmt.Sprintf("事务 ID 非法: %q (需匹配 ^[a-f0-9]{16}$)", id)), false
	}
	return id, exitOK, true
}

// failUsage 打印 {"error":...} 到 stdout(维持 stdout JSON 契约)+ 回显一句到 stderr;
// 返回 exitUsage。用于参数级用法错误(元数/形状)。
func failUsage(stdout, stderr io.Writer, msg string) int {
	printError(stdout, msg)
	fmt.Fprintf(stderr, "daedalus-tx: %s\n", msg)
	return exitUsage
}

// failRuntime 打印 {"error":...} 到 stdout(契约: 错误也是机器可读 JSON)+ 回显 stderr;
// 返回 exitRuntime。运行期错误(未找到 / 非法迁移 / 适配器失败)统一走此出口。
func failRuntime(stdout, stderr io.Writer, msg string) int {
	printError(stdout, msg)
	fmt.Fprintf(stderr, "daedalus-tx: %s\n", msg)
	return exitRuntime
}

// errDoc 是错误文档(stdout JSON 契约的 error 形态)。
type errDoc struct {
	Error string `json:"error"`
}

// beginDoc 是 begin 的 stdout 输出形态。
type beginDoc struct {
	TxID string `json:"tx_id"`
}

// printJSON 把 v 序列化为**单行紧凑** JSON 写入 w, 末尾换行(恰一份文档)。
func printJSON(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daedalus-tx: 序列化输出失败: %v\n", err)
		return
	}
	_, _ = w.Write(append(data, '\n'))
}

// printError 输出 {"error":...} 错误文档。
func printError(w io.Writer, msg string) { printJSON(w, errDoc{Error: msg}) }

// usage 返回 -h/--help 与用法错误时的帮助文本(列全子命令与 stdout 契约)。
func usage() string {
	return "daedalus-tx —— Daedalus 事务原语 CLI (core " + version.Version + ")\n\n" +
		"用法:\n" +
		"  daedalus-tx begin\n" +
		"  daedalus-tx propose  <tx-id> <adapter> <args-json> [--unit-dir <path>]\n" +
		"  daedalus-tx apply    <tx-id> [--unit-dir <path>]\n" +
		"  daedalus-tx rollback <tx-id> [--unit-dir <path>]\n" +
		"  daedalus-tx status   <tx-id>\n\n" +
		"旗标: --unit-dir <path> —— service.set 单元文件解析目录覆写(集成测试逃生舱;\n" +
		"      不给时按 $HOME/.config/systemd/user 解析; 只影响解析目录, 守卫恒生效)。\n" +
		"输出: 每条子命令向 stdout 打印恰一份机器可读 JSON 文档;\n" +
		"      begin → {\"tx_id\":\"<16hex>\"}; 其余 → 事务日志全文; 错误 → {\"error\":\"...\"}。\n" +
		"退出码: 0 成功 / 1 运行期 / 2 用法错误。\n"
}

// i18nUsageErr 保留一个稳定的未知子命令消息钩子(当前直接英文/中文内联, 不引 i18n
// 键面, 以免 tx CLI 在未定义 locale 键时产生噪声; 未来 i18n 化在此收敛)。
func i18nUsageErr(sub string) string {
	return fmt.Sprintf("未知子命令: %s", sub)
}
