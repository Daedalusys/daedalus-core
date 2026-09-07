package main

// main_test.go —— daedalus-tx CLI 端到端测试(直接调 run(), 不 spawn 子进程)。
// 夹具经 t.Setenv 隔离: DAEDALUS_TX_DIR→t.TempDir, DAEDALUS_AUDIT_LOG_PATH→临时 jsonl。
// 承重断言: 盖章规则(仅 begin/apply/rollback 带 tx_id)、事务链经 audit.Verify 通过、
// stdout JSON 契约、退出码、路径门、失败步 → MarkFailed + 部分回滚。

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/audit"
)

// harness 建一个隔离环境并返回 (audit 日志路径, runCapture 闭包)。
func harness(t *testing.T) (string, func(...string) (int, string, string)) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("DAEDALUS_TX_DIR", filepath.Join(d, "tx"))
	logPath := filepath.Join(d, "audit.jsonl")
	t.Setenv(audit.EnvLogPath, logPath)
	return logPath, func(argv ...string) (int, string, string) {
		var out, errBuf bytes.Buffer
		code := run(argv, &out, &errBuf)
		return code, out.String(), errBuf.String()
	}
}

// doBegin 跑 begin 并解析出 tx_id(stdout 契约 {"tx_id":"..."})。
func doBegin(t *testing.T, rc func(...string) (int, string, string)) string {
	t.Helper()
	code, out, errOut := rc("begin")
	if code != exitOK {
		t.Fatalf("begin 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc beginDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("begin stdout 非 beginDoc JSON: %v (%q)", err, out)
	}
	if !txIDPattern.MatchString(doc.TxID) {
		t.Fatalf("begin tx_id 形状非法: %q", doc.TxID)
	}
	return doc.TxID
}

// auditRec 是审计行按需读取的最小视图。
type auditRec struct {
	Tool      string `json:"tool"`
	Outcome   string `json:"outcome"`
	TxID      string `json:"tx_id"`
	TxStep    int    `json:"tx_step"`
	EntryHash string `json:"entry_hash"`
}

// readAudit 读取全部审计行(逐行 JSON)。
func readAudit(t *testing.T, path string) []auditRec {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计日志失败: %v", err)
	}
	var recs []auditRec
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r auditRec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("审计行非法 JSON: %v (%q)", err, line)
		}
		recs = append(recs, r)
	}
	return recs
}

func inTxCount(recs []auditRec, id string) int {
	n := 0
	for _, r := range recs {
		if r.TxID == id {
			n++
		}
	}
	return n
}

func mustVerify(t *testing.T, path string) {
	t.Helper()
	if _, err := audit.Verify(path); err != nil {
		t.Fatalf("audit.Verify 失败: %v", err)
	}
}

// statusDoc 是事务日志 stdout 的解析视图。
type statusDoc struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Index    int `json:"index"`
		OpResult struct {
			Returncode int    `json:"returncode"`
			Error      string `json:"error"`
		} `json:"op_result"`
	} `json:"steps"`
}

func parseStatus(t *testing.T, out string) statusDoc {
	t.Helper()
	var d statusDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatalf("status stdout 非法 JSON: %v (%q)", err, out)
	}
	return d
}

// ───────────────────────── 盖章规则 + 双链端到端 ─────────────────────────

func TestTx_Roundtrip_StampingRuleAndChain(t *testing.T) {
	logPath, rc := harness(t)
	id := doBegin(t, rc)

	// propose 两步(noop)—— 每次产出一条**空 tx_id**外围审计。
	for _, arg := range []string{`{"n":1}`, `{"n":2}`} {
		code, _, errOut := rc("propose", id, "noop", arg)
		if code != exitOK {
			t.Fatalf("propose 退出码 = %d; stderr: %s", code, errOut)
		}
	}
	// apply 两步 → 两条 tx_apply 步 1..2。
	code, out, errOut := rc("apply", id)
	if code != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s\nout=%s", code, errOut, out)
	}
	if st := parseStatus(t, out); st.Status != "applied" || len(st.Steps) != 2 {
		t.Fatalf("apply 后状态异常: %+v", st)
	}
	// status —— 空 tx_id 外围审计。
	if c, _, e := rc("status", id); c != exitOK {
		t.Fatalf("status 退出码 = %d; stderr: %s", c, e)
	}
	// rollback 两步 → 两条 tx_rollback 步 3..4。
	if c, _, e := rc("rollback", id); c != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s", c, e)
	}

	recs := readAudit(t, logPath)
	// 盖章规则: 该 tx_id 的 in-tx 记录恰为 begin(1)+apply(2)+rollback(2)=5。
	if got := inTxCount(recs, id); got != 5 {
		t.Fatalf("同 tx_id 记录数 = %d, want 5 (begin+2apply+2rollback)", got)
	}
	// propose/status 外围条目必须 tx_id 为空。
	var plain int
	for _, r := range recs {
		if r.TxID == "" {
			plain++
			if r.Tool != "daedalus_tx_propose" && r.Tool != "daedalus_tx_status" {
				t.Fatalf("空 tx_id 条目 tool 应为 propose/status, 实得 %s", r.Tool)
			}
		}
	}
	if plain != 3 { // 2 propose + 1 status
		t.Fatalf("外围(空 tx_id)条目数 = %d, want 3", plain)
	}
	// in-tx 步序升序 0..4。
	var steps []int
	for _, r := range recs {
		if r.TxID == id {
			steps = append(steps, r.TxStep)
		}
	}
	for i, s := range steps {
		if s != i {
			t.Fatalf("in-tx 步序非升序连续: %+v (index %d = %d)", steps, i, s)
		}
	}
	// 工具名映射。
	tools := map[string]bool{}
	for _, r := range recs {
		tools[r.Tool] = true
	}
	for _, want := range []string{"daedalus_tx_begin", "daedalus_tx_apply", "daedalus_tx_rollback", "daedalus_tx_propose", "daedalus_tx_status"} {
		if !tools[want] {
			t.Fatalf("缺少审计 tool %s", want)
		}
	}
	// 双链端到端: 整条日志经 audit.Verify 通过(全局 + 逐事务)。
	mustVerify(t, logPath)
}

// ───────────────────────── 事务链创世播种端到端 ─────────────────────────

func TestTx_BeginSeedsGenesisFromAudit(t *testing.T) {
	logPath, rc := harness(t)
	// 前置一条真实非 tx 记录(模拟 shell 调用), 事务 begin 应越过它挂创世。
	if _, err := audit.LogAudit(audit.Entry{Identity: "cli", Tool: "shell_exec",
		Args: audit.NewObject(), Outcome: "success", LogPath: logPath}); err != nil {
		t.Fatal(err)
	}
	id := doBegin(t, rc)
	// 核对 begin 落盘行的 tx_prev_hash = 前置那条非 tx 记录的 entry_hash(非 64 零创世)。
	data, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	nonTxHash := auditLineField(t, lines[0], "entry_hash")
	if got := auditLineField(t, lines[1], "tx_id"); got != id {
		t.Fatalf("第二条记录 tx_id = %s, 期望 begin 的 %s", got, id)
	}
	beginPrev := auditLineField(t, lines[1], "tx_prev_hash")
	if beginPrev != nonTxHash || beginPrev == audit.GenesisHash {
		t.Fatalf("begin tx_prev_hash = %s, 期望前置非 tx 哈希 %s(且非创世)", beginPrev, nonTxHash)
	}
	mustVerify(t, logPath)
}

// auditLineField 取某条审计行的顶层字符串字段。
func auditLineField(t *testing.T, line, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("审计行非法 JSON: %v (%q)", err, line)
	}
	s, _ := m[key].(string)
	return s
}

// ───────────────────────── 失败步 → MarkFailed + 部分回滚 ─────────────────────────

func TestTx_FailedApplyMarksFailedAndPartialRollback(t *testing.T) {
	logPath, rc := harness(t)
	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "noop", `{}`); c != exitOK {
		t.Fatalf("propose noop: %d %s", c, e)
	}
	if c, _, e := rc("propose", id, "failapply", `{}`); c != exitOK {
		t.Fatalf("propose failapply: %d %s", c, e)
	}
	// apply: step1(noop)成功 → step2(failapply)失败 → exit 1 + {"error":...}。
	code, out, _ := rc("apply", id)
	if code != exitRuntime {
		t.Fatalf("失败 apply 退出码 = %d, want 1; out=%s", code, out)
	}
	var e errDoc
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || !strings.Contains(e.Error, "boom") {
		t.Fatalf("失败 apply 应回含 boom 的 error 文档, 实得 %q", out)
	}
	// 状态经 status 回读: failed + 步骤 OpResult 落盘(noop 成功 / failapply error boom)。
	c2, statusOut, _ := rc("status", id)
	if c2 != exitOK {
		t.Fatalf("失败后 status 仍应可读, 得 %d", c2)
	}
	st := parseStatus(t, statusOut)
	if st.Status != "failed" {
		t.Fatalf("失败后状态 = %s, want failed", st.Status)
	}
	if len(st.Steps) != 2 || st.Steps[0].OpResult.Returncode != 0 || st.Steps[1].OpResult.Error != "boom" {
		t.Fatalf("步骤 OpResult 不符: %+v", st.Steps)
	}
	// rollback(失败事务) → 只回滚已成功的前缀(noop), 仍成功, 状态保持 failed。
	if c, _, e := rc("rollback", id); c != exitOK {
		t.Fatalf("部分回滚退出码 = %d; stderr: %s", c, e)
	}
	recs := readAudit(t, logPath)
	// in-tx: begin + apply(noop) + apply(fail) + rollback(noop) = 4。
	if got := inTxCount(recs, id); got != 4 {
		t.Fatalf("失败往返 in-tx 记录数 = %d, want 4", got)
	}
	// rollback 那条 outcome 应为 success(noop 回滚成功), failapply 步 outcome=error。
	var sawErrApply, sawOkRollback bool
	for _, r := range recs {
		if r.TxID == id && r.Tool == "daedalus_tx_apply" && r.Outcome == "error" {
			sawErrApply = true
		}
		if r.TxID == id && r.Tool == "daedalus_tx_rollback" && r.Outcome == "success" {
			sawOkRollback = true
		}
	}
	if !sawErrApply || !sawOkRollback {
		t.Fatalf("失败/回滚 outcome 缺失: errApply=%v okRollback=%v", sawErrApply, sawOkRollback)
	}
	mustVerify(t, logPath) // 失败步仍是合法盖章链 → Verify 通过
}
