package main

// task_set_cli_test.go —— task.set 的 CLI 全链路测试。
//
// 经由 run(argv...) 走 begin→propose→apply→rollback→status 完整回路:
// --unit-dir 旗标接线、盖章计数(3 条同 tx_id 事务记录)、audit.Verify 绿。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskSet_CLI_RoundTrip(t *testing.T) {
	logPath, rc := harness(t)
	dir := t.TempDir()
	content := "[Timer]\nOnCalendar=daily\n"
	if err := os.WriteFile(filepath.Join(dir, "backup.timer"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := installFakeSystemctl(t, "disabled", 0)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "task.set",
		`{"name":"backup","desired_state":"enabled"}`, "--unit-dir", dir); c != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", c, e)
	}
	if c, out, e := rc("apply", id, "--unit-dir", dir); c != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	if c, out, e := rc("rollback", id, "--unit-dir", dir); c != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	c, statusOut, e := rc("status", id)
	if c != exitOK {
		t.Fatalf("status 退出码 = %d; stderr: %s", c, e)
	}
	st := parseStatus(t, statusOut)
	if st.Status != "rolled_back" || len(st.Steps) != 1 || st.Steps[0].OpResult.Returncode != 0 {
		t.Fatalf("终态异常: %+v", st)
	}

	// 盖章规则: begin(0)+apply(1)+rollback(2) = 恰 3 条同 tx_id; propose/status 空 TxID。
	recs := readAudit(t, logPath)
	if got := inTxCount(recs, id); got != 3 {
		t.Fatalf("in-tx 记录数 = %d, want 3", got)
	}
	mustVerify(t, logPath)

	// argv 全序: is-enabled(propose) → enable(apply) → disable(rollback; prior=disabled)。
	want := []string{
		"--user is-enabled backup.timer",
		"--user enable backup.timer",
		"--user disable backup.timer",
	}
	if got := readCaptured(t, capture); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv 全序 = %v, want %v", got, want)
	}
}

func TestTaskSet_CLI_InvalidDesiredFailsAtPropose(t *testing.T) {
	logPath, rc := harness(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.timer"), []byte("[Timer]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installFakeSystemctl(t, "disabled", 0)
	id := doBegin(t, rc)
	// task.set 与 service.set 的关键差异: 表外 desired_state 在 **propose 即拒**
	// (package.set 同款,三值封闭无“先提议后拒绝”语义)。
	c, out, _ := rc("propose", id, "task.set",
		`{"name":"backup","desired_state":"started"}`, "--unit-dir", dir)
	if c != exitRuntime || !strings.Contains(out, "desired_state") {
		t.Fatalf("propose(表外 desired_state)应 exit1 且点名 desired_state: code=%d out=%s", c, out)
	}
	if _, statusOut, _ := rc("status", id); parseStatus(t, statusOut).Status != "proposed" {
		t.Fatalf("propose 失败后事务应留在 proposed")
	}
	mustVerify(t, logPath)
}
