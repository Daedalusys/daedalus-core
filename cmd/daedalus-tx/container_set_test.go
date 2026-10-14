package main

// container_set_test.go —— container.set 适配器正向行为测试。
//
// podman 一律经包级注入缝 podmanExecutable 指向 t.TempDir() 下的 shell 夹具
// (模式镜像 task_set_test.go:installFakeSystemctl): 每次调用把 "$*" 追加进
// capture 文件(一行一次调用),"$2"=="inspect" 且 "$3"=="--type" 时按夹具回显
// JSON,其他子命令返回 0。绝不触碰宿主真实 podman —— 测试确定性 + 隔离性双重保证。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// installFakePodman 安装假 podman 并注入包级变量(测试结束自动还原)。
// inspectJSON 是 `inspect --type container --format json <name>` 的固定回显;
// 其他子命令(start/stop/rm)返回 exitCode。
func installFakePodman(t *testing.T, inspectJSON string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) + "\n"
	script += "if [ \"$1\" = inspect ] && [ \"$2\" = --type ] && [ \"$3\" = container ]; then\n"
	script += "  printf '%s\\n' " + strconv.Quote(inspectJSON) + "\n"
	script += "  exit " + strconv.Itoa(exitCode) + "\n"
	script += "fi\n"
	script += "case \"$1\" in\n"
	script += "  start) exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "  stop)  exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "  rm)    exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "esac\nexit 0\n"
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 podman 失败: %v", err)
	}
	orig := podmanExecutable
	podmanExecutable = path
	t.Cleanup(func() { podmanExecutable = orig })
	return capture
}

// withNonRootEuid 临时把 geteuid 翻为 1000(模拟普通用户),测试结束自动还原。
// euid 守门是 Propose/Apply/Rollback 三段共用前置,unit / guard 测试都需要
// 走在 user-domain 才不会被 rootful 守门错拒。
func withNonRootEuid(t *testing.T) {
	t.Helper()
	orig := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = orig })
}

func TestContainerSet_Propose_PresentHappyPath(t *testing.T) {
	withNonRootEuid(t)

	fixtureJSON := `[{"Id":"abc123","State":{"Running":true},"Image":"nginx:latest","Status":"Up 5 minutes"}]`
	installFakePodman(t, fixtureJSON, 0)

	a := containerSetAdapter{}
	before, after, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}`))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}

	var b containerSetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if b.ContainerName != "web" {
		t.Errorf("container_name got %q want %q", b.ContainerName, "web")
	}
	if b.ContainerID != "abc123" {
		t.Errorf("container_id got %q want %q", b.ContainerID, "abc123")
	}
	if b.StateRunning == nil || !*b.StateRunning {
		t.Errorf("state_running 应为 true, got %v", b.StateRunning)
	}
	if b.Image != "nginx:latest" {
		t.Errorf("image got %q want %q", b.Image, "nginx:latest")
	}
	if b.Status != "Up 5 minutes" {
		t.Errorf("status got %q want %q", b.Status, "Up 5 minutes")
	}

	var af containerSetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if af.Container != "web" {
		t.Errorf("container got %q want %q", af.Container, "web")
	}
	if af.DesiredState != "present" {
		t.Errorf("desired_state got %q want %q", af.DesiredState, "present")
	}
	if af.Verb != "" {
		t.Errorf("present verb got %q want empty", af.Verb)
	}
}
