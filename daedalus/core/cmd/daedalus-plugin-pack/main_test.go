// daedalus-plugin-pack CLI 的端到端测试:直接调用 run(),断言退出码与输出。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// buildSmokePluginDir 构造最小 native 插件目录并返回根路径。
func buildSmokePluginDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "main"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := plugin.Manifest{
		ID:         "daedalus.smoke",
		Name:       "Smoke Plugin",
		Version:    "0.1.0",
		Type:       plugin.TypeCapability,
		Runtime:    plugin.RuntimeNative,
		Executable: "bin/main",
		Tools:      []string{"ping"},
	}
	data, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, plugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestCLI_PackThenVerify 走通 CLI 的两种模式:打包 → 校验通过并打印摘要。
func TestCLI_PackThenVerify(t *testing.T) {
	src := buildSmokePluginDir(t)
	out := filepath.Join(t.TempDir(), "smoke.daedalus")

	code, got := runCapture(t, []string{"-in", src, "-out", out})
	if code != exitOK {
		t.Fatalf("打包退出码 = %d, want 0; 输出:\n%s", code, got)
	}
	if !strings.Contains(got, "已打包") {
		t.Errorf("打包输出缺少摘要: %q", got)
	}

	code2, got := runCapture(t, []string{"-verify", out})
	if code2 != exitOK {
		t.Fatalf("校验退出码 = %d, want 0; 输出:\n%s", code2, got)
	}
	for _, want := range []string{"校验通过", "daedalus.smoke", "checksums"} {
		if !strings.Contains(got, want) {
			t.Errorf("校验输出缺少 %q:\n%s", want, got)
		}
	}
}

// TestCLI_UsageErrors 钉死用法错误退出码 2:互斥旗标、缺参、无参。
func TestCLI_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"无旗标", nil},
		{"打包缺 -out", []string{"-in", "/tmp"}},
		{"模式互斥", []string{"-in", "/tmp", "-out", "/tmp/x.zip", "-verify", "/tmp/y.zip"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := runCapture(t, tc.argv)
			if code != exitUsage {
				t.Errorf("退出码 = %d, want %d", code, exitUsage)
			}
		})
	}
}

// TestCLI_VerifyRuntimeFailure 校验不存在的包/坏 zip → 退出码 1。
func TestCLI_VerifyRuntimeFailure(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "broken.zip")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bad, filepath.Join(t.TempDir(), "missing.zip")} {
		code, out := runCapture(t, []string{"-verify", path})
		if code != exitRuntime {
			t.Errorf("对 %s 校验退出码 = %d, want 1; 输出: %s", path, code, out)
		}
		if !strings.Contains(out, "校验失败") {
			t.Errorf("输出缺少失败说明: %s", out)
		}
	}
}

// runCapture 执行 run() 并捕获 stdout(输出走 os.Stdout 的替身不可行,
// 因此 run 的签名接收 *os.File,这里用管道重定向)。
func runCapture(t *testing.T, argv []string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	code := run(argv, w)
	w.Close()
	return code, <-done
}
