package main

// service_set_exec.go —— service.set 的进程与文件助手层(todo 22)。
//
// service_set.go 负责三段生命周期的编排与校验; 本文件只做两件机械事:
// systemctl argv 直发(含超时/退出码规范化)与单元文件的逐字快照/恢复。
// 守卫与解析在 unitguard.go —— 三个文件各守一摊, 互不越界。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/daedalus-os/daedalus/core/internal/tx"
	"os"
	"os/exec"
	"strings"
)

// readUnitFileSnapshot 逐字读单元文件: 不存在 → (nil, nil)(快照键缺席); 其余读错上抛。
func readUnitFileSnapshot(path string) (*string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取单元文件失败: %w", err)
	}
	s := string(data)
	return &s, nil
}

// restoreUnitFile 恢复内核: 当前内容 == 快照 → 不动并回 false;
// 不同或缺失 → 逐字写回并回 true(已存在文件的权限位由 WriteFile 保留)。
func restoreUnitFile(path, want string) (bool, error) {
	cur, err := os.ReadFile(path)
	switch {
	case err == nil && string(cur) == want:
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("读取单元文件失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("恢复单元文件写入失败: %w", err)
	}
	return true, nil
}

// observeActiveState (b) 条款: argv 直发 `systemctl --user show <unit> --property=ActiveState`
// 并解析 KEY=VALUE; 执行失败或输出缺键 → 错误(Propose 据此 exit 1)。
func observeActiveState(ctx context.Context, unit string) (string, error) {
	res := systemctlUser(ctx, "show", unit, "--property=ActiveState")
	if res.Returncode != 0 || res.Error != "" {
		return "", fmt.Errorf("ActiveState 观测失败: %s", firstNonEmpty(res.Error, res.Stderr))
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ActiveState="); ok {
			return v, nil
		}
	}
	return "", errors.New("ActiveState 观测失败: systemctl 输出缺 ActiveState 字段")
}

// systemctlUser argv 直发执行 `systemctl --user <args...>`(绝不经过 sh -c)。
// 规范化为 tx.OpResult: rc=子进程退出码; 无法启动 → 126(shellpolicy 拒绝惯例);
// 超时 → 124(同款约定)。
func systemctlUser(ctx context.Context, subArgs ...string) tx.OpResult {
	execCtx, cancel := context.WithTimeout(ctx, systemctlUserTimeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, systemctlUserBinary, append([]string{"--user"}, subArgs...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	res := tx.OpResult{Stdout: strings.TrimSpace(out.String()), Stderr: strings.TrimSpace(errBuf.String())}
	switch {
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		res.Returncode = 124
		res.Error = fmt.Sprintf("systemctl --user %s 超时(%s)", strings.Join(subArgs, " "), systemctlUserTimeout)
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Returncode = ee.ExitCode()
		} else {
			res.Returncode = 126
			res.Error = fmt.Sprintf("systemctl 启动失败: %v", err)
		}
	}
	return res
}

// firstNonEmpty 返回首个非空串; 全空时给通用兜底消息。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "未知失败(无 stderr 输出)"
}
