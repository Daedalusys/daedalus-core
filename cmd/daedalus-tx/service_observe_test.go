package main

// service_observe_test.go —— service-observe 子命令的契约测试。
//
// 一律经包级注入缝 systemctlUserBinary 指向 t.TempDir() 下的 shell 夹具,
// 绝不触碰宿主真实 systemd。承重断言: stdout 恰一份 JSON、argv 只查两个属性、
// 观测不到(rc 非 0 / 缺字段)一律 known=false 且退出码 0 —— 档位判定侧据此走最严。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeSystemctlShow 安装假 systemctl: 记录每次 argv, 固定回显 lines, 恒以 exitCode 退出。
// 回显行按 KEY=VALUE 逐行给出(observability 侧只认这两个键)。
func fakeSystemctlShow(t *testing.T, lines []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) + "\n"
	for _, line := range lines {
		script += "printf '%s\\n' " + strconv.Quote(line) + "\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 systemctl 失败: %v", err)
	}
	orig := systemctlUserBinary
	systemctlUserBinary = path
	t.Cleanup(func() { systemctlUserBinary = orig })
	return capture
}

// parseObserve 解析 service-observe 的 stdout 为契约文档(非法 JSON 直接失败)。
func parseObserve(t *testing.T, out string) serviceObserveDoc {
	t.Helper()
	var doc serviceObserveDoc
	trimmed := strings.TrimSpace(out)
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		t.Fatalf("service-observe stdout 非法 JSON: %v (%q)", err, out)
	}
	// 恰一份文档: 单行紧凑 JSON, 没有第二条记录。
	if strings.Contains(trimmed, "\n") {
		t.Fatalf("service-observe stdout 应是单行 JSON: %q", out)
	}
	return doc
}

func TestServiceObserveShape(t *testing.T) {
	_, rc := harness(t)
	capture := fakeSystemctlShow(t, []string{"ActiveState=active", "UnitFileState=enabled"}, 0)

	code, out, errOut := rc("service-observe", "--unit", "demo.service")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0; stderr: %s", code, errOut)
	}
	want := serviceObserveDoc{
		Unit:          "demo.service",
		ActiveState:   "active",
		UnitFileState: "enabled",
		Enabled:       true,
		Running:       true,
		Known:         true,
	}
	if got := parseObserve(t, out); got != want {
		t.Fatalf("观测文档 = %+v, want %+v", got, want)
	}
	// argv 直发且只查两个属性(一次 systemctl 调用取齐 enabled/running 两维)。
	wantCalls := []string{"--user show demo.service --property=ActiveState,UnitFileState"}
	if got := captureCalls(t, capture); strings.Join(got, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("argv = %v, want %v", got, wantCalls)
	}
}

func TestServiceObserveUnknownUnit(t *testing.T) {
	_, rc := harness(t)
	// 单元不存在: systemctl 退出码非 0 且无输出。观测失败也是一种观测结果 → 退出码 0。
	fakeSystemctlShow(t, nil, 1)

	code, out, errOut := rc("service-observe", "--unit", "ghost.service")
	if code != exitOK {
		t.Fatalf("系统报错不应是用法/运行期错, 退出码 = %d; stderr: %s", code, errOut)
	}
	want := serviceObserveDoc{
		Unit:          "ghost.service",
		ActiveState:   "unknown",
		UnitFileState: "unknown",
		Enabled:       false,
		Running:       false,
		Known:         false,
	}
	if got := parseObserve(t, out); got != want {
		t.Fatalf("观测文档 = %+v, want %+v", got, want)
	}
	if strings.TrimSpace(errOut) == "" {
		t.Fatal("known=false 时应向 stderr 给出原因(人工可发现性)")
	}
}

func TestServiceObserveMissingPropertyIsUnknown(t *testing.T) {
	_, rc := harness(t)
	// 半份输出: 有 ActiveState 没有 UnitFileState ⇒ 属性不完整, 按查不到处理。
	fakeSystemctlShow(t, []string{"ActiveState=active"}, 0)

	code, out, _ := rc("service-observe", "--unit", "demo.service")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0", code)
	}
	got := parseObserve(t, out)
	if got.Known || got.Enabled || !got.Running {
		t.Fatalf("缺字段应 known=false/enabled=false, running 仍可按 active 推出: %+v", got)
	}
	if got.UnitFileState != "unknown" || got.ActiveState != "active" {
		t.Fatalf("已观测到的维度应保留, 缺失维度记 unknown: %+v", got)
	}
}

func TestServiceObserveEmptyUnitFileStateIsUnknown(t *testing.T) {
	_, rc := harness(t)
	// transient 单元的 UnitFileState 是空值: 键在但取不到值, 仍算查不到。
	fakeSystemctlShow(t, []string{"ActiveState=active", "UnitFileState="}, 0)

	code, out, _ := rc("service-observe", "--unit", "transient.service")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0", code)
	}
	if got := parseObserve(t, out); got.Known {
		t.Fatalf("UnitFileState 空值应 known=false: %+v", got)
	}
}

func TestServiceObserveUnitNameNormalized(t *testing.T) {
	_, rc := harness(t)
	capture := fakeSystemctlShow(t, []string{"ActiveState=inactive", "UnitFileState=disabled"}, 0)

	code, out, _ := rc("service-observe", "--unit", "demo")
	if code != exitOK {
		t.Fatalf("退出码 = %d, want 0", code)
	}
	if got := parseObserve(t, out); got.Unit != "demo.service" {
		t.Fatalf("unit 应补 .service 后缀: %q", got.Unit)
	}
	if got := captureCalls(t, capture); len(got) != 1 || !strings.Contains(got[0], "show demo.service") {
		t.Fatalf("argv 应使用归一化单元名: %v", got)
	}
}

func TestServiceObserveActiveStateEnum(t *testing.T) {
	// 中间态(activating/deactivating/…)不在契约枚举里 ⇒ active_state=unknown 且 running=false;
	// 但属性本身查到了, known 仍为 true。
	for _, tc := range []struct{ raw, want string }{
		{"active", "active"},
		{"inactive", "inactive"},
		{"failed", "failed"},
		{"activating", "unknown"},
		{"deactivating", "unknown"},
		{"reloading", "unknown"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			_, rc := harness(t)
			fakeSystemctlShow(t, []string{"ActiveState=" + tc.raw, "UnitFileState=enabled"}, 0)
			code, out, _ := rc("service-observe", "--unit", "demo.service")
			if code != exitOK {
				t.Fatalf("退出码 = %d, want 0", code)
			}
			got := parseObserve(t, out)
			if got.ActiveState != tc.want || !got.Known {
				t.Fatalf("active_state = %q, want %q (known=%v)", got.ActiveState, tc.want, got.Known)
			}
			if wantRunning := tc.raw == "active"; got.Running != wantRunning {
				t.Fatalf("running = %v, want %v", got.Running, wantRunning)
			}
		})
	}
}

func TestServiceObserveEnabledStates(t *testing.T) {
	// enabled 口径: 只有这四个 UnitFileState 视为「已启用」(static/indirect/generated
	// 无 [Install] 段或由别处生成, 但单元本就该在场, 不属于"未启用")。
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"enabled", true},
		{"enabled-runtime", true},
		{"static", true},
		{"indirect", true},
		{"generated", true},
		{"disabled", false},
		{"masked", false},
		{"linked", false},
		{"bad", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			_, rc := harness(t)
			fakeSystemctlShow(t, []string{"ActiveState=inactive", "UnitFileState=" + tc.state}, 0)
			code, out, _ := rc("service-observe", "--unit", "demo.service")
			if code != exitOK {
				t.Fatalf("退出码 = %d, want 0", code)
			}
			got := parseObserve(t, out)
			if !got.Known || got.Enabled != tc.want {
				t.Fatalf("UnitFileState=%s → enabled=%v, want %v (known=%v)", tc.state, got.Enabled, tc.want, got.Known)
			}
			if got.UnitFileState != tc.state {
				t.Fatalf("unit_file_state 应逐字回显, got %q", got.UnitFileState)
			}
		})
	}
}

func TestServiceObserveUsageErrors(t *testing.T) {
	_, rc := harness(t)
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"缺 --unit", []string{"service-observe"}},
		{"--unit 缺值", []string{"service-observe", "--unit"}},
		{"--unit 重复", []string{"service-observe", "--unit", "a.service", "--unit", "b.service"}},
		{"多余位置参数", []string{"service-observe", "--unit", "a.service", "extra"}},
		{"非法单元名", []string{"service-observe", "--unit", "../../etc/passwd"}},
		{"空单元名", []string{"service-observe", "--unit", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := rc(tc.argv...)
			if code != exitUsage {
				t.Fatalf("退出码 = %d, want 2; out=%s", code, out)
			}
			var e errDoc
			if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || e.Error == "" {
				t.Fatalf("用法错误应回 {\"error\":...}: %q", out)
			}
		})
	}
}

func TestServiceObserveProcessFailureExitsRuntime(t *testing.T) {
	_, rc := harness(t)
	// systemctl 进程根本起不来(二进制缺失)是部署故障, 不是该单元的观测结果 ⇒ exit 1。
	orig := systemctlUserBinary
	systemctlUserBinary = filepath.Join(t.TempDir(), "missing-systemctl")
	t.Cleanup(func() { systemctlUserBinary = orig })

	code, out, errOut := rc("service-observe", "--unit", "demo.service")
	if code != exitRuntime {
		t.Fatalf("进程级故障退出码 = %d, want 1; out=%s", code, out)
	}
	var e errDoc
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || e.Error == "" {
		t.Fatalf("进程级故障应回 {\"error\":...}: %q", out)
	}
	if strings.TrimSpace(errOut) == "" {
		t.Fatal("进程级故障应把原因写到 stderr")
	}
}

func TestServiceObserveStampsAudit(t *testing.T) {
	logPath, rc := harness(t)
	fakeSystemctlShow(t, []string{"ActiveState=inactive", "UnitFileState=enabled"}, 0)
	if code, out, _ := rc("service-observe", "--unit", "demo.service"); code != exitOK {
		t.Fatalf("退出码 = %d; out=%s", code, out)
	}
	recs := readAudit(t, logPath)
	var saw bool
	for _, r := range recs {
		if r.Tool == "daedalus_tx_service_observe" {
			saw = true
			if r.TxID != "" {
				t.Fatalf("service-observe 是事务外围观测, 不得带 tx_id: %+v", r)
			}
		}
	}
	if !saw {
		t.Fatal("缺少 daedalus_tx_service_observe 审计条目")
	}
	mustVerify(t, logPath)
}
