package main

// package_set_exec.go —— package.set 的进程与文件助手层。
//
// package_set.go 守"形状"与校验守卫; 本文件只做机械事: dnf/rpm argv 直发
// (含超时/退出码规范化)、dnf history 事务 ID 的解析与探测、以及 dnf history id
// 跨步骤传递 sidecar 的落盘。规范化与 service_set_exec.go 的 systemctlUser 同款
// 惯例: 退出码透传; 无法启动 → 126; 超时 → 124。命令一律 argv 逐参数直发,
// 绝不经过任何命令行解释器拼接。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/tx"
	"github.com/Daedalusys/daedalus-sdk/dirs"
)

// 进程助手的路径与预算常量: dnf 要做元数据同步, 比 systemctl 的 30s 慢, 给独立
// 60s; sidecar 文件权限与 tx journal 的私有性对齐。
const (
	dnfBinary      = "/usr/bin/dnf"
	rpmBinary      = "/usr/bin/rpm"
	dnfExecTimeout = 60 * time.Second
	sidecarMode    = 0o600
)

// dnfHistoryIDPattern 从 `dnf history` 表格行提取事务 ID(ID 列数字 + 竖线分隔)。
var dnfHistoryIDPattern = regexp.MustCompile(`\s+(\d+)\s+\|`)

// runNormalized 是 dnfExec/rpmQuery 共用的执行内核: argv 直发
// `binary <args...>` 并规范化为 tx.OpResult —— rc=子进程退出码(透传);
// 无法启动 → 126(shellpolicy 拒绝惯例); 超时 → 124(同款约定)。
// binary 提为形参是让"启动失败归一化"路径在装有 dnf 的机器上也能被单测锁定
// (移交条款: 包级常量不可临时覆写, 故内核做成可注入)。
func runNormalized(ctx context.Context, timeout time.Duration, binary string, args ...string) tx.OpResult {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, binary, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	res := tx.OpResult{Stdout: strings.TrimSpace(out.String()), Stderr: strings.TrimSpace(errBuf.String())}
	switch {
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		res.Returncode = 124
		res.Error = fmt.Sprintf("%s %s 超时(%s)", binary, strings.Join(args, " "), timeout)
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Returncode = ee.ExitCode()
		} else {
			res.Returncode = 126
			res.Error = fmt.Sprintf("%s 启动失败: %v", binary, err)
		}
	}
	return res
}

// dnfExec argv 直发 `dnf <args...>`(二进制路径锁死 dnfBinary 常量, 无运行时可变)。
func dnfExec(ctx context.Context, args ...string) tx.OpResult {
	return runNormalized(ctx, dnfExecTimeout, dnfBinary, args...)
}

// rpmQuery 查询包的安装现状: 先过 sanitizePackageName 名称门(唯一出口, 内含
// pkgquery.IsValidPackageName 字符集校验)——不合法直接返 error, 绝不 exec;
// 再取 `rpm -q` 的 VERSION-RELEASE。rpm 非零退出是"该包未装"的惯例信号 →
// (false, "", nil), 未装不是 error 态; 只有进程根本没跑成(启动失败/超时,
// Error 非空)才 fail-closed 上抛——"查不了"绝不谎报成"未装"。
func rpmQuery(ctx context.Context, name string) (bool, string, error) {
	if err := sanitizePackageName(name); err != nil {
		return false, "", err
	}
	res := runNormalized(ctx, dnfExecTimeout, rpmBinary, "-q", "--queryformat", "%{VERSION}-%{RELEASE}\n", name)
	if res.Error != "" {
		return false, "", fmt.Errorf("rpm 查询执行失败: %s", res.Error)
	}
	if res.Returncode != 0 {
		return false, "", nil
	}
	return true, res.Stdout, nil
}

// parseDnfHistoryLastEntry 从 `dnf history` 输出解析最近一次事务的
// (事务 ID, Command line 列): dnf 默认按时间正序输出, 末行即最新事务;
// 倒序扫描以跳过尾部空行/表头/分隔线噪音。Command line 按竖线切列取第二列
// (dnf 表格固定列序: ID | Command line | Date and time | Action(s) | Altered)。
// 纯函数, 不依赖真实 dnf——readDnfHistoryID 的无环境单测钉子。
func parseDnfHistoryLastEntry(output string) (int64, string, error) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := dnfHistoryIDPattern.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("dnf history 事务 ID 解析失败: %w", err)
		}
		cols := strings.Split(lines[i], "|")
		if len(cols) < 2 {
			return 0, "", fmt.Errorf("dnf history 事务行缺少 Command line 列: %s", strings.TrimSpace(lines[i]))
		}
		return id, strings.TrimSpace(cols[1]), nil
	}
	return 0, "", errors.New("dnf history 输出中无可解析的事务 ID 行")
}

// parseDnfHistoryLastID 是 parseDnfHistoryLastEntry 的纯 ID 投影
// (既有解析单测钉的旧形态, 生产路径消费的是带 Command line 的全 entry)。
func parseDnfHistoryLastID(output string) (int64, error) {
	id, _, err := parseDnfHistoryLastEntry(output)
	return id, err
}

// cmdlineMatches 判定 `dnf history` 末行记录的 Command line 是否属于本次事务:
// 按空白切 token, 要求首 token==verb 且其余 token 中存在一个==name。
// 不能用连续子串判定——Apply 实发 argv 是 [verb, "-y", name], dnf 记录的
// Command line 形如 "install -y htop", verb 与 name 之间隔着 -y;
// token 相等比较同时杜绝子串误伤(name=top 不会匹配 htop token)。
func cmdlineMatches(cmdline, verb, name string) bool {
	tokens := strings.Fields(cmdline)
	if len(tokens) == 0 || tokens[0] != verb {
		return false
	}
	for _, token := range tokens[1:] {
		if token == name {
			return true
		}
	}
	return false
}

// readDnfHistoryID 捕获当前最新 dnf 事务 ID(Apply 成功后的 sidecar 写入源)。
// dnf rc=0 到本捕获之间存在并发窗口: 若另一 dnf 事务
// (dnf-automatic timer 等)恰好提交, 末行就是他人事务, 回滚会 undo 错对象。
// 故校验末行 Command line 列的 verb 与 name 确属本次实发事务, 对不上即捕获
// 不可信、error 上抛(调用方 Apply 走 fail-closed)。进程调用次数不变——
// 相关性校验只在既有 `dnf history` 输出上多提取一列。
func readDnfHistoryID(ctx context.Context, wantVerb, wantName string) (int64, error) {
	res := dnfExecFn(ctx, "history")
	if res.Returncode != 0 || res.Error != "" {
		return 0, fmt.Errorf("dnf history 执行失败: %s", firstNonEmpty(res.Error, res.Stderr))
	}
	id, cmdline, err := parseDnfHistoryLastEntry(res.Stdout)
	if err != nil {
		return 0, err
	}
	if !cmdlineMatches(cmdline, wantVerb, wantName) {
		return 0, fmt.Errorf("dnf history 末行 Command line %q 与本次事务命令不符(期望 %s %s), 捕获不可信", cmdline, wantVerb, wantName)
	}
	return id, nil
}

// dnfHistoryExists 探测事务 ID 是否仍在 dnf history 中: `dnf history info <id>`
// rc==0 → 存在; rc==1 → 已被清理(Rollback 据此走 best-effort 分流);
// 其余(含 124/126 归一化)→ error, 探测不可靠时不给出存在性结论。
func dnfHistoryExists(ctx context.Context, id int64) (bool, error) {
	res := dnfExec(ctx, "history", "info", strconv.FormatInt(id, 10))
	switch res.Returncode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("dnf history info %d 异常退出: %s", id, firstNonEmpty(res.Error, res.Stderr))
}

// writeDnfHistorySidecar 把 dnf history id 以 `<txID>-<stepIndex>.dnf_history_id`
// 落盘(tx 根目录内), 供 Rollback 读取。
// dirs.TxRoot() 错误一律包装上抛, 绝不吞掉; 路径用
// filepath.Join 拼接; 写失败直接返 error 给调用方,
// 不做任何兜底——视写失败为 Apply 失败, 杜绝"成功但不可回滚"。
func writeDnfHistorySidecar(txID string, stepIndex int, dnfHistoryID int64) error {
	root, err := dirs.TxRoot()
	if err != nil {
		return fmt.Errorf("tx root 不可用: %w", err)
	}
	path := filepath.Join(root, fmt.Sprintf("%s-%d.dnf_history_id", txID, stepIndex))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(dnfHistoryID, 10)), sidecarMode)
}

// readDnfHistorySidecar 是 writeDnfHistorySidecar 的镜像消费侧(Rollback
// 寻址回滚材料): dirs.TxRoot() 错误同款包装上抛, filepath.Join 同款拼接
// 约定的命名与十进制裸 id 格式在此消费。sidecar 缺失、读失败、内容不可解析
// 一律返 error——不回退、不猜 id(fail-closed 哲学)。
// 本助手不设 mock 缝: 纯文件 IO 经 env 指临时目录即可真实测试, 更接近生产形态。
func readDnfHistorySidecar(txID string, stepIndex int) (int64, error) {
	root, err := dirs.TxRoot()
	if err != nil {
		return 0, fmt.Errorf("tx root 不可用: %w", err)
	}
	path := filepath.Join(root, fmt.Sprintf("%s-%d.dnf_history_id", txID, stepIndex))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("读取 sidecar 失败: %w", err)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sidecar 内容非十进制 dnf history id: %w", err)
	}
	return id, nil
}
