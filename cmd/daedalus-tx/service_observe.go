package main

// service_observe.go —— `service-observe` 子命令: 只读观测用户域单元状态。
//
// 供上层档位判定取三个机器自动查的属性(enabled / running / 可观测性), 自身零副作用:
// 只读 systemctl, 不建事务、不动单元文件、不发任何变更动词。
// 观测口径是 `systemctl --user`, 与 service.set 适配器同域 —— 调用者不在活着的用户会话里
// 时这里同样观测不到(known=false), 上层据此按最严处理。
//
// 失败分两类(决定了退出码与 known):
//   - 单元级失败(systemctl 正常回答"没有这个单元"/rc 非 0/输出缺字段)⇒ known=false + 退出码 0,
//     因为"查不到"本身就是一种观测结果, 上层有明确的 fail-closed 处理;
//   - 进程级故障(systemctl 起不来 / 超时)⇒ 退出码 1 + {"error":...}, 这是部署坏了,
//     不该伪装成某个单元的属性未知。
//
// 绝不经过 sh -c: argv 由 service_set_exec.go 的 systemctlUser 直发。

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/audit"
)

// serviceObserveDoc 是 service-observe 的 stdout 契约(恰一份单行 JSON)。
// 键名即线上契约, 上层按此解析; known=false 的文档同样完整, 不省略任何键。
type serviceObserveDoc struct {
	Unit          string `json:"unit"`
	ActiveState   string `json:"active_state"`
	UnitFileState string `json:"unit_file_state"`
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	Known         bool   `json:"known"`
}

// serviceObserveUnknown 是契约里"查不到该维度"的取值(与 active_state 封闭枚举同源)。
const serviceObserveUnknown = "unknown"

// serviceObserveActiveStates 是 active_state 的封闭枚举: 中间态(activating/deactivating/
// reloading/…)对档位判定没有意义, 一律折成 unknown —— 契约是封闭枚举, 不逐字透传。
var serviceObserveActiveStates = map[string]bool{
	"active":   true,
	"inactive": true,
	"failed":   true,
}

// serviceObserveEnabledStates 是 enabled=true 的口径: 这些 UnitFileState 都表示
// "单元本就该在场"(static/indirect/generated 没有可 enable 的 [Install] 段,
// 但也不是被停用), 与 disabled/masked/linked/bad 明确区分。
var serviceObserveEnabledStates = map[string]bool{
	"enabled":         true,
	"enabled-runtime": true,
	"static":          true,
	"indirect":        true,
	"generated":       true,
}

// cmdServiceObserve 解析 --unit, 一次 systemctl 调用取齐两个属性, 打印观测文档。
func cmdServiceObserve(args []string, stdout, stderr io.Writer) int {
	rest, unitArg, ok := extractFlag(args, "--unit", "--unit 需要跟随非空 <name> 参数", stdout, stderr)
	if !ok {
		return exitUsage
	}
	if len(rest) != 0 {
		return failUsage(stdout, stderr,
			fmt.Sprintf("service-observe 不接受位置参数: %s", strings.Join(rest, " ")))
	}
	if unitArg == "" {
		return failUsage(stdout, stderr, "service-observe 需要 --unit <name>")
	}
	// 与 service.set 同一名称门: 注入字符/路径分隔在拼 argv 之前即拒。
	unit, err := normalizeUnitName(unitArg)
	if err != nil {
		return failUsage(stdout, stderr, err.Error())
	}

	res := systemctlUser(context.Background(), "show", unit, "--property=ActiveState,UnitFileState")
	if res.Error != "" {
		// 进程级故障(无法启动 126 / 超时 124): 不是该单元的回答。
		stampServiceObserve(unit, false, "error")
		return failRuntime(stdout, stderr,
			fmt.Sprintf("service-observe 观测失败: %s", res.Error))
	}
	doc := serviceObserveDoc{
		Unit:          unit,
		ActiveState:   serviceObserveUnknown,
		UnitFileState: serviceObserveUnknown,
	}
	active, unitFile, complete := parseSystemctlShowProps(res.Stdout)
	if active != "" {
		v := active
		if !serviceObserveActiveStates[v] {
			v = serviceObserveUnknown
		}
		doc.ActiveState = v
		doc.Running = active == "active"
	}
	if unitFile != "" {
		doc.UnitFileState = unitFile
		doc.Enabled = serviceObserveEnabledStates[unitFile]
	}

	// rc 非 0 或属性不全都算查不到: 半份观测比没有观测更危险(会把"未启用"误读成
	// "未观测"), 所以 known 要求两个维度都拿到值。
	switch {
	case res.Returncode != 0:
		fmt.Fprintf(stderr, "daedalus-tx: service-observe: systemctl rc=%d: %s\n",
			res.Returncode, firstNonEmpty(res.Stderr, "无 stderr 输出"))
	case !complete:
		fmt.Fprintf(stderr, "daedalus-tx: service-observe: systemctl 输出缺 ActiveState/UnitFileState 字段, 按查不到处理\n")
	default:
		doc.Known = true
	}

	outcome := "error"
	if doc.Known {
		outcome = "success"
	}
	stampServiceObserve(unit, doc.Known, outcome)
	printJSON(stdout, doc)
	return exitOK
}

// parseSystemctlShowProps 解析 `systemctl show --property=...` 的 KEY=VALUE 行。
// complete 要求两个键都在且值非空: 空值(transient 单元的 UnitFileState)等同查不到。
func parseSystemctlShowProps(out string) (active, unitFile string, complete bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ActiveState="); ok {
			active = v
			continue
		}
		if v, ok := strings.CutPrefix(line, "UnitFileState="); ok {
			unitFile = v
		}
	}
	return active, unitFile, active != "" && unitFile != ""
}

// stampServiceObserve 发一条空 TxID 的事务外围审计(观测不是事务步骤, 但"AI 依据什么
// 判断可以自主执行"必须留证据)。写失败静默 —— 与事务盖章同纪律, 绝不拖垮观测本体。
func stampServiceObserve(unit string, known bool, outcome string) {
	_, _ = audit.LogAudit(audit.Entry{ //nolint:errcheck // 尽力而为, 与 hostAudit/stampPlain 同纪律
		Identity: txIdentity,
		Tool:     "daedalus_tx_service_observe",
		Args:     argsVal(map[string]any{"unit": unit, "known": known}),
		Outcome:  outcome,
	})
}
