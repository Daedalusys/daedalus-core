package main

// service_set_test.go —— service.set 适配器正向行为测试(todo 22)。
//
// systemctl 一律经包级注入缝 systemctlUserBinary 指向 t.TempDir() 下的 shell
// 夹具(镜像 cmd/daedalus-service 的 fakeSystemctl 模式): 每次调用把 "$*" 追加
// 进 capture 文件(一行一次调用), "$2"==show 时按夹具回显 ActiveState。
// 绝不触碰宿主真实 systemd —— 测试确定性 + 隔离性双重保证。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/tx"
)

// fakeSystemctlUser 安装假 systemctl 并注入包级变量(测试结束自动还原)。
// showOut 是 `show` 子命令的固定回显(如 "ActiveState=inactive");
// exitCode 是恒定的退出码(0=全成功; 非零用于观测失败路径)。返回 capture 路径。
func fakeSystemctlUser(t *testing.T, showOut string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) +
		"\nif [ \"$2\" = show ]; then printf '%s\\n' " + strconv.Quote(showOut) + "; fi\nexit " +
		strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 systemctl 失败: %v", err)
	}
	orig := systemctlUserBinary
	systemctlUserBinary = path
	t.Cleanup(func() { systemctlUserBinary = orig })
	return capture
}

// captureCalls 读取 capture 为"每次调用一行 argv"的切片; 文件不存在 → nil(从未执行)。
func captureCalls(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读 capture 失败: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// unitDirCtx 构造带 --unit-dir 覆写的上下文(CLI 侧由 commands.go 注入同款值)。
func unitDirCtx(dir string) context.Context {
	return withUnitDir(context.Background(), dir)
}

// mustPropose 跑 propose 并要求成功, 返回解析后的 before/after 快照。
func mustPropose(t *testing.T, ctx context.Context, args string) (serviceSetBefore, serviceSetAfter) {
	t.Helper()
	a := serviceSetAdapter{}
	rawBefore, rawAfter, err := a.Propose(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}
	var b serviceSetBefore
	if err := json.Unmarshal(rawBefore, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, rawBefore)
	}
	var af serviceSetAfter
	if err := json.Unmarshal(rawAfter, &af); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, rawAfter)
	}
	return b, af
}

// stepFor 用 propose 的真实产物拼一个可喂给 Apply/Rollback 的 tx.Step。
func stepFor(t *testing.T, ctx context.Context, args string) tx.Step {
	t.Helper()
	b, _ := mustPropose(t, ctx, args)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return tx.Step{Adapter: "service.set", Args: json.RawMessage(args), BeforeState: raw}
}

// ───────────────────────── Propose: 快照 + argv ─────────────────────────

func TestServiceSet_Propose_SnapshotAndArgv(t *testing.T) {
	dir := t.TempDir()
	content := "[Unit]\nDescription=demo\n[Service]\nType=oneshot\nExecStart=/bin/true\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.service"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)

	before, after := mustPropose(t, unitDirCtx(dir), `{"name":"demo","desired_state":"started"}`)

	if want := filepath.Join(dir, "demo.service"); before.UnitFile != want {
		t.Errorf("unit_file = %q, want %q", before.UnitFile, want)
	}
	if before.FileContent == nil || *before.FileContent != content {
		t.Errorf("file_content 未逐字快照: %+v", before.FileContent)
	}
	if before.ActiveState != "inactive" {
		t.Errorf("active_state = %q, want inactive", before.ActiveState)
	}
	if after.Unit != "demo.service" || after.DesiredState != "started" || after.Verb != "start" {
		t.Errorf("after_state 不符: %+v", after)
	}
	// argv 逐字钉死: 仅一次 show, 形态 = systemctl --user show <unit>.service --property=ActiveState。
	got := captureCalls(t, capture)
	want := []string{"--user show demo.service --property=ActiveState"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("capture argv =\n%v\nwant\n%v", got, want)
	}
}

func TestServiceSet_Propose_AbsentFileContentOmitted(t *testing.T) {
	dir := t.TempDir()
	fakeSystemctlUser(t, "ActiveState=inactive", 0)

	// 单元文件尚不存在: file_content 键必须整个缺席(而非空串)。
	raw, _, err := (serviceSetAdapter{}).Propose(unitDirCtx(dir),
		json.RawMessage(`{"name":"ghost","desired_state":"enabled"}`))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if _, ok := b["file_content"]; ok {
		t.Errorf("缺失单元不应带 file_content 键: %s", raw)
	}
	// .service 隐式后缀: name 已带后缀也不得重复追加(序列化面直查原文)。
	if strings.Contains(string(raw), "ghost.service.service") {
		t.Errorf("重复追加了 .service 后缀: %s", raw)
	}
}

func TestServiceSet_Propose_EmptyFileIsNotAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zero.service"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeSystemctlUser(t, "ActiveState=inactive", 0)
	before, _ := mustPropose(t, unitDirCtx(dir), `{"name":"zero","desired_state":"started"}`)
	if before.FileContent == nil || *before.FileContent != "" {
		t.Errorf("空文件应快照为空串而非缺席: %+v", before.FileContent)
	}
}

// ───────────────────────── Apply: 动词映射(d 条款)─────────────────────────

func TestServiceSet_Apply_VerbMap(t *testing.T) {
	cases := []struct{ desired, verb string }{
		{"started", "start"},
		{"stopped", "stop"},
		{"restarted", "restart"},
		{"enabled", "enable"},
		{"disabled", "disable"},
	}
	for _, tc := range cases {
		t.Run(tc.desired, func(t *testing.T) {
			dir := t.TempDir()
			capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
			ctx := unitDirCtx(dir)
			args := `{"name":"demo","desired_state":"` + tc.desired + `"}`
			step := stepFor(t, ctx, args)

			res := serviceSetAdapter{}.Apply(ctx, step)
			if res.Returncode != 0 || res.Error != "" {
				t.Fatalf("apply 结果异常: %+v", res)
			}
			got := captureCalls(t, capture)
			wantLast := "--user " + tc.verb + " demo.service"
			if len(got) != 2 || got[1] != wantLast {
				t.Errorf("capture = %v, want [show, %q]", got, wantLast)
			}
		})
	}
}

func TestServiceSet_Apply_UnknownDesiredFails(t *testing.T) {
	// propose 接受任意 desired_state(除 reload), 未知动词在 apply 拒绝
	// (plan todo 22 failure QA: apply exit 1 + 拒绝原因落 OpResult)。
	dir := t.TempDir()
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
	ctx := unitDirCtx(dir)
	step := stepFor(t, ctx, `{"name":"demo","desired_state":"invalid"}`)
	before := len(captureCalls(t, capture))

	res := serviceSetAdapter{}.Apply(ctx, step)
	if res.Returncode == 0 {
		t.Fatalf("未知 desired_state 必须失败: %+v", res)
	}
	if !strings.Contains(res.Error, "desired_state") {
		t.Errorf("拒绝原因应点名 desired_state: %q", res.Error)
	}
	if len(captureCalls(t, capture)) != before {
		t.Errorf("未知动词绝不执行 systemctl: %v", captureCalls(t, capture))
	}
}
