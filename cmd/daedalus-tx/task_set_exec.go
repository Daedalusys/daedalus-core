package main

// task_set_exec.go —— task.set 的进程与文件助手层。
//
// task_set.go 负责三段生命周期的编排与校验; 本文件只做 timer 单元名归一化与
// is-enabled 观测两件机械事; systemctl 直发与文件读写复用 service_set_exec.go 既有助手
// (readUnitFileSnapshot / restoreUnitFile / systemctlUser / firstNonEmpty)——
// 守卫与解析继续走 unitguard.go。三个文件各守一摊, 互不越界。

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// normalizeTimerName 名称门(复用 unitguard.go 的 unitNamePattern, 字符类同一)
// + 隐式 .timer 后缀(已带则不重复追加)。service.set 的 normalizeUnitName 以
// .service 为默认后缀;本函数镜像其结构, 是 task.set 名称门唯一出口。
func normalizeTimerName(name string) (string, error) {
	if name == "" || !unitNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid unit name: %q (仅允许单元名字符集)", name)
	}
	if strings.HasSuffix(name, ".timer") {
		return name, nil
	}
	return name + ".timer", nil
}

// observeTimerEnabledState argv 直发 `systemctl --user is-enabled <unit>`, 解析首行输出。
// 失败或输出空 → error(Propose 据此 exit 1, 与 service.set observeActiveState 同款语义)。
func observeTimerEnabledState(ctx context.Context, unit string) (string, error) {
	res := systemctlUser(ctx, "is-enabled", unit)
	if res.Returncode != 0 || res.Error != "" {
		return "", fmt.Errorf("is-enabled 观测失败: %s", firstNonEmpty(res.Error, res.Stderr))
	}
	s := strings.TrimSpace(res.Stdout)
	if s == "" {
		return "", errors.New("is-enabled 观测失败: systemctl 输出为空")
	}
	// `is-enabled` 单行输出, 截首行去掉尾随空白。
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s, nil
}
