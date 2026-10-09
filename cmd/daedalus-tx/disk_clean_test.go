package main

// disk_clean_test.go —— disk.clean 适配器的单元测试。
//
// 覆盖:
//   - TestDiskCleanRoundTrip: tmpdir + 2 个文件 → Propose → Apply 验证
//     文件已删 → Rollback 验证空文件已重建,mode 保留。
//   - TestDiskCleanRollbackRestoresDirectory: 目录目标 Apply 删目录 →
//     Rollback 必须重建为目录(而非空文件),即 I2 修后的 IsDir 分支。
//   - TestDiskCleanPathGuardRejects: /etc/shadow 写二档拒。
//   - TestDiskCleanDisallowUnknownFields: 多余键即拒。

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

func withAllowedDirsTest(t *testing.T, dirs []string) {
	t.Helper()
	prev := pathguard.AllowedDirs
	pathguard.WithAllowedDirs(dirs)
	t.Cleanup(func() { pathguard.WithAllowedDirs(prev) })
}

func TestDiskCleanRoundTrip(t *testing.T) {
	root := t.TempDir()
	withAllowedDirsTest(t, []string{root})

	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("hello "+filepath.Base(p)), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	raw, err := json.Marshal(diskCleanArgs{Paths: []string{a, b}})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	ad := diskCleanAdapter{}
	before, after, err := ad.Propose(context.Background(), raw)
	if err != nil {
		t.Fatalf("Propose 失败: %v", err)
	}
	if len(before) == 0 || len(after) == 0 {
		t.Fatalf("Propose before/after 必须非空")
	}

	step := tx.Step{Index: 0, Adapter: "disk.clean", BeforeState: before, Args: raw}
	if res := ad.Apply(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Apply 失败: %+v", res)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("Apply 后 %s 仍存在, 期望已删", p)
		}
	}

	if res := ad.Rollback(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Rollback 失败: %+v", res)
	}
	for _, p := range []string{a, b} {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("Rollback 后 %s 不可 stat: %v", p, err)
		}
		if info.IsDir() {
			t.Fatalf("Rollback 后 %s 重建为目录, 期望文件", p)
		}
		if got, _ := os.ReadFile(p); len(got) != 0 {
			t.Fatalf("Rollback 后 %s content 非空(%d bytes); 预期空(issue #1 范围排除内容恢复)", p, len(got))
		}
	}
}

func TestDiskCleanRollbackRestoresDirectory(t *testing.T) {
	// I2 修后 Rollback 必须按 IsDir 重建;目录删后还原为目录而非空文件。
	root := t.TempDir()
	withAllowedDirsTest(t, []string{root})

	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inner"), []byte("x"), 0644); err != nil {
		t.Fatalf("write inner: %v", err)
	}

	raw, _ := json.Marshal(diskCleanArgs{Paths: []string{dir}})
	ad := diskCleanAdapter{}
	before, _, err := ad.Propose(context.Background(), raw)
	if err != nil {
		t.Fatalf("Propose 失败: %v", err)
	}

	step := tx.Step{Index: 0, Adapter: "disk.clean", BeforeState: before, Args: raw}
	if res := ad.Apply(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Apply 失败: %+v", res)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatalf("Apply 后目录 %s 仍存在", dir)
	}

	if res := ad.Rollback(context.Background(), step); res.Returncode != 0 {
		t.Fatalf("Rollback 失败: %+v", res)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("Rollback 后目录不存在: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("Rollback 后 %s 是常规文件, 期望目录(I2 修)", dir)
	}
}

func TestDiskCleanPathGuardRejects(t *testing.T) {
	withAllowedDirsTest(t, []string{"/tmp"})

	raw := json.RawMessage(`{"paths":["/etc/shadow"]}`)
	ad := diskCleanAdapter{}
	_, _, err := ad.Propose(context.Background(), raw)
	if err == nil {
		t.Fatalf("Propose 必须拒绝 /etc/shadow")
	}
	if !strings.Contains(err.Error(), "写二档拒绝") && !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("Propose 错误信息未体现写二档拒绝: %v", err)
	}
}

func TestDiskCleanDisallowUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"paths":["/tmp/a"],"evil":1}`)
	ad := diskCleanAdapter{}
	_, _, err := ad.Propose(context.Background(), raw)
	if err == nil {
		t.Fatalf("Propose 必须拒多余键 evil")
	}
}
