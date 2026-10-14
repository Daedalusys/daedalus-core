package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/audit"
)

// runCLI 直接驱动 run(args, stdout, stderr) 并回传退出码与两个输出流。
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestAuditCLIRotate_NoPolicy_FailsClosed:缺 policy 时 rotate 必须 rc=1(fail-closed)。
func TestAuditCLIRotate_NoPolicy_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAEDALUS_POLICY_PATH", filepath.Join(dir, "missing.toml"))
	code, _, errOut := runCLI(t, "rotate", "--log-path", filepath.Join(dir, "audit.jsonl"), "--force")
	if code != exitRuntime {
		t.Fatalf("缺 policy 应 rc=1, got %d (stderr=%s)", code, errOut)
	}
	if !strings.Contains(errOut, "policy") {
		t.Fatalf("stderr 应含 policy, got %q", errOut)
	}
}

// TestAuditCLIVerify_AutoChain:活跃 + 归档段并存时 verify 自动走 VerifyChain。
func TestAuditCLIVerify_AutoChain(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.jsonl")
	archiveDir := filepath.Join(dir, "archive")
	t.Setenv("DAEDALUS_AUDIT_LOG_PATH", logPath)

	for i := 0; i < 3; i++ {
		if _, err := audit.LogAudit(audit.Entry{Tool: "t", LogPath: logPath}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := audit.Rotate(audit.RotateOptions{LogPath: logPath, ArchiveDir: archiveDir, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.LogAudit(audit.Entry{Tool: "t", LogPath: logPath}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI(t, "verify")
	if code != exitOK {
		t.Fatalf("verify 应 rc=0, got %d (stderr=%s)", code, errOut)
	}
	if !strings.Contains(out, "verified 4 entries") {
		t.Fatalf("stdout 应含 verified 4 entries, got %q", out)
	}
}
