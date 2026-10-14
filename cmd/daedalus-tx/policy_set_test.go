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
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/policy"
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

func TestPolicySet_Propose_RejectsEuidNonZero(t *testing.T) {
	euidOverride(t, 1000)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`))
	if err == nil {
		t.Fatal("euid!=0 必须拒绝")
	}
	if !strings.Contains(err.Error(), policyScopeRejectMsg) {
		t.Errorf("拒串漂移: got %q want substring %q", err.Error(), policyScopeRejectMsg)
	}
}

func TestPolicySet_Propose_RejectsUnknownDesiredState(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"unloaded"}`))
	if err == nil {
		t.Fatal("表外 desired_state 必须拒绝")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown desired_state "unloaded"`) {
		t.Errorf("未点名表外值: %s", msg)
	}
	if !strings.Contains(msg, "loaded|absent") {
		t.Errorf("未点名 v1 词表: %s", msg)
	}
}

func TestPolicySet_Propose_RejectsAbsoluteRef(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	ctx := withPolicyDir(context.Background(), dir)
	_, _, err := a.Propose(ctx,
		json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"/etc/passwd"}`))
	if err == nil {
		t.Fatal("绝对路径 ref 必须拒绝")
	}
	if !strings.Contains(err.Error(), "must be a relative path") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsParentTraversal(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	ctx := withPolicyDir(context.Background(), dir)
	_, _, err := a.Propose(ctx,
		json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"../escape.toml"}`))
	if err == nil {
		t.Fatal("含 .. 的 ref 必须拒绝")
	}
	if !strings.Contains(err.Error(), "must not contain '..'") && !strings.Contains(err.Error(), "escapes base dir") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsMissingRefFile(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte("enabled_kinds = [\"service\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	ctx := withPolicyDir(context.Background(), dir)
	_, _, err := a.Propose(ctx,
		json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"nonexistent.ref.toml"}`))
	if err == nil {
		t.Fatal("ref 文件不存在必须拒绝")
	}
	if !strings.Contains(err.Error(), "ref file not found") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsMissingPolicyFile(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	policyFilePath = filepath.Join(dir, "nonexistent-policy.toml")
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`))
	if err == nil {
		t.Fatal("policy 文件不存在必须拒绝(A-form)")
	}
	if !strings.Contains(err.Error(), "policy file not found") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestPolicySet_Propose_Absent_HappyPath(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	content := []byte("enabled_kinds = [\"service\"]\n")
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	before, after, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`))
	if err != nil {
		t.Fatalf("absent + policy 存在 应放行, 意外失败: %v", err)
	}
	var b policySetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatal(err)
	}
	if b.PreviousContent != string(content) {
		t.Errorf("absent 也要快照旧内容(可逆): got %q", b.PreviousContent)
	}
	var af policySetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatal(err)
	}
	if af.SourceRef != "" {
		t.Errorf("absent 不应带 source_ref, got %q", af.SourceRef)
	}
}

func TestPolicySet_Propose_Absent_RejectsMissingPolicyFile(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	policyFilePath = filepath.Join(dir, "nonexistent.toml")
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`))
	if err == nil {
		t.Fatal("absent + policy 文件不存在必须拒绝(A-form strict)")
	}
	if !strings.Contains(err.Error(), "policy file not found") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsMissingDesiredState(t *testing.T) {
	euidOverride(t, 0)
	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy"}`))
	if err == nil {
		t.Fatal("缺 desired_state 必须拒绝")
	}
	if !strings.Contains(err.Error(), `缺 "desired_state"`) {
		t.Errorf("未点名 missing field: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsTrailingJSON(t *testing.T) {
	euidOverride(t, 0)
	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}x`))
	if err == nil {
		t.Fatal("尾随 JSON 必须拒绝")
	}
	if !strings.Contains(err.Error(), "尾随数据") {
		t.Errorf("未点名 trailing data: %s", err.Error())
	}
}

func TestPolicySet_Propose_RejectsAlreadySentinel(t *testing.T) {
	euidOverride(t, 0)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(targetPath, []byte(policy.DisabledSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	a := policySetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"loaded","content_ref":"x.toml"}`))
	if err == nil {
		t.Fatal("已经是 sentinel 状态时必须拒绝(防 loaded 洗掉 absent)")
	}
	if !strings.Contains(err.Error(), "already in absent") {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}
