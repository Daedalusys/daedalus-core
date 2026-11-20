package main

// policy_set_immutable_test.go —— policy.set 在 immutable(bootc)节点上的
// fail-closed 论证防线测试。
//
// 判据两条:
//   - bootc 节点: 三段(Propose/Apply/Rollback)都在副作用前拒绝, 拒绝串逐字一致;
//     Apply/Rollback 即使被手改 journal 直接调用, 也不得改动 policy 文件;
//   - 普通节点: 行为零变化(由既有 policy_set_test.go / policy_set_cli_test.go 覆盖)。
//
// bootc 判定复用 package_set_immutable_test.go 的 TestMain 与 stubBootcMarker /
// bootcMarkerFile 注入缝, 不重复造夹具。

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// TestPolicySet_Propose_RejectsImmutableNode 钉 bootc 节点上 Propose 被拒 +
// 拒绝串逐字一致, 且拒绝发生在 euid/快照/ref 解析之前(bootc 语义与调用者无关)。
func TestPolicySet_Propose_RejectsImmutableNode(t *testing.T) {
	stubBootcMarker(t, bootcMarkerFile(t))

	policyFilePath = filepath.Join(t.TempDir(), "policy.toml")
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	_, _, err := policySetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`))
	if err == nil {
		t.Fatal("immutable(bootc)节点上的 Propose 必须被拒")
	}
	if err.Error() != policyImmutableNodeRejectMsg {
		t.Errorf("拒绝串 = %q, want %q", err.Error(), policyImmutableNodeRejectMsg)
	}
}

// TestPolicySet_Apply_RejectsImmutableNodeBeforeWrite 钉 Apply 侧纵深防御:
// 手改 journal 绕过 Propose 也不得写盘(拒绝串与 Propose 同串), 且目标文件
// 内容逐字节不变。
func TestPolicySet_Apply_RejectsImmutableNodeBeforeWrite(t *testing.T) {
	stubBootcMarker(t, bootcMarkerFile(t))

	targetPath := filepath.Join(t.TempDir(), "policy.toml")
	original := []byte("enabled_kinds = [\"service\"]\n")
	if err := os.WriteFile(targetPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	res := (policySetAdapter{}).Apply(context.Background(),
		tx.Step{Adapter: "policy.set", Index: 0,
			Args: json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`)})
	if res.Returncode == 0 {
		t.Fatal("immutable(bootc)节点上的 Apply 必须被拒")
	}
	if res.Error != policyImmutableNodeRejectMsg {
		t.Errorf("拒绝串 = %q, want %q", res.Error, policyImmutableNodeRejectMsg)
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("读取目标文件失败: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("Apply 被拒后文件内容被改动: got %q want %q", after, original)
	}
}

// TestPolicySet_Rollback_RejectsImmutableNodeBeforeWrite 钉 Rollback 侧:
// 即使 before_state 齐全, immutable 节点也必须在写回之前拒绝, 文件不变。
func TestPolicySet_Rollback_RejectsImmutableNodeBeforeWrite(t *testing.T) {
	stubBootcMarker(t, bootcMarkerFile(t))

	targetPath := filepath.Join(t.TempDir(), "policy.toml")
	original := []byte("# daedalus:disabled\n")
	if err := os.WriteFile(targetPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	policyFilePath = targetPath
	t.Cleanup(func() { policyFilePath = "/opt/daedalus/shared/policy.toml" })

	before, err := json.Marshal(policySetBefore{
		PolicyPath:      targetPath,
		PreviousContent: "enabled_kinds = [\"service\"]\n",
		PreviousSha256:  "deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}

	res := (policySetAdapter{}).Rollback(context.Background(),
		tx.Step{Adapter: "policy.set", Index: 0,
			Args:        json.RawMessage(`{"name":"system-policy","desired_state":"absent"}`),
			BeforeState: before})
	if res.Returncode == 0 {
		t.Fatal("immutable(bootc)节点上的 Rollback 必须被拒")
	}
	if res.Error != policyImmutableNodeRejectMsg {
		t.Errorf("拒绝串 = %q, want %q", res.Error, policyImmutableNodeRejectMsg)
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("读取目标文件失败: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("Rollback 被拒后文件内容被改动: got %q want %q", after, original)
	}
}
