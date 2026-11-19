package main

// service_set_rollback_test.go —— service.set 回滚面测试:
// 恢复性动词推导、漂移恢复 + daemon-reload-before-verb 顺序、无漂移零写入、
// restoreUnitFile 恢复内核直测。夹具机制见 service_set_test.go 头注释。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// TestServiceSet_Rollback_InverseVerbFromPriorState 钉"恢复到事务前的 ActiveState":
// before=active → start; before=inactive → stop; 其余状态 → 不发动词(尽力恢复, 记录说明)。
// (语义选择与计划括注 active→stop 的矛盾在服务实现注释与 DoneClaim 中说明。)
func TestServiceSet_Rollback_InverseVerbFromPriorState(t *testing.T) {
	cases := []struct {
		prior  string
		wants  []string
		skipIt bool
	}{
		{prior: "active", wants: []string{"--user start demo.service"}},
		{prior: "inactive", wants: []string{"--user stop demo.service"}},
		{prior: "activating", wants: nil, skipIt: true},
	}
	for _, tc := range cases {
		t.Run(tc.prior, func(t *testing.T) {
			dir := t.TempDir()
			capture := fakeSystemctlUserStates(t, "ActiveState="+tc.prior, "static", 0)
			ctx := unitDirCtx(dir)
			step := stepFor(t, ctx, `{"name":"demo","desired_state":"restarted"}`)

			res := serviceSetAdapter{}.Rollback(ctx, step)
			if res.Returncode != 0 || res.Error != "" {
				t.Fatalf("rollback 结果异常: %+v", res)
			}
			got := captureCalls(t, capture)
			// 前两行是 propose 期的观测(show + is-enabled); 其后是回滚期的动词(若有)。
			rest := got
			if len(rest) >= 2 {
				rest = rest[2:]
			}
			if strings.Join(rest, "\n") != strings.Join(tc.wants, "\n") {
				t.Errorf("回滚 argv = %v, want %v", rest, tc.wants)
			}
			if tc.skipIt && !strings.Contains(res.Stdout, "activating") {
				t.Errorf("跳过动词时 OpResult 应记录原因: %q", res.Stdout)
			}
		})
	}
}

// TestServiceSet_Rollback_DriftRestoreReloadBeforeVerb 是 (e)+(f) 的联合钉桩:
// 事务期外单元文件被人改动(内容漂移)→ 回滚必须恢复快照内容, 且
// `systemctl --user daemon-reload` 恰在恢复写入之后、逆动词之前执行, 并记入 OpResult。
func TestServiceSet_Rollback_DriftRestoreReloadBeforeVerb(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "demo.service")
	orig := "[Unit]\nDescription=original\n"
	drifted := "[Unit]\nDescription=hand-edited-drift\n"
	if err := os.WriteFile(unitPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUserStates(t, "ActiveState=active", "static", 0)
	ctx := unitDirCtx(dir)
	step := stepFor(t, ctx, `{"name":"demo","desired_state":"stopped"}`)

	// 模拟"适配器之外的改动": apply 后单元内容漂移。
	if err := os.WriteFile(unitPath, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	res := serviceSetAdapter{}.Rollback(ctx, step)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("回滚异常: %+v", res)
	}
	got := captureCalls(t, capture)
	want := []string{
		"--user show demo.service --property=ActiveState",
		"--user is-enabled demo.service",
		"--user daemon-reload",
		"--user start demo.service", // before=active → 恢复 start
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("argv 序列(含 daemon-reload-before-verb) =\n%v\nwant\n%v", got, want)
	}
	if !strings.Contains(res.Stdout, "daemon-reload") {
		t.Errorf("daemon-reload 已执行但没记入 OpResult: %q", res.Stdout)
	}
	data, err := os.ReadFile(unitPath)
	if err != nil || string(data) != orig {
		t.Errorf("单元内容未恢复: %q err=%v", data, err)
	}
}

// TestServiceSet_Rollback_NoDriftNoWrite 钉"v1 生命周期操作从不改文件 →
// 常态回滚无写入亦无 daemon-reload"。
func TestServiceSet_Rollback_NoDriftNoWrite(t *testing.T) {
	dir := t.TempDir()
	content := "[Service]\nExecStart=/bin/true\n"
	unitPath := filepath.Join(dir, "demo.service")
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUserStates(t, "ActiveState=inactive", "static", 0)
	ctx := unitDirCtx(dir)
	step := stepFor(t, ctx, `{"name":"demo","desired_state":"started"}`)

	res := serviceSetAdapter{}.Rollback(ctx, step)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("回滚异常: %+v", res)
	}
	got := captureCalls(t, capture)
	want := []string{
		"--user show demo.service --property=ActiveState",
		"--user is-enabled demo.service",
		"--user stop demo.service",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("无漂移时不应有 daemon-reload: %v", got)
	}
	if data, _ := os.ReadFile(unitPath); string(data) != content {
		t.Errorf("无漂移时绝不改文件: %q", data)
	}
}

// TestRestoreUnitFile_Internal 直接钉恢复内核: 差异→写并报 true;
// 相同→不动 false; 文件缺席→重建 true(机制存在的证明; v1 生命周期路径不会触发)。
func TestRestoreUnitFile_Internal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "u.service")

	// 缺 → 建
	wrote, err := restoreUnitFile(path, "AAA\n")
	if err != nil || !wrote {
		t.Fatalf("缺失应重建: wrote=%v err=%v", wrote, err)
	}
	// 同 → 不动
	if wrote, err := restoreUnitFile(path, "AAA\n"); err != nil || wrote {
		t.Fatalf("同内容不应写: wrote=%v err=%v", wrote, err)
	}
	// 异 → 覆写
	if wrote, err := restoreUnitFile(path, "BBB\n"); err != nil || !wrote {
		t.Fatalf("异内容应覆写: wrote=%v err=%v", wrote, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "BBB\n" {
		t.Errorf("覆写内容错误: %q", data)
	}
}

// TestServiceSet_Rollback_EnabledDimensionRestored 钉 #195 的修复点:
// desired_state=enabled/disabled 只动 enabled 维度(enable/disable 的符号链接),
// 回滚必须按 before.enabled_state 发恢复动词。旧实现只看 active_state ——
// 单元原本 inactive 时回滚发的是 stop(空转), enable 符号链接留在原地,
// 而事务照样报 rolled_back: 自称回滚、实际没回滚。
func TestServiceSet_Rollback_EnabledDimensionRestored(t *testing.T) {
	cases := []struct {
		name       string
		snapshot   string // Propose 期 is-enabled 的观测值
		desired    string
		wantVerbs  []string // 紧跟在 [show, is-enabled] 之后的 argv
		wantStdout []string
	}{
		{
			name:     "disabled → enabled,回滚必须 disable",
			snapshot: "disabled", desired: "enabled",
			wantVerbs:  []string{"--user disable demo.service", "--user stop demo.service"},
			wantStdout: []string{"[systemctl --user disable demo.service] rc=0"},
		},
		{
			name:     "enabled → disabled,回滚必须 enable",
			snapshot: "enabled", desired: "disabled",
			wantVerbs:  []string{"--user enable demo.service", "--user stop demo.service"},
			wantStdout: []string{"[systemctl --user enable demo.service] rc=0"},
		},
		{
			name:     "static(表外)不发 enabled 动词,但必须记原因",
			snapshot: "static", desired: "enabled",
			wantVerbs:  []string{"--user stop demo.service"},
			wantStdout: []string{`enabled_state="static" 无恢复动词映射`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "demo.service"),
				[]byte("[Unit]\nDescription=demo\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			capture := fakeSystemctlUserStates(t, "ActiveState=inactive", tc.snapshot, 0)
			ctx := unitDirCtx(dir)
			step := stepFor(t, ctx, `{"name":"demo","desired_state":"`+tc.desired+`"}`)

			res := serviceSetAdapter{}.Rollback(ctx, step)
			if res.Returncode != 0 || res.Error != "" {
				t.Fatalf("rollback 结果异常: %+v", res)
			}
			got := captureCalls(t, capture)
			if len(got) < 2 {
				t.Fatalf("抓到的 argv 太少: %v", got)
			}
			if tail := got[2:]; strings.Join(tail, "|") != strings.Join(tc.wantVerbs, "|") {
				t.Errorf("回滚 argv(去掉 propose 期观测) = %v, want %v", tail, tc.wantVerbs)
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(res.Stdout, want) {
					t.Errorf("OpResult.Stdout 缺 %q: %q", want, res.Stdout)
				}
			}
		})
	}
}

// TestServiceSet_Rollback_LegacyJournalWithoutEnabledState 钉向后兼容:
// 本次修复之前写下的事务日志没有 enabled_state 键 —— 回滚不得凭空猜一个动词,
// 但必须把「跳过该维度」写进 OpResult(静默跳过就是假成功的翻版)。
func TestServiceSet_Rollback_LegacyJournalWithoutEnabledState(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "demo.service")
	if err := os.WriteFile(unitPath, []byte("[Unit]\nDescription=demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := fakeSystemctlUser(t, "", 0) // 回滚不观测: 只读快照
	ctx := unitDirCtx(dir)
	raw, err := json.Marshal(map[string]any{
		"unit_file":    unitPath,
		"active_state": "inactive",
	})
	if err != nil {
		t.Fatal(err)
	}
	step := tx.Step{
		Adapter:     "service.set",
		Args:        json.RawMessage(`{"name":"demo","desired_state":"enabled"}`),
		BeforeState: raw,
	}

	res := serviceSetAdapter{}.Rollback(ctx, step)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("旧日志回滚应尽力完成: %+v", res)
	}
	got := captureCalls(t, capture)
	want := []string{"--user stop demo.service"} // 只有运行态维度, 无 enabled 动词
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("旧日志回滚 argv = %v, want %v(不得凭空发 enable/disable)", got, want)
	}
	if !strings.Contains(res.Stdout, `enabled_state="" 无恢复动词映射`) {
		t.Errorf("跳过 enabled 维度必须记进 OpResult: %q", res.Stdout)
	}
}
