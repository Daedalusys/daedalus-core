package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/dirs"
)

func TestCmdDrift_PrintsTableAndAudits(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(audit.EnvLogPath, filepath.Join(tmp, "audit.jsonl"))
	policyFile := filepath.Join(tmp, "policy.toml")
	// 拷一份真实镜像 policy.toml 作为 fixture(包含所有必需字段)。
	src, err := os.ReadFile(filepath.Join("..", "..", "files", "system", "opt", "daedalus", "shared", "policy.toml"))
	if err != nil {
		t.Fatalf("读镜像 policy.toml 失败: %v", err)
	}
	if err := os.WriteFile(policyFile, src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEDALUS_POLICY_PATH", policyFile)
	txRoot := filepath.Join(tmp, "tx")
	if err := os.MkdirAll(txRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dirs.EnvTxDir, txRoot)
	t.Setenv("DAEDALUS_STATE_PATH", filepath.Join(tmp, "state.jsonl"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"drift", "--kind=service"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("drift 应退出 0, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "KIND") {
		t.Errorf("应打印表头, got %q", stdout.String())
	}
}
