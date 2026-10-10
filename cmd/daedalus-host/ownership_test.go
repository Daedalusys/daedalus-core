package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/audit"
)

func TestCmdOwnership_GetByKind(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
	src, err := os.ReadFile(filepath.Join("..", "..", "files", "system", "opt", "daedalus", "shared", "policy.toml"))
	if err != nil {
		t.Fatalf("读镜像 policy.toml 失败: %v", err)
	}
	pf := filepath.Join(tmp, "policy.toml")
	if err := os.WriteFile(pf, src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEDALUS_POLICY_PATH", pf)

	var stdout, stderr bytes.Buffer
	code := cmdOwnership(&stdout, &stderr, []string{"get", "service/nginx.service"})
	if code != 0 {
		t.Fatalf("got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mode=observe") {
		t.Errorf("应打印 observe, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "source=by_kind") {
		t.Errorf("应标注来源 by_kind, got %q", stdout.String())
	}
}

func TestCmdOwnership_GetDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
	src, err := os.ReadFile(filepath.Join("..", "..", "files", "system", "opt", "daedalus", "shared", "policy.toml"))
	if err != nil {
		t.Fatalf("读镜像 policy.toml 失败: %v", err)
	}
	pf := filepath.Join(tmp, "policy.toml")
	if err := os.WriteFile(pf, src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEDALUS_POLICY_PATH", pf)

	var stdout bytes.Buffer
	code := cmdOwnership(&stdout, &bytes.Buffer{}, []string{"get", "package/vim"})
	if code != 0 {
		t.Fatalf("got %d", code)
	}
	if !strings.Contains(stdout.String(), "mode=unmanaged") {
		t.Errorf("应 fallback default, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "source=default") {
		t.Errorf("应标注来源 default, got %q", stdout.String())
	}
}