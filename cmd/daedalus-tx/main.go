// Command daedalus-tx 是 Daedalus 的事务原语 CLI(AIOS 对象模型 C3)。
//
//	子命令:
//
//	daedalus-tx begin
//	daedalus-tx propose  <tx-id> <adapter> <args-json> [--unit-dir <path>]
//	daedalus-tx apply    <tx-id> [--unit-dir <path>]
//	daedalus-tx rollback <tx-id> [--unit-dir <path>]
//	daedalus-tx resolve  <tx-id>
//	daedalus-tx status   <tx-id>
//	daedalus-tx service-observe --unit <name>
//	daedalus-tx grant add --scope <scope> [--ttl <dur>] [--count N] [--note <text>] [--for <user>]
//	daedalus-tx grant list
//	daedalus-tx grant revoke <id>
//	daedalus-tx pending list
//	daedalus-tx pending show <pending-id>
//	daedalus-tx approve  <pending-id> --expect-sha256 <hash>
//
// stdout 契约(每条子命令**恰一份**机器可读 JSON 文档; 上层 copilot 层
// 解析 CLI stdout, 而非 JSON-RPC):
//   - begin   → {"tx_id":"<16hex>"}
//   - propose/apply/rollback/resolve/status → 事务日志全文(id/created_at/status/steps/rollback_plan)
//   - service-observe → {"unit","active_state","unit_file_state","enabled","running","known"}(恰六键)
//   - grant add/list/revoke → 条子文档 / 条子列表 / {"revoked":"<id>"}
//   - pending list → {"pending":[...],"invalid":[...]}; pending show → 待办视图
//     (两者都带 requested_by / requested_by_uid, 归属取自待办文件的 stat 属主)
//   - approve → 批准记录文档(含被批准的 argv 回显); --expect-sha256 必填且须与当前待办一致;
//     非 root 一律拒绝(退出码 2)
//   - 一切错误 → {"error":"..."} 且退出码非零
//
// 退出码与 daedalus 家族 CLI 对齐(镜像 cmd/daedalus-host/main.go):
// 0 成功, 1 运行期, 2 用法错误。service-observe 的细分见 usage()
// (单元级"查不到"是观测结果 ⇒ 0 + known=false, 只有进程级故障才是 1)。
//
// ★ v1 执行模型: daedalus-tx 是**调用用户自己的进程**, 无 systemd 单元; 用户域
// 限制由适配器路径守卫无条件强制。本二进制**允许 spawn** 子进程(备份/回滚),
// 与"宿主绝不 spawn"分属不同二进制, 互不违反。
//
// ★ 审计盖章规则(本文件承重): 只有 begin(step 0)/
// apply(步 1..N 升序)/rollback(步 N+1..)的事务条目携带 Entry.TxID/TxStep;
// propose、status 与 resolve 发出**空 TxID** 条目(tx-id 只在 args 里)。identity=daedalus-tx,
// tool=daedalus_tx_<sub>。begin 的 tx_prev_hash 由 SDK audit 包的 flock 内播种
// (见 audit.LogAudit); apply/rollback 的步链由本 CLI 扫描审计日志
// 取该事务上一条 in-tx 记录 entry_hash 显式串接(跨进程续链)。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/version"
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
	case "resolve":
		return cmdResolve(args, stdout, stderr)
	case "status":
		return cmdStatus(args, stdout, stderr)
	case "service-observe":
		return cmdServiceObserve(args, stdout, stderr)
	case "grant":
		return cmdGrant(args, stdout, stderr)
	case "pending":
		return cmdPending(args, stdout, stderr)
	case "approve":
		return cmdApprove(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "daedalus-tx: 未知子命令: %s\n\n", sub)
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
}

// wantIDArgs 校验 <id 型子命令> 的位置参数: 恰好 1 个且匹配 tx-id 形状。
// 返回 (id, exitCode, ok)。ok=false 时 exitCode ∈ {exitUsage}; 形状非法在
// **触碰日志路径之前**即拒(非法 id 绝不参与拼路径/落盘)。
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
		"  daedalus-tx resolve   <tx-id>\n" +
		"  daedalus-tx status   <tx-id>\n" +
		"  daedalus-tx service-observe --unit <name>\n" +
		"  daedalus-tx grant add    --scope <scope> [--scope <scope>...] [--ttl <dur>] [--count N] [--note <text>] [--for <user>]\n" +
		"  daedalus-tx grant list\n" +
		"  daedalus-tx grant revoke <id>\n" +
		"  daedalus-tx pending list\n" +
		"  daedalus-tx pending show <pending-id>\n" +
		"  daedalus-tx approve  <pending-id> --expect-sha256 <hash>   (仅 root)\n\n" +
		"旗标:\n" +
		"  --unit-dir <path> —— service.set 单元文件解析目录覆写(集成测试逃生舱;\n" +
		"      不给时按 $HOME/.config/systemd/user 解析; 只影响解析目录, 守卫恒生效)。\n" +
		"  --unit <name> —— service-observe 观测的 user 域单元名; 必须带 .service 后缀\n" +
		"      (观测侧绝不隐式补后缀或改名: 防「以为观测 A、实际查 B」)。\n" +
		"  --scope <scope> —— grant add 覆盖的作用域, 可重复; 严格形态\n" +
		"      systemd:user:<action>:<target> 或 <runtime>:<action>:<target>。\n" +
		"  --ttl <dur> —— grant add 的有效期(默认 30m), 全局上限 24h。\n" +
		"  --count N —— grant add 的可用次数(默认 1), 全局上限 10。\n" +
		"  --note <text> —— 条子备注(只作线索, 不参与判定)。\n" +
		"  --for <user> —— grant add 把条子绑定给该用户(执行者); 只有 root 能用;\n" +
		"      root 签发必须写明给谁(未写明的 root 条子谁都用不了)。\n" +
		"  grant 条子目录默认 /run/daedalus/grants, 可用 DAEDALUS_GRANTS_DIR 覆盖;\n" +
		"      签发身份 = 文件属主 uid(SUDO_USER 优先, 否则当前用户), 撤销别人的条子需要 root。\n" +
		"  待办根目录默认 /run/daedalus/pending(1777 粘滞位), 可用 DAEDALUS_PENDING_DIR 覆盖;\n" +
		"      每个用户的待办落在 <根目录>/<uid>/(0700), 请求者归属按文件属主判定;\n" +
		"      pending list 列出所有用户的待办(按请求者归属显示), pending show 跨所有用户目录查找。\n" +
		"      批准目录默认 /run/daedalus/approvals(0755 root:root), 可用 DAEDALUS_APPROVALS_DIR 覆盖。\n" +
		"  approve 只绑定 argv_sha256 = sha256(NUL 连接的 argv), 参数一变就是新动作;\n" +
		"      --expect-sha256 必填, 从 pending show/list 的 argv_sha256 抄过来;\n" +
		"      当前待办的指纹与它不一致 ⇒ 拒绝批准(退出码 1)且不写批准记录。\n" +
		"      待办过期即作废, 非 root 一律拒绝(退出码 2)。\n" +
		"输出: 每条子命令向 stdout 打印恰一份机器可读 JSON 文档;\n" +
		"      begin → {\"tx_id\":\"<16hex>\"}; propose/apply/rollback/resolve/status → 事务日志全文;\n" +
		"      service-observe → {\"unit\",\"active_state\",\"unit_file_state\",\"enabled\",\"running\",\"known\"}(恰六键);\n" +
		"      grant add → 条子文档; grant list → {\"grants\":[...],\"invalid\":[...]}; grant revoke → {\"revoked\":\"<id>\"};\n" +
		"      pending list → {\"pending\":[...],\"invalid\":[...]}; pending show → 待办视图;\n" +
		"      approve → 批准记录文档(含被批准的 argv 回显); 错误 → {\"error\":\"...\"}。\n" +
		"可用 adapter: " + strings.Join(registeredAdapterNames(), ", ") + "\n" +
		"退出码: 0 成功 / 1 运行期 / 2 用法错误。\n" +
		"      service-observe 细分: 单元级失败(单元不存在/属性取不到)⇒ 0 且 known=false;\n" +
		"      systemctl 进程级故障(起不来 126 / 超时 124)⇒ 1; 用法错(含非 .service 单元)⇒ 2。\n" +
		"      known=false 时 stderr 会给出原因, 故「stderr 非空」不等于失败。\n"
}

// registeredAdapterNames 从 registry 动态列出已注册 adapter 名(propose --help 一眼
// 可见可用 adapter), 升序排序保证 help 文本确定性(map 遍历序随机, 不排序则非确定性输出)。
func registeredAdapterNames() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
