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

	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// denoBinary 是镜像内 Deno 运行时的固定路径(65-ai-safety.sh 安装位)。
const denoBinary = "/usr/local/bin/deno"

// cmdVerify 复用 plugin 包的已安装目录校验核心(与 zip 校验同规则:
// checksums 双向集合相等 + 逐条目 sha256 + manifest 规范化自摘要 + 可执行位)。
// 通过 exit 0;失败 exit 1 并把原因写到 stderr。
func cmdVerify(stdout, stderr io.Writer, st pluginState) int {
	if st.status == statusOK {
		fmt.Fprintf(stdout, "verify: %s 校验通过(sha256 全部匹配)\n", st.id)
		return exitOK
	}
	fmt.Fprintf(stderr, "daedalus-host: verify: %s 校验失败: %s\n", st.id, st.reason)
	return exitRuntime
}

// cmdRunPlugin 仅打印示例启动命令(可附额外参数拼在后面),永不 spawn。
// 损坏插件只输出 manifest 不承诺可运行性,因此拒绝为 degraded 插件打印(退出 1)。
func cmdRunPlugin(stdout, stderr io.Writer, st pluginState, extra []string) int {
	if st.manifest == nil || st.status != statusOK {
		reason := st.reason
		if reason == "" {
			reason = "manifest 不可用"
		}
		fmt.Fprintf(stderr, "daedalus-host: run-plugin: %s 处于 degraded 状态,拒绝产出启动命令: %s\n", st.id, reason)
		return exitRuntime
	}
	tokens := buildStartTokens(st.dir, st.manifest)
	tokens = append(tokens, extra...)
	// stdout 只含纯命令行文本(供 todo 8 的 wrapper 之类消费),
	// 非父进程语义写在 stderr 提示与 help 里,不污染命令输出。
	fmt.Fprintln(stdout, shellJoin(tokens))
	fmt.Fprintf(stderr, "note: host 不是进程父,以上仅为构造出的启动命令;systemd 按 ExecStart 执行\n")
	return exitOK
}

// cmdRenderUnit 输出 systemd 单元片段([Service] + ExecStart=),
// 供构建期 76-daedalus-plugin-gen.sh 拼接进 daedalus-<cap>.service。
// 与 run-plugin 同一前置:degraded 插件不产出可落盘的 ExecStart。
func cmdRenderUnit(stdout, stderr io.Writer, st pluginState) int {
	if st.manifest == nil || st.status != statusOK {
		reason := st.reason
		if reason == "" {
			reason = "manifest 不可用"
		}
		fmt.Fprintf(stderr, "daedalus-host: render-unit: %s 处于 degraded 状态,拒绝渲染单元: %s\n", st.id, reason)
		return exitRuntime
	}
	tokens := buildStartTokens(st.dir, st.manifest)
	fmt.Fprintf(stdout, "# 由 daedalus-host render-unit 生成(计划决策 16/22):内容来自插件 manifest,请勿手改\n")
	fmt.Fprintf(stdout, "# 宿主不是进程父:systemd 直接执行以下 ExecStart,沙箱语义由 .service.d/*.conf drop-in 保留\n")
	fmt.Fprintf(stdout, "[Service]\n")
	fmt.Fprintf(stdout, "ExecStart=%s\n", systemdJoin(tokens))
	return exitOK
}

// buildStartTokens 由 manifest 的 runtime/executable/entrypoint 构造 argv:
//   - native: [<插件目录>/<executable>, entrypoint...](Go 静态二进制直接 exec);
//   - deno  : [<deno>, "run", entrypoint..., <插件目录>/<executable>]
//     (entrypoint 携带 --allow-* 权限旗标,executable 是脚本路径,置于参数尾)。
func buildStartTokens(pluginDir string, m *plugin.Manifest) []string {
	absExec := pluginDir + "/" + m.Executable // executable 已被 manifest 校验为安全相对路径
	switch m.Runtime {
	case plugin.RuntimeDeno:
		tokens := append([]string{denoBinary, "run"}, m.Entrypoint...)
		return append(tokens, absExec)
	case plugin.RuntimeNative:
		return append([]string{absExec}, m.Entrypoint...)
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
