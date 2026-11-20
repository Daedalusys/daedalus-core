package main

// service_observe.go —— `service-observe` 子命令: 只读观测用户域 .service 单元状态。
//
// 供上层档位判定取机器自动查的属性(enabled / running / 可观测性), 自身零副作用:
// 只读 systemctl, 不建事务、不动单元文件、不发任何变更动词、不写审计(证据层归消费侧)。
// 退出码三分(单元级失败 0 + known=false / 进程级故障 1 / 用法错 2)见 usage()。
// argv 由 service_set_exec.go 的 systemctlUser 直发, 绝不经过 sh -c。

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// serviceObserveDoc 是 service-observe 的 stdout 契约(恰一份单行 JSON, 恰六键)。
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
// 取值集合 = systemd UnitFileState 词汇表, 与 service.set/task.set 侧 is-enabled
// 快照同源同词汇(systemd 的两处等价表达); 加新 kind 时勿另立第三套口径。
var serviceObserveEnabledStates = map[string]bool{
	"enabled":         true,
	"enabled-runtime": true,
	"static":          true,
	"indirect":        true,
	"generated":       true,
}

// parseObserveUnit 校验 --unit: 字符集沿用既有 unitNamePattern(与 service.set 同一道
// 名称门, 注入字符/路径分隔在拼 argv 之前即拒), 但**只接受 .service 后缀**。
// 观测命令绝不静默补后缀或改名 —— 那样调用方以为观测的是 A、实际查的是 B。
// 故此处不调用 normalizeUnitName(它的隐式 .service 语义属 service.set 的写入面)。
func parseObserveUnit(name string) (string, error) {
	if name == "" || !unitNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid unit name: %q (仅允许单元名字符集)", name)
	}
	if !strings.HasSuffix(name, ".service") {
		return "", fmt.Errorf("service-observe 只观测 .service 单元, 收到 %q", name)
	}
	return name, nil // 原样回显: 合法的 .service 单元名不做任何改写
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
	unit, err := parseObserveUnit(unitArg)
	if err != nil {
		return failUsage(stdout, stderr, err.Error())
	}

	res := systemctlUser(context.Background(), "show", unit, "--property=ActiveState,UnitFileState")
	if res.Error != "" {
		// 进程级故障(无法启动 126 / 超时 124): 这不是该单元的回答 ⇒ exit 1。
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

	// 单元级失败(rc 非 0 或属性不全都算查不到)不是错误: known=false + 退出码 0,
	// 上层据此 fail-closed 到最严档。半份观测比没有观测更危险(会把"未启用"误读成
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
