// CAS(比较并写回)回归测试:证明"两个进程各持一份日志快照"不再互相覆盖。
// 这些用例在 CAS 落地前是**绿的假象** —— flock 只串行化写的瞬间,后写者会静默
// 覆盖前写者(read-modify-write 丢写),状态机看起来推进了,盘上权威却已丢失。
// 全部经 t.Setenv("DAEDALUS_TX_DIR", t.TempDir()) 隔离,绝不触碰真实系统路径。
package tx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestTx_CAS_StaleTerminalWriteRejected 复现被报告的那条序列:
// apply 进程 MarkApplying 后,resolve 进程判 failed,apply 进程的 MarkApplied
// 必须被拒(旧实现返回 nil 且把盘上终态改回 applied)。
func TestTx_CAS_StaleTerminalWriteRejected(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)

	applier, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.MarkApplying(); err != nil {
		t.Fatal(err)
	}

	resolver, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.MarkFailed(); err != nil {
		t.Fatalf("resolve 侧 applying→failed 应合法: %v", err)
	}

	if err := applier.MarkApplied(); !errors.Is(err, ErrConcurrentModify) {
		t.Fatalf("迟到的 applied 写回必须撞 ErrConcurrentModify,实得: %v", err)
	}
	// Then:盘上终态仍是 failed,内存态已回退(不分叉)。
	if got := journalStatus(t, dir, tx.ID); got != string(StatusFailed) {
		t.Fatalf("盘上终态被覆盖: %s(want %s)", got, StatusFailed)
	}
	if applier.Status != StatusApplying {
		t.Fatalf("失败的 mark 必须回退内存态: %s", applier.Status)
	}
}

// TestTx_CAS_ExactlyOneWinnerPerBaseline 同一份基线的两个快照并发推进,
// 恰有一个写回成功;败者拿到 ErrConcurrentModify 而非静默丢写。
func TestTx_CAS_ExactlyOneWinnerPerBaseline(t *testing.T) {
	useTxDir(t)
	tx := mustBegin(t)

	const n = 6
	docs := make([]*Transaction, n)
	for i := range docs {
		loaded, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = loaded
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range docs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = docs[i].MarkApplying()
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrConcurrentModify):
		default:
			t.Fatalf("败者[%d] 必须是 ErrConcurrentModify,实得: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("同一基线必须恰有 1 个写回成功,实得 %d", winners)
	}
}

// TestTx_Save_DetectsExternalTampering 持快照期间日志被外部改写(合法的新内容、
// 损坏的半截 JSON、被清空)三类都必须拒绝覆盖,且拒绝发生在截断之前。
func TestTx_Save_DetectsExternalTampering(t *testing.T) {
	dir := useTxDir(t)
	path := func(id string) string { return filepath.Join(dir, id+".json") }

	t.Run("外部合法改写", func(t *testing.T) {
		tx := mustBegin(t)
		loaded, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		other, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Append(Step{Adapter: "service.set"}); err != nil {
			t.Fatal(err)
		}
		if err := loaded.Append(Step{Adapter: "pkg.set"}); !errors.Is(err, ErrConcurrentModify) {
			t.Fatalf("基线已分歧的追加必须被拒,实得: %v", err)
		}
		final, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(final.Steps) != 1 || final.Steps[0].Adapter != "service.set" {
			t.Fatalf("胜者步骤被覆盖: %+v", final.Steps)
		}
	})

	t.Run("外部损坏", func(t *testing.T) {
		tx := mustBegin(t)
		loaded, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path(tx.ID), []byte(`{"id":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := loaded.MarkApplying(); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("半截 JSON 必须命中 ErrJournalCorrupt,实得: %v", err)
		}
	})

	t.Run("外部清空", func(t *testing.T) {
		tx := mustBegin(t)
		loaded, err := Load(tx.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path(tx.ID), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := loaded.MarkApplying(); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("空日志必须命中 ErrJournalCorrupt,实得: %v", err)
		}
		// 拒绝不得把内存态留在与盘不一致的位置。
		if loaded.Status != StatusProposed {
			t.Fatalf("写失败后内存态未回退: %s", loaded.Status)
		}
	})
}

// TestTx_Save_RequiresBaseline 无基线的内存事务(未经 Begin/Load 得到)绝不允许
// 覆盖盘上既有日志 —— 少了这道门,构造出来的裸 Transaction 就是绕过 CAS 的后门。
func TestTx_Save_RequiresBaseline(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)
	if err := tx.MarkApplying(); err != nil {
		t.Fatal(err)
	}
	bare := &Transaction{ID: tx.ID, CreatedAt: tx.CreatedAt, Status: StatusApplied, journalPath: filepath.Join(dir, tx.ID+".json")}
	err := bare.save()
	if !errors.Is(err, ErrConcurrentModify) {
		t.Fatalf("无基线写盘必须被拒,实得: %v", err)
	}
	if got := journalStatus(t, dir, tx.ID); got != string(StatusApplying) {
		t.Fatalf("盘上状态被裸事务覆盖: %s", got)
	}
}

// TestTx_CAS_ReloadResumesWrite CAS 是拒绝而非死锁:重新 Load 即可拿到新基线续写。
func TestTx_CAS_ReloadResumesWrite(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)
	stale, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(Step{Adapter: "service.set"}); err != nil {
		t.Fatal(err)
	}
	if err := stale.Append(Step{Adapter: "pkg.set"}); !errors.Is(err, ErrConcurrentModify) {
		t.Fatalf("过期快照追加必须被拒,实得: %v", err)
	}

	fresh, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Append(Step{Adapter: "pkg.set"}); err != nil {
		t.Fatalf("重新载入后应可续写: %v", err)
	}
	var doc struct {
		Steps []Step `json:"steps"`
	}
	if err := json.Unmarshal(readJournalRaw(t, dir, tx.ID), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Steps) != 2 || doc.Steps[1].Adapter != "pkg.set" || doc.Steps[1].Index != 1 {
		t.Fatalf("续写后的步骤序列不符: %+v", doc.Steps)
	}
}
