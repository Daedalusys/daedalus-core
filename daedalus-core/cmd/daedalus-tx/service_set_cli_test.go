package main

// service_set_cli_test.go —— service.set 的 CLI 全链路测试(todo 22)。
// 经由 run(argv...) 走 begin→propose→apply→rollback→status 完整回路:
// --unit-dir 旗标接线、盖章计数(3 条同 tx_id 事务记录)、audit.Verify 绿、
// 失败回路(invalid desired_state / 手改日志被 apply/rollback 守卫兜住)。
// 真实 user-manager 门控回路单住 service_set_gated_test.go。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ───────────────────────── 全回路: propose→apply→rollback→status ─────────────────────────

func TestServiceSet_CLI_RoundtripApplyRollback(t *testing.T) {
	logPath, rc := harness(t)
	dir := t.TempDir()
	content := "[Unit]\nDescription=demo\n[Service]\nType=oneshot\nExecStart=/bin/true\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.service"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "service.set",
		`{"name":"demo","desired_state":"started"}`, "--unit-dir", dir); c != exitOK {
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
	// 双链验证必须绿(todo 13 验证器)。
	mustVerify(t, logPath)
	// argv 全序: show(propose) → start(apply) → stop(rollback, prior=inactive→stop)。
	// 无文件漂移 → 无恢复写 → 无 daemon-reload。
	want := []string{
		"--user show demo.service --property=ActiveState",
		"--user start demo.service",
		"--user stop demo.service",
	}
	if got := captureCalls(t, capture); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("argv 全序 = %v, want %v", got, want)
	}
}

// ───────────────────────── --unit-dir 旗标接线 ─────────────────────────

func TestServiceSet_CLI_UnitDirFlagPlumbing(t *testing.T) {
	_, rc := harness(t)
	id := doBegin(t, rc)

	// 旗标缺值 → 用法错误 exit 2 + error JSON 点名 unit-dir; 重复旗标同样拒。
	c, out, _ := rc("propose", id, "service.set", `{"name":"demo","desired_state":"started"}`, "--unit-dir")
	if c != exitUsage || !strings.Contains(out, "unit-dir") {
		t.Errorf("缺值旗标应 exit2 + error JSON: code=%d out=%s", c, out)
	}
	c, out, _ = rc("apply", id, "--unit-dir")
	if c != exitUsage || !strings.Contains(out, "unit-dir") {
		t.Errorf("apply 缺值旗标应 exit2: code=%d out=%s", c, out)
	}
	c, out, _ = rc("rollback", id, "--unit-dir", "/tmp/x", "--unit-dir", "/tmp/y")
	if c != exitUsage || !strings.Contains(out, "unit-dir") {
		t.Errorf("重复旗标应 exit2: code=%d out=%s", c, out)
	}
}

// ───────────────────────── failure QA: invalid desired_state → apply 失败 ─────────────────────────

func TestServiceSet_CLI_InvalidDesiredFailsAtApply(t *testing.T) {
	logPath, rc := harness(t)
	dir := t.TempDir()
	fakeSystemctlUser(t, "ActiveState=inactive", 0)
	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "service.set",
		`{"name":"demo","desired_state":"invalid"}`, "--unit-dir", dir); c != exitOK {
		t.Fatalf("propose(未知 desired_state)应成功: %d %s", c, e)
	}
	c, out, _ := rc("apply", id, "--unit-dir", dir)
	if c != exitRuntime || !strings.Contains(out, "desired_state") {
		t.Fatalf("apply 应 exit1 且 error JSON 点名 desired_state: code=%d out=%s", c, out)
	}
	_, statusOut, _ := rc("status", id)
	st := parseStatus(t, statusOut)
	if st.Status != "failed" || !strings.Contains(st.Steps[0].OpResult.Error, "desired_state") {
		t.Fatalf("journal 应 failed 且拒绝原因落 OpResult: %+v", st)
	}
	mustVerify(t, logPath) // 失败回路同样是合法盖章链(链≠journal)
}

// ───────────────────────── 手改日志: apply/rollback 侧守卫(纵深防御)─────────────────────────

// handEditJournal 先正规 begin, 再把日志重写为指定形态(tx 层不理解步骤内容,
// 守卫必须在适配器层兜住手改的 args / before_state)。返回 tx-id。
func handEditJournal(t *testing.T, rc func(...string) (int, string, string), doc map[string]any) string {
	t.Helper()
	id := doBegin(t, rc)
	data, err := os.ReadFile(filepath.Join(os.Getenv("DAEDALUS_TX_DIR"), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fresh map[string]any
	if err := json.Unmarshal(data, &fresh); err != nil {
		t.Fatal(err)
	}
	fresh["steps"] = doc["steps"]
	if s, ok := doc["status"]; ok {
		fresh["status"] = s
	}
	if rp, ok := doc["rollback_plan"]; ok {
		fresh["rollback_plan"] = rp
	}
	out, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("DAEDALUS_TX_DIR"), id+".json"), append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

// serviceSetStep 构造一个可被手改注入的 service.set 步骤文档。
func serviceSetStep(args, before string) map[string]any {
	return map[string]any{
		"index":        0,
		"adapter":      "service.set",
		"args":         json.RawMessage(args),
		"before_state": json.RawMessage(before),
	}
}

func TestServiceSet_CLI_HandEditedJournalGuarded(t *testing.T) {
	logPath, rc := harness(t)
	dir := t.TempDir()
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
	t.Run("apply 拒非法名", func(t *testing.T) {
		id := handEditJournal(t, rc, map[string]any{
			"status": "proposed",
			"steps": []any{serviceSetStep(
				`{"name":"../../etc/passwd","desired_state":"started"}`,
				`{"unit_file":"/tmp/x/demo.service","active_state":"inactive"}`)},
		})
		c, out, _ := rc("apply", id, "--unit-dir", dir)
		if c != exitRuntime || !strings.Contains(out, "invalid unit name") {
			t.Fatalf("apply 守卫应拒非法名: code=%d out=%s", c, out)
		}
	})
	t.Run("apply 侧守卫: 系统级 unit-dir 直接拒且零执行", func(t *testing.T) {
		id := handEditJournal(t, rc, map[string]any{
			"status": "proposed",
			"steps": []any{serviceSetStep(
				`{"name":"victim","desired_state":"started"}`,
				`{"unit_file":"/tmp/x/victim.service","active_state":"inactive"}`)},
		})
		// 名称合法, 但 --unit-dir 落在系统级目录 → apply 守卫必须拒(纵深防御)。
		c, out, _ := rc("apply", id, "--unit-dir", "/etc/systemd/system")
		if c != exitRuntime || !strings.Contains(out, "v1 supports user-scope units only") {
			t.Fatalf("apply 守卫应拒系统级 unit-dir: code=%d out=%s", c, out)
		}
		for _, line := range captureCalls(t, capture) {
			if strings.Contains(line, "victim") {
				t.Fatalf("守卫拒绝后绝不允许执行 systemctl: %v", line)
			}
		}
	})
	t.Run("rollback 拒系统级快照路径", func(t *testing.T) {
		id := handEditJournal(t, rc, map[string]any{
			"status": "applied",
			"steps": []any{serviceSetStep(
				`{"name":"victim2","desired_state":"stopped"}`,
				`{"unit_file":"/etc/systemd/system/victim2.service","active_state":"active"}`)},
			"rollback_plan": map[string]any{"steps": []any{serviceSetStep(
				`{"name":"victim2","desired_state":"stopped"}`,
				`{"unit_file":"/etc/systemd/system/victim2.service","active_state":"active"}`)}},
		})
		c, _, e := rc("rollback", id, "--unit-dir", dir)
		if c != exitOK { // T15 语义: 步失败仍走链, 结果落 outcome
			t.Fatalf("rollback 退出码 = %d; stderr: %s", c, e)
		}
		// 守卫必须在执行任何 systemctl 之前拒绝: 该 id 的 rollback 步 outcome=error,
		// 且 capture 里不允许出现 victim2 的任何动词调用。
		recs := readAudit(t, logPath)
		var sawErrRollback bool
		for _, r := range recs {
			if r.TxID == id && r.Tool == "daedalus_tx_rollback" && r.Outcome == "error" {
				sawErrRollback = true
			}
		}
		if !sawErrRollback {
			t.Fatalf("手改系统级快照的 rollback 步应 outcome=error: %+v", recs)
		}
		for _, line := range captureCalls(t, capture) {
			if strings.Contains(line, "victim2") {
				t.Fatalf("守卫拒绝后绝不允许执行 systemctl: %v", line)
			}
		}
		mustVerify(t, logPath)
	})
}
