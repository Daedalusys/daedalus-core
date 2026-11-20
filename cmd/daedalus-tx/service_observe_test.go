package main

// service_observe_test.go —— service-observe 子命令的契约测试。
//
// 一律经包级注入缝 systemctlUserBinary 指向 t.TempDir() 下的 shell 夹具,
// 绝不触碰宿主真实 systemd。承重断言: stdout 恰一份 JSON 且恰六键、argv 只查两个
// 属性、观测不到(rc 非 0 / 缺字段)一律 known=false 且退出码 0 —— 档位判定侧据此走最严;
// 只读承诺: 不建事务、不写审计、不动单元文件。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

// parseObserve 解析 service-observe 的 stdout 为契约文档(非法 JSON 直接失败),
// 并断言键集合恰为六键(契约是"六个键", 多一个少一个都要红)。
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
	assertObserveKeys(t, trimmed)
	return doc
}

// assertObserveKeys 断言 stdout JSON 的键集合逐字等于六键契约(不多不少)。
func assertObserveKeys(t *testing.T, out string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("service-observe stdout 非法 JSON: %v (%q)", err, out)
	}
	want := []string{"active_state", "enabled", "known", "running", "unit", "unit_file_state"}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stdout 键集合 = %v, want 恰六键 %v", got, want)
	}
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

func TestServiceObserveUnitNameVerbatim(t *testing.T) {
	_, rc := harness(t)
	capture := fakeSystemctlShow(t, []string{"ActiveState=inactive", "UnitFileState=disabled"}, 0)

	// 合法的 .service 单元名原样回显: 观测侧绝不改名(含 @ 实例名)。
	for _, unit := range []string{"nginx.service", "foo@bar.service"} {
		t.Run(unit, func(t *testing.T) {
			code, out, errOut := rc("service-observe", "--unit", unit)
			if code != exitOK {
				t.Fatalf("退出码 = %d, want 0; stderr: %s", code, errOut)
			}
			if got := parseObserve(t, out); got.Unit != unit {
				t.Fatalf("unit 应逐字回显 %q, 实得 %q", unit, got.Unit)
			}
		})
	}
	wantCalls := []string{
		"--user show nginx.service --property=ActiveState,UnitFileState",
		"--user show foo@bar.service --property=ActiveState,UnitFileState",
	}
	if got := captureCalls(t, capture); strings.Join(got, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("argv = %v, want %v", got, wantCalls)
	}
}

func TestServiceObserveRejectsNonServiceUnit(t *testing.T) {
	_, rc := harness(t)
	capture := fakeSystemctlShow(t, []string{"ActiveState=active", "UnitFileState=enabled"}, 0)

	// 非 .service 单元显式拒绝(用法错 exit 2), 不静默改名成 foo.timer.service ——
	// 静默改名会让调用方以为观测 A、实际查 B。
	for _, unit := range []string{"foo.timer", "foo", "foo.socket", "foo.target", "foo.service.timer"} {
		t.Run(unit, func(t *testing.T) {
			code, out, _ := rc("service-observe", "--unit", unit)
			if code != exitUsage {
				t.Fatalf("非 .service 单元退出码 = %d, want 2; out=%s", code, out)
			}
			var e errDoc
			if json.Unmarshal([]byte(strings.TrimSpace(out)), &e) != nil || e.Error == "" {
				t.Fatalf("用法错误应回 {\"error\":...}: %q", out)
			}
			if !strings.Contains(e.Error, ".service") {
				t.Fatalf("错误串应点明只观测 .service 单元: %q", e.Error)
			}
		})
	}
	// 拒绝发生在拼 argv 之前: 假 systemctl 一次都没被调起。
	if got := captureCalls(t, capture); len(got) != 0 {
		t.Fatalf("非 .service 单元应在拼 argv 之前被拒, 实得调用 %v", got)
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
	// enabled 口径: 只有这五个 UnitFileState 视为「已启用」(static/indirect/generated
	// 无 [Install] 段或由别处生成, 但单元本就该在场, 不属于"未启用");
	// 其余 systemd 真实取值一律 false。
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
		{"linked-runtime", false},
		{"alias", false},
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
		{"非 .service 单元", []string{"service-observe", "--unit", "demo.timer"}},
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

// TestServiceObserveExitCodeContract 把 usage() 里写死的退出码三分契约逐条钉住:
// 单元级失败 exit 0 + known=false; systemctl 进程级故障 exit 1; 用法错 exit 2。
// (超时与"起不来"同走 res.Error 分支, 故与"起不来"共用同一断言面。)
func TestServiceObserveExitCodeContract(t *testing.T) {
	t.Run("单元级失败 ⇒ 0 + known=false", func(t *testing.T) {
		_, rc := harness(t)
		fakeSystemctlShow(t, nil, 1)
		code, out, _ := rc("service-observe", "--unit", "ghost.service")
		if code != exitOK {
			t.Fatalf("单元级失败退出码 = %d, want 0", code)
		}
		if got := parseObserve(t, out); got.Known {
			t.Fatalf("单元级失败应 known=false: %+v", got)
		}
	})
	t.Run("进程级故障 ⇒ 1", func(t *testing.T) {
		_, rc := harness(t)
		orig := systemctlUserBinary
		systemctlUserBinary = filepath.Join(t.TempDir(), "missing-systemctl")
		t.Cleanup(func() { systemctlUserBinary = orig })
		if code, _, _ := rc("service-observe", "--unit", "demo.service"); code != exitRuntime {
			t.Fatalf("进程级故障退出码 = %d, want 1", code)
		}
	})
	t.Run("用法错 ⇒ 2", func(t *testing.T) {
		_, rc := harness(t)
		if code, _, _ := rc("service-observe"); code != exitUsage {
			t.Fatalf("缺 --unit 退出码 = %d, want 2", code)
		}
	})
}

// TestServiceObserveIsReadOnly 钉住只读承诺: 一次观测不建事务、不写审计、不动单元文件。
func TestServiceObserveIsReadOnly(t *testing.T) {
	logPath, rc := harness(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeSystemctlShow(t, []string{"ActiveState=active", "UnitFileState=enabled"}, 0)

	if code, out, errOut := rc("service-observe", "--unit", "demo.service"); code != exitOK {
		t.Fatalf("退出码 = %d, want 0; out=%s; stderr: %s", code, out, errOut)
	}
	// 不写审计(证据层归消费侧): 审计日志文件根本不该被创建。
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("service-observe 不得写审计日志: stat err=%v", err)
	}
	// 不建事务: DAEDALUS_TX_DIR 目录从未被创建。
	if _, err := os.Stat(os.Getenv("DAEDALUS_TX_DIR")); !os.IsNotExist(err) {
		t.Fatalf("service-observe 不得创建事务目录: stat err=%v", err)
	}
	// 不触碰用户单元目录: $HOME 下不应新增任何文件。
	err := filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != home {
			t.Errorf("观测不应在 $HOME 下新增文件: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 $HOME 失败: %v", err)
	}
}
