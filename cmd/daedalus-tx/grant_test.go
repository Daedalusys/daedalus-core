package main

// grant_test.go —— grant add/list/revoke 的 CLI 全链路测试。
//
// 钉住签发面的契约: 身份取文件属主(SUDO_USER 优先)、全局硬上限、
// 签发侧 Authorize 自检(范围/危险名单)、撤销的属主门、审计条目与 stdout 单文档。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/autonomy"
	"github.com/Daedalusys/daedalus-sdk/grant"
)

const testGrantScope = "systemd:user:restart:nginx.service"

// newGrantEnv 建隔离环境: 审计/事务目录(harness)+ 条子目录 + 默认不存在的档位表。
// 档位表路径必须显式指向不存在的位置: 否则测试会读到宿主机 /etc 下的真实配置。
func newGrantEnv(t *testing.T) (grantsDir, logPath string, rc func(...string) (int, string, string)) {
	t.Helper()
	logPath, rc = harness(t)
	t.Setenv("SUDO_USER", "")
	grantsDir = t.TempDir()
	t.Setenv(envGrantsDir, grantsDir)
	t.Setenv(autonomy.EnvPath, filepath.Join(t.TempDir(), "agent-autonomy.toml"))
	return grantsDir, logPath, rc
}

// writeAutonomy 把档位表写到临时文件并指向它。
func writeAutonomy(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent-autonomy.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写档位表失败: %v", err)
	}
	t.Setenv(autonomy.EnvPath, p)
}

// signerContext 配置「本进程的真实发起人」可签发的最小档位表。
// root 恒可签且无需档位表(SUDO_USER 清空 ⇒ 当前 uid 0);
// 非 root 走 SUDO_USER=当前用户, chown 到自己的 uid 不需要特权。
func signerContext(t *testing.T) (name string, uid uint32) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("取当前用户失败: %v", err)
	}
	parsed, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		t.Fatalf("解析当前 uid 失败: %v", err)
	}
	if parsed == 0 {
		return u.Username, 0
	}
	t.Setenv("SUDO_USER", u.Username)
	writeAutonomy(t, fmt.Sprintf("[[signer]]\nuser = %q\nmax_scope = [\"systemd:user:restart:*\", \"systemd:user:start:*\"]\nmax_ttl = \"2h\"\nmax_uses = 5\n", u.Username))
	return u.Username, uint32(parsed)
}

// nonRootUser 找一个非 root 账号(测试要绕过 root 的无条件放行)。
func nonRootUser(t *testing.T) *user.User {
	t.Helper()
	if u, err := user.Current(); err == nil && u.Uid != "0" {
		return u
	}
	for _, name := range []string{"nobody", "daemon", "games", "bin"} {
		if u, err := user.Lookup(name); err == nil && u.Uid != "0" {
			return u
		}
	}
	t.Skip("找不到可用的非 root 测试账号")
	return nil
}

// otherNonRootUser 找一个 uid 与 excludeUID 不同的非 root 账号。
func otherNonRootUser(t *testing.T, excludeUID string) *user.User {
	t.Helper()
	for _, name := range []string{"daemon", "nobody", "games", "bin", "lp"} {
		if u, err := user.Lookup(name); err == nil && u.Uid != "0" && u.Uid != excludeUID {
			return u
		}
	}
	t.Skip("找不到与当前用户不同的非 root 测试账号")
	return nil
}

// writeTestGrant 直写一张条子文件(列表/撤销测试用; add 的写入路径另测)。
func writeTestGrant(t *testing.T, dir string, g *grant.Grant) string {
	t.Helper()
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("序列化条子失败: %v", err)
	}
	path := filepath.Join(dir, g.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写条子失败: %v", err)
	}
	return path
}

// oneJSONDoc 断言 stdout 恰有一份 JSON 文档。
func oneJSONDoc(t *testing.T, out string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("stdout 不是 JSON: %v (%q)", err, out)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout 不止一份 JSON 文档: %q", out)
	}
}

// grantAuditLine 是签发面审计中断言所需的最小视图。
type grantAuditLine struct {
	Identity string         `json:"identity"`
	Tool     string         `json:"tool"`
	Outcome  string         `json:"outcome"`
	Args     map[string]any `json:"args"`
}

func readGrantAudit(t *testing.T, path string) []grantAuditLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计日志失败: %v", err)
	}
	var recs []grantAuditLine
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r grantAuditLine
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("审计行非法 JSON: %v (%q)", err, line)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestGrantAddWritesFileOwnedByRealIssuer(t *testing.T) {
	dir, _, rc := newGrantEnv(t)
	name, uid := signerContext(t)

	code, out, errOut := rc("grant", "add",
		"--scope", testGrantScope,
		"--scope", "systemd:user:start:web.service",
		"--ttl", "30m", "--count", "2", "--note", "半夜 nginx 挂了就重启")
	if code != exitOK {
		t.Fatalf("grant add 退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)

	var doc grantAddDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 grantAddDoc: %v (%q)", err, out)
	}
	if !grantIDPattern.MatchString(doc.ID) {
		t.Fatalf("条子 id 形状非法: %q", doc.ID)
	}
	if doc.IssuedBy != name {
		t.Errorf("stdout issued_by = %q, want %q", doc.IssuedBy, name)
	}

	path := filepath.Join(dir, doc.ID+".json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("条子文件不存在: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("条子文件 mode = %o, want 644", perm)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("无法读取文件属主: %T", fi.Sys())
	}
	if st.Uid != uid {
		t.Errorf("条子文件属主 uid = %d, want %d", st.Uid, uid)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读条子失败: %v", err)
	}
	g, err := grant.Parse(data)
	if err != nil {
		t.Fatalf("写出的条子过不了 grant.Parse: %v", err)
	}
	if g.IssuedBy != name || g.MaxUses != 2 || g.Note != "半夜 nginx 挂了就重启" {
		t.Errorf("条子字段异常: %+v", g)
	}
	if len(g.Scope) != 2 || g.Scope[0] != testGrantScope || g.Scope[1] != "systemd:user:start:web.service" {
		t.Errorf("条子范围 = %v", g.Scope)
	}
	if got := g.ExpiresAt.Sub(g.IssuedAt); got != 30*time.Minute {
		t.Errorf("条子有效期 = %s, want 30m", got)
	}
}

func TestGrantAddRejectsUnknownSudoUser(t *testing.T) {
	dir, _, rc := newGrantEnv(t)
	t.Setenv("SUDO_USER", "no-such-user-daedalus-test")

	code, out, errOut := rc("grant", "add", "--scope", testGrantScope)
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "SUDO_USER") {
		t.Errorf("stderr 应点名 SUDO_USER: %q", errOut)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读条子目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("拒绝签发后目录应为空, 实际: %v", entries)
	}
}

func TestGrantAddRejectsBeyondGlobalCaps(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"ttl 超 24h", []string{"--scope", testGrantScope, "--ttl", "25h"}, "24h"},
		{"次数超 10", []string{"--scope", testGrantScope, "--count", "11"}, "10"},
		{"ttl 非正", []string{"--scope", testGrantScope, "--ttl", "0s"}, "--ttl"},
		{"次数非正", []string{"--scope", testGrantScope, "--count", "0"}, "--count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, rc := newGrantEnv(t)
			code, out, errOut := rc(append([]string{"grant", "add"}, tc.args...)...)
			if code != exitUsage {
				t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitUsage, out)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("stderr 应含 %q: %q", tc.want, errOut)
			}
			oneJSONDoc(t, out)
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("拒绝后目录应为空: %v", entries)
			}
		})
	}
}

func TestGrantAddUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"缺 scope", []string{"grant", "add"}},
		{"非法 scope", []string{"grant", "add", "--scope", "not-a-scope"}},
		{"未知旗标", []string{"grant", "add", "--scope", testGrantScope, "--bogus", "x"}},
		{"旗标缺值", []string{"grant", "add", "--scope", testGrantScope, "--ttl"}},
		{"未知子命令", []string{"grant", "bogus"}},
		{"grant 无子命令", []string{"grant"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, rc := newGrantEnv(t)
			code, out, _ := rc(tc.argv...)
			if code != exitUsage {
				t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitUsage, out)
			}
			oneJSONDoc(t, out)
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("用法错误后目录应为空: %v", entries)
			}
		})
	}
}

func TestGrantAddRejectsScopeBeyondSignerLimit(t *testing.T) {
	_, _, rc := newGrantEnv(t)
	u := nonRootUser(t)
	t.Setenv("SUDO_USER", u.Username)
	writeAutonomy(t, fmt.Sprintf(
		"[[signer]]\nuser = %q\nmax_scope = [\"systemd:user:restart:*\"]\nmax_ttl = \"2h\"\nmax_uses = 5\n",
		u.Username))

	code, out, errOut := rc("grant", "add", "--scope", "systemd:user:stop:nginx.service")
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "超出授权范围") {
		t.Errorf("stderr 应说明范围越界: %q", errOut)
	}
}

func TestGrantAddRejectsDangerousScope(t *testing.T) {
	_, _, rc := newGrantEnv(t)
	u := nonRootUser(t)
	t.Setenv("SUDO_USER", u.Username)
	writeAutonomy(t, fmt.Sprintf(
		"deny = [\"systemd:user:restart:db-*\"]\n\n[[signer]]\nuser = %q\nmax_scope = [\"systemd:user:restart:*\"]\nmax_ttl = \"2h\"\nmax_uses = 5\n",
		u.Username))

	code, out, errOut := rc("grant", "add", "--scope", "systemd:user:restart:db-1.service")
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "危险名单") {
		t.Errorf("stderr 应说明命中危险名单: %q", errOut)
	}
}

func TestGrantAddNonRootWithoutAutonomyConfig(t *testing.T) {
	_, _, rc := newGrantEnv(t)
	u := nonRootUser(t)
	t.Setenv("SUDO_USER", u.Username)

	code, out, errOut := rc("grant", "add", "--scope", testGrantScope)
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "档位表") {
		t.Errorf("stderr 应是可读的「还没有档位表」提示: %q", errOut)
	}
}

func TestGrantAddWritesAuditEntry(t *testing.T) {
	_, logPath, rc := newGrantEnv(t)
	name, _ := signerContext(t)

	code, out, errOut := rc("grant", "add", "--scope", testGrantScope, "--ttl", "15m", "--count", "3")
	if code != exitOK {
		t.Fatalf("grant add 退出码 = %d; stderr: %s", code, errOut)
	}
	var doc grantAddDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 grantAddDoc: %v", err)
	}

	var found bool
	for _, rec := range readGrantAudit(t, logPath) {
		if rec.Tool != "grant_issue" {
			continue
		}
		found = true
		if rec.Identity != txIdentity {
			t.Errorf("审计 identity = %q, want %q", rec.Identity, txIdentity)
		}
		if rec.Outcome != "success" {
			t.Errorf("审计 outcome = %q, want success", rec.Outcome)
		}
		for _, key := range []string{"grant_id", "scope", "ttl_seconds", "max_uses", "issued_by"} {
			if _, ok := rec.Args[key]; !ok {
				t.Errorf("审计 args 缺 %q: %+v", key, rec.Args)
			}
		}
		if rec.Args["grant_id"] != doc.ID {
			t.Errorf("审计 grant_id = %v, want %s", rec.Args["grant_id"], doc.ID)
		}
		if rec.Args["issued_by"] != name {
			t.Errorf("审计 issued_by = %v, want %s", rec.Args["issued_by"], name)
		}
		if rec.Args["ttl_seconds"] != float64(900) {
			t.Errorf("审计 ttl_seconds = %v, want 900", rec.Args["ttl_seconds"])
		}
	}
	if !found {
		t.Fatalf("审计日志里没有 grant_issue 条目: %+v", readGrantAudit(t, logPath))
	}
}

func TestGrantListShowsGrantsAndUsage(t *testing.T) {
	dir, _, rc := newGrantEnv(t)
	txDir := t.TempDir()
	t.Setenv("DAEDALUS_TX_DIR", txDir)

	now := time.Now().UTC()
	live := &grant.Grant{
		ID: "g-20261120-aaaaaaaa", Scope: []string{testGrantScope}, IssuedBy: "alice",
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 3, Note: "live",
	}
	dead := &grant.Grant{
		ID: "g-20261120-bbbbbbbb", Scope: []string{"systemd:user:start:web.service"}, IssuedBy: "bob",
		IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute), MaxUses: 1,
	}
	writeTestGrant(t, dir, live)
	writeTestGrant(t, dir, dead)

	usagePath, err := grant.UsageLogPath()
	if err != nil {
		t.Fatalf("解析用量日志路径失败: %v", err)
	}
	if err := grant.RecordUse(usagePath, live.ID, now); err != nil {
		t.Fatalf("写用量记账失败: %v", err)
	}

	code, out, errOut := rc("grant", "list")
	if code != exitOK {
		t.Fatalf("grant list 退出码 = %d; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)
	var doc grantListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 grantListDoc: %v (%q)", err, out)
	}
	if len(doc.Grants) != 2 {
		t.Fatalf("条子数 = %d, want 2: %+v", len(doc.Grants), doc.Grants)
	}
	if doc.Grants[0].ID != live.ID || doc.Grants[1].ID != dead.ID {
		t.Errorf("列表应按 id 升序: %+v", doc.Grants)
	}
	if doc.Grants[0].Used != 1 || doc.Grants[0].Expired {
		t.Errorf("live 条子 used=%d expired=%v, want 1/false", doc.Grants[0].Used, doc.Grants[0].Expired)
	}
	if doc.Grants[0].IssuedBy != "alice" || doc.Grants[0].MaxUses != 3 {
		t.Errorf("live 条子字段异常: %+v", doc.Grants[0])
	}
	if !doc.Grants[1].Expired {
		t.Errorf("dead 条子应标记 expired: %+v", doc.Grants[1])
	}
}

func TestGrantListEmptyWhenDirMissing(t *testing.T) {
	dir, _, rc := newGrantEnv(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("删条子目录失败: %v", err)
	}

	code, out, errOut := rc("grant", "list")
	if code != exitOK {
		t.Fatalf("grant list 退出码 = %d; stderr: %s", code, errOut)
	}
	var doc grantListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 grantListDoc: %v (%q)", err, out)
	}
	if len(doc.Grants) != 0 || len(doc.Invalid) != 0 {
		t.Errorf("目录缺失应给出空列表: %+v", doc)
	}
}

func TestGrantListReportsInvalidFiles(t *testing.T) {
	dir, _, rc := newGrantEnv(t)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatalf("写坏条子失败: %v", err)
	}

	code, out, errOut := rc("grant", "list")
	if code != exitOK {
		t.Fatalf("grant list 退出码 = %d; stderr: %s", code, errOut)
	}
	var doc grantListDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout 非 grantListDoc: %v", err)
	}
	if len(doc.Invalid) != 1 || doc.Invalid[0].File != "bad.json" || doc.Invalid[0].Error == "" {
		t.Errorf("坏条子应单独列出: %+v", doc.Invalid)
	}
}

func TestGrantAddRefusesForeignSudoUserAsNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 能 chown 到任何用户, 本用例只测非 root 的属主门")
	}
	dir, _, rc := newGrantEnv(t)
	own, err := user.Current()
	if err != nil {
		t.Fatalf("取当前用户失败: %v", err)
	}
	other := otherNonRootUser(t, own.Uid)
	// SUDO_USER 伪造得再像也没用: 非 root chown 到别人的 uid 必然失败, 签发整体拒绝。
	t.Setenv("SUDO_USER", other.Username)
	writeAutonomy(t, fmt.Sprintf(
		"[[signer]]\nuser = %q\nmax_scope = [\"systemd:user:restart:*\"]\nmax_ttl = \"2h\"\nmax_uses = 5\n",
		other.Username))

	code, out, errOut := rc("grant", "add", "--scope", testGrantScope)
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "属主") {
		t.Errorf("stderr 应说明属主无法改成真实发起人: %q", errOut)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读条子目录失败: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("拒绝签发后不应留下条子文件: %s", e.Name())
		}
	}
}

func TestGrantRevokeRemovesOwnGrant(t *testing.T) {
	dir, logPath, rc := newGrantEnv(t)
	u, err := user.Current()
	if err != nil {
		t.Fatalf("取当前用户失败: %v", err)
	}
	t.Setenv("SUDO_USER", u.Username)
	now := time.Now().UTC()
	g := &grant.Grant{
		ID: "g-20261120-cccccccc", Scope: []string{testGrantScope}, IssuedBy: u.Username,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 1,
	}
	path := writeTestGrant(t, dir, g)

	code, out, errOut := rc("grant", "revoke", g.ID)
	if code != exitOK {
		t.Fatalf("grant revoke 退出码 = %d; stderr: %s", code, errOut)
	}
	oneJSONDoc(t, out)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("条子文件应已被删除, stat err = %v", err)
	}

	var found bool
	for _, rec := range readGrantAudit(t, logPath) {
		if rec.Tool != "grant_revoke" {
			continue
		}
		found = true
		if rec.Identity != txIdentity || rec.Outcome != "success" {
			t.Errorf("revoke 审计异常: %+v", rec)
		}
		if rec.Args["grant_id"] != g.ID {
			t.Errorf("revoke 审计 grant_id = %v, want %s", rec.Args["grant_id"], g.ID)
		}
	}
	if !found {
		t.Fatalf("审计日志里没有 grant_revoke 条目")
	}
}

func TestGrantRevokeRefusesOtherOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 允许撤销任何人的条子, 本用例只测非 root 的属主门")
	}
	dir, _, rc := newGrantEnv(t)
	own, err := user.Current()
	if err != nil {
		t.Fatalf("取当前用户失败: %v", err)
	}
	other := otherNonRootUser(t, own.Uid)
	t.Setenv("SUDO_USER", other.Username)

	now := time.Now().UTC()
	g := &grant.Grant{
		ID: "g-20261120-dddddddd", Scope: []string{testGrantScope}, IssuedBy: own.Username,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 1,
	}
	path := writeTestGrant(t, dir, g)

	code, out, errOut := rc("grant", "revoke", g.ID)
	if code != exitRuntime {
		t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
	}
	if !strings.Contains(errOut, "root") {
		t.Errorf("stderr 应提示需要 root: %q", errOut)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("拒绝后条子文件必须仍在: %v", err)
	}
}

func TestGrantRevokeUnsafeIDAndMissing(t *testing.T) {
	t.Run("路径逃逸的 id", func(t *testing.T) {
		_, _, rc := newGrantEnv(t)
		code, out, _ := rc("grant", "revoke", "../evil")
		if code != exitUsage {
			t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitUsage, out)
		}
	})
	t.Run("条子不存在", func(t *testing.T) {
		_, _, rc := newGrantEnv(t)
		code, out, errOut := rc("grant", "revoke", "g-20260101-deadbeef")
		if code != exitRuntime {
			t.Fatalf("退出码 = %d, want %d; stdout: %s", code, exitRuntime, out)
		}
		if !strings.Contains(errOut, "不存在") {
			t.Errorf("stderr 应说明条子不存在: %q", errOut)
		}
	})
}

func TestGrantUsageMentionsSubcommands(t *testing.T) {
	var buf bytes.Buffer
	text := usage()
	buf.WriteString(text)
	for _, want := range []string{"grant add", "grant list", "grant revoke", "--scope", "--ttl", "--count"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage 缺 %q", want)
		}
	}
}
