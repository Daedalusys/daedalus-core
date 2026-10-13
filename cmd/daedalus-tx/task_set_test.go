package main

// task_set_test.go —— task.set 适配器测试。
//
// systemctl 一律经包级注入缝 systemctlUserBinary 指向 t.TempDir() 下的 shell
// 夹具(模式镜像 service_set_test.go 的 fakeSystemctlUser): 每次调用把 "$*" 追加
// 进 capture 文件(一行一次调用), "$2"==is-enabled 时按夹具回显 enabled_state。
// 绝不触碰宿主真实 systemd —— 测试确定性 + 隔离性双重保证。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// installFakeSystemctl 安装假 systemctl 并注入包级变量(测试结束自动还原)。
// enabledOut 是 `is-enabled` 子命令的固定回显(如 "disabled");
// exitCode 是恒定的退出码(0=全成功; 非零用于观测失败路径)。返回 capture 路径。
func installFakeSystemctl(t *testing.T, enabledOut string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) +
		"\nif [ \"$2\" = is-enabled ]; then printf '%s\\n' " + strconv.Quote(enabledOut) + "; fi\nexit " +
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

// writeTimerFile 在 unitDir 下写一个 timer 单元文件。
func writeTimerFile(t *testing.T, unitDir, stem, content string) {
	t.Helper()
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("建 unitDir 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, stem), []byte(content), 0o644); err != nil {
		t.Fatalf("写 timer 文件失败: %v", err)
	}
}

func TestTaskSet_Propose_EnabledHappyPath(t *testing.T) {
	dir := t.TempDir()
	unitDir := filepath.Join(dir, "units")
	writeTimerFile(t, unitDir, "backup.timer", "[Timer]\n")
	installFakeSystemctl(t, "disabled", 0)

	a := taskSetAdapter{}
	before, after, err := a.Propose(unitDirCtx(unitDir),
		json.RawMessage(`{"name":"backup","desired_state":"enabled"}`))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}

	var b taskSetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if b.TimerName != "backup.timer" {
		t.Errorf("timer_name got %q want %q", b.TimerName, "backup.timer")
	}
	if b.EnabledState != "disabled" {
		t.Errorf("enabled_state got %q want %q", b.EnabledState, "disabled")
	}
	if b.FileContent == nil || *b.FileContent != "[Timer]\n" {
		t.Errorf("file_content 快照不符: %v", b.FileContent)
	}
	if b.UnitFile != filepath.Join(unitDir, "backup.timer") {
		t.Errorf("unit_file got %q want %q", b.UnitFile, filepath.Join(unitDir, "backup.timer"))
	}

	var af taskSetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if af.Timer != "backup.timer" {
		t.Errorf("timer got %q want %q", af.Timer, "backup.timer")
	}
	if af.DesiredState != "enabled" {
		t.Errorf("desired_state got %q want %q", af.DesiredState, "enabled")
	}
	if af.Verb != "enable" {
		t.Errorf("verb got %q want %q", af.Verb, "enable")
	}
}


