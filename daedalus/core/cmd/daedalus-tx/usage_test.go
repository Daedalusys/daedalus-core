package main

// usage_test.go —— 退出码 / stdout JSON 契约 / 用法与路径门。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTx_InvalidIDExit2(t *testing.T) {
	_, rc := harness(t)
	// 敌意 id 全部 exit 2(用法错), 且不因拼接路径而在日志根外落任何文件。
	for _, bad := range []string{"deadbeef", "DEADBEEFDEADBEEF", "../etc/passwd", "aaaaaaaaaaaaaaaag", "", "a/b/c/d/e/f/g/h"} {
		code, out, _ := rc("status", bad)
		if code != exitUsage {
			t.Fatalf("status %q 退出码 = %d, want 2; out=%s", bad, code, out)
		}
		var e errDoc
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || e.Error == "" {
			t.Fatalf("status %q 非法 id 应回 {\"error\":...}, 实得 %q", bad, out)
		}
	}
	// begin/propose/apply/rollback 的非法 id 同样 exit 2。
	for _, argv := range [][]string{{"apply", "zz"}, {"rollback", "zz"}, {"propose", "zz", "noop", "{}"}} {
		if code, _, _ := rc(argv...); code != exitUsage {
			t.Fatalf("%v 退出码 = %d, want 2", argv, code)
		}
	}
}

func TestTx_UnknownAdapterExit1(t *testing.T) {
	_, rc := harness(t)
	id := doBegin(t, rc)
	code, out, _ := rc("propose", id, "no_such_adapter", `{}`)
	if code != exitRuntime {
		t.Fatalf("未知适配器退出码 = %d, want 1", code)
	}
	var e errDoc
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || !strings.Contains(e.Error, "未知适配器") {
		t.Fatalf("未知适配器应回 error 文档, 实得 %q", out)
	}
}

func TestTx_NotFoundAndStateErrors(t *testing.T) {
	_, rc := harness(t)
	// apply 不存在的 id → exit 1 + "transaction <id> not found"。
	code, out, _ := rc("apply", "00112233445566aa")
	if code != exitRuntime || !strings.Contains(out, "not found") {
		t.Fatalf("apply 不存在 id: %d %q", code, out)
	}
	// rollback 从 proposed(从未 apply)→ exit 1 + nothing to rollback。
	id := doBegin(t, rc)
	code, out, _ = rc("rollback", id)
	if code != exitRuntime || !strings.Contains(out, "nothing to rollback") {
		t.Fatalf("proposed 回滚: %d %q", code, out)
	}
}

func TestTx_MalformedArgsJSONExit2(t *testing.T) {
	_, rc := harness(t)
	id := doBegin(t, rc)
	if code, _, _ := rc("propose", id, "noop", "{not json"); code != exitUsage {
		t.Fatalf("非法 args-json 退出码 = %d, want 2", code)
	}
}

func TestTx_UsageExitCodes(t *testing.T) {
	_, rc := harness(t)
	if code, out, _ := rc(); code != exitUsage || strings.TrimSpace(out) != "" {
		t.Fatalf("无参数应 exit 2 且 stdout 空(帮助走 stderr), 得 %d / %q", code, out)
	}
	if code, _, _ := rc("frobnicate"); code != exitUsage {
		t.Fatalf("未知子命令应 exit 2, 得 %d", code)
	}
	if code, out, _ := rc("-h"); code != exitOK || !strings.Contains(out, "begin") || !strings.Contains(out, "rollback") {
		t.Fatalf("-h 应 exit 0 + 列全子命令, 得 %d / %q", code, out)
	}
	// begin 不接受参数 → exit 2。
	if code, _, _ := rc("begin", "extra"); code != exitUsage {
		t.Fatalf("begin 多余参数应 exit 2, 得 %d", code)
	}
}

func TestTx_EmptyTxIDNeverReusedAndStatusShape(t *testing.T) {
	_, rc := harness(t)
	id := doBegin(t, rc)
	code, out, errOut := rc("status", id)
	if code != exitOK {
		t.Fatalf("status 退出码 = %d; stderr: %s", code, errOut)
	}
	st := parseStatus(t, out)
	if st.ID != id || st.Status != "proposed" || len(st.Steps) != 0 {
		t.Fatalf("status 形态异常: %+v", st)
	}
}
