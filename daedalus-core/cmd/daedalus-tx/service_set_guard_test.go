package main

// service_set_guard_test.go —— service.set 的论证防线测试(todo 22):
// 名称/参数门、UNCONDITIONAL 路径守卫、reload 拒绝。全部要求
// "拒绝发生在读取文件与执行 systemctl 之前" —— 以 capture 文件根本不存在为证据。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rejectPropose 断言 propose 出错且假 systemctl 从未被调用(capture 无文件)。
func rejectPropose(t *testing.T, unitDir, args, wantErr string) {
	t.Helper()
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
	_, _, err := (serviceSetAdapter{}).Propose(unitDirCtx(unitDir), json.RawMessage(args))
	if err == nil {
		t.Fatalf("propose 应失败(%s), args=%s", wantErr, args)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Errorf("错误 %q 不含 %q", err, wantErr)
	}
	if _, statErr := os.Stat(capture); !os.IsNotExist(statErr) {
		t.Errorf("拒绝必须发生在 systemctl 执行之前(capture 已存在): %v", captureCalls(t, capture))
	}
}

// ───────────────────────── 名称门(正则 + 路径字符)─────────────────────────

func TestServiceSet_Propose_RejectsBadNames(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ name, why string }{
		{"", "空名"},
		{"../../etc/passwd", "相对逃逸"},
		{"foo;rm", "分号注入"},
		{"foo bar", "空格"},
		{"foo|bar", "管道符"},
		{"$(id)", "命令替换"},
		{"foo\x00bar", "空字节"},
		{"*glob", "glob 星号"},
		{"a/b", "路径分隔符"},
		{`a\b`, "反斜杠"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"name": tc.name, "desired_state": "started"})
			if err != nil {
				t.Fatal(err)
			}
			rejectPropose(t, dir, string(args), "invalid unit name")
		})
	}
}

func TestServiceSet_Propose_RejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ args, wantErr string }{
		{`not json`, "service.set"},                                 // 非法 JSON
		{`{"name":1,"desired_state":"started"}`, "service.set"},     // name 类型错
		{`{"name":"a","desired_state":"started","x":1}`, "unknown"}, // 未知键
		{`{"name":"ok"}`, "desired_state"},                          // 缺 desired_state
	}
	for _, tc := range cases {
		t.Run(tc.args, func(t *testing.T) {
			rejectPropose(t, dir, tc.args, tc.wantErr)
		})
	}
}

// ───────────────────────── (f) reload 拒绝 ─────────────────────────

func TestServiceSet_Propose_RejectsReload(t *testing.T) {
	rejectPropose(t, t.TempDir(), `{"name":"demo","desired_state":"reload"}`,
		"desired_state reload not supported in v1")
}

// ───────────────────────── UNCONDITIONAL 路径守卫 ─────────────────────────

// 核心防线: --unit-dir(或解析结果)落入系统级单元目录 → 直接拒绝且不 exec。
// 断言形态: 逐字错误 "v1 supports user-scope units only: <path>"。
func TestServiceSet_Guard_RefusesSystemScopeDirsWithoutExec(t *testing.T) {
	cases := []struct{ dir, why string }{
		{"/etc/systemd/system", "系统级单元目录"},
		{"/usr/lib/systemd/system", "发行版单元目录"},
		{"/etc/systemd/system/../system", "归一化后仍是系统目录"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			rejectPropose(t, tc.dir, `{"name":"demo","desired_state":"started"}`,
				"v1 supports user-scope units only")
		})
	}
}

// 符号链接单元文件指向允许根之外 → 解析后拒绝, 不 exec。
func TestServiceSet_Guard_SymlinkEscapeRefused(t *testing.T) {
	dir := t.TempDir()
	out := t.TempDir()
	outside := filepath.Join(out, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "demo.service")); err != nil {
		t.Skipf("符号链接不可用: %v", err)
	}
	rejectPropose(t, dir, `{"name":"demo","desired_state":"started"}`,
		"v1 supports user-scope units only")
}

// 允许根正向面: 不带 --unit-dir 时默认解析 $HOME/.config/systemd/user → 放行。
func TestServiceSet_Guard_AllowedUserRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	guarded := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(guarded, 0o755); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
	raw, _, err := (serviceSetAdapter{}).Propose(unitDirCtx(""),
		json.RawMessage(`{"name":"demo","desired_state":"started"}`))
	if err != nil {
		t.Fatalf("默认 HOME 用户根应放行: %v", err)
	}
	var b serviceSetBefore
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(guarded, "demo.service"); b.UnitFile != want {
		t.Errorf("unit_file = %q, want %q", b.UnitFile, want)
	}
	if got := captureCalls(t, capture); len(got) != 1 {
		t.Errorf("应恰一次 show: %v", got)
	}
}
