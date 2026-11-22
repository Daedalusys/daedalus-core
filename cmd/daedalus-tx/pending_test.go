package main

// pending_test.go —— 待办(pending)与批准(approve)这道人工闸门的全链路测试。
//
// 钉住的契约: 批准目录是唯一的门(非 root 写不进去、必须 root 属主且非符号链接)、
// 批准必须带 --expect-sha256 且与当前待办逐字一致、argv 用 NUL 连接的规范哈希绑定
// 具体动作、过期待办一律作废、坏待办 fail-closed 而不是猜。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/pending"
)

// 固定 argv 的规范哈希(sha256(NUL 连接))。这里是**手算值**而不是调用实现:
// 哈希形状是跨语言契约(消费侧在另一仓实现), 必须被测试独立钉死。
const (
	testPendingArgvSHA256 = "802d8be0ec50f15ec1cff1b7187c5f7867d52f1e49891389b44051954c532251"
	testPendingID         = "p-20261120-abcdef01"
)

// otherTestUID 返回一个与测试进程 uid **不同**的分片 id。
// 不能用固定常量: CI 的 runner 恰好是 uid 1001, 写死 1001 会让「两个不同的用户」
// 落进同一个 <root>/1001/ 目录 —— 后写覆盖先写, 跨目录的用例就退化成单目录
// (「同一 id 出现在两个目录」那条更是直接红)。
func otherTestUID(t *testing.T) uint32 {
	t.Helper()
	if uid := testPendingUID(t); uid == 1001 {
		return 1000
	}
	return 1001
}

// TestOtherTestUIDDiffers 钉住上一条的**不变量**本身: 两个分片必须不同。
// 少了它, otherTestUID 哪天被改回常量也没人拦, 跨目录用例会静默退化成单目录。
func TestOtherTestUIDDiffers(t *testing.T) {
	if otherTestUID(t) == testPendingUID(t) {
		t.Fatalf("otherTestUID=%d 与测试进程 uid 相同, 跨目录用例会退化成单目录", otherTestUID(t))
	}
}

// testPendingUID 是跑测试的用户的 uid。测试进程写的待办文件自然属于它,
// 而请求者的归属只认文件属主, 所以这个值就是断言的期望。
func testPendingUID(t *testing.T) uint32 {
	t.Helper()
	return uint32(os.Getuid())
}

// pendingDirForUser 建出 <root>/<uid> 这个用户的待办目录(生产语义 0700)。
func pendingDirForUser(t *testing.T, root string, uid uint32) string {
	t.Helper()
	dir := filepath.Join(root, strconv.FormatUint(uint64(uid), 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建用户待办目录 %s 失败: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("设置用户待办目录 %s 权限失败: %v", dir, err)
	}
	return dir
}

// newPendingEnv 建隔离环境: 审计/事务目录(harness)+ 待办**根目录**(1777 语义)+
// 批准目录(生产是 0755 root:root; 测试里属主是跑测试的用户, 权限位与生产一致)。
// 每个用户的待办落在 <root>/<uid> 子目录里, 测试进程自己那份一并建好。
func newPendingEnv(t *testing.T) (pendingRoot, approvalsDir, logPath string, rc func(...string) (int, string, string)) {
	t.Helper()
	logPath, rc = harness(t)
	base := t.TempDir()
	pendingRoot = filepath.Join(base, "pending")
	approvalsDir = filepath.Join(base, "approvals")
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{pendingRoot, 0o1777}, {approvalsDir, 0o755}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", d.path, err)
		}
		// umask 会清掉权限位, 显式 chmod 让测试环境的权限与生产一致。
		if err := os.Chmod(d.path, d.mode); err != nil {
			t.Fatalf("设置目录 %s 权限失败: %v", d.path, err)
		}
	}
	pendingDirForUser(t, pendingRoot, testPendingUID(t))
	t.Setenv(envPendingDir, pendingRoot)
	t.Setenv(envApprovalsDir, approvalsDir)
	// 生产批准目录属主是 root; 非 root 的测试进程造不出这种目录, 把属主判定钉成
	// root(uid 判定本身由 TestCheckApprovalsDir 用构造的 FileInfo 钉住)。
	stubApprovalsOwner(t, 0)
	return pendingRoot, approvalsDir, logPath, rc
}

// writePendingFile 在当前测试用户的待办目录里直写一份待办, 模拟 AI 侧(消费侧)产出。
func writePendingFile(t *testing.T, root, id string, argv []string, expiresAt time.Time) string {
	t.Helper()
	return writePendingFileIn(t, pendingDirForUser(t, root, testPendingUID(t)), id, argv, expiresAt)
}

// writePendingFileIn 在指定目录里直写一份待办文件。
// 用 map 而不是实现里的结构体: 测试要钉的是线上 JSON 键, 不是内部类型。
func writePendingFileIn(t *testing.T, dir, id string, argv []string, expiresAt time.Time) string {
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

// stubApprovalsOwner 覆写批准目录属主判定, 测试结束自动还原。
func stubApprovalsOwner(t *testing.T, uid uint32) {
	t.Helper()
	orig := approvalsDirUID
	t.Cleanup(func() { approvalsDirUID = orig })
	approvalsDirUID = func(os.FileInfo) (uint32, bool) { return uid, true }
}

// fakeFileInfo 让批准目录判定可以脱离真实文件系统被钉住(符号链接/属主/mode)。
type fakeFileInfo struct {
	name string
	mode os.FileMode
	sys  any
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return f.sys }

// approvalOnDisk 是批准记录的读取视图: 字段声明与线上 JSON 逐字对齐, 因为测试要钉的是
// 写出去的字节形态(键名、时间格式), 不是 SDK 的内部类型。
type approvalOnDisk struct {
	ID             string  `json:"id"`
	ArgvSHA256     string  `json:"argv_sha256"`
	RequestedByUID *uint32 `json:"requested_by_uid"`
	RequestedBy    string  `json:"requested_by"`
	ApprovedBy     string  `json:"approved_by"`
	ApprovedAt     string  `json:"approved_at"`
}

// readApproval 读批准记录: 先过生产同款严格解析(写出去就读不回来是不可接受的),
// 再按线上 JSON 形态取字段供断言。
func readApproval(t *testing.T, dir, id string) approvalOnDisk {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("批准记录不存在: %v", err)
	}
	if _, err := pending.ParseApproval(data); err != nil {
		t.Fatalf("批准记录过不了生产侧严格解析: %v (%q)", err, data)
	}
	var rec approvalOnDisk
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
	userDir := pendingDirForUser(t, dir, testPendingUID(t))
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(userDir, "broken.json"), []byte("{not json"), 0o644); err != nil {
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

func TestPendingListRejectsUnknownFields(t *testing.T) {
	dir, _, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	// 手写一份带未知键的待办: 待办目录对本地用户可写, 宽松解析等于给伪造留门。
	rogueID := "p-20261120-unknown1"
	roguePath := writePendingFile(t, dir, rogueID, []string{"systemctl", "--user", "start", "evil.service"}, time.Now().UTC().Add(time.Hour))
	data, err := os.ReadFile(roguePath)
	if err != nil {
		t.Fatalf("读待办失败: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("待办不是合法 JSON: %v", err)
	}
	// requested_by 不在待办的线上形状里: 归属是文件属主, 不是自报字段。
	raw["requested_by"] = "root"
	patched, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("序列化待办失败: %v", err)
	}
	if err := os.WriteFile(roguePath, patched, 0o644); err != nil {
		t.Fatalf("写待办失败: %v", err)
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
		t.Fatalf("未知键的待办必须进 invalid 且不挡其他条目, 实得 pending=%d invalid=%d", len(doc.Pending), len(doc.Invalid))
	}
	if doc.Pending[0].ID != testPendingID {
		t.Errorf("合法的待办应仍在清单里: %+v", doc.Pending[0])
	}
	if doc.Invalid[0].File != rogueID+".json" || doc.Invalid[0].Error == "" {
		t.Errorf("invalid 条目应点名未知键的文件与原因: %+v", doc.Invalid[0])
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
		{"approve", "../../etc/passwd", "--expect-sha256", testPendingArgvSHA256},
		{"approve", "/etc/passwd", "--expect-sha256", testPendingArgvSHA256},
		{"approve", "not-a-pending-id", "--expect-sha256", testPendingArgvSHA256},
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

	code, out, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
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
	// 测试的审计日志是可写临时文件, 所以这里的非 root 进程能落痕; 生产里非 root
	// 写 /var/log/daedalus/audit.jsonl 会静默失败(stampGrant 尽力而为), 此处只钉代码路径。
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
			if r.Args["expect_sha256"] != testPendingArgvSHA256 {
				t.Errorf("被拒审计必须记下人批准的指纹: %+v", r.Args)
			}
			// 拒绝不是批准: 写 approved_by=root 会让事后审计误判。
			if _, ok := r.Args["approved_by"]; ok {
				t.Errorf("拒绝分支不得写 approved_by: %+v", r.Args)
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

	code, out, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
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
	if approveRec.Args["expect_sha256"] != testPendingArgvSHA256 {
		t.Errorf("审计 args 应钉住人批准的指纹: %+v", approveRec.Args)
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

	code, _, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
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

	code, out, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
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

	code, out, _ := rc("approve", "p-20261120-00000000", "--expect-sha256", testPendingArgvSHA256)
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
			userDir := pendingDirForUser(t, dir, testPendingUID(t))
			if err := os.WriteFile(filepath.Join(userDir, testPendingID+".json"), []byte(tc.body), 0o644); err != nil {
				t.Fatalf("写待办失败: %v", err)
			}
			stubEuid(t, 0)

			code, _, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
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
	staleUID := uint32(0)
	stale := approvalOnDisk{
		ID:             testPendingID,
		ArgvSHA256:     strings.Repeat("0", 64),
		RequestedByUID: &staleUID,
		ApprovedBy:     "root",
		ApprovedAt:     "2026-01-01T00:00:00Z",
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("序列化旧批准失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(approvalsDir, testPendingID+".json"), data, 0o644); err != nil {
		t.Fatalf("写旧批准失败: %v", err)
	}

	if code, _, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256); code != exitOK {
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
	a := pending.ArgvSHA256([]string{"a b", "c"})
	b := pending.ArgvSHA256([]string{"a", "b c"})
	if a == b {
		t.Fatalf("规范哈希发生碰撞: %q", a)
	}
	if pending.ArgvSHA256([]string{"systemctl", "--user", "stop", "nginx.service"}) != testPendingArgvSHA256 {
		t.Errorf("规范哈希实现与手算值不一致")
	}
}

// TestApproveRequiresExpectSHA256 钉住 --expect-sha256 必填: 没有人看过的指纹,
// 批准就只能绑定 approve 那一刻的文件, 等于对属主开放了整份改写。
func TestApproveRequiresExpectSHA256(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	code, out, errOut := rc("approve", testPendingID)
	if code != exitUsage {
		t.Fatalf("缺 --expect-sha256 退出码 = %d, want %d; stdout: %s", code, exitUsage, out)
	}
	oneJSONDoc(t, out)
	if !strings.Contains(errOut, "argv_sha256") || !strings.Contains(errOut, "pending show") {
		t.Errorf("stderr 应点明从 pending show/list 的 argv_sha256 抄指纹: %q", errOut)
	}
	if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
		t.Fatalf("缺指纹不得写批准记录: %v (err=%v)", entries, err)
	}
}

// TestApproveRejectsFingerprintMismatch 钉住 TOCTOU 的正面防线: 人看过指纹 A,
// 待办在 approve 之前被改写成 B ⇒ 拒绝, 不写记录, 待办文件原样保留。
func TestApproveRejectsFingerprintMismatch(t *testing.T) {
	dir, approvalsDir, logPath, rc := newPendingEnv(t)
	pendingPath := writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "start", "evil.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	// 人批准的是审查时看到的另一条 argv(known hash), 与当前待办必然不同。
	approvedFingerprint := strings.Repeat("0", 64)
	actualFingerprint := pending.ArgvSHA256([]string{"systemctl", "--user", "start", "evil.service"})
	code, out, errOut := rc("approve", testPendingID, "--expect-sha256", approvedFingerprint)
	if code != exitRuntime {
		t.Fatalf("指纹不一致退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	oneJSONDoc(t, out)
	if !strings.Contains(errOut, approvedFingerprint) {
		t.Errorf("stderr 应打印人批准的指纹: %q", errOut)
	}
	if !strings.Contains(errOut, actualFingerprint) || !strings.Contains(errOut, "evil.service") {
		t.Errorf("stderr 应打印当前待办的实际 argv 与指纹: %q", errOut)
	}
	if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
		t.Fatalf("指纹不一致绝不许写批准记录: %v (err=%v)", entries, err)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("拒绝批准不得删除待办文件: %v", err)
	}
	found := false
	for _, r := range readGrantAudit(t, logPath) {
		if r.Tool == "pending_approve" && r.Outcome == "denied" {
			found = true
			if r.Args["expect_sha256"] != approvedFingerprint {
				t.Errorf("拒绝审计应记下人批准的指纹: %+v", r.Args)
			}
		}
	}
	if !found {
		t.Error("指纹不一致的拒绝必须落审计")
	}
}

// TestCheckApprovalsDir 把"批准目录是否真的是只有 root 能写"的判定逐条钉住。
func TestCheckApprovalsDir(t *testing.T) {
	orig := approvalsDirUID
	t.Cleanup(func() { approvalsDirUID = orig })
	approvalsDirUID = statFileUID

	tests := []struct {
		name    string
		mode    os.FileMode
		sys     any
		wantErr string
	}{
		{"符号链接", os.ModeSymlink | 0o777, &syscall.Stat_t{Uid: 0}, "符号链接"},
		{"非 root 属主", os.ModeDir | 0o755, &syscall.Stat_t{Uid: 1000}, "属于 root"},
		{"过宽 mode", os.ModeDir | 0o777, &syscall.Stat_t{Uid: 0}, "可写"},
		{"不是目录", 0o644, &syscall.Stat_t{Uid: 0}, "不是目录"},
		{"无法判定属主", os.ModeDir | 0o755, nil, "无法判定"},
		{"正常", os.ModeDir | 0o755, &syscall.Stat_t{Uid: 0}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkApprovalsDir("/run/daedalus/approvals", fakeFileInfo{name: "approvals", mode: tc.mode, sys: tc.sys})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("正常批准目录不应被拒: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 = %v, 应包含 %q", err, tc.wantErr)
			}
		})
	}
}

// TestApproveRefusesSymlinkApprovalsDir 钉住 Lstat 语义: 符号链接指向一个
// "看起来 0755" 的目录也不能当批准目录, 否则链接目标可被属主随时换掉。
func TestApproveRefusesSymlinkApprovalsDir(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	writePendingFile(t, dir, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("建链接目标失败: %v", err)
	}
	if err := os.RemoveAll(approvalsDir); err != nil {
		t.Fatalf("移除夹具批准目录失败: %v", err)
	}
	if err := os.Symlink(target, approvalsDir); err != nil {
		t.Fatalf("建符号链接失败: %v", err)
	}

	code, _, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
	if code != exitRuntime {
		t.Fatalf("批准目录是符号链接时退出码 = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(errOut, "符号链接") {
		t.Errorf("stderr 应点明批准路径是符号链接: %q", errOut)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("拒绝时不得向链接目标写批准记录: %v (err=%v)", entries, err)
	}
}

// TestEnsureApprovalsDirChmodsAfterUmask 钉住建目录分支: MkdirAll 受 umask 削减,
// 必须显式 chmod 到 0755, 否则消费侧读不到批准记录、第三档永远执行不了。
func TestEnsureApprovalsDirChmodsAfterUmask(t *testing.T) {
	stubApprovalsOwner(t, 0)
	dir := filepath.Join(t.TempDir(), "approvals")

	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	if err := ensureApprovalsDir(dir); err != nil {
		t.Fatalf("建批准目录失败: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat 批准目录失败: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Errorf("批准目录 mode = %o, want 755(umask 不得把权限收窄)", perm)
	}
}

// TestPendingRejectsOversizedFile 钉住读取上限: 待办目录属主可写, 超大文件
// 不能把 root 的审查变成整份读入。
func TestPendingRejectsOversizedFile(t *testing.T) {
	dir, approvalsDir, _, rc := newPendingEnv(t)
	big := fmt.Sprintf(`{"id":%q,"kind":"service","name":"nginx.service","desired_state":"stopped","argv":["systemctl"],"scope":"systemd:user:stop:nginx.service","reason":%q,"created_at":"2026-11-20T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`,
		testPendingID, strings.Repeat("x", maxPendingFileBytes))
	if err := os.WriteFile(filepath.Join(pendingDirForUser(t, dir, testPendingUID(t)), testPendingID+".json"), []byte(big), 0o644); err != nil {
		t.Fatalf("写超大待办失败: %v", err)
	}
	stubEuid(t, 0)

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 0 || len(doc.Invalid) != 1 {
		t.Fatalf("超大待办应进 invalid, 实得 pending=%d invalid=%d", len(doc.Pending), len(doc.Invalid))
	}
	if !strings.Contains(doc.Invalid[0].Error, "上限") {
		t.Errorf("invalid 原因应点明超过上限: %+v", doc.Invalid[0])
	}

	code, _, _ = rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
	if code != exitRuntime {
		t.Fatalf("超大待办 approve 退出码 = %d, want %d", code, exitRuntime)
	}
	if entries, err := os.ReadDir(approvalsDir); err != nil || len(entries) != 0 {
		t.Fatalf("超大待办不得产出批准记录: %v (err=%v)", entries, err)
	}
}

// TestPendingListSpansUserDirs 钉住分目录之后的可见性: 待办按执行者散在
// <root>/<uid>/ 里, root 的 list 必须跨所有用户目录都看得到。
func TestPendingListSpansUserDirs(t *testing.T) {
	root, _, _, rc := newPendingEnv(t)
	otherID := "p-20261120-other001"

	ownPath := writePendingFile(t, root, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	otherDir := pendingDirForUser(t, root, otherTestUID(t))
	otherPath := writePendingFileIn(t, otherDir, otherID, []string{"systemctl", "--user", "restart", "web.service"}, time.Now().UTC().Add(time.Hour))
	// 归属的权威是文件属主, 不是目录名: 文件落在 <root>/<另一个 uid>/ 里, 报的仍是它真实的
	// stat 属主(root 跑测试时能把属主改成 1001, 普通用户改不动, 两种都钉这条)。
	if err := os.Chown(otherPath, int(otherTestUID(t)), int(otherTestUID(t))); err != nil {
		t.Logf("无法把夹具改成 uid=%d(非 root 测试进程的正常情况): %v", otherTestUID(t), err)
	}

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 2 || len(doc.Invalid) != 0 {
		t.Fatalf("跨用户目录应列出 2 条, 实得 pending=%d invalid=%d: %s", len(doc.Pending), len(doc.Invalid), out)
	}
	byID := map[string]pendingListEntry{}
	for _, entry := range doc.Pending {
		byID[entry.ID] = entry
	}
	// 期望值从文件系统独立读出, 不从被测实现推。
	if got, want := byID[testPendingID].RequestedByUID, int(statOwnerUID(t, ownPath)); got != want {
		t.Errorf("自己那份的 requested_by_uid = %d, want %d", got, want)
	}
	if got, want := byID[otherID].RequestedByUID, int(statOwnerUID(t, otherPath)); got != want {
		t.Errorf("另一份的 requested_by_uid = %d, want %d(归属看 stat 属主, 不看目录名)", got, want)
	}
}

// statOwnerUID 从文件系统独立读出属主 uid: 断言的期望值不能来自被测实现。
func statOwnerUID(t *testing.T, path string) uint32 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s 失败: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s 的 FileInfo 不含 Stat_t", path)
	}
	return st.Uid
}

// TestPendingListShowsRequesterName 钉住审批的人能看到「这是谁要的」: 账号名由
// stat 属主反查, 反查不到时留空串(uid 才是判定依据)。
func TestPendingListShowsRequesterName(t *testing.T) {
	root, _, _, rc := newPendingEnv(t)
	writePendingFile(t, root, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	uid := testPendingUID(t)
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		t.Skipf("本机无法由 uid=%d 反查账号名: %v", uid, err)
	}

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 1 {
		t.Fatalf("应列出 1 条待办, 实得 %d: %s", len(doc.Pending), out)
	}
	if doc.Pending[0].RequestedBy != u.Username {
		t.Errorf("requested_by = %q, want %q", doc.Pending[0].RequestedBy, u.Username)
	}
	if doc.Pending[0].RequestedByUID != int(uid) {
		t.Errorf("requested_by_uid = %d, want %d", doc.Pending[0].RequestedByUID, uid)
	}
}

// TestPendingShowFindsAcrossUserDirs 钉住 show 的跨目录查找: id 反推不出 uid,
// 只能遍历; 找到之后归属仍按真实属主报。
func TestPendingShowFindsAcrossUserDirs(t *testing.T) {
	root, _, _, rc := newPendingEnv(t)
	id := "p-20261120-othershow"
	otherPath := writePendingFileIn(t, pendingDirForUser(t, root, otherTestUID(t)), id,
		[]string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))

	code, out, errOut := rc("pending", "show", id)
	if code != exitOK {
		t.Fatalf("pending show 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var entry pendingListEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
		t.Fatalf("stdout 非待办视图: %v (%q)", err, out)
	}
	if entry.ID != id {
		t.Errorf("show 找到的 id = %q, want %q", entry.ID, id)
	}
	if entry.ArgvSHA256 != testPendingArgvSHA256 {
		t.Errorf("argv_sha256 = %q, want %q", entry.ArgvSHA256, testPendingArgvSHA256)
	}
	// 期望值从文件系统独立读出, 不从被测实现推 —— 文件落在 <root>/<另一个 uid>/ 里,
	// 报的必须是它真实的 stat 属主, 谁写的才是关键, 目录叫什么不是。
	if got, want := entry.RequestedByUID, int(statOwnerUID(t, otherPath)); got != want {
		t.Errorf("requested_by_uid = %d, want %d(属主不是目录名)", got, want)
	}
}

// TestPendingShowRejectsSameIDInTwoUserDirs 钉住「id 全局唯一」这条前提: id 里带执行者
// uid, 同一个 id 出现在两个用户目录里只可能是伪造 —— 两份都不采信, 而不是挑一份当真的。
func TestPendingShowRejectsSameIDInTwoUserDirs(t *testing.T) {
	root, _, _, rc := newPendingEnv(t)
	writePendingFile(t, root, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	writePendingFileIn(t, pendingDirForUser(t, root, otherTestUID(t)), testPendingID,
		[]string{"systemctl", "--user", "start", "evil.service"}, time.Now().UTC().Add(time.Hour))

	code, _, errOut := rc("pending", "show", testPendingID)
	if code != exitRuntime {
		t.Fatalf("同一 id 出现在两个用户目录时 show 退出码 = %d, want %d; stderr: %s", code, exitRuntime, errOut)
	}
	if !strings.Contains(errOut, "重复即伪造") {
		t.Errorf("stderr 应点明 id 重复即伪造: %q", errOut)
	}
}

// TestApproveRecordsRequesterFromFileOwner 钉住批准记录的请求者来源: 权威是待办文件的
// stat 属主, 不是待办 JSON 里的任何字段(那种字段谁都能写)。
func TestApproveRecordsRequesterFromFileOwner(t *testing.T) {
	root, approvalsDir, _, rc := newPendingEnv(t)
	pendingPath := writePendingFile(t, root, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))
	stubEuid(t, 0)

	code, out, errOut := rc("approve", testPendingID, "--expect-sha256", testPendingArgvSHA256)
	if code != exitOK {
		t.Fatalf("approve 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)

	rec := readApproval(t, approvalsDir, testPendingID)
	uid := statOwnerUID(t, pendingPath)
	if rec.RequestedByUID == nil {
		t.Fatal("批准记录必须写 requested_by_uid: 省略等于没写请求者")
	}
	if *rec.RequestedByUID != uid {
		t.Errorf("requested_by_uid = %d, want %d(待办文件的 stat 属主)", *rec.RequestedByUID, uid)
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		if rec.RequestedBy != u.Username {
			t.Errorf("requested_by = %q, want %q", rec.RequestedBy, u.Username)
		}
	}
}

// TestPendingListKeepsNineFieldPendings 钉住兼容性: 加请求者归属时不许改待办的线上形状,
// 上一轮发出去的 9 字段待办必须原样读得出来。
func TestPendingListKeepsNineFieldPendings(t *testing.T) {
	root, _, _, rc := newPendingEnv(t)
	path := writePendingFile(t, root, testPendingID, []string{"systemctl", "--user", "stop", "nginx.service"}, time.Now().UTC().Add(time.Hour))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读待办失败: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("待办不是合法 JSON: %v", err)
	}
	wantKeys := []string{"id", "kind", "name", "desired_state", "argv", "scope", "reason", "created_at", "expires_at"}
	if len(raw) != len(wantKeys) {
		t.Fatalf("待办的线上形状应仍是 %d 个键, 实得 %d: %s", len(wantKeys), len(raw), data)
	}
	for _, key := range wantKeys {
		if _, ok := raw[key]; !ok {
			t.Fatalf("待办缺少线上键 %q: %s", key, data)
		}
	}

	code, out, errOut := rc("pending", "list")
	if code != exitOK {
		t.Fatalf("pending list 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	var doc pendingListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 pendingListDoc: %v (%q)", err, out)
	}
	if len(doc.Pending) != 1 || len(doc.Invalid) != 0 {
		t.Fatalf("9 字段待办必须读得出来, 实得 pending=%d invalid=%d: %s", len(doc.Pending), len(doc.Invalid), out)
	}
	if doc.Pending[0].Kind != "service" || doc.Pending[0].Scope != "systemd:user:stop:nginx.service" {
		t.Errorf("待办声明字段丢失: %+v", doc.Pending[0])
	}
}
