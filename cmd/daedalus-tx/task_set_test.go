package main

// task_set_test.go —— task.set 适配器测试。
//
// systemctl 一律经包级注入缝 systemctlUserBinary 指向 t.TempDir() 下的 shell
// 夹具(模式镜像 service_set_test.go 的 fakeSystemctlUser): 每次调用把 "$*" 追加
// 进 capture 文件(一行一次调用), "$2"==is-enabled 时按夹具回显 enabled_state。
// 绝不触碰宿主真实 systemd —— 测试确定性 + 隔离性双重保证。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// installFakeSystemctl 安装假 systemctl 并注入包级变量(测试结束自动还原)。
// enabledOut 是 `is-enabled` 子命令的固定回显(如 "disabled" / "enabled");
// disableExit 是 `disable` 子命令独立退出码(0=成功 / 非零用于镶试 apply 负面路径);
// 其他子命令均返回 0。返回 capture 路径。
func installFakeSystemctl(t *testing.T, enabledOut string, disableExit int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) + "\n"
	script += "case \"$2\" in\n"
	script += "  is-enabled) printf '%s\\n' " + strconv.Quote(enabledOut) + " ;;\n"
	script += "  disable)    exit " + strconv.Itoa(disableExit) + " ;;\n"
	script += "esac\nexit 0\n"
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 systemctl 失败: %v", err)
	}
	orig := systemctlUserBinary
	systemctlUserBinary = path
	t.Cleanup(func() { systemctlUserBinary = orig })
	return capture
}

// readCaptured 读取 capture 为"每次调用一行 argv"的切片; 文件不存在 → nil(从未执行)。
func readCaptured(t *testing.T, capture string) []string {
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

// mustBuildTaskSetBefore 构造 step.BeforeState JSON, 仅填 Propose/Apply 必需的 UnitFile。
// Apply/Rollback 测试用本帮手生成 before_state: 内容与 enabled_state 供 Rollback 检查。
func mustBuildTaskSetBefore(t *testing.T, unitDir, stem, content, enabledState string) json.RawMessage {
	t.Helper()
	unitFile, err := filepath.Abs(filepath.Join(unitDir, stem))
	if err != nil {
		t.Fatalf("计算 unitFile 路径失败: %v", err)
	}
	if content == "" {
		b, err := json.Marshal(taskSetBefore{TimerName: stem, UnitFile: unitFile, EnabledState: enabledState})
		if err != nil {
			t.Fatalf("序列化 before_state 失败: %v", err)
		}
		return b
	}
	b, err := json.Marshal(taskSetBefore{TimerName: stem, UnitFile: unitFile, FileContent: &content, EnabledState: enabledState})
	if err != nil {
		t.Fatalf("序列化 before_state 失败: %v", err)
	}
	return b
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

func TestTaskSet_Propose_RejectsUnknownDesiredState(t *testing.T) {
	a := taskSetAdapter{}
	_, _, err := a.Propose(unitDirCtx(t.TempDir()),
		json.RawMessage(`{"name":"backup","desired_state":"started"}`))
	if err == nil {
		t.Fatal("表外 desired_state 竟然 Propose 成功")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown desired_state "started"`) {
		t.Errorf("报错未包含表外值逐字标识: %s", msg)
	}
	if !strings.Contains(msg, "enabled|disabled|absent") {
		t.Errorf("报错未点名 v1 词表: %s", msg)
	}
}

func TestTaskSet_Propose_RejectsMissingFile_AllStates(t *testing.T) {
	unitDir := t.TempDir() // 空：没有任何 timer 文件
	for _, state := range []string{"enabled", "disabled", "absent"} {
		t.Run(state, func(t *testing.T) {
			a := taskSetAdapter{}
			_, _, err := a.Propose(unitDirCtx(unitDir),
				json.RawMessage(fmt.Sprintf(`{"name":"backup","desired_state":%q}`, state)))
			if err == nil {
				t.Fatalf("missing-file + desired_state=%q 竟然 Propose 成功", state)
			}
			if !strings.Contains(err.Error(), "task file not found:") {
				t.Errorf("[%s] 报错未逐字点名 task file not found: %s", state, err.Error())
			}
		})
	}
}

func TestTaskSet_Propose_RejectsMissingDesiredState(t *testing.T) {
	a := taskSetAdapter{}
	_, _, err := a.Propose(unitDirCtx(t.TempDir()),
		json.RawMessage(`{"name":"backup"}`))
	if err == nil {
		t.Fatal("缺 desired_state 竟然 Propose 成功")
	}
	if !strings.Contains(err.Error(), `缺 "desired_state"`) {
		t.Errorf("报错未点名 missing field: %s", err.Error())
	}
}

func TestTaskSet_Propose_RejectsTrailingJSON(t *testing.T) {
	a := taskSetAdapter{}
	_, _, err := a.Propose(unitDirCtx(t.TempDir()),
		json.RawMessage(`{"name":"backup","desired_state":"enabled"}x`))
	if err == nil {
		t.Fatal("尾随 JSON 竟然 Propose 成功")
	}
	if !strings.Contains(err.Error(), "尾随数据") {
		t.Errorf("报错未点名 trailing data: %s", err.Error())
	}
}

// ===== Apply =====

func TestTaskSet_Apply_Enabled(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	writeTimerFile(t, unitDir, "backup.timer", "[Timer]\n")
	capture := installFakeSystemctl(t, "disabled", 0)

	a := taskSetAdapter{}
	res := a.Apply(unitDirCtx(unitDir), tx.Step{
		Args:        json.RawMessage(`{"name":"backup","desired_state":"enabled"}`),
		BeforeState: mustBuildTaskSetBefore(t, unitDir, "backup.timer", "[Timer]\n", "disabled"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{"--user enable backup.timer"}
	if got := readCaptured(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("systemctl 调用序列 got %v want %v", got, want)
	}
}

func TestTaskSet_Apply_Disabled(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	writeTimerFile(t, unitDir, "backup.timer", "[Timer]\n")
	capture := installFakeSystemctl(t, "enabled", 0)

	a := taskSetAdapter{}
	res := a.Apply(unitDirCtx(unitDir), tx.Step{
		Args:        json.RawMessage(`{"name":"backup","desired_state":"disabled"}`),
		BeforeState: mustBuildTaskSetBefore(t, unitDir, "backup.timer", "[Timer]\n", "enabled"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{"--user disable backup.timer"}
	if got := readCaptured(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("systemctl 调用序列 got %v want %v", got, want)
	}
}

func TestTaskSet_Apply_Absent_DisablesRemovesReloads(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	writeTimerFile(t, unitDir, "backup.timer", "[Timer]\n")
	capture := installFakeSystemctl(t, "enabled", 0)

	a := taskSetAdapter{}
	res := a.Apply(unitDirCtx(unitDir), tx.Step{
		Args:        json.RawMessage(`{"name":"backup","desired_state":"absent"}`),
		BeforeState: mustBuildTaskSetBefore(t, unitDir, "backup.timer", "[Timer]\n", "enabled"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{
		"--user is-enabled backup.timer",
		"--user disable backup.timer",
		"--user daemon-reload",
	}
	if got := readCaptured(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("systemctl 调用序列 got %v want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(unitDir, "backup.timer")); !os.IsNotExist(err) {
		t.Errorf("absent 后 timer 文件应被删除, stat err = %v", err)
	}
}

func TestTaskSet_Apply_Absent_DisableFails_DoesNotRemove(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	writeTimerFile(t, unitDir, "backup.timer", "[Timer]\n")
	_ = installFakeSystemctl(t, "enabled", 1) // disable 返回非零

	a := taskSetAdapter{}
	res := a.Apply(unitDirCtx(unitDir), tx.Step{
		Args:        json.RawMessage(`{"name":"backup","desired_state":"absent"}`),
		BeforeState: mustBuildTaskSetBefore(t, unitDir, "backup.timer", "[Timer]\n", "enabled"),
	})
	if res.Returncode == 0 {
		t.Fatal("disable 失败时 apply 应返回 rc!=0")
	}
	if _, err := os.Stat(filepath.Join(unitDir, "backup.timer")); err != nil {
		t.Errorf("disable 失败后 timer 文件必须保留, stat err = %v", err)
	}
}


