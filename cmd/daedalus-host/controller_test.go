package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/audit"
)

func TestCmdController_Start_ExitsClean(t *testing.T) {
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
	done := make(chan int, 1)
	go func() {
		done <- cmdController(&stdout, &stderr, []string{"start", "--poll-interval=50ms"})
	}()
	// 给 informer 跑几个 tick 后 SIGTERM 终止。
	time.Sleep(200 * time.Millisecond)
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("应退出 0, got %d stderr=%s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller start 未在 SIGTERM 后退出")
	}
}