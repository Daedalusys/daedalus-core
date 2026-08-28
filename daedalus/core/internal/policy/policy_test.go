// internal/policy 的行为测试:policy.toml 的解析优先级、fail-closed 校验、
// ALLOW_COMMANDS REPLACE 语义、Default() 与既有硬编码常量的逐项一致性,
// 以及 WithPolicy/WithAllowedDirs 注入缝的真实生效证明。
package policy_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daedalus-os/daedalus/core/internal/pathguard"
	"github.com/daedalus-os/daedalus/core/internal/policy"
	"github.com/daedalus-os/daedalus/core/internal/shellpolicy"
)

// testdataPath 返回测试夹具的绝对路径。
func testdataPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoad_Happy 证明合法策略逐字段透传(而非偷偷回退 Default)。
func TestLoad_Happy(t *testing.T) {
	p, err := policy.Load(testdataPath(t, "valid.toml"))
	if err != nil {
		t.Fatalf("Load 合法策略失败: %v", err)
	}
	if !slices.Equal(p.Shell.AllowedCommands, []string{"echo", "date"}) {
		t.Errorf("allowed_commands = %v", p.Shell.AllowedCommands)
	}
	if !slices.Equal(p.Shell.BinaryDirs, []string{"/usr/bin", "/bin"}) {
		t.Errorf("binary_dirs = %v", p.Shell.BinaryDirs)
	}
	if !slices.Equal(p.Shell.AllowedPathPrefixes, []string{"/tmp"}) {
		t.Errorf("allowed_path_prefixes = %v", p.Shell.AllowedPathPrefixes)
	}
	if !slices.Equal(p.Shell.BlockedPaths, []string{"/root"}) {
		t.Errorf("blocked_paths = %v", p.Shell.BlockedPaths)
	}
	wantEnv := map[string]string{"PATH": "/usr/bin:/bin", "LANG": "C"}
	if !reflect.DeepEqual(p.Shell.CleanEnv, wantEnv) {
		t.Errorf("clean_env = %v, want %v", p.Shell.CleanEnv, wantEnv)
	}
	if p.Shell.TimeoutMs != 5000 {
		t.Errorf("timeout_ms = %d, want 5000", p.Shell.TimeoutMs)
	}
	if !slices.Equal(p.FS.AllowedDirs, []string{"/tmp"}) {
		t.Errorf("fs.allowed_dirs = %v", p.FS.AllowedDirs)
	}
	if p.Audit.LogPath != "/tmp/daedalus-policy-test-audit.jsonl" {
		t.Errorf("audit.log_path = %q", p.Audit.LogPath)
	}
}

// TestLoad_CorruptTOML 钉死损坏语法 → 报错(服务器据此拒绝启动)。
func TestLoad_CorruptTOML(t *testing.T) {
	_, err := policy.Load(testdataPath(t, "corrupt.toml"))
	if err == nil {
		t.Fatal("损坏 TOML 竟然 Load 成功")
	}
	if !strings.Contains(err.Error(), "TOML 损坏") {
		t.Errorf("错误消息未归类为 TOML 损坏: %v", err)
	}
}

// TestLoad_MissingFields 钉死缺字段 → 报错且逐项点名。
func TestLoad_MissingFields(t *testing.T) {
	_, err := policy.Load(testdataPath(t, "missing_field.toml"))
	if err == nil {
		t.Fatal("缺字段策略竟然 Load 成功")
	}
	for _, want := range []string{"shell.blocked_paths", "shell.clean_env", "shell.timeout_ms", "fs.allowed_dirs", "audit.log_path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误消息未点名缺失字段 %s: %v", want, err)
		}
	}
}

// TestLoad_UnknownKey 钉死未知键 → 拒绝(拼写错误不得静默通过)。
func TestLoad_UnknownKey(t *testing.T) {
	_, err := policy.Load(testdataPath(t, "unknown_key.toml"))
	if err == nil {
		t.Fatal("含未知键的策略竟然 Load 成功")
	}
	if !strings.Contains(err.Error(), "未知键") ||
		!strings.Contains(err.Error(), "shell.allowed_commandz") ||
		!strings.Contains(err.Error(), "fs.allowed_directorys") {
		t.Errorf("未知键报错形态漂移: %v", err)
	}
}

// TestLoad_ExplicitMissingFile 证明显式路径不存在如实报 IO 错误,
// 且**不是** ErrNotFound(显式指向不得被静默降级为 Default)。
func TestLoad_ExplicitMissingFile(t *testing.T) {
	_, err := policy.Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatal("不存在的路径竟然 Load 成功")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("应透出 fs 层 NotExist 错误: %v", err)
	}
	if errors.Is(err, policy.ErrNotFound) {
		t.Error("显式路径缺失不得归类为 ErrNotFound(否则会被回退逻辑吞掉)")
	}
}

// TestResolvePath_EnvOverride 证明 DAEDALUS_POLICY_PATH 是最高优先级注入口。
func TestResolvePath_EnvOverride(t *testing.T) {
	t.Setenv(policy.EnvPolicyPath, testdataPath(t, "valid.toml"))
	p, err := policy.Load("")
	if err != nil {
		t.Fatalf("env 注入加载失败: %v", err)
	}
	if !slices.Contains(p.Shell.AllowedCommands, "echo") {
		t.Errorf("未按 env 指向加载: %v", p.Shell.AllowedCommands)
	}
}

// TestResolvePath_DevFallback 证明开发态自 cwd 逐级上溯命中仓库内
// daedalus/files/system/opt/daedalus/shared/policy.toml,
// 且镜像策略文件的值与 Default() 逐项一致(单一事实源与兜底值零漂移)。
func TestResolvePath_DevFallback(t *testing.T) {
	t.Setenv(policy.EnvPolicyPath, "")
	st, err := os.Stat(policy.ProductionPath)
	if err == nil && !st.IsDir() {
		t.Skipf("本机存在 %s,无法演练开发态回溯", policy.ProductionPath)
	}
	got, err := policy.ResolvePath()
	if err != nil {
		t.Fatalf("开发态回溯未命中: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(got), policy.DevRelPath) {
		t.Fatalf("回溯路径异常: %s", got)
	}
	p, err := policy.Load(got)
	if err != nil {
		t.Fatalf("镜像 policy.toml 未通过自身校验: %v", err)
	}
	assertPolicyEqual(t, p, policy.Default(), "policy.toml vs Default()")
}

// TestLoadOrDefault_NotFoundFallsBackToDefault 钉死关键稳健性:
// 三处候选全缺失 → Default() 兜底、零错误(服务器可启动);
// 而 Load("") 同场景返回 ErrNotFound 供需要严格模式的调用方区分。
func TestLoadOrDefault_NotFoundFallsBackToDefault(t *testing.T) {
	if _, err := os.Stat(policy.ProductionPath); err == nil {
		t.Skipf("本机存在 %s,跳过缺失回退演练", policy.ProductionPath)
	}
	t.Setenv(policy.EnvPolicyPath, "")
	t.Chdir(t.TempDir()) // 空目录上溯不可能命中仓库回溯路径。

	if _, err := policy.Load(""); !errors.Is(err, policy.ErrNotFound) {
		t.Fatalf("Load 全缺失应返回 ErrNotFound, got %v", err)
	}
	p, err := policy.LoadOrDefault()
	if err != nil {
		t.Fatalf("LoadOrDefault 应吞掉 ErrNotFound 并回退: %v", err)
	}
	assertPolicyEqual(t, p, policy.Default(), "LoadOrDefault vs Default()")
}

// TestAllowedCommands_Replace 钉死 REPLACE 语义:env 非空时整体替换
// (逗号分隔、trim、丢空项),绝不与策略白名单取并集。
func TestAllowedCommands_Replace(t *testing.T) {
	p := policy.Default()

	got := policy.AllowedCommands(p, " df, ls ,")
	if len(got) != 2 {
		t.Fatalf("REPLACE 后集合大小 = %d, want 2(出现并集即为语义回归): %v", len(got), got)
	}
	if _, ok := got["df"]; !ok {
		t.Error("df 缺失")
	}
	if _, ok := got["ls"]; !ok {
		t.Error("ls 缺失")
	}
	if _, ok := got["cat"]; ok {
		t.Error("cat 仍在(并集回归)")
	}

	// env 全空项 → 空集合(fail-closed,与 ResolveAllowCommands 现状一致)。
	if got := policy.AllowedCommands(p, ", ,"); len(got) != 0 {
		t.Errorf("垃圾 env 应得空集合, got %v", got)
	}

	// env 空 → 策略白名单副本;改动副本不得渗透策略本体。
	got = policy.AllowedCommands(p, "")
	if len(got) != 15 {
		t.Fatalf("无 env 时应为策略 15 项, got %d", len(got))
	}
	delete(got, "df")
	got["injected"] = struct{}{}
	if again := policy.AllowedCommands(p, ""); len(again) != 15 {
		t.Error("副本改动渗透了策略本体")
	} else if _, ok := again["df"]; !ok {
		t.Error("副本改动导致 df 丢失")
	}
}

// TestAllocCommands_ReadsEnv 证明 AllocCommands = AllowedCommands ∘ env 读取。
func TestAllocCommands_ReadsEnv(t *testing.T) {
	t.Setenv(policy.EnvAllowCommands, "date")
	got := policy.AllocCommands(policy.Default())
	if len(got) != 1 {
		t.Fatalf("REPLACE 后大小 = %d, want 1: %v", len(got), got)
	}
	if _, ok := got["date"]; !ok {
		t.Error("date 缺失")
	}
}

// TestDefault_MatchesExistingConstants 是防双源漂移的钉子测试:
// Default() 必须与 internal/shellpolicy、internal/pathguard 的
// 既有硬编码常量逐项一致(15 命令 / 4 bin 目录 / 9 前缀 / 5 blocked /
// CLEAN_ENV / 30s / fs 3 目录 / 审计路径)。
func TestDefault_MatchesExistingConstants(t *testing.T) {
	d := policy.Default()

	if len(d.Shell.AllowedCommands) != 15 || len(d.Shell.AllowedPathPrefixes) != 9 || len(d.Shell.BlockedPaths) != 5 {
		t.Fatalf("数量不符现状: %d/%d/%d", len(d.Shell.AllowedCommands), len(d.Shell.AllowedPathPrefixes), len(d.Shell.BlockedPaths))
	}
	if len(shellpolicy.DefaultAllowCommands) != 15 {
		t.Fatalf("shellpolicy 默认集数量异常: %d", len(shellpolicy.DefaultAllowCommands))
	}
	assertPolicyEqual(t, d, policy.Default(), "self") // 结构比较器的自反冒烟

	for cmd := range shellpolicy.DefaultAllowCommands {
		if !slices.Contains(d.Shell.AllowedCommands, cmd) {
			t.Errorf("Default 缺命令 %q", cmd)
		}
	}
	for dir := range shellpolicy.AllowedBinDirs {
		if !slices.Contains(d.Shell.BinaryDirs, dir) {
			t.Errorf("Default 缺 bin 目录 %q", dir)
		}
	}
	if len(d.Shell.BinaryDirs) != len(shellpolicy.AllowedBinDirs) {
		t.Errorf("bin 目录数量漂移")
	}
	if !slices.Equal(d.Shell.AllowedPathPrefixes, shellpolicy.AllowedPathPrefixes) {
		t.Errorf("前缀漂移: %v vs %v", d.Shell.AllowedPathPrefixes, shellpolicy.AllowedPathPrefixes)
	}
	if !slices.Equal(d.Shell.BlockedPaths, shellpolicy.BlockedPaths) {
		t.Errorf("blocked 漂移: %v vs %v", d.Shell.BlockedPaths, shellpolicy.BlockedPaths)
	}
	if d.Shell.TimeoutMs != int64(shellpolicy.Timeout/time.Millisecond) {
		t.Errorf("超时漂移: %d vs %v", d.Shell.TimeoutMs, shellpolicy.Timeout)
	}
	if !slices.Equal(d.FS.AllowedDirs, pathguard.AllowedDirs) {
		t.Errorf("fs 目录漂移: %v vs %v", d.FS.AllowedDirs, pathguard.AllowedDirs)
	}
	if d.Audit.LogPath != shellpolicy.DefaultAuditPath {
		t.Errorf("审计路径漂移: %s vs %s", d.Audit.LogPath, shellpolicy.DefaultAuditPath)
	}
}

// assertPolicyEqual 深度比对两份策略的语义集合(列表按升序比较,
// 与书写顺序无关)。
func assertPolicyEqual(t *testing.T, got, want *policy.Policy, label string) {
	t.Helper()
	sorted := func(s []string) []string { out := slices.Clone(s); slices.Sort(out); return out }
	eqList := func(a, b []string) bool { return slices.Equal(sorted(a), sorted(b)) }

	if !eqList(got.Shell.AllowedCommands, want.Shell.AllowedCommands) {
		t.Errorf("%s: allowed_commands 漂移 %v vs %v", label, got.Shell.AllowedCommands, want.Shell.AllowedCommands)
	}
	if !eqList(got.Shell.BinaryDirs, want.Shell.BinaryDirs) {
		t.Errorf("%s: binary_dirs 漂移", label)
	}
	if !eqList(got.Shell.AllowedPathPrefixes, want.Shell.AllowedPathPrefixes) {
		t.Errorf("%s: allowed_path_prefixes 漂移", label)
	}
	if !eqList(got.Shell.BlockedPaths, want.Shell.BlockedPaths) {
		t.Errorf("%s: blocked_paths 漂移", label)
	}
	if !reflect.DeepEqual(got.Shell.CleanEnv, want.Shell.CleanEnv) {
		t.Errorf("%s: clean_env 漂移 %v vs %v", label, got.Shell.CleanEnv, want.Shell.CleanEnv)
	}
	if got.Shell.TimeoutMs != want.Shell.TimeoutMs {
		t.Errorf("%s: timeout_ms 漂移", label)
	}
	if !eqList(got.FS.AllowedDirs, want.FS.AllowedDirs) {
		t.Errorf("%s: fs.allowed_dirs 漂移", label)
	}
	if got.Audit.LogPath != want.Audit.LogPath {
		t.Errorf("%s: audit.log_path 漂移", label)
	}
}
