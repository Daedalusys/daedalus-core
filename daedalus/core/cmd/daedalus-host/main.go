// Command daedalus-host 是 Daedalus 的插件宿主 CLI:发现/检视/校验/审计。
//
// ★ 安全边界(计划决策 16):宿主不是任何 MCP 服务器的父进程。
// run-plugin / render-unit 只**打印**由 manifest 构造的启动命令 / systemd
// ExecStart 片段;真正的进程父是 systemd,它按渲染出的 ExecStart 直接执行
// 服务器二进制,并保留每服务的 DynamicUser/Landlock/seccomp/LoadCredential
// drop-in 沙箱语义。宿主进程自身绝不 spawn 子进程。
//
// 用法:
//
//	daedalus-host list [-dir <插件目录>]
//	daedalus-host inspect <id> [-dir <插件目录>]
//	daedalus-host verify <id> [-dir <插件目录>]
//	daedalus-host run-plugin <id> [-dir <插件目录>] [-- 追加参数...]
//	daedalus-host render-unit <id> [-dir <插件目录>]
//
// 插件目录默认 /opt/daedalus/plugins(镜像内构建期内建),可被环境变量
// DAEDALUS_PLUGIN_DIR 或 -dir 旗标覆盖(旗标优先)。
// 退出码:0 成功,1 运行期/校验失败,2 用法错误。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daedalus-os/daedalus/core/internal/audit"
	"github.com/daedalus-os/daedalus/core/internal/plugin"
	"github.com/daedalus-os/daedalus/core/internal/version"
)

// 退出码与 daedalus 家族 CLI 对齐:0 成功,1 运行期,2 用法错误。
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// DefaultPluginDir 是镜像内插件根目录(决策 22:构建期内建,无运行时安装)。
const DefaultPluginDir = "/opt/daedalus/plugins"

// EnvPluginDir 覆盖默认插件目录(开发态用);-dir 旗标又优先于环境变量。
const EnvPluginDir = "DAEDALUS_PLUGIN_DIR"

// hostIdentity 写入审计日志的调用者身份。
const hostIdentity = "daedalus-host"

// usage 是 -h/--help 与用法错误时打印的帮助文本(列全子命令,含非父进程声明)。
const usage = `usage: daedalus-host <command> [args] [-dir <插件目录>]

Daedalus plugin host (core %s) —— 插件发现/检视/校验/审计。
注意:宿主不是任何 MCP 服务器的父进程。run-plugin / render-unit 只打印
manifest 构造出的启动命令 / systemd ExecStart 片段,systemd 才是真正的执行方
(计划决策 16);宿主自身绝不 spawn 子进程。

Commands:
  list                 扫描插件目录,逐条打印 id/name/version/type/runtime;损坏插件标 degraded
  inspect <id>         打印该插件 manifest 详情(checksums 摘要、tools、permissions)
  verify <id>          复用 internal/plugin 校验核心做 sha256 完整性检查;通过 0,失败 1+原因
  run-plugin <id>      仅打印启动命令(native: <exe> <args>;deno: deno run <args> <script>)
  render-unit <id>     仅输出 systemd 片段(ExecStart= 行),供构建期 76-daedalus-plugin-gen.sh 使用

插件目录:默认 /opt/daedalus/plugins;可用 DAEDALUS_PLUGIN_DIR 环境变量或 -dir 旗标覆盖(旗标优先)。
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 解析全局旗标、分派子命令,返回退出码。stdout/stderr 注入以便测试捕获。
func run(argv []string, stdout, stderr io.Writer) int {
	rest, dirFlag, err := splitDirFlag(argv)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host: %v\n", err)
		return exitUsage
	}
	if len(rest) == 0 {
		fmt.Fprintf(stderr, usage, version.Version)
		return exitUsage
	}
	cmd, args := rest[0], rest[1:]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Fprintf(stdout, usage, version.Version)
		return exitOK
	}
	pluginDir := resolvePluginDir(dirFlag)

	switch cmd {
	case "list":
		code := cmdList(stdout, stderr, pluginDir)
		hostAudit("host_list", pluginDir, "", code)
		return code
	case "inspect", "verify", "run-plugin", "render-unit":
		id, tail, err := parseIDArgs(args)
		if err != nil {
			fmt.Fprintf(stderr, "daedalus-host: %s: %v\n", cmd, err)
			hostAudit("host_"+auditSlug(cmd), pluginDir, "", exitUsage)
			return exitUsage
		}
		if id == "" {
			fmt.Fprintf(stderr, "daedalus-host: %s: 缺少 <id> 参数\n", cmd)
			return exitUsage
		}
		tool := "host_" + auditSlug(cmd)
		// 只有 run-plugin 接受 `--` 追加参数;其余子命令给 tail 即用法错误。
		if len(tail) > 0 && cmd != "run-plugin" {
			fmt.Fprintf(stderr, "daedalus-host: %s: 该子命令不接受追加参数\n", cmd)
			hostAudit(tool, pluginDir, id, exitUsage)
			return exitUsage
		}
		// id 注入防线:文法非法 '../' 等一律拒绝,绝不用它拼路径。
		if !plugin.ValidID(id) {
			fmt.Fprintf(stderr, "daedalus-host: %s: 插件 id %q 不匹配文法,拒绝\n", cmd, id)
			hostAudit(tool, pluginDir, id, exitUsage)
			return exitUsage
		}
		st := loadPlugin(pluginDir, id)
		var code int
		switch cmd {
		case "inspect":
			code = cmdInspect(stdout, stderr, st)
		case "verify":
			code = cmdVerify(stdout, stderr, st)
		case "run-plugin":
			code = cmdRunPlugin(stdout, stderr, st, tail)
		case "render-unit":
			code = cmdRenderUnit(stdout, stderr, st)
		}
		hostAudit(tool, pluginDir, id, code)
		return code
	default:
		fmt.Fprintf(stderr, "daedalus-host: 未知子命令 %q\n\n", cmd)
		fmt.Fprintf(stderr, usage, version.Version)
		return exitUsage
	}
}

// splitDirFlag 从任意位置抽出 -dir/--dir <value>(子命令后置旗标也接受)。
func splitDirFlag(argv []string) (rest []string, dir string, err error) {
	rest = make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "-dir" || a == "--dir" {
			if i+1 >= len(argv) {
				return nil, "", errors.New("-dir 需要跟一个值")
			}
			dir = argv[i+1]
			i++
			continue
		}
		rest = append(rest, a)
	}
	return rest, dir, nil
}

// parseIDArgs 取出 <id> 位置参数,并支持 `-- extra...` 形式的追加参数(run-plugin)。
func parseIDArgs(args []string) (id string, tail []string, err error) {
	for i, a := range args {
		if a == "--" {
			if i != 1 {
				return "", nil, errors.New("`--` 之前必须且只能有一个 <id>")
			}
			return args[0], args[2:], nil
		}
	}
	if len(args) > 1 {
		return "", nil, fmt.Errorf("多余参数 %v(追加参数请用 -- 分隔)", args[1:])
	}
	if len(args) == 0 {
		return "", nil, nil
	}
	return args[0], nil, nil
}

// resolvePluginDir 应用旗标 > 环境变量 > 镜像默认值的优先级。
func resolvePluginDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(EnvPluginDir); v != "" {
		return v
	}
	return DefaultPluginDir
}

// auditSlug 把子命令名映射为审计工具名后缀(run-plugin → run_plugin)。
func auditSlug(cmd string) string {
	return strings.ReplaceAll(cmd, "-", "_")
}

// hostAudit best-effort 追加一条哈希链审计:identity=daedalus-host,
// tool=host_<command>,args 记录插件目录与 id,结果映射退出码
// (0→success,1→error,2→denied)。任何写失败都静默忽略——
// 审计是尽力而为,绝不能反过来拖垮宿主子命令。
func hostAudit(tool, pluginDir, id string, code int) {
	outcome := "success"
	switch code {
	case exitRuntime:
		outcome = "error"
	case exitUsage:
		outcome = "denied"
	}
	argsJSON := marshalArgs(map[string]string{"dir": pluginDir, "id": id})
	v, err := audit.ParseValue(string(argsJSON))
	if err != nil { // map[string]string 序列化必为合法 JSON;兜底原始字符串。
		v = audit.NewString(string(argsJSON))
	}
	_, _ = audit.LogAudit(audit.Entry{ // 错误静默丢弃(尽力而为)。
		Identity: hostIdentity,
		Tool:     tool,
		Args:     v,
		Outcome:  outcome,
	})
}
