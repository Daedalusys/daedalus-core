package main

// task_set_guard_test.go —— task.set 的路径守卫与单元名门负向测试。
//
// 本测试主要钉 unitguard.go + normalizeTimerName 的拒绝串, task.set 适配器作为消费方
// 在 Propose 三段均使用。任何需要改 unitguard.go 才能让本测试通过的修改,
// 均视作 spec §7 框架验证失败(应直接拒绝并返回 spec 重审)。
//
// 注: ".." 作为单元名不在名称门拒范围。`normalizeTimerName` 按单元名字符集
// 接受 .. 和多重 ., 转换后是合法单元名(如 `..timer`)。路径遍历的防护任然隐含:
// `..` 作为路径(例如 spec.resources[].name=".")会被 guardUnitPath + ReadFile
// 拿到不存在文件而以 “task file not found” 拒,这是不同语义。为减少 single point of
// concern, 名称门仅限字符类与 .timer 缀转换, 路径语义交给后续守卫。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskSet_Guard_RejectsSystemScope(t *testing.T) {
	a := taskSetAdapter{}
	_, _, err := a.Propose(unitDirCtx("/etc/systemd/system"),
		json.RawMessage(`{"name":"backup","desired_state":"enabled"}`))
	if err == nil {
		t.Fatal("系统域 unitDir 必须被拒")
	}
	want := "v1 supports user-scope units only: /etc/systemd/system/backup.timer"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("reject 串漂移, got %q want substring %q", err.Error(), want)
	}
}

func TestTaskSet_Guard_RejectsInvalidName(t *testing.T) {
	for _, name := range []string{
		"a b",    // 空格
		"a/b",    // 路径分隔
		"-x",     // 前导中划线(argv 注入)
		"a\x00b", // 空字节
	} {
		t.Run(name, func(t *testing.T) {
			// normalizeTimerName 是名称门唯一出口——未达此已拒,证明门起作用。
			if _, err := normalizeTimerName(name); err == nil {
				t.Fatalf("%q 应被名称门拒", name)
			} else if !strings.Contains(err.Error(), "invalid unit name:") {
				t.Errorf("%q 报错未逐字点名 invalid unit name:, got %q", name, err.Error())
			}
		})
	}
}

func TestTaskSet_Guard_RejectsPathTraversalViaSymlink(t *testing.T) {
	// unitDir 是一个指向 /etc/systemd/system 的符号链接: 请求的 --unit-dir 表面是
	// 一个用户可写临时目录下的路径, resolveSymlinks 必须看穿到系统域并拒。
	tmp := t.TempDir()
	userLink := filepath.Join(tmp, "user-link")
	if err := os.Symlink("/etc/systemd/system", userLink); err != nil {
		t.Fatalf("建符号链接失败: %v", err)
	}

	a := taskSetAdapter{}
	_, _, err := a.Propose(unitDirCtx(userLink),
		json.RawMessage(`{"name":"backup","desired_state":"enabled"}`))
	if err == nil {
		t.Fatal("通过符号链接换址进入系统域必须被拒")
	}
	if !strings.Contains(err.Error(), "v1 supports user-scope units only:") {
		t.Errorf("符号链接换址 拒串漂移, got %q", err.Error())
	}
}
