package main

// container_set_guard_test.go —— container.set 的 euid / 名称门 / podman 子进程错误
// 负向测试。主要钉 containerSetAdapter 三段 + podmanExec 的拒绝串与失败模式。
// 本测试只覆盖 container_set*.go 自身的防御;**严禁**修改 unitguard.go /
// service_set*.go / task_set*.go / internal/tx/* 来让本测试通过 —— spec §7 钉.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

func TestContainerSet_Guard_RejectsEuidZero_AllSegments(t *testing.T) {
	for _, state := range []string{"present", "absent", "started", "stopped"} {
		t.Run(state, func(t *testing.T) {
			orig := geteuid
			geteuid = func() int { return 0 }
			t.Cleanup(func() { geteuid = orig })

			a := containerSetAdapter{}
			args := json.RawMessage(`{"name":"web","desired_state":"` + state + `"}`)
			// Propose 测试
			_, _, err := a.Propose(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), containerScopeRejectMsg) {
				t.Errorf("Propose[%s] euid==0 拒漂移: %v", state, err)
			}
			// Apply 测试
			res := a.Apply(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, containerScopeRejectMsg) {
				t.Errorf("Apply[%s] euid==0 拒漂移: %+v", state, res)
			}
			// Rollback 测试
			res = a.Rollback(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, containerScopeRejectMsg) {
				t.Errorf("Rollback[%s] euid==0 拒漂移: %+v", state, res)
			}
		})
	}
}

func TestContainerSet_Guard_RejectsInvalidName_AllSegments(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = orig })

	for _, name := range []string{"a b", "a/b", "-x"} {
		t.Run(name, func(t *testing.T) {
			a := containerSetAdapter{}
			args, err := json.Marshal(containerSetArgs{Name: name, DesiredState: "present"})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = a.Propose(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "invalid container name:") {
				t.Errorf("Propose[%s] 名称门拒漂移: %v", name, err)
			}
			res := a.Apply(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, "invalid container name:") {
				t.Errorf("Apply[%s] 名称门拒漂移: %+v", name, res)
			}
			res = a.Rollback(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, "invalid container name:") {
				t.Errorf("Rollback[%s] 名称门拒漂移: %+v", name, res)
			}
		})
	}
}

// writeFakeExecutable 写一个可执行 shell 脚本(本文件局部 helper).
func writeFakeExecutable(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestContainerSet_Guard_RejectsPodmanSubprocessFailure 锁死 A3 守门: podman 子
// 进程失败(socket / namespace 异常,fixture 返回 126 + stderr "no daemon socket")
// 不应被 observeContainerState 当作"不存在"; 上抛错误.
func TestContainerSet_Guard_RejectsPodmanSubprocessFailure(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = orig })

	// inject 不可启动的 podman(返回 126).
	path := writeFakeExecutable(t, "echo 'Error: cannot connect to Podman socket' >&2\nexit 126")
	prev := podmanExecutable
	podmanExecutable = path
	t.Cleanup(func() { podmanExecutable = prev })

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}`))
	if err == nil {
		t.Fatal("podman 126 (进程级失败) 必须上抛, 不应谎报'容器不存在'")
	}
	if !strings.Contains(err.Error(), "podman inspect 失败") {
		t.Errorf("拒串应点名 'podman inspect 失败', got %q", err.Error())
	}
}

// TestContainerSet_Guard_RejectsPodmanStartFailure 锁死 Apply 的 rc 透传:
// `podman start` 失败 → Apply rc=1 + Stderr.
func TestContainerSet_Guard_RejectsPodmanStartFailure(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = orig })

	path := writeFakeExecutable(t, "if [ \"$1\" = start ]; then echo 'boom' >&2; exit 1; fi\nexit 0")
	prev := podmanExecutable
	podmanExecutable = path
	t.Cleanup(func() { podmanExecutable = prev })

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"started"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", false, "nginx:latest", "Exited"),
	})
	if res.Returncode != 1 {
		t.Errorf("podman start 失败应 rc=1, got %+v", res)
	}
	if res.Stderr != "boom" {
		t.Errorf("stderr 应透传, got %q", res.Stderr)
	}
}
