// start.go: verify / run-plugin / render-unit 子命令与启动命令构造。
//
// ★ 安全边界(计划决策 16):宿主不是任何 MCP 服务器的父进程。
// run-plugin 与 render-unit 都只**打印**命令文本,绝不 spawn/exec;
// 真正的进程父是 systemd,它按 manifest 渲染出的 ExecStart 直接执行服务器,
// 每个服务保留各自的 DynamicUser/Landlock/seccomp drop-in 沙箱语义。
package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/daedalus-os/daedalus/core/internal/i18n"
	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// cmdVerify 复用 plugin 包的已安装目录校验核心(与 zip 校验同规则:
// checksums 双向集合相等 + 逐条目 sha256 + manifest 规范化自摘要 + 可执行位)。
// 通过 exit 0;失败 exit 1 并把原因写到 stderr。
func cmdVerify(stdout, stderr io.Writer, st pluginState) int {
	if st.status == statusOK {
		fmt.Fprintf(stdout, "%s\n", i18n.T("host.verify.pass", st.id))
		return exitOK
	}
	fmt.Fprintf(stderr, "daedalus-host: verify: %s %s\n", st.id, i18n.T("host.verify.fail", st.reason))
	return exitRuntime
}

// cmdRunPlugin 仅打印示例启动命令(可附额外参数拼在后面),永不 spawn。
// 损坏插件只输出 manifest 不承诺可运行性,因此拒绝为 degraded 插件打印(退出 1)。
func cmdRunPlugin(stdout, stderr io.Writer, st pluginState, extra []string) int {
	if st.manifest == nil || st.status != statusOK {
		reason := st.reason
		if reason == "" {
			reason = i18n.T("host.error.manifest_unavailable")
		}
		fmt.Fprintf(stderr, "daedalus-host: run-plugin: %s %s: %s\n", st.id, i18n.T("host.verify.degraded_run_plugin"), reason)
		return exitRuntime
	}
	tokens := buildStartTokens(st.dir, st.manifest)
	tokens = append(tokens, extra...)
	// stdout 只含纯命令行文本(供 todo 8 的 wrapper 之类消费),
	// 非父进程语义写在 stderr 提示与 help 里,不污染命令输出。
	fmt.Fprintln(stdout, shellJoin(tokens))
	fmt.Fprint(stderr, i18n.T("host.run_plugin.note"))
	return exitOK
}

// cmdRenderUnit 输出 systemd 单元片段([Service] + ExecStart=),
// 供构建期 76-daedalus-plugin-gen.sh 拼接进 daedalus-<cap>.service。
// 与 run-plugin 同一前置:degraded 插件不产出可落盘的 ExecStart。
func cmdRenderUnit(stdout, stderr io.Writer, st pluginState) int {
	if st.manifest == nil || st.status != statusOK {
		reason := st.reason
		if reason == "" {
			reason = i18n.T("host.error.manifest_unavailable")
		}
		fmt.Fprintf(stderr, "daedalus-host: render-unit: %s %s: %s\n", st.id, i18n.T("host.verify.degraded_render_unit"), reason)
		return exitRuntime
	}
	tokens := buildStartTokens(st.dir, st.manifest)
	// 注释两行经 i18n.T(76 脚本只消费 ExecStart= 行,注释语言变化无影响);
	// [Service] 与 ExecStart= 行是 systemd 消费的协议文本,永不翻译。
	fmt.Fprint(stdout, i18n.T("host.render_unit.note1"))
	fmt.Fprint(stdout, i18n.T("host.render_unit.note2"))
	fmt.Fprintf(stdout, "[Service]\n")
	fmt.Fprintf(stdout, "ExecStart=%s\n", systemdJoin(tokens))
	return exitOK
}

// buildStartTokens 由 manifest 的 runtime/executable/entrypoint 构造 argv:
//   - native: [<插件目录>/<executable>, entrypoint...](Go 静态二进制直接 exec);
//   - deno  : [<deno>, "run", entrypoint..., <插件目录>/<executable>]
//     (entrypoint 携带 --allow-* 权限旗标,executable 是脚本路径,置于参数尾)。
//
// demo 构建(-tags demo,devMode==true)额外把 entrypoint 字符串里的镜像
// 路径 /opt/daedalus 与 /usr/local/bin 改写为 devPrefix 下的对应路径
// (native 偶尔也带镜像路径,因此无论 runtime 一律过 rewriteEntry);
// prod 构建 devMode==false 恒为编译期常量,重写分支被常量折叠整体消除,
// 行为与历史版本逐字节一致。devPrefix 为空串时改写结果是恒等替换,
// 现有 prod 断言(denoBinary + " run --allow-read=/opt/daedalus,/home …")
// 在两种 tag 下均不受影响。
func buildStartTokens(pluginDir string, m *plugin.Manifest) []string {
	// rewriteEntry 对单个 entrypoint 字符串做 dev 路径改写;
	// !devMode 时直接原样返回,prod 下整个分支被编译期折叠。
	rewriteEntry := func(s string) string {
		if !devMode {
			return s
		}
		s = strings.ReplaceAll(s, "/opt/daedalus", devPrefix+"/opt/daedalus")
		return strings.ReplaceAll(s, "/usr/local/bin", devPrefix+"/usr/local/bin")
	}
	entry := make([]string, len(m.Entrypoint))
	for i, e := range m.Entrypoint {
		entry[i] = rewriteEntry(e)
	}
	absExec := pluginDir + "/" + m.Executable // executable 已被 manifest 校验为安全相对路径
	switch m.Runtime {
	case plugin.RuntimeDeno:
		return append(append([]string{denoBinary, "run"}, entry...), absExec)
	case plugin.RuntimeNative:
		return append([]string{absExec}, entry...)
	default:
		// Manifest.Validate 已排除该分支;防御性返回,避免渲染出半截命令。
		return []string{absExec}
	}
}

// shellQuote 对含空白或 shell 元字符的 token 做 POSIX 单引号包裹。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !isSafeShellRune(r)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isSafeShellRune 是 shell 中无需引号的稳定字符集(与 systemd 侧共用)。
func isSafeShellRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case strings.ContainsRune(`@%+=:,./-_`, r):
		return true
	}
	return false
}

func shellJoin(tokens []string) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = shellQuote(t)
	}
	return strings.Join(parts, " ")
}

// systemdQuote 按 systemd ExecStart 的词法用 C 风格双引号包裹不稳 token。
// systemd 不做 shell 展开,双引号内只需转义 '"' 与 '\'。
func systemdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.IndexFunc(s, func(r rune) bool { return !isSafeShellRune(r) }) < 0 {
		return s
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func systemdJoin(tokens []string) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = systemdQuote(t)
	}
	return strings.Join(parts, " ")
}
