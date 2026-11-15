package main

// bootc 子命令的测试。全部用**假 bootc 可执行文件 + PATH 注入**驱动:
// 本机与 CI 都不保证装了 bootc,而且真实 bootc switch/rollback 会改系统,
// 绝不能出现在测试里。假实现把每次调用的参数追加一行到 marker 文件 ——
// "闸门生效"的证据不是 mock 计数,而是"那条命令在生产里会留下的痕迹不存在"。

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/audit"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// writeFakeBootc 写一个假 bootc 到临时目录,把它排到 PATH 最前面,返回
// 调用记录文件路径。body 是该脚本除记录动作外的附加行为(stdout/stderr/退出码)。
func writeFakeBootc(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + marker + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, "bootc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// bootcCalls 读假 bootc 的调用记录;文件不存在 = 一次都没跑,返回 nil。
func bootcCalls(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读假 bootc 调用记录失败: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// assertBootcNotExecuted 断言假 bootc 一次都没被调用(marker 文件未生成或零行)。
func assertBootcNotExecuted(t *testing.T, marker string) {
	t.Helper()
	if calls := bootcCalls(t, marker); len(calls) > 0 {
		t.Fatalf("闸门失效:不应执行任何 bootc 调用,实际调用了 %d 次: %v", len(calls), calls)
	}
}

// stubBootcTimeout 把写操作/只读操作的超时缩短,供超时用例使用。
func stubBootcTimeout(t *testing.T, status, write time.Duration) {
	t.Helper()
	origStatus, origWrite := bootcStatusTimeout, bootcWriteTimeout
	bootcStatusTimeout, bootcWriteTimeout = status, write
	t.Cleanup(func() { bootcStatusTimeout, bootcWriteTimeout = origStatus, origWrite })
}

// ---------------------------------------------------------------- status ----

// TestBootcStatusPassthrough:status 是只读,把 bootc 的 json 原样透传。
func TestBootcStatusPassthrough(t *testing.T) {
	writeFakeBootc(t, `echo '{"status":"ok","deployments":[{"id":"dep-1","booted":true}]}'
`)
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"status", "--json"}); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if !strings.Contains(out.String(), `"dep-1"`) {
		t.Fatalf("stdout 应原样透传 JSON, got %q", out.String())
	}
}

// TestBootcStatusAlwaysJSON:v1 只支持 json。用户不带 --json 时也必须按
// `status --json` 调用 —— 否则输出格式随 bootc 版本漂移,前端无法依赖。
func TestBootcStatusAlwaysJSON(t *testing.T) {
	marker := writeFakeBootc(t, `echo '{}'
`)
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"status"}); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	calls := bootcCalls(t, marker)
	if len(calls) != 1 || calls[0] != "status --json" {
		t.Fatalf("应恰好调用一次 `bootc status --json`, 实际 %v", calls)
	}
}

// TestBootcStatusPermissionHint:非 root 跑 status 时 bootc 可能报权限错误。
// 这时必须给出可操作的 sudo 提示,而不是把 stderr 吞掉或静默退化成空结果。
func TestBootcStatusPermissionHint(t *testing.T) {
	writeFakeBootc(t, `echo 'must be root' >&2
exit 1
`)
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"status"})
	if rc != 1 {
		t.Fatalf("rc=%d, want 1(透传子进程退出码)", rc)
	}
	if !strings.Contains(errb.String(), "must be root") {
		t.Errorf("应透传 bootc 的 stderr, got %q", errb.String())
	}
	if !strings.Contains(errb.String(), "sudo daedalus-host bootc status") {
		t.Errorf("权限失败时应提示 `sudo daedalus-host bootc status`, got %q", errb.String())
	}
}

// TestBootcStatusMissingBinary:bootc 不存在时报明确错误,不是静默成功。
func TestBootcStatusMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // 空 PATH:bootc 一定找不到
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"status"}); rc == 0 {
		t.Fatalf("bootc 缺失时应非零退出, got 0; stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "bootc") {
		t.Errorf("stderr 应提到 bootc, got %q", errb.String())
	}
}

// TestBootcTimeouts:超时是硬约束(status 60s、switch/rollback 900s),不许被
// `--yes` 或参数解析绕过。
func TestBootcTimeouts(t *testing.T) {
	if bootcStatusTimeout != 60*time.Second {
		t.Errorf("status 超时 = %v, want 60s", bootcStatusTimeout)
	}
	if bootcWriteTimeout != 900*time.Second {
		t.Errorf("switch/rollback 超时 = %v, want 900s", bootcWriteTimeout)
	}
}

// TestBootcStatusTimeoutEnforced:超时必须真生效(exec.CommandContext),
// 不能只是写在常量里当装饰。
func TestBootcStatusTimeoutEnforced(t *testing.T) {
	stubBootcTimeout(t, 50*time.Millisecond, time.Second)
	writeFakeBootc(t, `exec sleep 2
`)
	var out, errb bytes.Buffer
	start := time.Now()
	rc := cmdBootc(&out, &errb, []string{"status"})
	if rc == 0 {
		t.Fatalf("超时应非零退出, got 0")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("超时未生效:耗时 %v(假 bootc 睡 2s)", elapsed)
	}
	if !strings.Contains(errb.String(), "超时") {
		t.Errorf("stderr 应说明超时, got %q", errb.String())
	}
}

// ---------------------------------------------------------------- switch ----

// TestBootcSwitchWithoutYesDoesNothing:无 --yes 时只打印将执行什么,退出码 2,
// 一条写命令都不执行(marker 证明)。
func TestBootcSwitchWithoutYesDoesNothing(t *testing.T) {
	marker := writeFakeBootc(t, "")
	ref := "quay.io/daedalusys/daedalus-os:latest"
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"switch", ref, "--digest", testDigest})
	if rc != exitUsage {
		t.Fatalf("rc=%d, want %d", rc, exitUsage)
	}
	assertBootcNotExecuted(t, marker)
	want := "would run: bootc switch " + ref + "@" + testDigest
	if !strings.Contains(errb.String(), want) {
		t.Errorf("stderr 应打印计划 %q, got %q", want, errb.String())
	}
}

// TestBootcSwitchMissingDigest:digest 必填 —— 只给 tag 的切换等于"你看过 A、
// 装的是 B"。缺 digest 直接拒绝,且不执行。
func TestBootcSwitchMissingDigest(t *testing.T) {
	marker := writeFakeBootc(t, "")
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"switch", "quay.io/daedalusys/daedalus-os:latest", "--yes"})
	if rc != exitUsage {
		t.Fatalf("rc=%d, want %d", rc, exitUsage)
	}
	assertBootcNotExecuted(t, marker)
	for _, want := range []string{"--digest", "bootc status --json"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr 应包含 %q, got %q", want, errb.String())
		}
	}
}

// TestBootcSwitchInvalidDigest:digest 格式不对一律拒绝(仅接受
// sha256:<64 位小写 hex>),不执行。
func TestBootcSwitchInvalidDigest(t *testing.T) {
	cases := []string{
		"sha256:abc",                              // 太短
		"sha256:" + strings.Repeat("A", 64),       // 大写 hex
		strings.Repeat("0", 64),                   // 缺 sha256: 前缀
		"sha512:" + strings.Repeat("0", 128),      // 算法不对
		"sha256:" + strings.Repeat("0", 63) + "z", // 非 hex
	}
	for _, dg := range cases {
		t.Run(dg, func(t *testing.T) {
			marker := writeFakeBootc(t, "")
			var out, errb bytes.Buffer
			rc := cmdBootc(&out, &errb, []string{"switch", "quay.io/x/y:1", "--digest", dg, "--yes"})
			if rc != exitUsage {
				t.Fatalf("digest %q: rc=%d, want %d", dg, rc, exitUsage)
			}
			assertBootcNotExecuted(t, marker)
			if !strings.Contains(errb.String(), "sha256") {
				t.Errorf("stderr 应说明 digest 格式要求, got %q", errb.String())
			}
		})
	}
}

// TestBootcSwitchWithYesExecutes:--yes 时执行一次 `switch <ref>@<digest>`,
// 随后再取一次 status 打印新部署 id(切换要有自证)。
func TestBootcSwitchWithYesExecutes(t *testing.T) {
	marker := writeFakeBootc(t, `if [ "$1" = status ]; then
  echo '{"deployments":[{"id":"dep-9","booted":true}]}'
fi
`)
	ref := "quay.io/daedalusys/daedalus-os:latest"
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"switch", ref, "--digest", testDigest, "--yes"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	calls := bootcCalls(t, marker)
	if len(calls) != 2 {
		t.Fatalf("应恰好两次调用(switch + status),实际 %v", calls)
	}
	if want := "switch " + ref + "@" + testDigest; calls[0] != want {
		t.Errorf("第一次调用 = %q, want %q", calls[0], want)
	}
	if calls[1] != "status --json" {
		t.Errorf("第二次调用 = %q, want %q", calls[1], "status --json")
	}
	if !strings.Contains(out.String(), "dep-9") {
		t.Errorf("stdout 应打印新部署 id, got %q", out.String())
	}
}

// TestBootcSwitchDigestEqualsForm:`--digest=<hex>` 等号写法同样生效(不接受
// 就是给用户留坑,而这里放宽的只是写法,不是 digest 的必填性)。
func TestBootcSwitchDigestEqualsForm(t *testing.T) {
	marker := writeFakeBootc(t, "")
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"switch", "quay.io/x/y:1", "--digest=" + testDigest, "--yes"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	calls := bootcCalls(t, marker)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "switch quay.io/x/y:1@sha256:") {
		t.Fatalf("第一次调用应带 digest, 实际 %v", calls)
	}
}

// TestBootcSwitchRejectsExtraArgs:多给一个 ref 或未知旗标 → 用法错误,不执行。
func TestBootcSwitchRejectsExtraArgs(t *testing.T) {
	for _, argv := range [][]string{
		{"switch", "quay.io/x/y:1", "quay.io/x/z:2", "--digest", testDigest, "--yes"},
		{"switch", "quay.io/x/y:1", "--digest", testDigest, "--force"},
		{"switch", "--digest", testDigest, "--yes"}, // 缺 ref
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			marker := writeFakeBootc(t, "")
			var out, errb bytes.Buffer
			if rc := cmdBootc(&out, &errb, argv); rc != exitUsage {
				t.Fatalf("rc=%d, want %d; stderr=%s", rc, exitUsage, errb.String())
			}
			assertBootcNotExecuted(t, marker)
		})
	}
}

// -------------------------------------------------------------- rollback ----

// TestBootcRollbackWithoutYesDoesNothing:回滚同样要 --yes,否则只打印计划。
func TestBootcRollbackWithoutYesDoesNothing(t *testing.T) {
	marker := writeFakeBootc(t, "")
	var out, errb bytes.Buffer
	rc := cmdBootc(&out, &errb, []string{"rollback"})
	if rc != exitUsage {
		t.Fatalf("rc=%d, want %d", rc, exitUsage)
	}
	assertBootcNotExecuted(t, marker)
	if !strings.Contains(errb.String(), "would run: bootc rollback") {
		t.Errorf("stderr 应打印计划, got %q", errb.String())
	}
}

// TestBootcRollbackWithYesExecutes:--yes 时执行一次 rollback,随后再取一次
// status 打印新部署 id —— 否则用户无法判断"回滚成功"还是"命令返回 0 但没动"。
func TestBootcRollbackWithYesExecutes(t *testing.T) {
	marker := writeFakeBootc(t, `if [ "$1" = status ]; then
  echo '{"deployments":[{"id":"dep-0","booted":true}]}'
fi
`)
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"rollback", "--yes"}); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	calls := bootcCalls(t, marker)
	if len(calls) != 2 || calls[0] != "rollback" || calls[1] != "status --json" {
		t.Fatalf("应 `rollback` 后紧跟 `status --json`,实际 %v", calls)
	}
	if !strings.Contains(out.String(), "dep-0") {
		t.Errorf("stdout 应打印回滚后的部署 id, got %q", out.String())
	}
}

// TestBootcRollbackStatusUnreadableStillSucceeds:写操作成功但自证读不到 → 仍
// 算成功(不把读失败当写失败),但必须明说"读不到部署,建议人工确认"。
func TestBootcRollbackStatusUnreadableStillSucceeds(t *testing.T) {
	writeFakeBootc(t, `if [ "$1" = status ]; then
  echo 'status exploded' >&2
  exit 1
fi
`)
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"rollback", "--yes"}); rc != 0 {
		t.Fatalf("回滚成功时 rc 应为 0, got %d; stderr=%s", rc, errb.String())
	}
	if !strings.Contains(errb.String(), "读不到部署") {
		t.Errorf("stderr 应提示读不到部署、建议人工确认, got %q", errb.String())
	}
}

// TestBootcRollbackRejectsExtraArgs:rollback 不接受位置参数或未知旗标。
func TestBootcRollbackRejectsExtraArgs(t *testing.T) {
	for _, argv := range [][]string{
		{"rollback", "dep-1"},
		{"rollback", "--digest", testDigest},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			marker := writeFakeBootc(t, "")
			var out, errb bytes.Buffer
			if rc := cmdBootc(&out, &errb, argv); rc != exitUsage {
				t.Fatalf("rc=%d, want %d; stderr=%s", rc, exitUsage, errb.String())
			}
			assertBootcNotExecuted(t, marker)
		})
	}
}

// ------------------------------------------------------------ 分发与帮助 ----

// TestBootcUnknownSubcommand:未知子命令是用法错误(退出码 2),且不执行。
func TestBootcUnknownSubcommand(t *testing.T) {
	marker := writeFakeBootc(t, "")
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"upgrade"}); rc != exitUsage {
		t.Fatalf("rc=%d, want %d", rc, exitUsage)
	}
	assertBootcNotExecuted(t, marker)
	// 帮助文本必须写明 digest 是 switch 的必填项(用户第一现场就在这里)。
	if !strings.Contains(errb.String(), "--digest") {
		t.Errorf("帮助文本应说明 switch 必须带 --digest, got %q", errb.String())
	}
}

// TestBootcNoArgsPrintsUsage:无子命令 → 用法错误 + 帮助。
func TestBootcNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, nil); rc != exitUsage {
		t.Fatalf("rc=%d, want %d", rc, exitUsage)
	}
	for _, want := range []string{"status", "switch", "rollback", "--yes"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("帮助文本应包含 %q, got %q", want, errb.String())
		}
	}
}

// TestBootcHelpIsOK:`bootc --help` 打到 stdout 且退出码 0(与宿主其它子命令一致)。
func TestBootcHelpIsOK(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"--help"}); rc != exitOK {
		t.Fatalf("rc=%d, want %d", rc, exitOK)
	}
	if !strings.Contains(out.String(), "bootc") {
		t.Errorf("stdout 应打印 bootc 帮助, got %q", out.String())
	}
}

// -------------------------------------------------------------- 退出码与审计 ----

// TestBootcWriteExitCodePassesThrough:bootc 自己失败时透传其退出码(不是一律
// 归并为 1),否则脚本侧无法区分"用法错误"与"运行失败"。
func TestBootcWriteExitCodePassesThrough(t *testing.T) {
	writeFakeBootc(t, `exit 3
`)
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"rollback", "--yes"}); rc != 3 {
		t.Fatalf("rc=%d, want 3(透传子进程退出码)", rc)
	}
}

// TestBootcRunDispatchAndAudit:run() 必须把 bootc 分派进 cmdBootc,并落一条
// host_bootc 审计(写操作是可追溯的特权动作,拒绝也要留痕)。
func TestBootcRunDispatchAndAudit(t *testing.T) {
	marker := writeFakeBootc(t, "")
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, logPath)

	code, _, errOut := runCapture(t, "bootc", "rollback")
	if code != exitUsage {
		t.Fatalf("run(bootc rollback) rc=%d, want %d; stderr=%s", code, exitUsage, errOut)
	}
	assertBootcNotExecuted(t, marker)

	recs := readAuditLines(t, logPath, 1)
	if recs[0]["tool"] != "host_bootc" || recs[0]["outcome"] != "denied" {
		t.Fatalf("审计 = %v/%v, want host_bootc/denied", recs[0]["tool"], recs[0]["outcome"])
	}
}

// TestBootcStatusRejectsUnknownFlags:status 只接受 --json;别的旗标是用法错误。
func TestBootcStatusRejectsUnknownFlags(t *testing.T) {
	marker := writeFakeBootc(t, "")
	var out, errb bytes.Buffer
	if rc := cmdBootc(&out, &errb, []string{"status", "--yaml"}); rc != exitUsage {
		t.Fatalf("rc=%d, want %d; stderr=%s", rc, exitUsage, errb.String())
	}
	assertBootcNotExecuted(t, marker)
}

// TestExtractBootedDeployment:自证只取稳定字段 —— deployments 里 booted=true 的
// id,退路是 status.booted.image。解析失败返回空串(由调用方转成提示),不 panic。
func TestExtractBootedDeployment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"deployments 取 booted 的 id",
			`{"deployments":[{"id":"dep-1","booted":false},{"id":"dep-2","booted":true}]}`, "dep-2"},
		{"退路 status.booted.image",
			`{"status":{"booted":{"image":"quay.io/x/y@sha256:abc"}}}`,
			"quay.io/x/y@sha256:abc"},
		{"都没有 → 空串", `{"hello":"world"}`, ""},
		{"非法 JSON → 空串", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractBootedDeployment([]byte(tc.in)); got != tc.want {
				t.Errorf("extractBootedDeployment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBootcSubcommandList:帮助里列出的子命令必须与分发实现一致(防漂移)。
func TestBootcSubcommandList(t *testing.T) {
	want := []string{"status", "switch", "rollback"}
	if !slices.Equal(bootcSubcommands, want) {
		t.Fatalf("bootcSubcommands = %v, want %v", bootcSubcommands, want)
	}
}
