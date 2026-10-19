package main

// policy_set_guard_test.go —— policy.set 的 euid / ref 逃逸 / 原子写失败负向测试。
// 只覆盖 policy_set.go / policy_set_exec.go 的防御;**严禁**修改 unitguard.go /
// service_set*.go / task_set*.go / container_set*.go / internal/tx/* 来让本测试通过(spec §7)。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

func TestPolicySet_Guard_RejectsEuidNonZero_AllSegments(t *testing.T) {
	for _, state := range []string{"loaded", "absent"} {
		t.Run(state, func(t *testing.T) {
			euidOverride(t, 1000)
			dir := t.TempDir()
			targetPath := filepath.Join(dir, "policy.toml")
			if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			policyFilePath = targetPath
			t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

			a := policySetAdapter{}
			args := json.RawMessage(`{"name":"system-policy","desired_state":"` + state + `"}`)
			_, _, err := a.Propose(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), policyScopeRejectMsg) {
				t.Errorf("Propose[%s] euid!=0 拒漂移: %v", state, err)
			}
			res := a.Apply(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, policyScopeRejectMsg) {
				t.Errorf("Apply[%s] euid!=0 拒漂移: %+v", state, res)
			}
			res = a.Rollback(context.Background(), tx.Step{Args: args})
			if res.Returncode == 0 || !strings.Contains(res.Error, policyScopeRejectMsg) {
				t.Errorf("Rollback[%s] euid!=0 拒漂移: %+v", state, res)
			}
		})
	}
}

func TestPolicySet_Guard_RejectsRefEscape(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	for _, ref := range []string{
		"/etc/passwd",      // 绝对
		"../escape.toml",   // ..
		"sub/../../escape", // 内含 ..
	} {
		t.Run(ref, func(t *testing.T) {
			a := policySetAdapter{}
			ctx := withPolicyDir(context.Background(), dir)
			_, _, err := a.Propose(ctx,
				json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"`+ref+`"}`))
			if err == nil {
				t.Fatalf("%q 应被 ref 守卫拒", ref)
			}
		})
	}
}

func TestPolicySet_Guard_ApplyWriteFailureRc1(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	content := []byte("enabled_kinds = [\"service\"]\n")
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// 让父目录变为只读:CreateTemp 创建临时文件会失败 → writePolicyFileAtomic 返错 → Apply rc=1。
	readonlyDir := filepath.Join(dir, "readonly-parent")
	if err := os.MkdirAll(readonlyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath = filepath.Join(readonlyDir, "policy.toml")
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	if err := os.Chmod(readonlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(readonlyDir, 0o755)
		policyFilePath = "/opt/daedalus/shared/policy.toml"
	})

	before, _ := json.Marshal(policySetBefore{
		PolicyPath:      targetPath,
		PreviousContent: string(content),
		PreviousSha256:  sha256Hex(content),
	})
	a := policySetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`),
		BeforeState: before,
	})
	// root 通常无视 mode 位(0o555 也可写);  这种环境下本测试会 skip.
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行,只读 mode 位不生效")
	}
	if res.Returncode == 0 {
		t.Errorf("不可写目录下 Apply 应 rc=1, got %+v", res)
	}
}
