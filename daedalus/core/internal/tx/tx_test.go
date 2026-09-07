// tx 包线上契约与生命周期测试(todo 14)。
// 全部用例经 t.Setenv("DAEDALUS_TX_DIR", t.TempDir()) 隔离,绝不触碰真实系统路径。
package tx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daedalus-os/daedalus/core/internal/dirs"
)

// useTxDir 把日志根钉到临时目录并返回该目录(测试唯一的 root 注入方式)。
func useTxDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(dirs.EnvTxDir, dir)
	return dir
}

// mustBegin 是 Begin 的致命错误包装。
func mustBegin(t *testing.T) *Transaction {
	t.Helper()
	tx, err := Begin()
	if err != nil {
		t.Fatalf("Begin 失败: %v", err)
	}
	return tx
}

// readJournalRaw 读取指定事务的日志文件原始字节(断言"文件内容"而非内存态)。
func readJournalRaw(t *testing.T, dir, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("读取日志 %s 失败: %v", id, err)
	}
	return data
}

// journalStatus 返回日志文件中持久化的 status token。
func journalStatus(t *testing.T, dir, id string) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(readJournalRaw(t, dir, id), &doc); err != nil {
		t.Fatalf("日志 JSON 解析失败: %v", err)
	}
	var status string
	if err := json.Unmarshal(doc["status"], &status); err != nil {
		t.Fatalf("status 字段解析失败: %v", err)
	}
	return status
}

// requireErrIs 断言 err 命中哨兵。
func requireErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("预期错误 %v,实得: %v", target, err)
	}
}

// ──── 线上 JSON 契约(T15/T22/T23 消费者钉桩) ────

func TestTx_JSONShape_Minimal(t *testing.T) {
	// Given: 一个只有必填键的步骤(returncode 0 也必须发射,不得 omit)。
	step := Step{Index: 1, Adapter: "service.set", Args: json.RawMessage(`{"name":"x"}`)}

	// When
	b, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}

	// Then: 键名/键序/缺省抑制逐字节钉死。
	want := `{"index":1,"adapter":"service.set","args":{"name":"x"},"op_result":{"returncode":0}}`
	if string(b) != want {
		t.Fatalf("Step JSON 漂移:\n got %s\nwant %s", b, want)
	}
}

func TestTx_JSONShape_Full(t *testing.T) {
	step := Step{
		Index: 2, Adapter: "a", Args: json.RawMessage(`{}`),
		BeforeState: json.RawMessage(`{"b":1}`), AfterState: json.RawMessage(`{"a":2}`),
		OpResult: OpResult{Returncode: 3, Stdout: "o", Stderr: "e", Error: "boom"},
	}
	b, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"index":2,"adapter":"a","args":{},"before_state":{"b":1},"after_state":{"a":2},"op_result":{"returncode":3,"stdout":"o","stderr":"e","error":"boom"}}`
	if string(b) != want {
		t.Fatalf("全字段 Step JSON 漂移:\n got %s\nwant %s", b, want)
	}
}

// ──── 生命周期迁移全表(钉死的 from×to 矩阵) ────

// reach 构造一个处于指定状态的合法事务(沿迁移表正向走)。
func reach(t *testing.T, target Status) *Transaction {
	t.Helper()
	tx := mustBegin(t)
	if err := tx.Append(Step{Adapter: "svc", Args: json.RawMessage(`{}`), BeforeState: json.RawMessage(`{"v":1}`)}); err != nil {
		t.Fatalf("Append 失败: %v", err)
	}
	if target == StatusProposed {
		return tx
	}
	if err := tx.MarkApplying(); err != nil {
		t.Fatalf("MarkApplying 失败: %v", err)
	}
	if target == StatusApplying {
		return tx
	}
	if target == StatusFailed {
		if err := tx.MarkFailed(); err != nil {
			t.Fatalf("MarkFailed 失败: %v", err)
		}
		return tx
	}
	if err := tx.MarkApplied(); err != nil {
		t.Fatalf("MarkApplied 失败: %v", err)
	}
	if target == StatusApplied {
		return tx
	}
	if err := tx.MarkRolledBack(); err != nil {
		t.Fatalf("MarkRolledBack 失败: %v", err)
	}
	return tx
}

// markTo 把目标状态分派到对应的 Mark* 方法。
func markTo(t *testing.T, tx *Transaction, to Status) error {
	t.Helper()
	switch to {
	case StatusApplying:
		return tx.MarkApplying()
	case StatusApplied:
		return tx.MarkApplied()
	case StatusRolledBack:
		return tx.MarkRolledBack()
	case StatusFailed:
		return tx.MarkFailed()
	default:
		t.Fatalf("未知目标状态 %q", to)
		return nil
	}
}

func TestTx_Lifecycle_TransitionTable(t *testing.T) {
	froms := []Status{StatusProposed, StatusApplying, StatusApplied, StatusRolledBack, StatusFailed}
	tos := []Status{StatusApplying, StatusApplied, StatusRolledBack, StatusFailed}
	// 与 tx.go 中文迁移表逐项一致。
	legal := map[Status]map[Status]bool{
		StatusProposed: {StatusApplying: true, StatusFailed: true},
		StatusApplying: {StatusApplied: true, StatusFailed: true},
		StatusApplied:  {StatusRolledBack: true},
	}

	for _, from := range froms {
		for _, to := range tos {
			name := string(from) + "→" + string(to)
			t.Run(name, func(t *testing.T) {
				dir := useTxDir(t)
				tx := reach(t, from)
				before := readJournalRaw(t, dir, tx.ID)

				err := markTo(t, tx, to)
				if legal[from][to] {
					if err != nil {
						t.Fatalf("合法迁移 %s 被拒: %v", name, err)
					}
					if got := journalStatus(t, dir, tx.ID); got != string(to) {
						t.Fatalf("日志状态未落盘: got %s want %s", got, to)
					}
					return
				}
				// Then(拒绝侧):ErrInvalidTransition + 内存与日志均不变。
				requireErrIs(t, err, ErrInvalidTransition)
				if tx.Status != from {
					t.Fatalf("被拒迁移改动了内存态: %s → %s", from, tx.Status)
				}
				after := readJournalRaw(t, dir, tx.ID)
				if string(before) != string(after) {
					t.Fatalf("被拒迁移改动了日志内容:\nbefore %s\nafter  %s", before, after)
				}
			})
		}
	}
}

// TestTx_Lifecycle_HappyPath 走通计划钉的 5 态链路:
// proposed→applying→applied→rolled_back(+failed 分支),逐步断言日志文件。
func TestTx_Lifecycle_HappyPath(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)
	if tx.Status != StatusProposed {
		t.Fatalf("初始状态应为 %s,实得 %s", StatusProposed, tx.Status)
	}
	if got := journalStatus(t, dir, tx.ID); got != "proposed" {
		t.Fatalf("初始日志状态: %s", got)
	}

	if err := tx.Append(Step{Adapter: "service.set", Args: json.RawMessage(`{"name":"nginx"}`), BeforeState: json.RawMessage(`{"ActiveState":"inactive"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkApplying(); err != nil {
		t.Fatal(err)
	}
	if got := journalStatus(t, dir, tx.ID); got != "applying" {
		t.Fatalf("MarkApplying 后日志状态: %s", got)
	}
	if err := tx.MarkApplied(); err != nil {
		t.Fatal(err)
	}
	// applied 固化回滚计划(含逆序快照)。
	if len(tx.RollbackPlan.Steps) != 1 || tx.RollbackPlan.Steps[0].Adapter != "service.set" {
		t.Fatalf("回滚计划未固化: %+v", tx.RollbackPlan)
	}
	if err := tx.MarkRolledBack(); err != nil {
		t.Fatal(err)
	}
	if got := journalStatus(t, dir, tx.ID); got != "rolled_back" {
		t.Fatalf("MarkRolledBack 后日志状态: %s", got)
	}
	// 终态再迁移必须被拒(表驱动已全覆盖,此处只验失败分支持久化)。
	failed := mustBegin(t)
	if err := failed.MarkFailed(); err != nil {
		t.Fatal(err)
	}
	if got := journalStatus(t, dir, failed.ID); got != "failed" {
		t.Fatalf("MarkFailed 后日志状态: %s", got)
	}
}

// TestTx_JournalContent_Fixture 用钉死的 CreatedAt/ID 断言日志全文逐字节
// 等于 fixture(线上契约的最终防线;时间戳与 id 固定后 JSON 完全确定)。
func TestTx_JournalContent_Fixture(t *testing.T) {
	dir := useTxDir(t)
	tx, err := beginWithID("0123deadbeef00ff")
	if err != nil {
		t.Fatal(err)
	}
	tx.CreatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := tx.save(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(Step{Adapter: "service.set", Args: json.RawMessage(`{"name":"nginx"}`), BeforeState: json.RawMessage(`{"active":"inactive"}`)}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []func() error{tx.MarkApplying, tx.MarkApplied, tx.MarkRolledBack} {
		if err := m(); err != nil {
			t.Fatal(err)
		}
	}

	stepJSON := `{"index":0,"adapter":"service.set","args":{"name":"nginx"},"before_state":{"active":"inactive"},"op_result":{"returncode":0}}`
	want := `{"id":"0123deadbeef00ff","created_at":"2026-01-02T03:04:05Z","status":"rolled_back","steps":[` +
		stepJSON + `],"rollback_plan":{"steps":[` + stepJSON + `]}}` + "\n"
	if got := string(readJournalRaw(t, dir, tx.ID)); got != want {
		t.Fatalf("日志内容漂移:\n got %q\nwant %q", got, want)
	}
}

// ──── Append 校验(计划 Failure QA a/b + 状态门) ────

func TestTx_Append_Rejections(t *testing.T) {
	useTxDir(t)
	t.Run("空Adapter拒绝", func(t *testing.T) {
		tx := mustBegin(t)
		if err := tx.Append(Step{Adapter: ""}); err == nil || errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("空 Adapter 必须被拒,实得: %v", err)
		}
	})
	t.Run("负Index拒绝", func(t *testing.T) {
		tx := mustBegin(t)
		if err := tx.Append(Step{Adapter: "a", Index: -1}); err == nil {
			t.Fatal("负 Index 必须被拒")
		}
		if len(tx.Steps) != 0 {
			t.Fatal("被拒的 Append 不得污染内存态")
		}
	})
	t.Run("MarkApplied跳过applying被拒", func(t *testing.T) {
		// 计划 Failure QA (c):proposed 直达 applied 非法。
		tx := mustBegin(t)
		requireErrIs(t, tx.MarkApplied(), ErrInvalidTransition)
	})
}

func TestTx_Append_AutoIndexOverridesCaller(t *testing.T) {
	useTxDir(t)
	tx := mustBegin(t)
	// 调用方传 42/7 → 一律被覆盖为 0/1(序号权威在 tx 层;非负即合法输入)。
	for _, junk := range []int{42, 7} {
		if err := tx.Append(Step{Adapter: "a", Index: junk}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Steps) != 2 || loaded.Steps[0].Index != 0 || loaded.Steps[1].Index != 1 {
		t.Fatalf("Index 未按 len(Steps) 自增: %+v", loaded.Steps)
	}
}

func TestTx_Append_RejectedAfterApplying(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)
	requireErrIs(t, tx.MarkApplying(), nil)
	err := tx.Append(Step{Adapter: "late"})
	if err == nil {
		t.Fatal("applying 之后禁止追加步骤(steps 冻结)")
	}
	requireErrIs(t, tx.MarkFailed(), nil)
	if err := tx.Append(Step{Adapter: "late"}); err == nil {
		t.Fatal("failed 终态禁止追加步骤")
	}
	// 内存被拒后日志里也只有最初 0 步(omitempty 下 "steps" 键应整体缺失)。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(readJournalRaw(t, dir, tx.ID), &doc); err != nil {
		t.Fatal(err)
	}
	if raw, ok := doc["steps"]; ok {
		var steps []Step
		if err := json.Unmarshal(raw, &steps); err != nil {
			t.Fatal(err)
		}
		if len(steps) != 0 {
			t.Fatalf("被拒步骤不得入日志: %d 条", len(steps))
		}
	}
}

// TestTx_Append_PersistFailureRollsBack 写失败时内存回滚(日志与内存不分叉)。
func TestTx_Append_PersistFailureRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 绕过 DAC 权限位,只读夹具不适用")
	}
	dir := useTxDir(t)
	tx := mustBegin(t)
	path := filepath.Join(dir, tx.ID+".json")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if err := tx.Append(Step{Adapter: "a"}); err == nil {
		t.Fatal("日志不可写时 Append 必须报错")
	}
	if len(tx.Steps) != 0 {
		t.Fatalf("写失败必须回滚内存步骤,实得 %d 条", len(tx.Steps))
	}
}
