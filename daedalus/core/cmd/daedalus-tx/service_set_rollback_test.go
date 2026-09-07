package main

// service_set_rollback_test.go —— service.set 回滚面测试(todo 22 (e)(f) 条款):
// 恢复性动词推导、漂移恢复 + daemon-reload-before-verb 顺序、无漂移零写入、
// restoreUnitFile 恢复内核直测。夹具机制见 service_set_test.go 头注释。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ───────────────────────── Rollback: 逆动词 + 文件恢复(e/f 条款)─────────────────────────

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
			capture := fakeSystemctlUser(t, "ActiveState="+tc.prior, 0)
			ctx := unitDirCtx(dir)
			step := stepFor(t, ctx, `{"name":"demo","desired_state":"restarted"}`)

			res := serviceSetAdapter{}.Rollback(ctx, step)
			if res.Returncode != 0 || res.Error != "" {
				t.Fatalf("rollback 结果异常: %+v", res)
			}
			got := captureCalls(t, capture)
			// 首行是 propose 期的 show; 其后是回滚期的动词(若有)。
			rest := got
			if len(rest) > 0 {
				rest = rest[1:]
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
	capture := fakeSystemctlUser(t, "ActiveState=active", 0)
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
	capture := fakeSystemctlUser(t, "ActiveState=inactive", 0)
	ctx := unitDirCtx(dir)
	step := stepFor(t, ctx, `{"name":"demo","desired_state":"started"}`)

	res := serviceSetAdapter{}.Rollback(ctx, step)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("回滚异常: %+v", res)
	}
	got := captureCalls(t, capture)
	want := []string{"--user show demo.service --property=ActiveState", "--user stop demo.service"}
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
