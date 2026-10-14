package main

// container_set_exec.go —— container.set 的进程与文件助手层。
//
// container_set.go 负责三段生命周期的编排与校验; 本文件只做 podman argv 直发、
// inspect 解析、名称门三件机械事。复用 service_set_exec.go 的 firstNonEmpty
// 守门(同包函数, 一字不改)。其余既不依赖 service.set 也不依赖 task.set
// 的 systemd 特定工具 —— podman 不是 systemctl, 进程助手是全新一套。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// podmanExecutable v1 默认路径注入缝, 生产态固定 /usr/bin/podman, argv 直发
// 无 $PATH 查找, 测试覆写指向 t.TempDir() 下的 shell 夹具(模式镜像
// systemctlUserBinary 同款契约)。
var podmanExecutable = "/usr/bin/podman"

// podmanTimeout 单次 podman 调用兜底超时(30s, 与 systemctlUserTimeout 同值)。
const podmanTimeout = 30 * time.Second

// podmanNamePattern 容器名字符集整词匹配(与 unitNamePattern 同思路但范围
// 不含 [后缀规则]):
//   - 字符类 = 字母数字 + _ + @ + . + -(systemd unit 实例引用兼容;
//     podman 容器名允许 . 与 @ 介于单元兼容)
//   - /  \  空字节 空格  |  $  ( ) 引号  均被字符类自然排除
//   - 首字符类刻意排除 - / .(避免 argv 注入与隐藏文件语义)
var podmanNamePattern = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_.@-]*$`)

// podmanNameGate 名称门唯一出口: 空串或字符类不符 → 拒, 错误串逐字锁定
// (测试与 AGENTS.md 引用)。
func podmanNameGate(name string) error {
	if name == "" || !podmanNamePattern.MatchString(name) {
		return fmt.Errorf("invalid container name: %q (仅允许容器名字符集)", name)
	}
	return nil
}

// podmanExec argv 直发执行 `podman <args...>`(绝不经过 sh -c)。
// 规范化为 tx.OpResult: rc=子进程退出码; 无法启动 → 126; 超时 → 124。
// 模式镜像 systemctlUser, 内部名字不同。
func podmanExec(ctx context.Context, subArgs ...string) tx.OpResult {
	execCtx, cancel := context.WithTimeout(ctx, podmanTimeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, podmanExecutable, subArgs...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	res := tx.OpResult{Stdout: strings.TrimSpace(out.String()), Stderr: strings.TrimSpace(errBuf.String())}
	switch {
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		res.Returncode = 124
		res.Error = fmt.Sprintf("podman %s 超时(%s)", strings.Join(subArgs, " "), podmanTimeout)
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Returncode = ee.ExitCode()
		} else {
			res.Returncode = 126
			res.Error = fmt.Sprintf("podman 启动失败: %v", err)
		}
	}
	return res
}

// observeContainerState 调 `podman inspect --type container --format json <name>`,
// 解析首项的 (Running bool, Id string, Image string, Status string, exists bool)。
// exists 语义:
//   - rc=0 + 合法非空 JSON → exists=true, 字段填充
//   - rc=0 + 空 JSON 数组 → exists=false (与"不存在"一致语义)
//   - rc!=0 且进程级错误(res.Error 非空,即无法启动 / 超时)→ 上抛(A3 守门:
//     不谎报"不存在")
//   - rc!=0 + Error 为空 → 视为 podman 报告"找不到" → exists=false(正常 not-exists)
func observeContainerState(ctx context.Context, name string) (running bool, id, image, status string, exists bool, err error) {
	res := podmanExec(ctx, "inspect", "--type", "container", "--format", "json", name)
	if res.Error != "" {
		return false, "", "", "", false, fmt.Errorf("podman inspect 失败: %s", firstNonEmpty(res.Error, res.Stderr))
	}
	if res.Returncode != 0 {
		return false, "", "", "", false, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &arr); err != nil {
		return false, "", "", "", false, fmt.Errorf("podman inspect JSON 解析失败: %w (stdout=%s)", err, res.Stdout)
	}
	if len(arr) == 0 {
		return false, "", "", "", false, nil
	}
	state, _ := arr[0]["State"].(map[string]any)
	runningB, _ := state["Running"].(bool)
	id, _ = arr[0]["Id"].(string)
	image, _ = arr[0]["Image"].(string)
	status, _ = arr[0]["Status"].(string)
	return runningB, id, image, status, true, nil
}
