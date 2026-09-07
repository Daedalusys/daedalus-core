// Begin/Load/journalPath 测试:路径安全三防线、同 id 幂等、并发序列化、
// 加载器防御(内容 id 掉包 / 未知状态 / 符号链接逃逸)。
// 全部经 t.Setenv("DAEDALUS_TX_DIR", t.TempDir()) 隔离,绝不触碰真实系统路径。
package tx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daedalus-os/daedalus/core/internal/dirs"
)

// ──── Begin:形状 + 落位 ────

func TestTx_Begin_IDShapeAndJournalLocation(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)

	if !txIDPattern.MatchString(tx.ID) {
		t.Fatalf("自生成 id 未过形状门: %q", tx.ID)
	}
	if _, err := os.Lstat(filepath.Join(dir, tx.ID+".json")); err != nil {
		t.Fatalf("日志应落在 <root>/<id>.json: %v", err)
	}
	if got := journalStatus(t, dir, tx.ID); got != "proposed" {
		t.Fatalf("初始持久化状态: %s", got)
	}
	// created_at 必须是可解析的 RFC3339(T15 status 输出消费)。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(readJournalRaw(t, dir, tx.ID), &doc); err != nil {
		t.Fatal(err)
	}
	var createdAt string
	if err := json.Unmarshal(doc["created_at"], &createdAt); err != nil {
		t.Fatalf("created_at 缺失或非法: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Fatalf("created_at 非 RFC3339: %q: %v", createdAt, err)
	}
}

func TestTx_Begin_TopLevelKeysPinned(t *testing.T) {
	dir := useTxDir(t)
	tx := mustBegin(t)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(readJournalRaw(t, dir, tx.ID), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "created_at", "status", "rollback_plan"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("Transaction 顶层键缺失: %s", key)
		}
	}
}

// ──── 路径安全:敌意 id(计划钉桩:../、大写、短 id) ────

func TestTx_JournalPath_RejectsHostileIDs(t *testing.T) {
	dir := useTxDir(t)
	hostile := []string{
		"../../etc/passwd",     // 目录穿越
		"../escape",            // 相对逃逸
		"..",                   // 纯穿越段
		"DEADBEEFDEADBEEF",     // 大写十六进制(要求小写)
		"deadbeef",             // 过短
		"deadbeefdeadbeef0",    // 过长(17)
		"deadbeefdeadbeefg",    // 非十六进制字符
		"dead/beefdeadbee",     // 含分隔符
		"",                     // 空
		"deadbeefdeadbee.json", // 缺一个字符且带扩展名
		"./deadbeefdeadbeef",   // 前缀路径段
	}
	for _, id := range hostile {
		// journalPath:形状门先于任何路径拼接/文件系统操作。
		if _, err := journalPath(dir, id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("journalPath(%q) 必须命中 ErrInvalidID,实得: %v", id, err)
		}
		// Load:同一道门(CLI todo 15 的 id 位置参数在触盘前即拒 → exit 2)。
		if _, err := Load(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Load(%q) 必须命中 ErrInvalidID,实得: %v", id, err)
		}
	}
	// Then:敌意尝试不得留下任何 fs 痕迹(root 仍为空)。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("敌意 id 触碰了文件系统: %d 个残留条目", len(entries))
	}
}

func TestTx_JournalPath_ContainmentRejectsSymlinkEscape(t *testing.T) {
	dir := useTxDir(t)
	// Given: root 内一个"日志名"实为指向外部的符号链接(掉包攻击面)。
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.json")
	if err := os.WriteFile(victim, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "aaaaaaaaaaaaaaaa"
	link := filepath.Join(dir, id+".json")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	// When:形状门通过,但 realpath 包含性门必须拒绝(读路径 Load)。
	_, err := Load(id)
	if err == nil || !strings.Contains(err.Error(), "逃逸") {
		t.Fatalf("符号链接逃逸必须被包含性门拒绝,实得: %v", err)
	}

	// Then:外部被害文件未被触碰。
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("逃逸检查失败前已污染外部文件: %q", data)
	}
}

func TestTx_Begin_RejectsExistingJournal(t *testing.T) {
	dir := useTxDir(t)
	tx, err := beginWithID("0000111122223333")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginWithID(tx.ID); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("同 id 二次 Begin 必须拒绝(幂等防线),实得: %v", err)
	}
	// Then:原日志逐字节未变(绝不覆盖)。
	original := readJournalRaw(t, dir, tx.ID)
	if string(original) == "" {
		t.Fatal("原日志不应为空")
	}
}

func TestTx_ConcurrentBeginSameID_ExactlyOneWins(t *testing.T) {
	useTxDir(t)
	const id = "ffffddddaaaabbbb"
	errs := make([]error, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = beginWithID(id)
		}(i)
	}
	close(start)
	wg.Wait()

	nWin := 0
	for _, err := range errs {
		switch {
		case err == nil:
			nWin++
		case !errors.Is(err, ErrAlreadyExists):
			t.Fatalf("败者错误必须是 ErrAlreadyExists,实得: %v", err)
		}
	}
	if nWin != 1 {
		t.Fatalf("同 id 并发 Begin 必须恰有 1 个成功,实得 %d", nWin)
	}
}

// ──── 并发 Append:同进程串行 + 跨进程锁 → 日志永不撕裂 ────

func TestTx_ConcurrentAppend_Serializes(t *testing.T) {
	useTxDir(t)
	tx := mustBegin(t)
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = tx.Append(Step{Adapter: fmt.Sprintf("a%d", i)})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发 Append[%d] 失败: %v", i, err)
		}
	}

	// Then:磁盘日志完整可解析,n 个唯一索引/唯一 adapter 齐全。
	loaded, err := Load(tx.ID)
	if err != nil {
		t.Fatalf("并发写后日志必须仍是完整 JSON 文档: %v", err)
	}
	if len(loaded.Steps) != n {
		t.Fatalf("步骤丢失: got %d want %d", len(loaded.Steps), n)
	}
	seen := map[int]string{}
	for _, s := range loaded.Steps {
		if dup, ok := seen[s.Index]; ok {
			t.Fatalf("索引重复: %d 被 %s 与 %s 共用", s.Index, dup, s.Adapter)
		}
		seen[s.Index] = s.Adapter
	}
	for i := 0; i < n; i++ {
		if _, ok := seen[i]; !ok {
			t.Fatalf("缺失索引 %d(自增序列必须是 0..%d)", i, n-1)
		}
	}
}

// ──── Load 防御与往返 ────

func TestTx_Load_RoundTrip(t *testing.T) {
	useTxDir(t)
	tx := mustBegin(t)
	if err := tx.Append(Step{Adapter: "service.set", Args: json.RawMessage(`{"name":"a"}`), BeforeState: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != tx.ID || loaded.Status != StatusProposed || len(loaded.Steps) != 1 {
		t.Fatalf("往返不一致: %+v", loaded)
	}
	if loaded.Steps[0].Adapter != "service.set" || string(loaded.Steps[0].Args) != `{"name":"a"}` {
		t.Fatalf("RawMessage 载荷漂移: %s", loaded.Steps[0].Args)
	}
	if !loaded.CreatedAt.Equal(tx.CreatedAt) {
		t.Fatalf("时间戳往返漂移: %v vs %v", loaded.CreatedAt, tx.CreatedAt)
	}
	// 同一事务跨进程续写:从 loaded 继续 Append,索引从持久化态自增。
	if err := loaded.Append(Step{Adapter: "b"}); err != nil {
		t.Fatal(err)
	}
	again, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Steps) != 2 || again.Steps[1].Index != 1 {
		t.Fatalf("续写索引错误: %+v", again.Steps)
	}
	// applying 态的 Load 往返同样保真(状态门在 loaded 上依旧生效)。
	if err := again.MarkApplying(); err != nil {
		t.Fatal(err)
	}
	frozen, err := Load(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != StatusApplying {
		t.Fatalf("applying 往返漂移: %s", frozen.Status)
	}
	if err := frozen.Append(Step{Adapter: "late"}); err == nil {
		t.Fatal("loaded 事务必须同样拒绝 applying 态追加")
	}
}

func TestTx_Load_NotFound(t *testing.T) {
	useTxDir(t)
	_, err := Load("00112233445566aa")
	requireErrIs(t, err, ErrNotFound)
}

func TestTx_Load_RejectsTamperedContent(t *testing.T) {
	dir := useTxDir(t)
	write := func(id, content string) string {
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// (a) 文件名与内容 id 不一致 → 掉包拒绝。
	write("aaaaaaaaaaaaaaaa", `{"id":"bbbbbbbbbbbbbbbb","created_at":"2026-01-01T00:00:00Z","status":"proposed"}`)
	if _, err := Load("aaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("内容 id 与文件名不一致必须被拒")
	}
	// (b) 未知状态 token → fail-closed。
	write("cccccccccccccccc", `{"id":"cccccccccccccccc","created_at":"2026-01-01T00:00:00Z","status":"nope"}`)
	if _, err := Load("cccccccccccccccc"); err == nil {
		t.Fatal("未知状态必须被拒")
	}
	// (c) 非法 JSON → 报错不 panic。
	write("eeeeeeeeeeeeeeee", `{{{`)
	if _, err := Load("eeeeeeeeeeeeeeee"); err == nil {
		t.Fatal("损坏 JSON 必须被拒")
	}
}

// TestTx_Begin_SymlinkedRootWorks 证明包含性门只拦逃逸、不误伤合法符号链接根
// (env 覆盖 root 本身是符号链接时,Begin/Load 全程可用)。
func TestTx_Begin_SymlinkedRootWorks(t *testing.T) {
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dirs.EnvTxDir, linkRoot)

	tx := mustBegin(t)
	if _, err := os.Lstat(filepath.Join(realRoot, tx.ID+".json")); err != nil {
		t.Fatalf("日志应穿过符号链接根落在真实目录: %v", err)
	}
	if _, err := Load(tx.ID); err != nil {
		t.Fatalf("符号链接根下 Load 被误伤: %v", err)
	}
}
