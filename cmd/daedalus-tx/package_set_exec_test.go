package main

// package_set_exec_test.go —— 进程助手的最小无-dnf 测试子集(plan Scope Must-have #10 第一批)。
//
// 原则: 纯函数(sidecar 落盘 / history 解析)直接钉; 进程路径只钉可注入的规范化
// 内核 runNormalized 与"名称门不 exec"边界——单测不真启 dnf/rpm(慢且依赖机器
// 状态, 端到端断言归 v3 构建机 F 阶段)。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// TestWriteDnfHistorySidecar_Happy: Given tx 根目录经 env 指向临时目录,
// When 写入 (txID, stepIndex, historyID) 三元组, Then 落盘路径为
// `<txID>-<stepIndex>.dnf_history_id`、内容为十进制 ID、权限位 == 0o600。
func TestWriteDnfHistorySidecar_Happy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAEDALUS_TX_DIR", root)

	const (
		txID  = "0123456789abcdef"
		step  = 3
		hisID = int64(42)
	)
	if err := writeDnfHistorySidecar(txID, step, hisID); err != nil {
		t.Fatalf("writeDnfHistorySidecar 返回错误: %v", err)
	}

	path := filepath.Join(root, "0123456789abcdef-3.dnf_history_id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("sidecar 文件应存在: %v", err)
	}
	if got, want := string(data), strconv.FormatInt(hisID, 10); got != want {
		t.Errorf("sidecar 内容 = %q, 期望 %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		// 断言字面量而非 sidecarMode 常量: 防注入(改常量)时两侧同漂、测试恒绿。
		t.Errorf("sidecar 权限位 = %o, 期望 0o600", perm)
	}
}

// TestWriteDnfHistorySidecar_TxRootErrorPropagates: 守门——
// dirs.TxRoot 报错(env 给了相对路径, fail-closed 拒用且不回落)时,
// 错误必须传播给调用方, 绝不静默兜底写别处。
func TestWriteDnfHistorySidecar_TxRootErrorPropagates(t *testing.T) {
	t.Setenv("DAEDALUS_TX_DIR", "relative/not/allowed")

	if err := writeDnfHistorySidecar("0123456789abcdef", 0, 1); err == nil {
		t.Fatal("dirs.TxRoot 错误必须传播, 实际返回 nil(静默兜底)")
	} else if !strings.Contains(err.Error(), "tx root 不可用") {
		t.Errorf("错误应包装为 tx root 不可用形态, 实际: %v", err)
	}
}

// TestDnfExec_StartFailure_Normalized: dnfExec 的规范化内核在二进制无法启动时
// 归一化为 rc=126 + Error 说明(与 systemctlUser 同款), 不 panic。
// 本机装有真实 dnf 且 dnfBinary 是不可覆写的包级常量, 故经 runNormalized 的
// binary 形参注入一个不存在的路径来钉这条失败路径。
func TestDnfExec_StartFailure_Normalized(t *testing.T) {
	res := runNormalized(context.Background(), dnfExecTimeout, "/nonexistent/daedalus-missing-binary", "history")
	if res.Returncode != 126 {
		t.Errorf("启动失败 rc = %d, 期望归一化 126", res.Returncode)
	}
	if res.Error == "" {
		t.Error("启动失败必须携带 Error 说明")
	}
}

// TestRunNormalized_ExitCodePassthrough: 内核透传子进程退出码(0 与非 0),
// 正常执行不置 Error。
func TestRunNormalized_ExitCodePassthrough(t *testing.T) {
	if res := runNormalized(context.Background(), dnfExecTimeout, "/usr/bin/true"); res.Returncode != 0 || res.Error != "" {
		t.Errorf("true 应 rc=0 无 Error, 实际 %+v", res)
	}
	if res := runNormalized(context.Background(), dnfExecTimeout, "/usr/bin/false"); res.Returncode != 1 || res.Error != "" {
		t.Errorf("false 应 rc=1 透传且无 Error, 实际 %+v", res)
	}
}

// TestRpmQuery_RejectsBadName: 名称门在 exec 之前拒非法名(含注入分隔符),
// 返回零值 + error——非法名永远轮不到 rpm 进程被启动。
func TestRpmQuery_RejectsBadName(t *testing.T) {
	installed, version, err := rpmQuery(context.Background(), "htop; rm -rf /")
	if err == nil {
		t.Fatal("非法包名必须被名称门拒绝")
	}
	if !strings.Contains(err.Error(), "Invalid package name or pattern") {
		t.Errorf("错误应为名称门统一形态, 实际: %v", err)
	}
	if installed || version != "" {
		t.Errorf("名称门拒绝时应返回零值, 实际 installed=%v version=%q", installed, version)
	}
}

// TestParseDnfHistoryLastID: 纯解析器对真实 `dnf history` 尾部形态的行为——
// 取最后一条事务行的 ID; 空输出/表头噪声/无可解析行一律 error。
func TestParseDnfHistoryLastID(t *testing.T) {
	const header = "    ID  | Command line             | Date and time    | Action(s)      | Altered\n" +
		"-------------------------------------------------------------------------------\n"
	const rows = "     1 | remove vim               | 2026-09-10 08:00 | Removed        |    1\n" +
		"     4 | install htop             | 2026-09-12 09:30 | Install        |    1\n" +
		"     5 | upgrade kernel           | 2026-09-15 10:15 | Updated        |    2"

	cases := []struct {
		name    string
		output  string
		wantID  int64
		wantErr bool
	}{
		{"最后事务行", header + rows, 5, false},
		{"尾部空行仍取最后事务", header + rows + "\n\n", 5, false},
		{"单行事务", "    37 | install nginx | 2026-09-15 08:00 | Install | 2", 37, false},
		{"空输出", "", 0, true},
		{"仅表头与分隔线", strings.TrimSuffix(header, "\n"), 0, true},
		{"无关文本", "No matches found", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseDnfHistoryLastID(tc.output)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望 error, 实际返回 id=%d", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if id != tc.wantID {
				t.Errorf("id = %d, 期望 %d", id, tc.wantID)
			}
		})
	}
}

//
// 真 readDnfHistoryID 的无-dnf 单测: dnfExecFn 本就是注入缝, stub 它回造
// `dnf history` 表格输出, 钉末行 Command line 相关性校验的三种落点
// (匹配捕获 / 不符 fail-closed / upgrade 动词)。真 dnf 输出形态的端到端
// 校验留 v3 构建机闭环, 此处只钉解析与判定的形状契约。

// stubDnfHistoryTable 覆写 dnfExecFn 缝, 固定回造给定 `dnf history` 表格输出;
// 顺带钉 readDnfHistoryID 只应实发 history 子命令 argv(既有契约零改动)。
func stubDnfHistoryTable(t *testing.T, table string) {
	t.Helper()
	orig := dnfExecFn
	t.Cleanup(func() { dnfExecFn = orig })
	dnfExecFn = func(_ context.Context, args ...string) tx.OpResult {
		if len(args) != 1 || args[0] != "history" {
			t.Errorf("捕获只应实发 [history], got argv=%q", args)
		}
		return tx.OpResult{Stdout: table}
	}
}

// dnfHistoryTableWith 回造"表头 + 分隔线 + 一条 wget 历史事务 + 指定末行"的
// 两行表格(末行即最新事务, 是各测试的相关性判定对象)。
func dnfHistoryTableWith(lastCmdline string, lastID int) string {
	return fmt.Sprintf(
		"    ID  | Command line             | Date and time    | Action(s)      | Altered\n"+
			"-------------------------------------------------------------------------------\n"+
			"     3 | install -y wget              | 2026-09-10 08:00 | Install        |    1\n"+
			"    %2d | %-26s | 2026-09-15 10:15 | Install        |    1\n",
		lastID, lastCmdline)
}

// TestReadDnfHistoryID_CaptureOnMatchedInstall: Given 末行 Command line 为
// Apply 实发形态 "install -y htop", When readDnfHistoryID(install, htop),
// Then 返回末行 id 42(-y 中间 token 不得破坏 verb/name 相关性判定)。
func TestReadDnfHistoryID_CaptureOnMatchedInstall(t *testing.T) {
	stubDnfHistoryTable(t, dnfHistoryTableWith("install -y htop", 42))

	id, err := readDnfHistoryID(context.Background(), "install", "htop")
	if err != nil {
		t.Fatalf("末行与本次事务相符必须捕获成功, got err=%v", err)
	}
	if id != 42 {
		t.Errorf("捕获 id = %d, want 42", id)
	}
}

// TestReadDnfHistoryID_FailClosedOnMismatch: Given 并发窗口——倒数第二行才是
// 本次 install htop, 末行是他人事务提交的 "remove -y vim";
// When readDnfHistoryID(install, htop), Then error 含"与本次事务命令不符"语义、
// id 为 0——绝不返回倒数第二行或末行的 id(fail-closed, 交由 Apply 判失败)。
func TestReadDnfHistoryID_FailClosedOnMismatch(t *testing.T) {
	const table = "    ID  | Command line             | Date and time    | Action(s)      | Altered\n" +
		"-------------------------------------------------------------------------------\n" +
		"    42 | install -y htop              | 2026-09-15 10:00 | Install        |    1\n" +
		"    43 | remove -y vim                | 2026-09-15 10:01 | Removed        |    1\n"
	stubDnfHistoryTable(t, table)

	id, err := readDnfHistoryID(context.Background(), "install", "htop")
	if err == nil {
		t.Fatalf("末行属他人事务必须判捕获不可信, got id=%d err=nil", id)
	}
	if !strings.Contains(err.Error(), "与本次事务命令不符") {
		t.Errorf("错误串必须含不符语义, got %q", err.Error())
	}
	if id != 0 {
		t.Errorf("不符时必须返零值 id, got %d", id)
	}
}

// TestReadDnfHistoryID_CaptureOnMatchedUpgrade: Given 末行 "upgrade -y kernel-core",
// When readDnfHistoryID(upgrade, kernel-core), Then 捕获末行 id——
// 钉相关性判定对三个动词同样成立(名称含连字符不影响 token 相等比较)。
func TestReadDnfHistoryID_CaptureOnMatchedUpgrade(t *testing.T) {
	stubDnfHistoryTable(t, dnfHistoryTableWith("upgrade -y kernel-core", 7))

	id, err := readDnfHistoryID(context.Background(), "upgrade", "kernel-core")
	if err != nil {
		t.Fatalf("upgrade 相关性匹配必须捕获成功, got err=%v", err)
	}
	if id != 7 {
		t.Errorf("捕获 id = %d, want 7", id)
	}
}

// TestCmdlineMatches_TokenBoundaries: 纯函数表驱动——首 token 必须逐字等于
// verb(前缀/子串不算), name 必须是独立 token(top 不得匹配 htop), 容忍
// -y 等中间 token, 空 Command line 恒 false。
func TestCmdlineMatches_TokenBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		verb    string
		pkg     string
		want    bool
	}{
		{"实发形态含-y", "install -y htop", "install", "htop", true},
		{"无-y 形态仍匹配", "install htop", "install", "htop", true},
		{"verb 不符拒", "remove -y vim", "install", "vim", false},
		{"verb 前缀不误伤", "reinstall -y htop", "install", "htop", false},
		{"name 子串不误伤: top 不匹配 htop token", "install -y htop", "install", "top", false},
		{"name 缺失拒", "install -y", "install", "htop", false},
		{"仅 verb 拒", "install", "install", "htop", false},
		{"空 Command line 拒", "", "install", "htop", false},
		{"verb 出现在非首位不算", "history undo -y install", "install", "htop", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmdlineMatches(tc.cmdline, tc.verb, tc.pkg); got != tc.want {
				t.Errorf("cmdlineMatches(%q, %q, %q) = %v, want %v", tc.cmdline, tc.verb, tc.pkg, got, tc.want)
			}
		})
	}
}
