package main

// cmdBootc 实现 daedalus 本体的 bootc 部署管理子命令:
//
//	daedalus-host bootc status [--json]                      只读,json 透传
//	daedalus-host bootc switch <imgref> --digest sha256:<hex> [--yes]
//	daedalus-host bootc rollback [--yes]
//
// ★ 写操作双重闸:必须 --yes 才动手,否则只打印 `would run: …` 并以用法错误
// (退出码 2)结束;switch 还必须给出 `--digest sha256:<64 位小写 hex>`,把
// 镜像钉死到具体摘要 —— 只给 tag 的切换等于"你看过 A、装的是 B",tag 会漂。
// `--yes` 只跳过"打印计划"这一步,不会顺带跳过 digest 校验或超时。
//
// ★ 特权的把关方式与 rescue 一致:操作直接在宿主二进制里做,靠 root + 单元
// 沙箱兜底,不走 confirm_token(那是给 AI 提案用的通道)。v1 不做 agent 通道:
// 不加 objectmodel Kind、不动 SDK、不改 fleet。
//
// 超时:只读探测 60s,写操作(可能拉镜像)900s,一律 exec.CommandContext。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// bootcStatusTimeout / bootcWriteTimeout 声明为变量只为让测试缩短它们
// (超时是真防线,不是装饰);生产值是固定常量,不接受任何外部输入覆盖。
var (
	bootcStatusTimeout = 60 * time.Second
	bootcWriteTimeout  = 900 * time.Second
)

// bootcSubcommands 是帮助文本与分发实现共用的子命令清单(测试钉防漂移)。
var bootcSubcommands = []string{"status", "switch", "rollback"}

// digestPattern 是 switch 唯一接受的摘要形态。大写 hex 也不收:bootc 自己
// 区分大小写,放宽只会把问题推迟到执行期。
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// bootcUsage 是 bootc 子家族的帮助文本(刻意不进 i18n locales:那份
// host.usage.body 属 SDK 仓,本改动不跨仓)。
func bootcUsage() string {
	return "usage: daedalus-host bootc {" + strings.Join(bootcSubcommands, "|") + "}\n\n" +
		"  status [--json]                                  只读:打印 bootc status --json 原文\n" +
		"  switch <imgref> --digest sha256:<64 hex> [--yes] 切换部署;**必须**带 --digest\n" +
		"  rollback [--yes]                                 回滚到上一个部署\n\n" +
		"写操作不加 --yes 时只打印 `would run: …` 并以退出码 2 结束,不改系统。\n" +
		"digest 从 `bootc status --json` 或镜像仓库查询;\n" +
		"非 root 时 status 可能报权限错误,用 `sudo daedalus-host bootc status`。\n"
}

// cmdBootc 分发 status / switch / rollback。签名与 cmdRescue 同风格。
func cmdBootc(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, bootcUsage())
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, bootcUsage())
		return exitOK
	case "status":
		return bootcStatus(stdout, stderr, args[1:])
	case "switch":
		return bootcSwitch(stdout, stderr, args[1:])
	case "rollback":
		return bootcRollback(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "bootc: 未知子命令 %q\n\n", args[0])
		fmt.Fprint(stderr, bootcUsage())
		return exitUsage
	}
}

// bootcAuditTarget 把子命令名放进审计 args 的 id 字段(bootc 没有插件 id;
// 记下是哪类特权动作,事后才追得回来)。
func bootcAuditTarget(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// bootcStatus:只读,把 `bootc status --json` 的 stdout 原样写出去。
// v1 只支持 json(输出格式不随 bootc 版本漂移);权限不足时给 sudo 提示,
// 不把 stderr 吞掉、也不静默退化成"空结果"。
func bootcStatus(stdout, stderr io.Writer, args []string) int {
	for _, a := range args {
		if a != "--json" {
			fmt.Fprintf(stderr, "bootc status: 未知参数 %q(v1 只输出 JSON)\n", a)
			return exitUsage
		}
	}
	out, errOut, err := runBootc(bootcStatusTimeout, "status", "--json")
	io.WriteString(stdout, out)
	io.WriteString(stderr, errOut)
	if err != nil {
		reportBootcFailure(stderr, err, bootcStatusTimeout, "status --json")
		if needsRootHint(errOut) {
			fmt.Fprintln(stderr, "hint: 权限不足,请用 sudo 重跑: sudo daedalus-host bootc status")
		}
		return bootcExitCode(err)
	}
	return exitOK
}

// errBootcTimeout 是超时哨兵:文案与判定同源,避免靠字符串匹配认超时。
var errBootcTimeout = errors.New("bootc 调用超时")

// runBootc 是 bootc 子命令的唯一执行出口:exec.CommandContext + 超时。
// 返回 stdout/stderr 原文与子进程错误(超时、二进制缺失、非零退出都算错误,
// 由调用方决定文案与退出码)。`args` 全部由本文件构造,无外部拼接面。
func runBootc(timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "bootc", args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%w(%s): %v", errBootcTimeout, timeout, err)
	}
	return outBuf.String(), errBuf.String(), err
}

// bootcExitCode 把子进程错误映射成宿主退出码:子进程自己给出的正码原样透传
// (脚本侧能区分用法错误与运行失败),其余(找不到二进制 / 被超时杀掉)归为 1。
func bootcExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if c := ee.ExitCode(); c > 0 {
			return c
		}
	}
	return exitRuntime
}

// reportBootcFailure 打印失败原因,并对两类高频病因给可操作提示。
func reportBootcFailure(stderr io.Writer, err error, timeout time.Duration, what string) {
	fmt.Fprintf(stderr, "bootc %s: %v\n", what, err)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		fmt.Fprintln(stderr, "hint: 本机没有 bootc 可执行文件(不是 bootc 部署系统?),该子命令只在 bootc 机器上有意义")
	case errors.Is(err, errBootcTimeout):
		fmt.Fprintf(stderr, "hint: 已按 %s 超时强杀;确认网络与镜像仓库可达后重试\n", timeout)
	}
}

// needsRootHint:bootc 报权限类错误时要给 sudo 提示。bootc 的措辞随版本变,
// 只认三类高频关键词;宁可漏判(用户仍看到原始 stderr)也不误判。
func needsRootHint(stderrText string) bool {
	low := strings.ToLower(stderrText)
	for _, s := range []string{"root", "permission denied", "not permitted", "operation not permitted"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

// extractBootedDeployment 只取稳定字段:deployments 里 booted=true 的 id,
// 退路是 status.booted.image。解析不出来返回空串(由调用方转成人工确认提示),
// 绝不拿它做业务判断。
func extractBootedDeployment(raw []byte) string {
	var doc struct {
		Deployments []struct {
			ID     string `json:"id"`
			Booted bool   `json:"booted"`
		} `json:"deployments"`
		Status struct {
			Booted struct {
				Image string `json:"image"`
			} `json:"booted"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, d := range doc.Deployments {
		if d.Booted && d.ID != "" {
			return d.ID
		}
	}
	return doc.Status.Booted.Image
}

// bootcSwitch 实现 `switch <imgref> --digest sha256:<hex> [--yes]`。
//
// 闸门顺序刻意固定:先做参数/digest 校验,再看 --yes —— `--yes` 只跳过"打印
// 计划",绝不允许它把校验或超时一起跳过去。
func bootcSwitch(stdout, stderr io.Writer, args []string) int {
	var (
		ref    string
		digest string
		yes    bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yes":
			yes = true
		case a == "--digest":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "bootc switch: --digest 需要 sha256:<64 位小写 hex> 参数")
				return exitUsage
			}
			digest = args[i+1]
			i++
		case strings.HasPrefix(a, "--digest="):
			digest = strings.TrimPrefix(a, "--digest=")
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "bootc switch: 未知参数 %q\n", a)
			return exitUsage
		default:
			if ref != "" {
				fmt.Fprintf(stderr, "bootc switch: 多余的参数 %q(只接受一个 <imgref>)\n", a)
				return exitUsage
			}
			ref = a
		}
	}
	if ref == "" {
		fmt.Fprintln(stderr, "bootc switch: 缺少 <imgref>")
		return exitUsage
	}
	if strings.Contains(ref, "@") {
		fmt.Fprintln(stderr, "bootc switch: <imgref> 不要自带 @digest;请拆成 <imgref> + --digest sha256:<hex>")
		return exitUsage
	}
	if digest == "" {
		fmt.Fprintln(stderr, "bootc switch: 必须带 --digest sha256:<64 位小写 hex>(只给 tag 的切换不安全:tag 会漂)")
		fmt.Fprintln(stderr, "hint: 先 `bootc status --json` 或到镜像仓库查该 tag 的 digest")
		return exitUsage
	}
	if !digestPattern.MatchString(digest) {
		fmt.Fprintf(stderr, "bootc switch: --digest 格式不合法 %q,只接受 sha256:<64 位小写 hex>\n", digest)
		fmt.Fprintln(stderr, "hint: 先 `bootc status --json` 或到镜像仓库查该 tag 的 digest")
		return exitUsage
	}

	target := ref + "@" + digest
	if !yes {
		fmt.Fprintf(stderr, "would run: bootc switch %s\n", target)
		fmt.Fprintln(stderr, "(未执行:写操作必须加 --yes)")
		return exitUsage
	}

	out, errOut, err := runBootc(bootcWriteTimeout, "switch", target)
	io.WriteString(stdout, out)
	io.WriteString(stderr, errOut)
	if err != nil {
		reportBootcFailure(stderr, err, bootcWriteTimeout, "switch")
		return bootcExitCode(err)
	}
	return reportBootedDeployment(stdout, stderr)
}

// bootcRollback 实现 `rollback [--yes]`。无 --yes 时只打印计划。
func bootcRollback(stdout, stderr io.Writer, args []string) int {
	yes := false
	for _, a := range args {
		if a == "--yes" {
			yes = true
			continue
		}
		fmt.Fprintf(stderr, "bootc rollback: 未知参数 %q\n", a)
		return exitUsage
	}
	if !yes {
		fmt.Fprintln(stderr, "would run: bootc rollback")
		fmt.Fprintln(stderr, "(未执行:写操作必须加 --yes)")
		return exitUsage
	}

	out, errOut, err := runBootc(bootcWriteTimeout, "rollback")
	io.WriteString(stdout, out)
	io.WriteString(stderr, errOut)
	if err != nil {
		reportBootcFailure(stderr, err, bootcWriteTimeout, "rollback")
		return bootcExitCode(err)
	}
	return reportBootedDeployment(stdout, stderr)
}

// reportBootedDeployment 是写操作之后的自证:再取一次 status,打印当前部署 id。
// 没有它,用户无法区分"切换/回滚真的生效"与"命令返回 0 但什么都没动"。
// 读失败不算写失败(系统状态已经落了),但必须明说"读不到部署,建议人工确认"。
func reportBootedDeployment(stdout, stderr io.Writer) int {
	out, errOut, err := runBootc(bootcStatusTimeout, "status", "--json")
	if err != nil {
		fmt.Fprintf(stderr, "bootc: 写操作已返回成功,但读不到部署(%v);建议人工确认: sudo daedalus-host bootc status\n", err)
		return exitOK
	}
	io.WriteString(stdout, errOut) // bootc status 的告警原样透传
	id := extractBootedDeployment([]byte(out))
	if id == "" {
		fmt.Fprintln(stderr, "bootc: 写操作已返回成功,但读不到部署 id;建议人工确认: sudo daedalus-host bootc status")
		return exitOK
	}
	fmt.Fprintf(stdout, "bootc: 当前部署 = %s\n", id)
	return exitOK
}
