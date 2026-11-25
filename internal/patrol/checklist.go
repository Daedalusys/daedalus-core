// Package patrol 实现巡检子系统:清单模型、发现模型、跨轮状态、变化四分类,
// 以及把一台机器的只读工具面开给 AI 客户端的一轮 MCP 网关。
//
// 除网关那层 HTTP/MCP 之外,本包不碰网络与子进程 —— AI 客户端编排与 SMTP 传输
// 在别处接入(本包只把变化渲染成邮件);数据面只有「读配置 / 算变化 / 落盘」三类动作,
// 便于单独钉住语义。
package patrol

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// 严重级词表。模型只能上调不能下调,四分类判恶化时按 info < warning < critical 比较。
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// itemIDPattern 是清单项 id 的形状。id 是跨轮去重与邮件分组的唯一依据,
// 形状放宽会让「同一项换了写法」被当成新发现。
var itemIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// nodeIDPattern 是节点 id 的白名单。状态文件按 <node>.json 落盘,id 里出现路径分隔符
// 或 ".." 就会把路径拼到目录之外。
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidNodeID 判断节点 id 能否安全用作路径的一段。空 id 不是节点标识 ——
// 需要「全局视图」的调用方自行用空字符串走 ChecklistFor,不要把它当节点名。
func ValidNodeID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return nodeIDPattern.MatchString(id)
}

// Item 是一条巡检清单项。
//
// Tools 只是提示级信息(写进提示词,告诉模型这一项建议用哪些工具),不是强制白名单:
// 强制「某一项只能用某几个工具」需要「模型当前在做哪一项」的额外协议,本轮不做。
type Item struct {
	ID       string   `json:"id"`
	Prompt   string   `json:"prompt"`
	Tools    []string `json:"tools,omitempty"`
	Severity string   `json:"severity"`
	Enabled  bool     `json:"enabled"`
}

// Checklist 是某个节点视角下合并后的清单。
type Checklist struct {
	Items []Item `json:"items"`
}

// AIClientConfig 是 [patrol.ai_client] 段:换 AI 客户端只改命令,不改代码。
type AIClientConfig struct {
	Command []string `json:"command"`
}

// SMTPConfig 是 [patrol.smtp] 段:只放投递参数。凭据走 systemd LoadCredential,
// 不进配置文件。
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	StartTLS bool     `json:"starttls"`
}

// PatrolConfig 是 [patrol] 段:调度与外部依赖的运行参数。
type PatrolConfig struct {
	Interval    string         `json:"interval"`
	NodeTimeout string         `json:"node_timeout"`
	AIClient    AIClientConfig `json:"ai_client"`
	SMTP        SMTPConfig     `json:"smtp"`
}

// Config 是巡检配置的完整形态:运行参数 + 全局清单 + 每个节点的合并视图。
type Config struct {
	Patrol PatrolConfig
	Global Checklist
	Nodes  map[string]Checklist
}

// ChecklistFor 返回某节点视角的清单(拷贝:Config 是只读快照)。未知节点不是错误:
// 它只是没有专属项,退回全局基线即可。
func (c Config) ChecklistFor(nodeID string) Checklist {
	if nodeID == "" {
		return copyChecklist(c.Global)
	}
	if ck, ok := c.Nodes[nodeID]; ok {
		return copyChecklist(ck)
	}
	return copyChecklist(c.Global)
}

// copyChecklist 连同每项的 Tools 一起复制:调用方改了拿到的清单,不该回写进 Config。
func copyChecklist(c Checklist) Checklist {
	items := make([]Item, len(c.Items))
	for i, it := range c.Items {
		items[i] = it
		if it.Tools != nil {
			items[i].Tools = append([]string(nil), it.Tools...)
		}
	}
	return Checklist{Items: items}
}

// LoadChecklist 读配置并返回某节点视角的合并清单 —— 一轮巡检的输入。
// nodeID 为空表示只要全局基线;非空则必须是合法节点 id(它会被拼进状态文件路径)。
func LoadChecklist(path, nodeID string) (Checklist, error) {
	if nodeID != "" && !ValidNodeID(nodeID) {
		return Checklist{}, fmt.Errorf("节点 id %q 非法(只允许字母、数字与 . _ -)", nodeID)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return Checklist{}, err
	}
	return cfg.ChecklistFor(nodeID), nil
}

// LoadConfig 严格解析配置文件:未知键、非法项一律报错。
//
// 清单解析 fail-closed —— 坏配置必须让本轮巡检整体失败,不静默丢项:
// 悄悄少跑一项检查,比报警更难发现。
func LoadConfig(path string) (Config, error) {
	var raw rawFile
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return Config{}, decodeError(path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("巡检配置 %s 含未知键: %s", path, strings.Join(keys, ", "))
	}

	global, err := mergeItems(nil, raw.Checklist.Item)
	if err != nil {
		return Config{}, fmt.Errorf("巡检配置 %s: %w", path, err)
	}

	nodes := make(map[string]Checklist, len(raw.Node))
	for nodeID, sec := range raw.Node {
		// 节点键会被拼进状态文件路径,和请求侧的 --node 走同一条白名单。
		if !ValidNodeID(nodeID) {
			return Config{}, fmt.Errorf("巡检配置 %s 的节点键 %q 非法(只允许字母、数字与 . _ -)", path, nodeID)
		}
		items, err := mergeItems(raw.Checklist.Item, sec.Checklist.Item)
		if err != nil {
			return Config{}, fmt.Errorf("巡检配置 %s 的节点 %q: %w", path, nodeID, err)
		}
		nodes[nodeID] = Checklist{Items: items}
	}

	return Config{
		Patrol: PatrolConfig{
			Interval:    raw.Patrol.Interval,
			NodeTimeout: raw.Patrol.NodeTimeout,
			AIClient:    AIClientConfig{Command: raw.Patrol.AIClient.Command},
			SMTP: SMTPConfig{
				Host:     raw.Patrol.SMTP.Host,
				Port:     raw.Patrol.SMTP.Port,
				From:     raw.Patrol.SMTP.From,
				To:       raw.Patrol.SMTP.To,
				StartTLS: raw.Patrol.SMTP.StartTLS,
			},
		},
		Global: Checklist{Items: global},
		Nodes:  nodes,
	}, nil
}

// decodeError 给解码失败补一句最容易撞上的写法提示:清单项必须是数组表,
// 单表写法 [node.<id>.checklist.item] 只放得下一项也解析不了。
func decodeError(path string, err error) error {
	msg := fmt.Sprintf("解析巡检配置 %s 失败: %v", path, err)
	if strings.Contains(err.Error(), "incompatible types") {
		msg += `(清单项要用数组表:[[checklist.item]] 与 [[node."<节点 id>".checklist.item]])`
	}
	return errors.New(msg)
}

// rawItem 用指针区分「本机段没声明这个字段」与「声明为空」:
// 前者继承全局值,后者是坏配置(如 prompt = "")。
type rawItem struct {
	ID       string    `toml:"id"`
	Prompt   *string   `toml:"prompt"`
	Tools    *[]string `toml:"tools"`
	Severity *string   `toml:"severity"`
	Enabled  *bool     `toml:"enabled"`
}

type rawChecklist struct {
	Item []rawItem `toml:"item"`
}

type rawNode struct {
	Checklist rawChecklist `toml:"checklist"`
}

type rawAIClient struct {
	Command []string `toml:"command"`
}

type rawSMTP struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	From     string   `toml:"from"`
	To       []string `toml:"to"`
	StartTLS bool     `toml:"starttls"`
}

type rawPatrol struct {
	Interval    string      `toml:"interval"`
	NodeTimeout string      `toml:"node_timeout"`
	AIClient    rawAIClient `toml:"ai_client"`
	SMTP        rawSMTP     `toml:"smtp"`
}

// rawFile 是配置文件的内存形态。所有字段都显式声明,未知键才会落进
// md.Undecoded() 被拒。
type rawFile struct {
	Patrol    rawPatrol          `toml:"patrol"`
	Checklist rawChecklist       `toml:"checklist"`
	Node      map[string]rawNode `toml:"node"`
}

// mergeItems 把本机专属项并进全局基线:同 id 逐字段覆盖(只覆盖本机段显式声明的字段),
// 不同 id 追加到末尾。两段各自的 id 唯一性先查 —— 同一段里写两遍同名项是坏配置,
// 而不是覆盖。
func mergeItems(base, local []rawItem) ([]Item, error) {
	for _, side := range []struct {
		name  string
		items []rawItem
	}{{"全局清单", base}, {"本机清单", local}} {
		if err := checkUniqueIDs(side.items, side.name); err != nil {
			return nil, err
		}
	}

	merged := make([]rawItem, len(base))
	copy(merged, base)
	for _, it := range local {
		idx := slices.IndexFunc(merged, func(x rawItem) bool { return x.ID == it.ID })
		if idx < 0 {
			merged = append(merged, it)
			continue
		}
		merged[idx] = overlay(merged[idx], it)
	}
	return toItems(merged)
}

// overlay 把本机项显式声明的字段盖到基线上;没声明的字段沿用基线值。
func overlay(base, local rawItem) rawItem {
	out := base
	if local.Prompt != nil {
		out.Prompt = local.Prompt
	}
	if local.Tools != nil {
		out.Tools = local.Tools
	}
	if local.Severity != nil {
		out.Severity = local.Severity
	}
	if local.Enabled != nil {
		out.Enabled = local.Enabled
	}
	return out
}

func checkUniqueIDs(items []rawItem, scope string) error {
	seen := make(map[string]struct{}, len(items))
	for i, it := range items {
		if _, ok := seen[it.ID]; ok {
			return fmt.Errorf("%s里 id %q 重复(第 %d 项)", scope, it.ID, i+1)
		}
		seen[it.ID] = struct{}{}
	}
	return nil
}

// toItems 把合并后的原始项转成对外模型,顺带做完语义校验。
func toItems(raw []rawItem) ([]Item, error) {
	items := make([]Item, 0, len(raw))
	for _, it := range raw {
		if !itemIDPattern.MatchString(it.ID) {
			return nil, fmt.Errorf("清单项 id %q 非法(需匹配 %s)", it.ID, itemIDPattern)
		}
		tools := derefStrings(it.Tools)
		for _, tool := range tools {
			if strings.TrimSpace(tool) == "" {
				return nil, fmt.Errorf("清单项 %q 的 tools 里有空串", it.ID)
			}
		}
		prompt := derefString(it.Prompt)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("清单项 %q 的 prompt 为空", it.ID)
		}
		severity := derefString(it.Severity)
		if !validSeverity(severity) {
			return nil, fmt.Errorf("清单项 %q 的 severity %q 不在 {%s, %s, %s} 里",
				it.ID, severity, SeverityInfo, SeverityWarning, SeverityCritical)
		}
		// 省略 enabled 即启用:清单项的默认姿态是「跑」。
		enabled := true
		if it.Enabled != nil {
			enabled = *it.Enabled
		}
		items = append(items, Item{
			ID:       it.ID,
			Prompt:   prompt,
			Tools:    tools,
			Severity: severity,
			Enabled:  enabled,
		})
	}
	return items, nil
}

func validSeverity(s string) bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	}
	return false
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefStrings(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}
