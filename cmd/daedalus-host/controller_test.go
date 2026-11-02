package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
// 单元里写的 ExecStart argv 必须能被本二进制的旗标集解析。
// 这条钉住 daedalus-host-controller.service 与 CLI 的一致性:
// 单元路径/旗标任一漂移,本测即红。
func TestControllerUnit_ExecStartParsesAgainstFlagSet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "files", "system", "usr",
		"lib", "systemd", "system", "daedalus-host-controller.service"))
	if err != nil {
		t.Fatalf("读单元失败: %v", err)
	}
	var execLine string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			execLine = strings.TrimPrefix(line, "ExecStart=")
		}
	}
	if execLine == "" {
		t.Fatal("单元缺 ExecStart")
	}
	fields := strings.Fields(execLine)
	if fields[0] != "/usr/local/bin/daedalus-host" {
		t.Errorf("ExecStart 二进制应为 /usr/local/bin/daedalus-host, got %q", fields[0])
	}
	// fields = [bin, "controller", "start", flags...]
	if len(fields) < 3 || fields[1] != "controller" || fields[2] != "start" {
		t.Fatalf("ExecStart 子命令形态异常: %v", fields)
	}
	fs, _ := controllerFlagSet()
	if err := fs.Parse(fields[3:]); err != nil {
		t.Fatalf("单元 argv 被旗标集拒绝: %v", err)
	}
}
