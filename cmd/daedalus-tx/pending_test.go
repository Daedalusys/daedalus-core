package main

// pending_test.go —— 待办(pending)与批准(approve)这道人工闸门的全链路测试。
//
// 钉住的契约: 批准目录是唯一的门(非 root 写不进去)、argv 用 NUL 连接的规范
// 哈希绑定具体动作、过期待办一律作废、坏待办 fail-closed 而不是猜。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 固定 argv 的规范哈希(sha256(NUL 连接))。这里是**手算值**而不是调用实现:
// 哈希形状是跨语言契约(消费侧在另一仓实现), 必须被测试独立钉死。
const (
	testPendingArgvSHA256 = "802d8be0ec50f15ec1cff1b7187c5f7867d52f1e49891389b44051954c532251"
	testPendingID         = "p-20261120-abcdef01"
)

// newPendingEnv 建隔离环境: 审计/事务目录(harness)+ 待办目录(1777 语义)+
// 批准目录(生产是 0755 root:root; 测试里属主是跑测试的用户, 权限位与生产一致)。
func newPendingEnv(t *testing.T) (pendingDir, approvalsDir, logPath string, rc func(...string) (int, string, string)) {
	t.Helper()
	logPath, rc = harness(t)
	base := t.TempDir()
	pendingDir = filepath.Join(base, "pending")
	approvalsDir = filepath.Join(base, "approvals")
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{pendingDir, 0o1777}, {approvalsDir, 0o755}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", d.path, err)
		}
		// umask 会清掉权限位, 显式 chmod 让测试环境的权限与生产一致。
		if err := os.Chmod(d.path, d.mode); err != nil {
			t.Fatalf("设置目录 %s 权限失败: %v", d.path, err)
		}
	}
	t.Setenv(envPendingDir, pendingDir)
	t.Setenv(envApprovalsDir, approvalsDir)
	return pendingDir, approvalsDir, logPath, rc
}

// writePendingFile 直写一份待办文件, 模拟 AI 侧(消费侧)产出。
// 用 map 而不是实现里的结构体: 测试要钉的是线上 JSON 键, 不是内部类型。
func writePendingFile(t *testing.T, dir, id string, argv []string, expiresAt time.Time) string {
	t.Helper()
	doc := map[string]any{
		"id":            id,
		"kind":          "service",
		"name":          "nginx.service",
		"desired_state": "stopped",
		"argv":          argv,
		"scope":         "systemd:user:stop:nginx.service",
		"reason":        "tier=human",
		"created_at":    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
		"expires_at":    expiresAt.UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化待办失败: %v", err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写待办失败: %v", err)
	}
	return path
}

// stubEuid 覆写 euid 注入缝, 测试结束自动还原。
func stubEuid(t *testing.T, uid int) {
	t.Helper()
	orig := geteuid
	t.Cleanup(func() { geteuid = orig })
	geteuid = func() int { return uid }
}

// readApproval 读批准记录, 顺带断言它是合法 JSON。
func readApproval(t *testing.T, dir, id string) approvalDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("批准记录不存在: %v", err)
	}
	var rec approvalDoc
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("批准记录不是合法 JSON: %v (%q)", err, data)
	}
	return rec
}

func TestPendingListShowsArgv(t *testing.T) {
	dir, _, _, rc := newPendingEnv(t)
	argv := []string{"systemctl", "--user", "stop", "nginx.service"}
	writePendingFile(t, dir, testPendingID, argv, time.Now().UTC().Add(time.Hour))

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 1 {
		t.Fatalf("应列出 1 条待办, 实得 %d: %s", len(doc.Pending), out)
	}
	entry := doc.Pending[0]
	if entry.ID != testPendingID {
		t.Errorf("id = %q, want %q", entry.ID, testPendingID)
	}
	if len(entry.Argv) != len(argv) || entry.Argv[0] != "systemctl" {
		t.Errorf("待办必须列出将执行的 argv, 实得 %v", entry.Argv)
	}
	if entry.Expired {
		t.Error("未过期待办不得标 expired")
	}
	if entry.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("argv_sha256 = %q, want %q(规范哈希被改变?)", entry.ArgvSHA256, testPendingArgvSHA256)
	}
}

func TestPendingListMissingDirIsEmpty(t *testing.T) {
	_, _, _, rc := newPendingEnv(t)
	t.Setenv(envPendingDir, filepath.Join(t.TempDir(), "no-such-dir"))

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("目录不存在时 pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	if !strings.Contains(out, `"pending":[]`) {
		t.Errorf("空列表必须是 [] 而不是 null: %q", out)
	}
}

func TestPendingListToleratesInvalidFiles(t *testing.T) {
	dir, _, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("写坏文件失败: %v", err)
	}

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 1 || len(doc.Invalid) != 1 {
		t.Fatalf("好文件进 pending、坏文件进 invalid, 实得 pending=%d invalid=%d", len(doc.Pending), len(doc.Invalid))
	}
	if doc.Invalid[0].File != "broken.json" || doc.Invalid[0].Error == "" {
		t.Errorf("invalid 条目应点名文件与原因: %+v", doc.Invalid[0])
	}
}

func TestPendingShowPrintsArgvAndHash(t *testing.T) {
	dir, _, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))

	code, out, errOut := rc("pending", "show", testPendingID)
	if code != exitOK {
		t.Fatalf("pending show 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)
	var entry pendingListEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
		t.Fatalf("stdout 非待办视图: %v (%q)", err, out)
	}
	if entry.ID != testPendingID || entry.Argv[3] != "nginx.service" {
		t.Errorf("show 输出异常: %+v", entry)
	}
	if entry.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("argv_sha256 = %q, want %q", entry.ArgvSHA256, testPendingArgvSHA256)
	}
	if entry.Kind != "service" || entry.DesiredState != "stopped" || entry.Scope != "systemd:user:stop:nginx.service" {
		t.Errorf("待办声明字段丢失: %+v", entry)
	}
}

func TestPendingShowUnknownIDIsRuntimeError(t *testing.T) {
	_, _, _, rc := newPendingEnv(t)
	code, out, _ := rc("pending", "show", "p-20261120-00000000")
	if code != exitRuntime {
		t.Fatalf("未知待办退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	var e errDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &e); err != nil || !strings.Contains(e.Error, "不存在") {
		t.Errorf("未知待办应回 {\"error\":\"...不存在\"}: %q", out)
	}
}

func TestPendingRejectsPathTraversal(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	for _, argv := range [][]string{
		{"pending", "show", "../" + testPendingID},
		{"approve", "../../etc/passwd"},
		{"approve", "/etc/passwd"},
		{"approve", "not-a-pending-id"},
	} {
		code, out, _ := rc(argv...)
		if code != exitUsage {
			t.Fatalf("%v 退出码 = %d, want 2; stdout: %s", argv, code, out)
		}
		// 敌意 id 绝不许在批准目录外落任何东西。
		entries, err := os.ReadDir(approvalsDir)
		if err != nil {
			t.Fatalf("读批准目录失败: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("%v 之后批准目录应为空: %v", argv, entries)
		}
	}
}

func TestApproveRequiresRoot(t *testing.T) {
	dir, approvalsDir, logPath, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 1000)

	code, out, errOut := rc("approve", testPendingID)
	if code != exitUsage {
		t.Fatalf("非 root approve 退出码 = %d, want %d; stdout: %s", code, exitUsage, out)
	}
	oneJSONDoc(t, out)
	if !strings.Contains(errOut, "root") {
		t.Errorf("stderr 应说明需要 root: %q", errOut)
	}
	entries, err := os.ReadDir(approvalsDir)
	if err != nil {
		t.Fatalf("读批准目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("非 root 绝不许留下批准记录: %v", entries)
	}
	recs := readGrantAudit(t, logPath)
	found := false
	for _, r := range recs {
		if r.Tool == "pending_approve" {
			found = true
			if r.Outcome != "denied" {
				t.Errorf("非 root 批准应记 denied, 实得 %q", r.Outcome)
			}
			if r.Identity != txIdentity {
				t.Errorf("审计 identity = %q, want %q", r.Identity, txIdentity)
			}
			if r.Args["pending_id"] != testPendingID {
				t.Errorf("审计 args.pending_id = %v", r.Args["pending_id"])
			}
		}
	}
	if !found {
		t.Error("非 root 批准也必须落一条 pending_approve 审计(拒绝也是证据)")
	}
}

func TestApproveWritesApprovalRecord(t *testing.T) {
	dir, approvalsDir, logPath, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	code, out, errOut := rc("approve", testPendingID)
	if code != exitOK {
		t.Fatalf("approve 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)
	var doc approveDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 approveDoc: %v (%q)", err, out)
	}
	if doc.ID != testPendingID || doc.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("stdout 与待办不一致: %+v", doc)
	}
	// 人必须从批准输出里看到自己被批准的具体 argv(待办文件属主可随时改写)。
	if len(doc.Argv) != 4 || doc.Argv[0] != "systemctl" || doc.Argv[3] != "nginx.service" {
		t.Errorf("approve 输出应回显被批准的 argv: %+v", doc.Argv)
	}

	rec := readApproval(t, approvalsDir, testPendingID)
	if rec.ID != testPendingID {
		t.Errorf("批准记录 id = %q", rec.ID)
	}
	if rec.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("argv_sha256 = %q, want %q", rec.ArgvSHA256, testPendingArgvSHA256)
	}
	if rec.ApprovedBy != "root" {
		t.Errorf("approved_by = %q, want root", rec.ApprovedBy)
	}
	if _, err := time.Parse(time.RFC3339, rec.ApprovedAt); err != nil {
		t.Errorf("approved_at 不是 RFC3339: %q", rec.ApprovedAt)
	}
	fi, err := os.Stat(filepath.Join(approvalsDir, testPendingID+".json"))
	if err != nil {
		t.Fatalf("stat 批准记录失败: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("批准记录 mode = %o, want 644", perm)
	}
	// 原子写入不得留下临时文件。
	entries, err := os.ReadDir(approvalsDir)
	if err != nil {
		t.Fatalf("读批准目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("批准目录应只有一条记录, 实得 %v", entries)
	}

	recs := readGrantAudit(t, logPath)
	var approveRec *grantAuditLine
	for i := range recs {
		if recs[i].Tool == "pending_approve" {
			approveRec = &recs[i]
		}
	}
	if approveRec == nil {
		t.Fatal("批准必须落 pending_approve 审计")
	}
	if approveRec.Outcome != "success" {
		t.Errorf("审计 outcome = %q, want success", approveRec.Outcome)
	}
	if approveRec.Args["argv_sha256"] != testPendingArgvSHA256 || approveRec.Args["approved_by"] != "root" {
		t.Errorf("审计 args 应钉住被批准的 argv 哈希: %+v", approveRec.Args)
	}
}

// TestApproveRefusesWorldWritableApprovalsDir 钉住闸门本身: 批准目录一旦组/其他
// 可写(等于谁都能伪造批准), approve 必须拒绝, 而不是照写不误。
func TestApproveRefusesWorldWritableApprovalsDir(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)
	if err := os.Chmod(approvalsDir, 0o777); err != nil {
		t.Fatalf("改批准目录权限失败: %v", err)
	}

	code, _, errOut := rc("approve", testPendingID)
	if code != exitRuntime {
		t.Fatalf("批准目录可被非 root 写时必须拒绝, 退出码 = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(errOut, "可写") {
		t.Errorf("stderr 应点明目录被非 root 可写: %q", errOut)
	}
	if entries, _ := os.ReadDir(approvalsDir); len(entries) != 0 {
		t.Fatalf("拒绝时不得写批准记录: %v", entries)
	}
}

func TestApproveRejectsExpiredPending(t *testing.T) {
	dir, approvalsDir, logPath, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(-time.Minute))
	stubEuid(t, 0)

	code, out, errOut := rc("approve", testPendingID)
	if code != exitRuntime {
		t.Fatalf("过期待办 approve 退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "过期") {
		t.Errorf("stderr 应说明待办已过期: %q", errOut)
	}
	if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
		t.Fatalf("过期待办不得产出批准记录: %v (err=%v)", entries, err)
	}
	found := false
	for _, r := range readGrantAudit(t, logPath) {
		if r.Tool == "pending_approve" {
			found = true
			if r.Outcome != "denied" {
				t.Errorf("过期拒绝应记 denied, 实得 %q", r.Outcome)
			}
		}
	}
	if !found {
		t.Error("过期拒绝也必须落审计")
	}
}

func TestApproveRejectsMissingPending(t *testing.T) {
	_, approvalsDir, _, rc := newPendingEnv(t)
	stubEuid(t, 0)

	code, out, _ := rc("approve", "p-20261120-00000000")
	if code != exitRuntime {
		t.Fatalf("不存在待办 approve 退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
		t.Fatalf("不存在的待办不得产出批准记录: %v (err=%v)", entries, err)
	}
}

func TestApproveRejectsMalformedPending(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"坏 JSON", `{not json`},
		{"缺 argv", fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","scope":"systemd:user:stop:nginx.service","created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`, testPendingID)},
		{"空 argv", fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","argv":[],"scope":"systemd:user:stop:nginx.service","created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`, testPendingID)},
		{"缺 expires_at", fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","argv":["systemctl"],"scope":"systemd:user:stop:nginx.service","created_at":"2026-11-20T00:00:00Z"}`, testPendingID)},
		{"argv 含 NUL", fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","argv":["systemctl","--user","stop","a\u0000b"],"scope":"systemd:user:stop:nginx.service","created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`, testPendingID)},
		{"id 与文件名不一致", `{"id":"p-20261120-deadbeef","kind":"service","name":"nginx.service","desired_state":"stopped","argv":["systemctl"],"scope":"systemd:user:stop:nginx.service","created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`},
		{"scope 形态非法", fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","argv":["systemctl"],"scope":"not-a-scope","created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`, testPendingID)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, approvalsDir, _, rc := newPendingEnv(t)
			if err := os.WriteFile(filepath.Join(dir, testPendingID+".json"), []byte(tc.body), 0o644); err != nil {
				t.Fatalf("写待办失败: %v", err)
			}
			stubEuid(t, 0)

			code, _, errOut := rc("approve", testPendingID)
			if code != exitRuntime {
				t.Fatalf("坏待办 approve 退出码 = %d, want 1; stderr: %s", code, errOut)
			}
			if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
				t.Fatalf("坏待办不得产出批准记录: %v (err=%v)", entries, err)
			}
		})
	}
}

func TestApproveReplacesStaleApproval(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	// 旧批准记录对应另一条 argv: 重新批准必须整份替换, 绝不残留旧哈希。
	stale := approvalDoc{ID: testPendingID, ArgvSHA256: "deadbeef", ApprovedBy: "root", ApprovedAt: "2026-01-01T00:00:00Z"}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("序列化旧批准失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(approvalsDir, testPendingID+".json"), data, 0o644); err != nil {
		t.Fatalf("写旧批准失败: %v", err)
	}

	if code, _, errOut := rc("approve", testPendingID); code != exitOK {
		t.Fatalf("重新批准退出码 = %d; stderr: %s", code, errOut)
	}
	rec := readApproval(t, approvalsDir, testPendingID)
	if rec.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("重新批准应整份替换哈希, 实得 %q", rec.ArgvSHA256)
	}
	if entries, _ := os.ReadDir(approvalsDir); len(entries) != 1 {
		t.Errorf("批准目录应只有一条记录: %v", entries)
	}
}

// TestPendingArgvHashIsInjective 钉住规范哈希必须先消歧再连接:
// 空格拼接会让 ["a b","c"] 与 ["a","b c"] 撞成同一个哈希。
func TestPendingArgvHashIsInjective(t *testing.T) {
	a := argvSHA256([]string{"a b", "c"})
	b := argvSHA256([]string{"a", "b c"})
	if a == b {
		t.Fatalf("规范哈希发生碰撞: %q", a)
	}
	if argvSHA256([]string{"systemctl", "--user", "stop", "nginx.service"}) != testPendingArgvSHA256 {
		t.Errorf("规范哈希实现与手算值不一致")
	}
}
