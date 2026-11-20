package main

// apply_guard_test.go —— issue #205: 零步骤事务必须拒绝 apply。
// begin 建出的是 proposed + 零步骤;propose 失败时也不追加步骤。两种情形下
// apply 都不得把空事务盖成 applied(否则成功响应 + rollback_plan:{} 会骗过调用方)。

import (
	"strings"
	"testing"
)

func TestApply_RejectsZeroStepTransaction(t *testing.T) {
	_, rc := harness(t)

	// 用例 1: begin 后直接 apply —— 事务从未 propose 过。
	id := doBegin(t, rc)
	code, out, errOut := rc("apply", id)
	if code != exitRuntime {
		t.Fatalf("零步骤 apply 退出码 = %d, want %d; out=%s stderr=%s", code, exitRuntime, out, errOut)
	}
	if !strings.Contains(errOut, "无步骤") {
		t.Fatalf("stderr 应说明事务无步骤, 实得 %q", errOut)
	}
	if !strings.Contains(errOut, id) {
		t.Fatalf("stderr 应带 tx id %s, 实得 %q", id, errOut)
	}
	if strings.Contains(out, `"status":"applied"`) {
		t.Fatalf("零步骤 apply 不得打印 applied JSON: %q", out)
	}
	c, statusOut, _ := rc("status", id)
	if c != exitOK {
		t.Fatalf("apply 被拒后 status 应可读, 得 %d", c)
	}
	st := parseStatus(t, statusOut)
	if st.Status != "proposed" || len(st.Steps) != 0 {
		t.Fatalf("apply 被拒后事务应保持 proposed/0 步, 实得 status=%s steps=%d", st.Status, len(st.Steps))
	}

	// 用例 2: propose 必然失败(未知适配器)后 apply —— 仍是零步骤。
	id2 := doBegin(t, rc)
	if c, _, e := rc("propose", id2, "does.not.exist", `{}`); c == exitOK {
		t.Fatalf("未知适配器 propose 应失败, 得 rc=%d stderr=%s", c, e)
	}
	// propose 失败只允许重试/放弃, 不得把事务标成 failed。
	c, statusOut, _ = rc("status", id2)
	if c != exitOK {
		t.Fatalf("propose 失败后 status 应可读, 得 %d", c)
	}
	if st := parseStatus(t, statusOut); st.Status != "proposed" || len(st.Steps) != 0 {
		t.Fatalf("propose 失败后事务应保持 proposed/0 步, 实得 status=%s steps=%d", st.Status, len(st.Steps))
	}

	code, out, errOut = rc("apply", id2)
	if code != exitRuntime {
		t.Fatalf("propose 失败后 apply 退出码 = %d, want %d; out=%s stderr=%s", code, exitRuntime, out, errOut)
	}
	if !strings.Contains(errOut, "无步骤") || !strings.Contains(errOut, id2) {
		t.Fatalf("stderr 应带「无步骤」与 tx id, 实得 %q", errOut)
	}
	if strings.Contains(out, `"status":"applied"`) {
		t.Fatalf("零步骤 apply 不得打印 applied JSON: %q", out)
	}
	c, statusOut, _ = rc("status", id2)
	if c != exitOK {
		t.Fatalf("apply 被拒后 status 应可读, 得 %d", c)
	}
	if st := parseStatus(t, statusOut); st.Status != "proposed" || len(st.Steps) != 0 {
		t.Fatalf("apply 被拒后事务应保持 proposed/0 步, 实得 status=%s steps=%d", st.Status, len(st.Steps))
	}

	// 用例 3: 成功路径不回归 —— 有步骤的事务照常 applied。
	id3 := doBegin(t, rc)
	if c, _, e := rc("propose", id3, "noop", `{}`); c != exitOK {
		t.Fatalf("propose noop 退出码 = %d; stderr: %s", c, e)
	}
	c, out, errOut = rc("apply", id3)
	if c != exitOK {
		t.Fatalf("有步骤事务 apply 退出码 = %d; stderr: %s", c, errOut)
	}
	if st := parseStatus(t, out); st.Status != "applied" || len(st.Steps) != 1 {
		t.Fatalf("成功路径回归: status=%s steps=%d", st.Status, len(st.Steps))
	}
}
