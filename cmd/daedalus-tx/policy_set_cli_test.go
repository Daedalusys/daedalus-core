package main

// policy_set_cli_test.go —— policy.set 的 CLI 全链路测试。
// 经由 run(argv...) 走 begin→propose→apply→rollback→status 完整回路。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/policy"
)

// setPolicyFilePathForTest 把 package var policyFilePath 临时改为 path(模拟 manager
// 把目标 policy.toml 改到测试目录),测试结束自动还原。CLI 路径是 --policy-dir,
// 但 harness 的 run() 暂未提供 policy_dir 旗标透传; 走 package var 直改是 v1
// 测试的最简方式,见 plan "what's left to the builder"。
func setPolicyFilePathForTest(t *testing.T, path string) {
	t.Helper()
	orig := policyFilePath
	policyFilePath = path
	t.Cleanup(func() { policyFilePath = orig })
}

func TestPolicySet_CLI_RoundTrip_Loaded(t *testing.T) {
	euidOverride(t, 0)
	logPath, rc := harness(t)
	dir := t.TempDir()

	// ref + target
	refPath := filepath.Join(dir, "policy.ref.toml")
	targetPath := filepath.Join(dir, "policy.toml")
	origContent := []byte("enabled_kinds = [\"service\"]\n")
	newContent := []byte("enabled_kinds = [\"service\", \"package\"]\n")
	if err := os.WriteFile(refPath, newContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, origContent, 0o644); err != nil {
		t.Fatal(err)
	}
	setPolicyFilePathForTest(t, targetPath)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "policy.set",
		`{"name":"system-policy","desired_state":"loaded","content_ref":"policy.ref.toml"}`,
		"--policy-dir", dir); c != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", c, e)
	}
	if c, out, e := rc("apply", id, "--policy-dir", dir); c != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	if c, out, e := rc("rollback", id, "--policy-dir", dir); c != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	c, statusOut, e := rc("status", id)
	if c != exitOK {
		t.Fatalf("status 退出码 = %d; stderr: %s", c, e)
	}
	st := parseStatus(t, statusOut)
	if st.Status != "rolled_back" {
		t.Fatalf("终态异常: %+v", st)
	}
	// 文件经 apply + rollback 恢复,应为 origContent
	got, _ := os.ReadFile(targetPath)
	if string(got) != string(origContent) {
		t.Errorf("rollback 后文件未恢复, got %q", got)
	}
	// 盖章: begin + apply + rollback = 3 条同 tx_id
	recs := readAudit(t, logPath)
	if got := inTxCount(recs, id); got != 3 {
		t.Fatalf("in-tx 记录数 = %d, want 3", got)
	}
	mustVerify(t, logPath)
}

func TestPolicySet_CLI_RoundTrip_AbsentReversible(t *testing.T) {
	euidOverride(t, 0)
	logPath, rc := harness(t)
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "policy.toml")
	origContent := []byte("enabled_kinds = [\"service\"]\n")
	if err := os.WriteFile(targetPath, origContent, 0o644); err != nil {
		t.Fatal(err)
	}
	setPolicyFilePathForTest(t, targetPath)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "policy.set",
		`{"name":"system-policy","desired_state":"absent"}`); c != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", c, e)
	}
	if c, out, e := rc("apply", id); c != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	// apply 后文件应为 sentinel
	got, _ := os.ReadFile(targetPath)
	if string(got) != policy.DisabledSentinel {
		t.Fatalf("absent apply 后文件应为 sentinel, got %q", got)
	}
	// rollback
	if c, out, e := rc("rollback", id); c != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	got, _ = os.ReadFile(targetPath)
	if string(got) != string(origContent) {
		t.Errorf("absent rollback 应恢复原内容(kill switch 可逆), got %q", got)
	}
	// 链 + 盖章
	recs := readAudit(t, logPath)
	if got := inTxCount(recs, id); got != 3 {
		t.Fatalf("in-tx 记录数 = %d, want 3", got)
	}
	mustVerify(t, logPath)
}

func TestPolicySet_CLI_RejectsEuidNonZero(t *testing.T) {
	euidOverride(t, 1000)
	_, rc := harness(t)
	id := doBegin(t, rc)
	c, out, _ := rc("propose", id, "policy.set",
		`{"name":"system-policy","desired_state":"absent"}`)
	if c != exitRuntime {
		t.Fatalf("propose(euid!=0) 应 exit1, got %d out=%s", c, out)
	}
	if !strings.Contains(out, "requires root") {
		t.Errorf("error JSON 应点名 root, got %s", out)
	}
}
