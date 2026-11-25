// aiclient.go 生成本轮 AI 客户端(默认 opencode)的临时配置。
//
// 客户端配置是**追加层**:OPENCODE_CONFIG 指向的文件叠加在全局配置之上,不替换它。
// 因此「除本轮 MCP 网关的工具外全 deny」必须写进这份配置(规则按序匹配、最后匹配者胜),
// 不能指望全局配置里没有别的 allow 规则。轮次令牌只落本轮目录(0600);模型凭据在
// 客户端自己的 auth.json,不抄进这里,也不进 patrol.toml。
package patrol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// aiServerName 是本轮网关在客户端配置里的 server 名。客户端看到的工具动作名是
	// <server>_<tool>(见 ActionName),放行规则据此生成。
	aiServerName = "patrol-node"
	// aiConfigName / aiConfigMode:配置里有本轮令牌,与轮次目录同生命周期、同机不可读。
	aiConfigName = "opencode.json"
	aiConfigMode = 0o600
	// aiConfigSchema 让客户端的配置校验有据可依。
	aiConfigSchema = "https://opencode.ai/config.json"
	// aiGlobalConfigRel 是全局配置相对用户配置目录的位置:模型/provider 从这里继承。
	aiGlobalConfigRel = "opencode/opencode.json"
)

// ActionName 按客户端的命名规则把「server 名 + 工具名」拼成动作名:只把字母、数字、
// _、- 之外的字符换成 _,保留 - 与 _。
//
// 这条规则同时决定放行规则长什么样:server 名 patrol-node 的工具动作名是
// patrol-node_report_finding,而不是 patrol_node_report_finding —— 写错前缀的规则
// 一条也命中不了,客户端会被 deny-all 挡住。
func ActionName(server, tool string) string {
	return normalizeActionPart(server) + "_" + normalizeActionPart(tool)
}

// allowRule 是放行本轮网关全部工具的动作模式。
func allowRule() string { return normalizeActionPart(aiServerName) + "_*" }

func normalizeActionPart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// aiPermissionRule 是一条权限规则;客户端按数组顺序匹配,后者覆盖前者。
type aiPermissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// aiServerConfig 是本轮网关的 remote MCP 声明。
type aiServerConfig struct {
	Type     string            `json:"type"`
	URL      string            `json:"url"`
	OAuth    bool              `json:"oauth"`
	CodeMode bool              `json:"codemode"`
	Headers  map[string]string `json:"headers"`
}

type aiMCPConfig struct {
	Servers map[string]aiServerConfig `json:"servers"`
}

// aiClientConfig 是生成的配置。Model/SmallModel/Provider 只做继承,不做改写。
type aiClientConfig struct {
	Schema      string             `json:"$schema"`
	Permissions []aiPermissionRule `json:"permissions"`
	MCP         aiMCPConfig        `json:"mcp"`
	Model       string             `json:"model,omitempty"`
	SmallModel  string             `json:"small_model,omitempty"`
	Provider    json.RawMessage    `json:"provider,omitempty"`
}

// WriteAIClientConfig 在 dir 下写本轮客户端配置,返回配置路径(供 OPENCODE_CONFIG 使用)。
func WriteAIClientConfig(dir, endpoint, token string) (string, error) {
	cfg := aiClientConfig{
		Schema: aiConfigSchema,
		Permissions: []aiPermissionRule{
			// 先全 deny,再只放行本轮网关 —— 顺序不能反,后者胜。
			{Action: "*", Resource: "*", Effect: "deny"},
			{Action: allowRule(), Resource: "*", Effect: "allow"},
		},
		MCP: aiMCPConfig{Servers: map[string]aiServerConfig{
			aiServerName: {
				Type:     "remote",
				URL:      endpoint,
				OAuth:    false,
				CodeMode: false,
				Headers:  map[string]string{"Authorization": "Bearer " + token},
			},
		}},
	}
	if err := mergeGlobalAIConfig(&cfg); err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化 AI 客户端配置失败: %w", err)
	}
	path := filepath.Join(dir, aiConfigName)
	// 先按 0600 建文件再写:WriteFile 不会收紧已存在文件的权限。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, aiConfigMode)
	if err != nil {
		return "", fmt.Errorf("创建 AI 客户端配置 %s 失败: %w", path, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("写入 AI 客户端配置 %s 失败: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("关闭 AI 客户端配置 %s 失败: %w", path, err)
	}
	return path, nil
}

// mergeGlobalAIConfig 把全局配置里的 model / small_model / provider 抄进本轮配置。
//
// 客户端不会自动继承全局 provider(靶场实测):只写 permissions + mcp 时模型没有来源,
// 要么在 [patrol.ai_client].command 里用 --model 显式指定,要么像这里一样把三项合并进来。
// auth.json 里的密钥不在配置里,不抄。
func mergeGlobalAIConfig(cfg *aiClientConfig) error {
	path, ok := globalAIConfigPath()
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取全局客户端配置 %s 失败: %w", path, err)
	}
	var global struct {
		Model      string          `json:"model"`
		SmallModel string          `json:"small_model"`
		Provider   json.RawMessage `json:"provider"`
	}
	if err := json.Unmarshal(data, &global); err != nil {
		return fmt.Errorf("解析全局客户端配置 %s 失败: %w", path, err)
	}
	cfg.Model = global.Model
	cfg.SmallModel = global.SmallModel
	if len(global.Provider) > 0 {
		cfg.Provider = global.Provider
	}
	return nil
}

// globalAIConfigPath 返回全局客户端配置路径;不存在时 ok=false(不是错误:
// 没有全局配置的形态只写 permissions + mcp,由命令行 --model 决定模型)。
func globalAIConfigPath() (string, bool) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(base, filepath.FromSlash(aiGlobalConfigRel))
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}
