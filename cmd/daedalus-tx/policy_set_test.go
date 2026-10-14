package main

// policy_set_test.go —— policy.set 适配器正向行为测试。
//
// 文件 I/O 走真实路径( t.TempDir() ),不走 shell 夹具——policy 是本机文件,
// 与 task 的 systemctl / container 的 podman 不同。euid / policyFilePath 走
// 包级 var 注入缝与 policy.DisabledSentinel (SDK 跨仓常量) 的 SDK TestPolicy_LoadDisabledSentinel
// 独立验证。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// euidOverride 临时把 geteuid 翻为 e(测试 root/non-root 路径),测试结束自动还原。
func euidOverride(t *testing.T, e int) {
	t.Helper()
	orig := geteuid
	geteuid = func() int { return e }
	t.Cleanup(func() { geteuid = orig })
}

func TestPolicySet_Propose_LoadedHappyPath(t *testing.T) {
	euidOverride(t, 0) // root

	dir := t.TempDir()
	refPath := filepath.Join(dir, "policy.ref.toml")
	targetPath := filepath.Join(dir, "policy.toml")
	content := []byte("enabled_kinds = [\"service\", \"package\"]\n")
	if err := os.WriteFile(refPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	ctx := withPolicyDir(context.Background(), dir)
	before, after, err := a.Propose(ctx,
		json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"policy.ref.toml"}`))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}

	var b policySetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if b.PolicyPath != targetPath {
		t.Errorf("policy_path got %q want %q", b.PolicyPath, targetPath)
	}
	if b.PreviousContent != string(content) {
		t.Errorf("previous_content 快照不符: got %q want %q", b.PreviousContent, string(content))
	}
	wantSha := sha256.Sum256(content)
	if b.PreviousSha256 != hex.EncodeToString(wantSha[:]) {
		t.Errorf("previous_sha256 got %q want %q", b.PreviousSha256, hex.EncodeToString(wantSha[:]))
	}

	var af policySetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if af.PolicyPath != targetPath {
		t.Errorf("after policy_path got %q want %q", af.PolicyPath, targetPath)
	}
	if af.DesiredState != "loaded" {
		t.Errorf("after desired_state got %q want %q", af.DesiredState, "loaded")
	}
	if af.SourceRef != "policy.ref.toml" {
		t.Errorf("after source_ref got %q want %q", af.SourceRef, "policy.ref.toml")
	}
}
