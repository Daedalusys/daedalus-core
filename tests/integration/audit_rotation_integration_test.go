// Package integration 是 issue #37 端到端集成测试:在同一进程里跑 SDK
// 轮转/归档/跨归档验证 + core tx 跨轮转 + state 跨代读,证明 spec §7
// 验收映射的可观测性。
package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/state"
)

// noopAttrs 免 root 注入 audit 轮转所需 attrs。
type noopAttrs struct{}

func (noopAttrs) HasAppend(string) (bool, error) { return false, nil }
func (noopAttrs) ClearAppend(string) error       { return nil }
func (noopAttrs) SetAppend(string) error         { return nil }

// TestE2E_AuditChainIntactAcrossRotation:写入 → 强制轮转 → 再写入 → 跨归档
// 校验,期望全链计数正确且 prev_hash 续接。
func TestE2E_AuditChainIntactAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.jsonl")
	archiveDir := filepath.Join(dir, "archive")
	orig := audit.CurrentAppendOnlyOps()
	audit.SetAppendOnlyOps(noopAttrs{})
	t.Cleanup(func() { audit.SetAppendOnlyOps(orig) })

	var last string
	for i := 0; i < 50; i++ {
		rec, err := audit.LogAudit(audit.Entry{Tool: "t", LogPath: logPath})
		if err != nil {
			t.Fatal(err)
		}
		last = rec.EntryHash
	}
	if _, err := audit.Rotate(audit.RotateOptions{LogPath: logPath, ArchiveDir: archiveDir, Force: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		rec, err := audit.LogAudit(audit.Entry{Tool: "t", LogPath: logPath})
		if err != nil {
			t.Fatal(err)
		}
		if rec.PrevHash != last {
			t.Fatalf("断链: prev=%s want %s", rec.PrevHash, last)
		}
		last = rec.EntryHash
	}
	n, err := audit.VerifyChain(logPath, archiveDir)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if n != 100 {
		t.Fatalf("want 100, got %d", n)
	}
}

// TestE2E_TxChainCrossesRotation:事务跨轮转(begin 进归档,apply 写活跃),
// daedalus-tx 通过 audit.OpenLog 仍能正确续链;模拟扫描拿到的 prev_hash
// 必须是 begin 的 entry_hash,而不是 GenesisHash。
func TestE2E_TxChainCrossesRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.jsonl")
	archiveDir := filepath.Join(dir, "archive")
	orig := audit.CurrentAppendOnlyOps()
	audit.SetAppendOnlyOps(noopAttrs{})
	t.Cleanup(func() { audit.SetAppendOnlyOps(orig) })

	txID := "0a1b2c3d4e5f6071"
	beginRec, err := audit.LogAudit(audit.Entry{
		Identity: "daedalus-tx", Tool: "daedalus_tx_begin",
		TxID: txID, TxStep: 0, LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.Rotate(audit.RotateOptions{LogPath: logPath, ArchiveDir: archiveDir, Force: true}); err != nil {
		t.Fatal(err)
	}
	r, err := audit.OpenLog(logPath, audit.NewestFirst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var maxStep int
	var prevHash string
	for {
		rec, err := r.Next()
		if err != nil {
			break
		}
		if rec.TxID == txID && rec.TxStep >= maxStep {
			maxStep, prevHash = rec.TxStep, rec.EntryHash
		}
	}
	if maxStep != 0 {
		t.Fatalf("begin 应在归档里, maxStep=0, got %d", maxStep)
	}
	if prevHash != beginRec.EntryHash {
		t.Fatalf("跨归档应找到 begin 尾哈希, got %s want %s", prevHash, beginRec.EntryHash)
	}
	// 用此尾哈希写 apply:若漏了归档,apply 的 tx_prev_hash 会接非 tx 记录,
	// Verify 会断链。
	applyRec, err := audit.LogAudit(audit.Entry{
		Identity: "daedalus-tx", Tool: "daedalus_tx_apply",
		TxID: txID, TxStep: 1, TxPrevHash: prevHash, LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.VerifyChain(logPath, archiveDir); err != nil {
		t.Fatalf("tx 跨轮转后整链应过 VerifyChain, got %v", err)
	}
	_ = applyRec
}

// TestE2E_StateCrossGeneration:用大阈值写 5 条,SetMaxSizeBytes(1) 触发一次
// 轮转,再写 1 条;Read 跨 .1 + 活跃应得 6 条(无 drift 假阳性窗口)。
func TestE2E_StateCrossGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")
	t.Setenv("DAEDALUS_STATE_PATH", path)
	state.SetMaxSizeBytes(0) // 默认大阈值
	t.Cleanup(func() { state.SetMaxSizeBytes(0) })
	for i := 0; i < 5; i++ {
		if err := state.Log(state.StateEntry{
			Kind: "k", Name: nameN(i), ObservedAt: time.Now(),
			Payload: json.RawMessage(`{"x":"y"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	state.SetMaxSizeBytes(1) // 下一次写触发轮转
	if err := state.Log(state.StateEntry{
		Kind: "k", Name: nameN(5), ObservedAt: time.Now(),
		Payload: json.RawMessage(`{"x":"y"}`),
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := state.Read(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("want 6 跨代, got %d", len(entries))
	}
}

func nameN(i int) string { return "n" + itoa(i) }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// 触发 SetMaxSizeBytes 测试前先确保 state 路径归属(避免与其它包测试串扰)。
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
