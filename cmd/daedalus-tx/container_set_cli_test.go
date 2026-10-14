package main

// container_set_cli_test.go —— container.set 的 CLI 全链路测试。
//
// 经由 run(argv...) 走 begin→propose→apply→rollback→status 完整回路:
// 盖章计数(3 条同 tx_id 事务记录)、audit.Verify 绿、absent 单向 stdoute note.

import (
	"strings"
	"testing"
)

func TestContainerSet_CLI_RoundTrip_Started(t *testing.T) {
	withNonRootEuid(t)
	logPath, rc := harness(t)
	fixtureJSON := `[{"Id":"abc123","State":{"Running":false},"Image":"nginx:latest","Status":"Exited"}]`
	capture := installFakePodman(t, fixtureJSON, 0)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "container.set",
		`{"name":"web","desired_state":"started"}`); c != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", c, e)
	}
	if c, out, e := rc("apply", id); c != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	if c, out, e := rc("rollback", id); c != exitOK {
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

	// 盖章: begin + apply + rollback = 3 条同 tx_id;propose/status 空 TxID.
	recs := readAudit(t, logPath)
	if got := inTxCount(recs, id); got != 3 {
		t.Fatalf("in-tx 记录数 = %d, want 3", got)
	}
	mustVerify(t, logPath)

	// argv 全序: inspect(propose) → start(apply;before running=false) → stop
	// (rollback;before running=false → 还原到非运行).
	want := []string{
		"inspect --type container --format json web",
		"start web",
		"stop web",
	}
	if got := readPodmanCalls(t, capture); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("podman argv 全序 = %v, want %v", got, want)
	}
}

func TestContainerSet_CLI_RoundTrip_AbsentUnidirectional(t *testing.T) {
	withNonRootEuid(t)
	logPath, rc := harness(t)
	fixtureJSON := `[{"Id":"abc123","State":{"Running":false},"Image":"nginx:latest","Status":"Exited"}]`
	capture := installFakePodman(t, fixtureJSON, 0)

	id := doBegin(t, rc)
	if c, _, e := rc("propose", id, "container.set",
		`{"name":"web","desired_state":"absent"}`); c != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", c, e)
	}
	if c, out, e := rc("apply", id); c != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	if c, out, e := rc("rollback", id); c != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s out=%s", c, e, out)
	}
	c, statusOut, e := rc("status", id)
	if c != exitOK {
		t.Fatalf("status 退出码 = %d; stderr: %s", c, e)
	}
	st := parseStatus(t, statusOut)
	if st.Status != "rolled_back" {
		t.Fatalf("终态异常: %+v", st)
	}
	// absent 单向: rollback 应 return rc=0(OpResult.Stdout 不可见,单测已锁).
	if st.Steps[0].OpResult.Returncode != 0 {
		t.Errorf("absent rollback 应返 rc=0(单向), got %+v", st.Steps[0].OpResult)
	}
	// argv: inspect(propose) + rm(apply);rollback 无 podman.
	want := []string{
		"inspect --type container --format json web",
		"rm --force web",
	}
	if got := readPodmanCalls(t, capture); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("podman argv 全序 = %v, want %v", got, want)
	}
	mustVerify(t, logPath)
}

// TestContainerSet_CLI_RejectsEuidZero 走完整 CLI 回路: euid==0 守门必须在
// propose 阶段 exit 1 + stderr 点名 rootless.
func TestContainerSet_CLI_RejectsEuidZero(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = orig })

	_, rc := harness(t)
	id := doBegin(t, rc)
	c, out, _ := rc("propose", id, "container.set",
		`{"name":"web","desired_state":"present"}`)
	if c != exitRuntime {
		t.Fatalf("propose(euid==0) 应 exit1, got %d out=%s", c, out)
	}
	if !strings.Contains(out, "requires rootless") {
		t.Errorf("error JSON 应点名 rootless, got %s", out)
	}
}

