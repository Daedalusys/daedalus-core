// daedalus-host CLI 的端到端测试:直接调用 run(),断言退出码与输出。
// 夹具走真实安装链(源目录 → plugin.Pack → plugin.ExtractZip 落盘),
// 与镜像构建期把 .daedalus 包装入 /opt/daedalus/plugins/<id>/ 的路径同构。
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/audit"
	"github.com/daedalus-os/daedalus/core/internal/i18n"
	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// TestMain 在全部测试前把 locale 锁到 en_US:防止开发者本机 zh 环境
// 泄漏进断言(与 tests/deno/main.test.ts 的既有先例一致)。
// i18n.Init 幂等且包级状态一次定死,这里最先执行即生效于整个测试进程。
// 个别中英切换测试用 t.Setenv + 重置包级状态自行接管。
func TestMain(m *testing.M) {
	os.Setenv("LC_ALL", "en_US.UTF-8")
	i18n.ResetForTest()
	i18n.Init()
	os.Exit(m.Run())
}

// switchLocaleForTest 把 i18n 切到指定 locale 并注册恢复:
// 先重置包级状态(t.Setenv 保证测试结束后 env 还原,再恢复状态),
// 供中英表头切换等 locale 敏感测试使用。
func switchLocaleForTest(t *testing.T, lcAll string) {
	t.Helper()
	t.Setenv("LC_ALL", lcAll)
	i18n.ResetForTest()
	t.Cleanup(func() {
		i18n.ResetForTest()
		i18n.Init() // 回到 TestMain 锁定的 en_US 基线
	})
	i18n.Init()
}

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

// i18nManifest 是带 i18n 声明的 manifest 变体(覆盖 list 的 I18N 列与
// inspect 的 i18n 段展示)。
func i18nManifest() *plugin.Manifest {
	m := *nativeManifest()
	m.ID = "daedalus.i18n"
	m.Name = "I18N Fixture"
	m.I18N = []string{"en_US", "zh_CN"}
	return &m
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
	// 表头经 i18n.T 取期望值(与生产同源),7 列制含 I18N。
	for _, want := range []string{
		i18n.T("host.table.id"), i18n.T("host.table.status"), i18n.T("host.table.i18n"),
		"daedalus.smoke", "Smoke Plugin", "0.1.0", "capability", "native",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺少 %q:\n%s", want, out)
		}
	}
	// 无 i18n 声明的插件,I18N 列显示 "-"(按空白归一化匹配,免疫 tabwriter 补格)。
	if !normalizeRow(out, "daedalus.smoke", "Smoke Plugin\t0.1.0\tcapability\tnative\t-\tok") {
		t.Errorf("smoke 行 I18N 列应为 -:\n%s", out)
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
	if good == "" || !normalizeRow(out, "daedalus.smoke", "ok") {
		t.Error("健康插件状态列缺失")
	}
}

// normalizeRow 断言输出中存在"以 first 开头、以 rest 字段序列结尾"的行:
// 字段间空白任意(免疫 tabwriter 补格),行内其他列(如 I18N 列)不参与匹配。
// rest 里的 \t 表示字段边界;first 作为行首锚点(插件 id)。
func normalizeRow(out, first, rest string) bool {
	parts := strings.Split(rest, "\t")
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = regexp.QuoteMeta(p)
	}
	pattern := `(?m)^` + regexp.QuoteMeta(first) + `\s+.*` + strings.Join(quoted, `\s+`) + `\s*$`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(out)
}

// TestList_I18NColumn 显示 locale 列表;无声明的插件显示 "-"。
func TestList_I18NColumn(t *testing.T) {
	// Given: 一个带 i18n 声明的插件 + 一个无声明的插件。
	root := t.TempDir()
	installPlugin(t, root, i18nManifest())
	installPlugin(t, root, nativeManifest())

	// When: list。
	code, out, errOut := runCapture(t, "list", "-dir", root)
	if code != exitOK {
		t.Fatalf("list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}

	// Then: 有声明行显示逗号列表,无声明行显示 "-";表头列存在。
	if !strings.Contains(out, i18n.T("host.table.i18n")) {
		t.Errorf("表头缺少 I18N 列:\n%s", out)
	}
	if !normalizeRow(out, "daedalus.i18n", "I18N Fixture\t0.1.0\tcapability\tnative\ten_US, zh_CN\tok") {
		t.Errorf("i18n 插件行应显示 locale 列表:\n%s", out)
	}
	if !normalizeRow(out, "daedalus.smoke", "Smoke Plugin\t0.1.0\tcapability\tnative\t-\tok") {
		t.Errorf("无声明插件 I18N 列应为 -:\n%s", out)
	}
}

// TestList_LocaleSwitchHeader 验证 LC_ALL 切换时表头跟随中英切换。
func TestList_LocaleSwitchHeader(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, nativeManifest())

	// zh_CN:表头为中文译名(标识/名称/…/多语言/状态)。
	switchLocaleForTest(t, "zh_CN.UTF-8")
	code, out, errOut := runCapture(t, "list", "-dir", root)
	if code != exitOK {
		t.Fatalf("zh list 退出码 = %d; stderr: %s", code, errOut)
	}
	for _, want := range []string{"标识", "名称", "版本", "类型", "运行时", "多语言", "状态"} {
		if !strings.Contains(out, want) {
			t.Errorf("zh 表头缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nID\t") {
		t.Errorf("zh 表头不应再含英文列名:\n%s", out)
	}

	// en_US:表头为英文列名。
	switchLocaleForTest(t, "en_US.UTF-8")
	code, out, _ = runCapture(t, "list", "-dir", root)
	if code != exitOK {
		t.Fatalf("en list 退出码 = %d", code)
	}
	for _, want := range []string{"ID", "NAME", "VERSION", "TYPE", "RUNTIME", "I18N", "STATUS"} {
		if !strings.Contains(out, want) {
			t.Errorf("en 表头缺少 %q:\n%s", want, out)
		}
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
		i18n.T("host.inspect.id") + "          daedalus.smoke",
		i18n.T("host.inspect.name") + "        Smoke Plugin",
		i18n.T("host.inspect.version") + "     0.1.0",
		i18n.T("host.inspect.type") + "        capability",
		i18n.T("host.inspect.runtime") + "     native",
		i18n.T("host.inspect.executable") + "  bin/main",
		"smoke_ping", "read:", "/usr/bin/git", // tools + permissions
		i18n.T("host.inspect.checksums") + "   2 条", "sha256:",
		i18n.T("host.inspect.integrity"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect 输出缺少 %q:\n%s", want, out)
		}
	}
}

// TestInspect_I18NSection:manifest 声明了 i18n 时输出 i18n 段,无声明的整段省略。
func TestInspect_I18NSection(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, root, i18nManifest())
	installPlugin(t, root, nativeManifest())

	// 有声明:列出 supported locale。
	code, out, errOut := runCapture(t, "inspect", "daedalus.i18n", "-dir", root)
	if code != exitOK {
		t.Fatalf("inspect i18n 插件退出码 = %d; stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "i18n:\n  supported:  en_US, zh_CN\n") {
		t.Errorf("应含 i18n 段与 supported 列表:\n%s", out)
	}

	// 无声明:整段省略。
	code, out, errOut = runCapture(t, "inspect", "daedalus.smoke", "-dir", root)
	if code != exitOK {
		t.Fatalf("inspect smoke 退出码 = %d; stderr: %s", code, errOut)
	}
	if strings.Contains(out, "i18n:\n") {
		t.Errorf("无声明插件不应输出 i18n 段:\n%s", out)
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
	if code != exitOK || !strings.Contains(out, i18n.T("host.verify.pass", "daedalus.smoke")) {
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
	if code != exitRuntime || !strings.Contains(errOut, i18n.T("host.verify.fail", "")) {
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
	if !strings.Contains(errOut, i18n.T("host.run_plugin.note")) {
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
	// 拒绝文案经 i18n.T 与生产同源。
	if !strings.Contains(errOut, i18n.T("host.verify.degraded_run_plugin")) {
		t.Errorf("拒绝原因应含 degraded 文案: %q", errOut)
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
	// 注释行与 systemd 协议行经 i18n.T 与生产同源;ExecStart 行保持纯协议文本。
	if !strings.Contains(out, i18n.T("host.render_unit.note2")) {
		t.Errorf("片段注释应含 render_unit.note2:\n%s", out)
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
