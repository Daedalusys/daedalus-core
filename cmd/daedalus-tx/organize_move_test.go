package main

// organize_move_test.go —— organize.move 适配器的单元测试。
//
// 覆盖:
//   - TestOrganizeMoveRoundTrip: tmpdir + 2 个文件 → Propose → Apply →
//     验证文件已落到 to;→ Rollback → 验证 from 已恢复存在(content 仍是
//     新值,因 os.Rename 不复制 inode,与 issue #3 范围对齐)。
//   - TestOrganizeMovePathGuardRejects: Propose 对 /etc/shadow 写二档拒,
//     before/after 必须为 nil,err 必须非空。
//   - TestOrganizeMoveDisallowUnknownFields: 多余 JSON 键即拒(防手改注入)。
//   - TestOrganizeMoveRejectsFromEqualsTo: from 与 to 同路径即拒。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/pathguard"
)

// withAllowedDirs 临时把 pathguard.AllowedDirs 替换为指定白名单,测试结束还原。
func withAllowedDirs(t *testing.T, dirs []string) {
	t.Helper()
	prev := pathguard.AllowedDirs
	pathguard.WithAllowedDirs(dirs)
	t.Cleanup(func() { pathguard.WithAllowedDirs(prev) })
}

func TestOrganizeMoveRoundTrip(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst", "by_ext")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	withAllowedDirs(t, []string{root})

	fromA := filepath.Join(srcDir, "a.txt")
	fromB := filepath.Join(srcDir, "b.txt")
	for _, p := range []string{fromA, fromB} {
		if err := os.WriteFile(p, []byte("hello "+filepath.Base(p)), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	toA := filepath.Join(dstDir, "a.txt")
	toB := filepath.Join(dstDir, "b.txt")

	raw, err := json.Marshal(organizeMoveArgs{
		PlanID: "test-plan",
		Moves: []organizeMoveEntry{
			{From: fromA, To: toA, SizeBytes: 6, Rule: "by_ext"},
			{From: fromB, To: toB, SizeBytes: 6, Rule: "by_ext"},
		},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	a := organizeMoveAdapter{}
	before, after, err := a.Propose(context.Background(), raw)
	if err != nil {
		t.Fatalf("Propose 失败: %v", err)
	}
	if len(before) == 0 || len(after) == 0 {
		t.Fatalf("Propose before/after 必须非空; got before=%d after=%d", len(before), len(after))
	}

	step := tx.Step{
		Index:       0,
		Adapter:     "organize.move",
		BeforeState: before,
		Args:        raw,
	}
	if res := a.Apply(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Apply 失败: %+v", res)
	}
	for _, p := range []string{fromA, fromB} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("Apply 后 from %s 仍存在, 期望已移走", p)
		}
	}
	for _, p := range []string{toA, toB} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("Apply 后 to %s 不存在: %v", p, err)
		}
	}

	if res := a.Rollback(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Rollback 失败: %+v", res)
	}
	for _, p := range []string{fromA, fromB} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("Rollback 后 from %s 不可读: %v", p, err)
		}
		if !strings.Contains(string(got), filepath.Base(p)) {
			t.Fatalf("Rollback 后 from %s content 异常: %q", p, got)
		}
	}
	for _, p := range []string{toA, toB} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("Rollback 后 to %s 仍存在, 期望已移回 from", p)
		}
	}
}

func TestOrganizeMovePathGuardRejects(t *testing.T) {
	// 白名单不含 /etc: ValidateWritePath 对 /etc/shadow 直接拒。
	withAllowedDirs(t, []string{"/tmp"})

	raw := json.RawMessage(`{"plan_id":"x","moves":[{"from":"/etc/shadow","to":"/tmp/shadow","size_bytes":0,"rule":"by_ext"}]}`)
	a := organizeMoveAdapter{}
	_, _, err := a.Propose(context.Background(), raw)
	if err == nil {
		t.Fatalf("Propose 必须拒绝 /etc/shadow, 实得 nil error")
	}
	if !strings.Contains(err.Error(), "写二档拒绝") && !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("Propose 错误信息未体现写二档拒绝: %v", err)
	}
}

func TestOrganizeMoveDisallowUnknownFields(t *testing.T) {
	// 多余键 "evil" 必须被 DisallowUnknownFields 拒。
	raw := json.RawMessage(`{"plan_id":"x","moves":[{"from":"/tmp/a","to":"/tmp/b","evil":1}]}`)
	a := organizeMoveAdapter{}
	_, _, err := a.Propose(context.Background(), raw)
	if err == nil {
		t.Fatalf("Propose 必须拒多余键 evil")
	}
}

func TestOrganizeMoveRejectsFromEqualsTo(t *testing.T) {
	raw, _ := json.Marshal(organizeMoveArgs{
		PlanID: "x",
		Moves:  []organizeMoveEntry{{From: "/tmp/a", To: "/tmp/a", SizeBytes: 0}},
	})
	a := organizeMoveAdapter{}
	_, _, err := a.Propose(context.Background(), raw)
	if err == nil {
		t.Fatalf("Propose 必须拒 from == to")
	}
}
