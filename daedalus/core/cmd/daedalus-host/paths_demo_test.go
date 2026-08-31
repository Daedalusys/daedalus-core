//go:build demo

// paths_demo_test.go: 仅在 `-tags demo` 时编译,覆盖 dev 路径配置加载
// 与 buildStartTokens 的镜像路径改写语义(改写仅命中 /opt/daedalus 与
// /usr/local/bin 两个前缀,普通路径原样保留)。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// writeDevToml 在 path 处写入一份最小 daedalus-dev.toml,供各用例复用。
func writeDevToml(t *testing.T, path, prefix, deno string) {
	t.Helper()
	content := "Prefix = \"" + prefix + "\"\n"
	if deno != "" {
		content += "deno = \"" + deno + "\"\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

// TestLoadDevPaths_FromEnv 验证 $DAEDALUS_DEV_PATHS 指向绝对路径配置文件时,
// loadDevPaths 读取成功且 Prefix/Deno 字段原样返回。
func TestLoadDevPaths_FromEnv(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "daedalus-dev.toml")
	writeDevToml(t, cfgFile, "/tmp/devroot", "/tmp/devroot/usr/local/bin/deno")
	t.Setenv("DAEDALUS_DEV_PATHS", cfgFile)

	cfg, err := loadDevPaths()
	if err != nil {
		t.Fatalf("loadDevPaths 失败: %v", err)
	}
	if cfg.Prefix != "/tmp/devroot" {
		t.Errorf("Prefix = %q, 期望 /tmp/devroot", cfg.Prefix)
	}
	if cfg.Deno != "/tmp/devroot/usr/local/bin/deno" {
		t.Errorf("Deno = %q, 期望 /tmp/devroot/usr/local/bin/deno", cfg.Deno)
	}
}

// TestLoadDevPaths_CwdSearch 验证未设环境变量时,loadDevPaths 从 cwd
// 向上逐级查找 daedalus-dev.toml(配置放在父目录,子目录作为 cwd 命中)。
func TestLoadDevPaths_CwdSearch(t *testing.T) {
	root := t.TempDir()
	writeDevToml(t, filepath.Join(root, devPathsFileName), "/tmp/cwdroot", "")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	t.Setenv("DAEDALUS_DEV_PATHS", "")
	t.Chdir(sub)

	cfg, err := loadDevPaths()
	if err != nil {
		t.Fatalf("loadDevPaths 失败: %v", err)
	}
	if cfg.Prefix != "/tmp/cwdroot" {
		t.Errorf("Prefix = %q, 期望 /tmp/cwdroot", cfg.Prefix)
	}
}

// TestLoadDevPaths_NotFound 验证环境变量为空且各级目录都没有配置文件时,
// loadDevPaths 返回非 nil error(fail-closed,绝不静默退化为 prod 行为)。
func TestLoadDevPaths_NotFound(t *testing.T) {
	t.Setenv("DAEDALUS_DEV_PATHS", "")
	t.Chdir(t.TempDir())

	if cfg, err := loadDevPaths(); err == nil {
		t.Fatalf("期望返回 error,实际得到 cfg=%+v", cfg)
	}
}

// TestBuildStartTokens_DemoRewritesEntrypoint 验证 deno 插件的 entrypoint
// 中镜像路径被改写到 devPrefix 下:deno 二进制、/opt/daedalus、
// /usr/local/bin/daedalus-audit 全部落到 /tmp/devroot;裸路径形态消失;
// 普通路径 /home 不受改写影响。
func TestBuildStartTokens_DemoRewritesEntrypoint(t *testing.T) {
	devPrefix = "/tmp/devroot"
	denoBinary = "/tmp/devroot/usr/local/bin/deno"
	t.Cleanup(func() { devPrefix, denoBinary = "", "" })

	m := plugin.Manifest{
		Runtime:    plugin.RuntimeDeno,
		Executable: "main.ts",
		Entrypoint: []string{
			"--allow-env",
			"--allow-read=/opt/daedalus,/home",
			"--allow-run=/usr/local/bin/daedalus-audit",
		},
	}
	tokens := buildStartTokens("/plugdir", &m)
	cmdLine := strings.Join(tokens, " ")

	for _, want := range []string{
		"/tmp/devroot/opt/daedalus",
		"/tmp/devroot/usr/local/bin/daedalus-audit",
		"/tmp/devroot/usr/local/bin/deno",
	} {
		if !strings.Contains(cmdLine, want) {
			t.Errorf("cmdLine 缺少 %q:\n%s", want, cmdLine)
		}
	}
	// 改写后的 token 是 /tmp/devroot/opt/daedalus,/home;裸形态
	// "=/opt/daedalus" 必须消失(注意 "/opt/daedalus," 本身是替换结果的
	// 子串,不能作为判据),普通路径 /home 原样保留。
	if strings.Contains(cmdLine, "=/opt/daedalus") {
		t.Errorf("cmdLine 仍含未改写的裸 /opt/daedalus 形态:\n%s", cmdLine)
	}
	if !strings.Contains(cmdLine, ",/home") {
		t.Errorf("普通路径 /home 应原样保留:\n%s", cmdLine)
	}
}

// TestBuildStartTokens_DemoNativeEntrypoint 验证 native 插件的 entrypoint
// 同样经过改写管线;无镜像路径时改写是 no-op,tokens 逐元素原样。
func TestBuildStartTokens_DemoNativeEntrypoint(t *testing.T) {
	devPrefix = "/tmp/devroot"
	t.Cleanup(func() { devPrefix = "" })

	m := plugin.Manifest{
		Runtime:    plugin.RuntimeNative,
		Executable: "bin/main",
		Entrypoint: []string{"--stdio", "--log-level=info"},
	}
	tokens := buildStartTokens("/plugdir", &m)
	want := []string{"/plugdir/bin/main", "--stdio", "--log-level=info"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, 期望 %v", tokens, want)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Errorf("tokens[%d] = %q, 期望 %q", i, tokens[i], want[i])
		}
	}
}
