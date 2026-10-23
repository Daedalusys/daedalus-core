package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRescue_NotOnHeadlessVariant: 在非 headless 镜像上跑 rescue 应非零退出 +
// stderr 提到 "headless"。CI / 开发机 / 大多数主机都不带 VARIANT=headless 标记,
// 这一断言保证救援子命令被限定在 daedalus-atomic headless 变体内。
//
// 这里调 cmdRescue 时传 ["status"] 子子命令:空 args 会被 usage 拦截(stderr
// 不含 "headless"),非空 args 才能走到 IsHeadless 闸门,stderr 报
// "rescue: only on daedalus-atomic headless variant" → 满足断言。
func TestRescue_NotOnHeadlessVariant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdRescue(&stdout, &stderr, []string{"status"})
	if code == 0 {
		t.Fatalf("expected non-zero on non-headless host, got 0; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "headless") {
		t.Fatalf("expected stderr to mention headless; got %s", stderr.String())
	}
}

// TestRescue_Status_Structure: 文档化 status 输出所需的最小数据结构契约。
// detectRescueState 必须至少能取到 XrdpUnitState 字符串 — 即便在非 headless
// 主机(systemctl 不可达 / xrdp 未装)也要返回非空("unknown" 兜底),这是
// status 输出格式的稳定前提。
func TestRescue_Status_Structure(t *testing.T) {
	s := detectRescueState()
	if s.XrdpUnitState == "" {
		t.Fatalf("expected XrdpUnitState non-empty, got %+v", s)
	}
}

// ============================================================================
// 2026-10-22 真机回归:三个缺陷的表驱动 + 流程测试
//
// 触发场景:在真的 daedalus-atomic headless 镜像里跑 rescue 三子命令。headless
// 镜像按 files/scripts/77-xrdp.sh 的设计**不带 xrdp**,而当时 rescue 实现全程
// 假设 xrdp 已在。三条命令的真机输出见各测试注释。
// ============================================================================

// stubExec 把 execOut 换成记录调用的假实现,返回给调用方的 out/err 由
// fn 决定。t.Cleanup 自动还原,避免污染其他测试。
func stubExec(t *testing.T, fn func(name string, args ...string) (string, error)) *[]string {
	t.Helper()
	orig := execOut
	var calls []string
	execOut = func(name string, args ...string) (string, error) {
		calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		return fn(name, args...)
	}
	t.Cleanup(func() { execOut = orig })
	return &calls
}

// stubPaths 把三个会被写/读的文件路径指向临时目录。生产值是字面量,这里换成
// t.TempDir() 以免测试真去动 /etc/xrdp 与 /etc/yum.repos.d。
func stubPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origStartwm, origSesman, origCopr := startwmPath, sesmanIniPath, coprRepoPath
	startwmPath = filepath.Join(dir, "startwm.sh")
	sesmanIniPath = filepath.Join(dir, "sesman.ini")
	coprRepoPath = filepath.Join(dir, "_daedalus-xrdp.repo")
	t.Cleanup(func() {
		startwmPath, sesmanIniPath, coprRepoPath = origStartwm, origSesman, origCopr
	})
	return dir
}

// TestMapXrdpUnitState:状态映射表驱动。
//
// 回归点是 not-found 一行:真机 `systemctl show xrdp` 在 unit 缺失时**仍返回 0**
// 并输出 LoadState=not-found / ActiveState=inactive / SubState=dead。原代码把
// not-found 映射成 "disabled",`rescue status` 于是报 `xrdp: disabled`,把排障
// 方向带偏成“去 enable 一下”—— 实际是没装。
func TestMapXrdpUnitState(t *testing.T) {
	cases := []struct {
		name           string
		load, sub, act string
		want           string
		wantOK         bool
	}{
		{"unit 不存在(headless 默认)", "not-found", "dead", "inactive", "not-installed", true},
		{"被 mask(装过但禁)", "masked", "dead", "inactive", "masked", true},
		{"已 enable 且在跑", "loaded", "running", "active", "running", true},
		{"已 enable 未跑", "loaded", "dead", "active", "enabled", true},
		{"其它 load 值原样透传", "loaded", "dead", "failed", "loaded", true},
		{"全空 → unknown", "", "", "", "unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapXrdpUnitState(tc.load, tc.sub, tc.act)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("mapXrdpUnitState(%q,%q,%q) = (%q,%v), want (%q,%v)",
					tc.load, tc.sub, tc.act, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestUnitMissing:systemctl “unit 不存在”文案判定。只认三种高频措辞,
// 拿不准一律返回 false(宁可多报错,不可吃掉真错误)。
func TestUnitMissing(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"Failed to disable unit: Unit xrdp.service does not exist", true},
		{"Unit xrdp.service could not be found.", true},
		{"xrdp.service not loaded.", true},
		{"Failed to start xrdp.service: Unit xrdp.service is masked.", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := unitMissing(tc.out); got != tc.want {
			t.Errorf("unitMissing(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}

// TestRescue_DisableVnc_IdempotentWhenNotInstalled:回归 —— 真机上在一台
// 从未开过 VNC 的机器跑 `daedalus-host rescue disable-vnc` 得到:
//	Failed to disable unit: Unit xrdp.service does not exist
//	rescue: systemctl disable --now 失败: exit status 1
//	EXIT=1
// 但“本就没开”正是 disable 想达到的状态,报非零是自相矛盾(且会让自动化
// 脚本误判)。这里钉:unit 缺失 → 仍 exitOK,且 stderr 给出说明。
func TestRescue_DisableVnc_IdempotentWhenNotInstalled(t *testing.T) {
	stubPaths(t)
	stubExec(t, func(name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
			return "Failed to disable unit: Unit xrdp.service does not exist",
				errors.New("exit status 1")
		}
		return "", nil // mask / firewall 一律放行
	})

	var stdout, stderr bytes.Buffer
	if code := rescueDisableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "not-installed"}); code != 0 {
		t.Fatalf("disable-vnc on a machine where VNC was never enabled: exit %d, want 0; stderr=%s",
			code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "本就未启用") {
		t.Errorf("stderr 应说明为何跳过 disable, got %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "xrdp: masked") {
		t.Errorf("stdout 应报最终状态 masked, got %s", stdout.String())
	}
}

// TestRescue_DisableVnc_RealFailureStillFails:幂等不能吃掉真错误 ——
// 报的是 masked 而非 does not exist 时,必须照旧非零退出。
func TestRescue_DisableVnc_RealFailureStillFails(t *testing.T) {
	stubPaths(t)
	stubExec(t, func(name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
			return "Failed to start xrdp.service: Job failed.", errors.New("exit status 1")
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	if code := rescueDisableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "running"}); code == 0 {
		t.Fatalf("disable-vnc 真失败时应非零退出,got 0; stdout=%s", stdout.String())
	}
}

// TestRescue_EnableVnc_InstallsXrdpWhenNotInstalled:回归 —— 真机上在 headless
// 镜像跑 `daedalus-host rescue enable-vnc` 得到:
//	rescue: 写 startwm.sh 失败: open /etc/xrdp/startwm.sh: no such file or directory
//	EXIT=1
// 根因:/etc/xrdp 目录由 xrdp 包创建,而 headless 镜像不装 xrdp。
// 这里钉:状态为 not-installed 时,dnf 装包必须排在写 startwm.sh **之前**,
// 且走 EPEL → COPR 的既有顺序;最终 exitOK 且 startwm.sh 真落盘。
func TestRescue_EnableVnc_InstallsXrdpWhenNotInstalled(t *testing.T) {
	dir := stubPaths(t)
	// EL10 常态:EPEL 装不上 xrdp → 代码必须写 COPR repo 后再试一次,第二次成功。
	var xrdpInstalls int
	calls := stubExec(t, func(name string, args ...string) (string, error) {
		if name == "dnf" && len(args) >= 3 && args[0] == "install" &&
			args[len(args)-1] == "xrdp" && !slices.Contains(args, "epel-release") {
			xrdpInstalls++
			if xrdpInstalls == 1 {
				return "No match for argument: xrdp", errors.New("exit status 1")
			}
			return "", nil // COPR repo 写好后装上
		}
		return "", nil
	})

	// sesman.ini 先摆好,让 hardenSesmanDepth 有活干
	sesman := "[Globals]\nini_version=1\n\n[Xvnc]\nparam=Xvnc\nport=5900\n"
	if err := os.WriteFile(sesmanIniPath, []byte(sesman), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := rescueEnableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "not-installed"})
	if code != 0 {
		t.Fatalf("enable-vnc 应成功,exit %d; stderr=%s", code, stderr.String())
	}

	// startwm.sh 落盘(内容是 xterm stub)
	got, err := os.ReadFile(filepath.Join(dir, "startwm.sh"))
	if err != nil {
		t.Fatalf("startwm.sh 未落盘: %v", err)
	}
	if !strings.Contains(string(got), "exec xterm") {
		t.Errorf("startwm.sh 应写 xterm 兜底 stub,got %q", string(got))
	}

	// COPR repo 文件写出,且 $releasever 保持字面量交给 dnf 展开
	repo, err := os.ReadFile(coprRepoPath)
	if err != nil {
		t.Fatalf("COPR repo 文件未写出: %v", err)
	}
	if !strings.Contains(string(repo), "rhel+epel-$releasever-x86_64") {
		t.Errorf("COPR baseurl 必须保留字面量 $releasever 交 dnf 展开,got %q", string(repo))
	}

	// 顺序:第一次 dnf install 必须早于 startwm 写入,而 startwm 写入早于 unmask
	assertOrder(t, *calls,
		"dnf install -y epel-release",
		"systemctl unmask xrdp xrdp-sesman",
	)
}

// TestRescue_EnableVnc_SkipsInstallWhenPresent:xrdp 已装(非 headless 或已
// rescue 过一次)时不得再动 dnf —— 避免无谓出网。
func TestRescue_EnableVnc_SkipsInstallWhenPresent(t *testing.T) {
	stubPaths(t)
	// 必须把二进制也摆上:判据是文件存在性,不是 unit 状态(见
	// TestRescue_EnableVnc_InstallsWhenMaskedButAbsent 的反面)。
	stubXrdpBinary(t, true)
	calls := stubExec(t, func(name string, args ...string) (string, error) { return "", nil })

	var stdout, stderr bytes.Buffer
	if code := rescueEnableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "masked"}); code != 0 {
		t.Fatalf("enable-vnc 应成功,exit %d; stderr=%s", code, stderr.String())
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "dnf ") {
			t.Errorf("xrdp 已装时不应跑 dnf,却跑了: %q", c)
		}
	}
}

// TestRescue_EnableVnc_InstallFailureFallsBackToConsoleHint:装不上(典型场景:
// 机器正因为断网而砖)时必须非零退出,并在 stderr 指明串口/sshd 两条不依赖
// xrdp 的备选路径,而不是留一句天书。
func TestRescue_EnableVnc_InstallFailureFallsBackToConsoleHint(t *testing.T) {
	stubPaths(t)
	stubExec(t, func(name string, args ...string) (string, error) {
		if name == "dnf" {
			return "Could not resolve host: download.fedorainfracloud.org",
				errors.New("exit status 1")
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := rescueEnableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "not-installed"})
	if code == 0 {
		t.Fatalf("dnf 全失败时 enable-vnc 应非零退出,got 0")
	}
	if !strings.Contains(stderr.String(), "串口") || !strings.Contains(stderr.String(), "sshd") {
		t.Errorf("装包失败应提示串口/sshd 备选路径,stderr=%s", stderr.String())
	}
}

// TestRescueStatus_HintsWhenNotInstalled:status 报 not-installed 时补指引行。
// 前两行结构固定不变(既有契约),第三行是纯增量提示。
func TestRescueStatus_HintsWhenNotInstalled(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := rescueStatus(&stdout, &stderr, rescueState{XrdpUnitState: "not-installed"}); code != 0 {
		t.Fatalf("status 应 exit 0,got %d", code)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("status 至少两行,got %d: %q", len(lines), stdout.String())
	}
	if lines[0] != "xrdp: not-installed" {
		t.Errorf("首行 = %q, want %q", lines[0], "xrdp: not-installed")
	}
	if lines[1] != "3389/tcp: closed" {
		t.Errorf("第二行 = %q, want %q", lines[1], "3389/tcp: closed")
	}
	if !strings.Contains(stdout.String(), "enable-vnc") {
		t.Errorf("not-installed 时应提示 enable-vnc,got %q", stdout.String())
	}
}

// TestHardenSesmanDepth:在 [Xvnc] 段后插 depth=24(TigerVNC 1.15 不支持 32bpp,
// 不改会 VNC 黑屏),且已存在时保持幂等。
func TestHardenSesmanDepth(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantHas  bool
		wantOnce int
	}{
		{"缺 depth 时插入", "[Globals]\nini_version=1\n\n[Xvnc]\nparam=Xvnc\nport=5900\n", true, 1},
		{"已有 depth 时不动", "[Xvnc]\nparam=Xvnc\nparam=-depth\nparam=24\n", true, 1},
		{"无 [Xvnc] 段时报错不改", "[Globals]\nini_version=1\n", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubPaths(t)
			if err := os.WriteFile(sesmanIniPath, []byte(tc.in), 0o644); err != nil {
				t.Fatal(err)
			}
			err := hardenSesmanDepth(nil)
			if tc.wantHas && err != nil {
				t.Fatalf("应成功,got %v", err)
			}
			if !tc.wantHas && err == nil {
				t.Fatalf("应报错,got nil")
			}
			out, _ := os.ReadFile(filepath.Join(dir, "sesman.ini"))
			n := strings.Count(string(out), "param=-depth")
			if n != tc.wantOnce {
				t.Errorf("param=-depth 出现 %d 次, want %d; 内容=%q", n, tc.wantOnce, string(out))
			}
			if tc.wantOnce == 1 && !strings.Contains(string(out), "param=24") {
				t.Errorf("应同时写入 param=24,内容=%q", string(out))
			}
		})
	}
}

// assertOrder:断言 needle 按给定先后顺序出现在 calls 里(不强求相邻)。
func assertOrder(t *testing.T, calls []string, needles ...string) {
	t.Helper()
	pos := -1
	for _, n := range needles {
		found := -1
		for i, c := range calls {
			if strings.HasPrefix(c, n) && i > pos {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("调用序列里找不到 %q;实际序列=%v", n, calls)
		}
		pos = found
	}
}

// TestRescue_EnableVnc_InstallsWhenMaskedButAbsent:真机第二轮回归。
//
// 场景:先跑 disable-vnc(它会 `systemctl mask xrdp`,哪怕包根本没装 —— mask 的
// 语义就是“不论 unit 存不存在一律拦”),再跑 enable-vnc。
//
// 踩过的坑:第一版修复用 state.XrdpUnitState == "not-installed" 判断要不要装包。
// 但 disable-vnc 之后 `systemctl show xrdp` 报的是 **masked** 不是 not-found,
// 于是 enable-vnc 认为“装过了,跳过安装”,紧接着写 /etc/xrdp/startwm.sh 仍然
// ENOENT。真机输出:
//	rescue: WARN sesman.ini depth 未调整: open /etc/xrdp/sesman.ini: no such file or directory
//	rescue: 写 startwm.sh 失败: open /etc/xrdp/startwm.sh: no such file or directory
//	EXIT=1
//
// 这里钉:装没装只看包落下的文件,unit 状态一律不作数。
func TestRescue_EnableVnc_InstallsWhenMaskedButAbsent(t *testing.T) {
	stubPaths(t)
	stubXrdpBinary(t, false) // 包不在,尽管 unit 状态是 masked

	var xrdpInstalls int
	stubExec(t, func(name string, args ...string) (string, error) {
		if name == "dnf" && len(args) >= 3 && args[0] == "install" &&
			args[len(args)-1] == "xrdp" && !slices.Contains(args, "epel-release") {
			xrdpInstalls++
			if xrdpInstalls == 1 {
				return "No match for argument: xrdp", errors.New("exit status 1")
			}
			return "", nil
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := rescueEnableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "masked"})
	if code != 0 {
		t.Fatalf("unit 报 masked 但包不存在时也应装包并成功,exit %d; stderr=%s", code, stderr.String())
	}
	if xrdpInstalls < 1 {
		t.Errorf("应至少尝试一次 dnf install xrdp,实际 %d 次", xrdpInstalls)
	}
	if _, err := os.Stat(startwmPath); err != nil {
		t.Errorf("startwm.sh 应落盘: %v", err)
	}
}

// TestRescue_EnableVnc_SkipsInstallWhenBinaryPresent:二进制在就不该再动 dnf。
// 这条与上面那条成对,把“装没装”的判据钉死在文件存在性上。
func TestRescue_EnableVnc_SkipsInstallWhenBinaryPresent(t *testing.T) {
	stubPaths(t)
	stubXrdpBinary(t, true)
	calls := stubExec(t, func(name string, args ...string) (string, error) { return "", nil })

	var stdout, stderr bytes.Buffer
	if code := rescueEnableVnc(&stdout, &stderr, rescueState{XrdpUnitState: "not-installed"}); code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "dnf install") && slices.Contains(strings.Fields(c), "xrdp") {
			t.Errorf("二进制已存在时不应装包,却跑了: %q", c)
		}
	}
}

// stubXrdpBinary:把 xrdpBinPath 指向 temp 下的文件,按 present 决定建不建。
func stubXrdpBinary(t *testing.T, present bool) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "xrdp")
	if present {
		if err := os.WriteFile(p, []byte("#!/bin/false\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := xrdpBinPath
	xrdpBinPath = p
	t.Cleanup(func() { xrdpBinPath = orig })
}
