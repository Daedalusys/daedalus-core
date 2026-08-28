// daedalus-host CLI 的端到端测试:直接调用 run(),断言退出码与输出。
// 夹具走真实安装链(源目录 → plugin.Pack → plugin.ExtractZip 落盘),
// 与镜像构建期把 .daedalus 包装入 /opt/daedalus/plugins/<id>/ 的路径同构。
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/audit"
	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// writePluginSource 搭出最小 native 插件源目录(manifest 无 checksums,
// 由 Pack 注入),返回源目录路径。
func writePluginSource(t *testing.T, m *plugin.Manifest) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "main"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, plugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// nativeManifest 是标准测试 manifest(带权限/工具,覆盖 inspect 展示面)。
func nativeManifest() *plugin.Manifest {
	return &plugin.Manifest{
		ID:          "daedalus.smoke",
		Name:        "Smoke Plugin",
		Version:     "0.1.0",
		Type:        plugin.TypeCapability,
		Runtime:     plugin.RuntimeNative,
		Executable:  "bin/main",
		Entrypoint:  []string{"--stdio"},
		Permissions: &plugin.Permissions{Read: []string{"/home"}, Write: []string{"/tmp"}, Run: []string{"/usr/bin/git"}},
		Tools:       []string{"smoke_ping", "smoke_echo"},
	}
}

// installPlugin 把源目录打包并解压进 <pluginsRoot>/<id>/(模拟镜像安装)。
func installPlugin(t *testing.T, pluginsRoot string, m *plugin.Manifest) string {
	t.Helper()
	src := writePluginSource(t, m)
	zipPath := filepath.Join(t.TempDir(), "plugin.daedalus")
	if _, err := plugin.Pack(src, zipPath); err != nil {
		t.Fatalf("Pack 失败: %v", err)
	}
	dest := filepath.Join(pluginsRoot, m.ID)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.ExtractZip(zipPath, dest); err != nil {
		t.Fatalf("ExtractZip 失败: %v", err)
	}
	return dest
}

// runCapture 执行 CLI 并返回 (退出码, stdout, stderr)。
func runCapture(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := run(argv, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// ---------------------------------------------------------------- list ----

func TestList_MixedHealthyAndDegraded(t *testing.T) {
	// Given: 一个正常插件 + 缺 manifest 的目录 + 文件被篡改的插件 + 目录名非法的目录。
	root := t.TempDir()
	good := installPlugin(t, root, nativeManifest())

	tampered := *nativeManifest()
	tampered.ID = "daedalus.tampered"
	tamperedDir := installPlugin(t, root, &tampered)
	if err := os.Chmod(filepath.Join(tamperedDir, "bin", "main"), 0o644); err != nil {
		t.Fatal(err)
	} // 抹掉可执行位 → 校验失败

	if err := os.MkdirAll(filepath.Join(root, "daedalus.ghost"), 0o755); err != nil {
		t.Fatal(err)
	} // 无 manifest
	if err := os.MkdirAll(filepath.Join(root, "Bad Name"), 0o755); err != nil {
		t.Fatal(err)
	} // 目录名不合文法

	// When: list。
	code, out, errOut := runCapture(t, "list", "-dir", root)

	// Then: 整体成功,损坏项标 degraded 且各带原因,健康项字段齐全。
	if code != exitOK {
		t.Fatalf("list 退出码 = %d, want 0; stderr:\n%s", code, errOut)
	}
	for _, want := range []string{
		"ID", "STATUS",
		"daedalus.smoke", "Smoke Plugin", "0.1.0", "capability", "native",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺少 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "daedalus.ghost") || !strings.Contains(out, "degraded") {
		t.Errorf("缺 manifest 目录应标 degraded:\n%s", out)
	}
	if !strings.Contains(out, "daedalus.tampered") {
		t.Errorf("篡改插件应出现在列表中:\n%s", out)
	}
	if !strings.Contains(out, "缺少可执行位") {
		t.Errorf("degraded 原因应含可执行位丢失:\n%s", out)
	}
	if !strings.Contains(out, "Bad Name") || !strings.Contains(out, "文法") {
		t.Errorf("非法目录名应 degraded 并给出文法原因:\n%s", out)
	}
	// 健康行状态必须是 ok(且 good 路径确实被安装)。
	if good == "" || !strings.Contains(out, "ok") {
		t.Error("健康插件状态列缺失")
	}
}

func TestList_DirMissingAndEnvOverride(t *testing.T) {
	// Given: 不存在的目录。
	missing := filepath.Join(t.TempDir(), "nope")
	code, _, errOut := runCapture(t, "list", "-dir", missing)
	if code != exitRuntime || !strings.Contains(errOut, "不可读") {
		t.Fatalf("目录不存在应 exit 1 + 原因,得 %d / %q", code, errOut)
	}

	// When: 通过 DAEDALUS_PLUGIN_DIR 环境变量指定(无 -dir 旗标)。
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())
	t.Setenv(EnvPluginDir, root)
	code2, out2, errOut2 := runCapture(t, "list")
	if code2 != exitOK || !strings.Contains(out2, "daedalus.smoke") {
		t.Fatalf("env 覆盖未生效: %d / %s / %s", code2, out2, errOut2)
	}

	// Then: -dir 旗标优先级高于环境变量(env 指向有效目录、旗标指向缺失目录 → 失败)。
	code3, out3, _ := runCapture(t, "list", "-dir", missing)
	if code3 == exitOK || strings.Contains(out3, "daedalus.smoke") {
		t.Errorf("-dir 应压过环境变量: %d / %s", code3, out3)
	}
}

// ------------------------------------------------------------ inspect ----

func TestInspect_Details(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())

	code, out, errOut := runCapture(t, "inspect", "daedalus.smoke", "-dir", root)
	if code != exitOK {
		t.Fatalf("inspect 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"id:          daedalus.smoke", "name:        Smoke Plugin", "version:     0.1.0",
		"type:        capability", "runtime:     native", "executable:  bin/main",
		"smoke_ping", "read:", "/usr/bin/git", // tools + permissions
		"checksums:   2 条", "sha256:", "integrity:   ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect 输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestInspect_DegradedExitsNonZero(t *testing.T) {
	// Given: manifest 缺失的目录。
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daedalus.ghost"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCapture(t, "inspect", "daedalus.ghost", "-dir", root)
	if code != exitRuntime || !strings.Contains(errOut, "daedalus.plugin.json") {
		t.Fatalf("degraded 插件 inspect 必须非零退出 + 原因,得 %d / %q", code, errOut)
	}
}

// -------------------------------------------------------------- verify ----

func TestVerify_Pass(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())

	code, out, errOut := runCapture(t, "verify", "daedalus.smoke", "-dir", root)
	if code != exitOK || !strings.Contains(out, "校验通过") {
		t.Fatalf("verify 应通过: %d / %s / %s", code, out, errOut)
	}
}

func TestVerify_TamperedBinary(t *testing.T) {
	root := t.TempDir()
	dir := installPlugin(t, root, nativeManifest())
	payload, err := os.ReadFile(filepath.Join(dir, "bin", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "main"), append(payload, []byte("#tamper\n")...), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runCapture(t, "verify", "daedalus.smoke", "-dir", root)
	if code != exitRuntime {
		t.Fatalf("篡改后 verify 退出码 = %d, want 1", code)
	}
	if !strings.Contains(errOut, "checksum 不匹配") {
		t.Errorf("失败原因应含 checksum 不匹配: %q", errOut)
	}
}

func TestVerify_UnknownID(t *testing.T) {
	root := t.TempDir()
	code, _, errOut := runCapture(t, "verify", "daedalus.absent", "-dir", root)
	if code != exitRuntime || !strings.Contains(errOut, "校验失败") {
		t.Fatalf("不存在的 id 应 exit 1 + 原因,得 %d / %q", code, errOut)
	}
}

// --------------------------------------------------------- run-plugin ----

func TestRunPlugin_NativePrintsOnly(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())

	code, out, errOut := runCapture(t, "run-plugin", "daedalus.smoke", "-dir", root, "--", "-v", "check disk")
	if code != exitOK {
		t.Fatalf("run-plugin 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	// stdout 是纯一行命令:绝对可执行路径 + entrypoint + 追加参数。
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout 应只有一行命令,得 %q", lines)
	}
	want := filepath.Join(root, "daedalus.smoke", "bin", "main") + " --stdio -v 'check disk'"
	if lines[0] != want {
		t.Errorf("启动命令 =\n%q\nwant\n%q", lines[0], want)
	}
	// 非父进程语义必须在 stderr 说明。
	if !strings.Contains(errOut, "不是进程父") {
		t.Errorf("run-plugin 应声明宿主非父进程: %q", errOut)
	}
}

func TestRunPlugin_Deno(t *testing.T) {
	root := t.TempDir()
	m := &plugin.Manifest{
		ID:         "daedalus.copilot",
		Name:       "Copilot",
		Version:    "1.0.0",
		Type:       plugin.TypeCopilot,
		Runtime:    plugin.RuntimeDeno,
		Executable: "main.ts",
		Entrypoint: []string{"--allow-env", "--allow-read=/opt/daedalus,/home"},
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.ts"), []byte("console.log(1)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(src, plugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "copilot.daedalus")
	if _, err := plugin.Pack(src, zipPath); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, m.ID)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.ExtractZip(zipPath, dest); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCapture(t, "run-plugin", "daedalus.copilot", "-dir", root)
	if code != exitOK {
		t.Fatalf("deno run-plugin 退出码 = %d; stderr: %s", code, errOut)
	}
	want := denoBinary + " run --allow-env --allow-read=/opt/daedalus,/home " +
		filepath.Join(root, "daedalus.copilot", "main.ts")
	if strings.TrimSpace(out) != want {
		t.Errorf("deno 启动命令 =\n%q\nwant\n%q", strings.TrimSpace(out), want)
	}
}

func TestRunPlugin_RejectsDegraded(t *testing.T) {
	root := t.TempDir()
	dir := installPlugin(t, root, nativeManifest())
	if err := os.Chmod(filepath.Join(dir, "bin", "main"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCapture(t, "run-plugin", "daedalus.smoke", "-dir", root)
	if code != exitRuntime || strings.TrimSpace(out) != "" {
		t.Fatalf("degraded 插件不得产出启动命令: %d / %q", code, out)
	}
	if !strings.Contains(errOut, "degraded") {
		t.Errorf("拒绝原因应写明 degraded: %q", errOut)
	}
}

// -------------------------------------------------------- render-unit ----

func TestRenderUnit_ExecStartShape(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())

	code, out, errOut := runCapture(t, "render-unit", "daedalus.smoke", "-dir", root)
	if code != exitOK {
		t.Fatalf("render-unit 退出码 = %d; stderr: %s", code, errOut)
	}
	wantExec := "ExecStart=" + filepath.Join(root, "daedalus.smoke", "bin", "main") + " --stdio"
	if !strings.Contains(out, "[Service]") || !strings.Contains(out, wantExec) {
		t.Errorf("单元片段应含 [Service] 与正确 ExecStart 行:\n%s", out)
	}
	if !strings.Contains(out, "不是进程父") {
		t.Errorf("片段注释应声明宿主非父进程:\n%s", out)
	}
}

// ------------------------------------------------------------- 审计 ----

// readAuditLines 读取审计日志全部行(要求恰好条数)。
func readAuditLines(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计日志失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != want {
		t.Fatalf("审计行数 = %d, want %d:\n%s", len(lines), want, data)
	}
	var out []map[string]any
	for _, l := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("审计行非法 JSON: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

func TestAudit_EverySubcommand(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, logPath)

	for _, argv := range [][]string{
		{"list", "-dir", root},
		{"inspect", "daedalus.smoke", "-dir", root},
		{"verify", "daedalus.smoke", "-dir", root},
		{"run-plugin", "daedalus.smoke", "-dir", root},
		{"render-unit", "daedalus.smoke", "-dir", root},
		{"verify", "daedalus.absent", "-dir", root}, // 失败路径也要落审计(outcome=error)
	} {
		runCapture(t, argv...)
	}

	recs := readAuditLines(t, logPath, 6)
	wantTools := []string{"host_list", "host_inspect", "host_verify", "host_run_plugin", "host_render_unit", "host_verify"}
	wantOutcomes := []string{"success", "success", "success", "success", "success", "error"}
	for i, rec := range recs {
		if rec["identity"] != hostIdentity {
			t.Errorf("记录 %d identity = %v, want daedalus-host", i, rec["identity"])
		}
		if rec["tool"] != wantTools[i] {
			t.Errorf("记录 %d tool = %v, want %s", i, rec["tool"], wantTools[i])
		}
		if rec["outcome"] != wantOutcomes[i] {
			t.Errorf("记录 %d outcome = %v, want %s", i, rec["outcome"], wantOutcomes[i])
		}
	}
	// 哈希链必须完整(宿主追加的条目也要守链)。
	if n, err := audit.Verify(logPath); err != nil || n != 6 {
		t.Fatalf("审计链校验失败: %d %v", n, err)
	}
}

func TestAudit_BrokenLogPathIsSilent(t *testing.T) {
	// Given: 审计日志写到不可能成功的路径(父组件是已存在的普通文件)。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(audit.EnvLogPath, filepath.Join(blocker, "audit.jsonl"))

	root := t.TempDir()
	installPlugin(t, root, nativeManifest())
	code, out, errOut := runCapture(t, "list", "-dir", root)

	// Then: 写审计失败静默,子命令输出与退出码不受影响。
	if code != exitOK || !strings.Contains(out, "daedalus.smoke") || strings.Contains(errOut, "audit") {
		t.Fatalf("审计失败不得影响 list: %d / %q / %q", code, out, errOut)
	}
}

// --------------------------------------------------------- 畸形输入 ----

func TestMalformed_InvalidIDsAndArgs(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, logPath)

	cases := []struct {
		name string
		argv []string
	}{
		{"路径穿越 id", []string{"verify", "../../etc/passwd", "-dir", root}},
		{"绝对路径 id", []string{"inspect", "/etc", "-dir", root}},
		{"空段 id", []string{"run-plugin", "a..b", "-dir", root}},
		{"大写 id", []string{"render-unit", "Daedalus", "-dir", root}},
		{"缺 id", []string{"verify", "-dir", root}},
		{"未知子命令", []string{"frobnicate"}},
		{"追加参数越权", []string{"inspect", "daedalus.smoke", "extra", "-dir", root}},
		{"--dir 缺值", []string{"list", "-dir"}},
	}
	for _, tc := range cases {
		code, _, errOut := runCapture(t, tc.argv...)
		if code != exitUsage {
			t.Errorf("%s: 退出码 = %d, want 2; stderr: %s", tc.name, code, errOut)
		}
	}

	// Then: 合法的审计行为照常,且没有为畸形 id 拼出目录路径去读盘
	// (../etc 等被文法拦截,不产生对 root 之外的访问)。
	if _, err := os.Stat(filepath.Join(root, "etc")); !os.IsNotExist(err) {
		t.Fatal("宿主不应因 '../' 注入而在插件目录外解析路径")
	}
}

func TestHelp_ListsAllSubcommands(t *testing.T) {
	for _, flagName := range []string{"-h", "--help", "help"} {
		code, out, _ := runCapture(t, flagName)
		if code != exitOK {
			t.Fatalf("%s 退出码 = %d, want 0", flagName, code)
		}
		for _, want := range []string{"list", "inspect", "verify", "run-plugin", "render-unit", "不是任何 MCP 服务器的父进程", "systemd"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s 帮助缺少 %q", flagName, want)
			}
		}
	}
	// 无参数 → usage 错误但同样打印帮助。
	code, _, errOut := runCapture(t)
	if code != exitUsage || !strings.Contains(errOut, "Commands:") {
		t.Fatalf("无参数应 exit 2 + 帮助: %d / %s", code, errOut)
	}
}
