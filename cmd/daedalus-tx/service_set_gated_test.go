package main

// service_set_gated_test.go —— service.set 对真实 systemd user manager 的
// 端到端门控测试(plan todo 22 Happy QA 的 Go 形态)。门控协议: 经注入缝
// systemctlUserBinary 探测 `systemctl --user show-environment`, 失败即
// t.Skip("no user manager") —— 无用户会话的 CI(headless runner)恒绿。
// 通过时把一次性 oneshot fixture 写进真实 ~/.config/systemd/user, 全 CLI
// 回路 begin→propose(started)→apply→观测 active→rollback→观测恢复到
// inactive→status, t.Cleanup 强制 stop + 删 fixture + daemon-reload。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ───────────────────────── 门控: 真实 systemd user manager ─────────────────────────

// showActiveState 用真 systemctl 读一个单元的 ActiveState(门控测试专用)。
func showActiveState(t *testing.T, unit string) string {
	t.Helper()
	data, err := exec.Command(systemctlUserBinary, "--user", "show", unit, "--property=ActiveState").Output()
	if err != nil {
		t.Fatalf("真实 systemctl show 失败: %v", err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(data)), "ActiveState=")
}

// TestServiceSet_Gated_UserManagerRoundtrip 是 plan todo 22 Happy QA 的 Go 形态:
// 先经注入缝探测 `systemctl --user show-environment`, 失败即 t.Skip —— 无用户
// 会话的 CI 恒绿。通过时把一次性 fixture 单元放进真实 ~/.config/systemd/user,
// 走 begin→propose(started)→apply→(观测 active)→rollback→(观测恢复到 inactive),
// t.Cleanup 负责 stop + 删 fixture + daemon-reload(plan 强制 teardown)。
func TestServiceSet_Gated_UserManagerRoundtrip(t *testing.T) {
	if err := exec.Command(systemctlUserBinary, "--user", "show-environment").Run(); err != nil {
		t.Skip("no user manager")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无 $HOME: %v", err)
	}
	userDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Skipf("无法建用户单元目录: %v", err)
	}
	var rb [4]byte
	if _, err := rand.Read(rb[:]); err != nil {
		t.Skipf("无熵源: %v", err)
	}
	name := "daedalus-tx-gated-" + hex.EncodeToString(rb[:])
	unit := name + ".service"
	path := filepath.Join(userDir, unit)
	fixture := "[Unit]\nDescription=daedalus-tx gated fixture\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n"
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写 fixture 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command(systemctlUserBinary, "--user", "stop", unit).Run()
		_ = os.Remove(path)
		_ = exec.Command(systemctlUserBinary, "--user", "daemon-reload").Run()
	})
	if out, err := exec.Command(systemctlUserBinary, "--user", "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("daemon-reload 失败: %v(%s)", err, out)
	}

	logPath, rc := harness(t)
	id := doBegin(t, rc)
	args := fmt.Sprintf(`{"name":%q,"desired_state":"started"}`, name)
	if c, out, e := rc("propose", id, "service.set", args); c != exitOK {
		t.Fatalf("真实 propose 失败: %d %s %s", c, out, e)
	}
	if got := showActiveState(t, unit); got != "inactive" {
		t.Fatalf("前置状态应为 inactive, 实得 %q", got)
	}
	if c, out, e := rc("apply", id); c != exitOK {
		t.Fatalf("真实 apply 失败: %d %s %s", c, out, e)
	}
	if got := showActiveState(t, unit); got != "active" {
		t.Fatalf("apply 后 ActiveState 应变 active, 实得 %q", got)
	}
	if c, out, e := rc("rollback", id); c != exitOK {
		t.Fatalf("真实 rollback 失败: %d %s %s", c, out, e)
	}
	if got := showActiveState(t, unit); got != "inactive" {
		t.Fatalf("rollback 应恢复到 inactive, 实得 %q", got)
	}
	if c, so, _ := rc("status", id); c != exitOK || parseStatus(t, so).Status != "rolled_back" {
		t.Fatalf("status 终态异常: %d %s", c, so)
	}
	if got := inTxCount(readAudit(t, logPath), id); got != 3 {
		t.Fatalf("in-tx 记录数 = %d, want 3", got)
	}
	mustVerify(t, logPath)
}
