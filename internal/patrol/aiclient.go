// aiclient.go 生成本轮 AI 客户端(默认 opencode)的临时配置。
//
// 客户端配置是**追加层**:OPENCODE_CONFIG 指向的文件叠加在全局配置之上,不替换它。
// 因此 deny-all 必须写在这里(规则按序匹配、后者胜),不能指望全局没有别的 allow。
// 轮次令牌只落轮次目录(0600);模型凭据在客户端自己的 auth.json,不抄进这里。
package patrol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// aiServerName 是网关在客户端配置里的 server 名;动作名 = <server>_<tool>(见 ActionName)。
	aiServerName = "patrol-node"
	// aiConfigName / aiConfigMode:配置里有本轮令牌,与轮次目录同生命周期、同机不可读。
	aiConfigName = "opencode.json"
	aiConfigMode = 0o600

	aiConfigSchema = "https://opencode.ai/config.json"
	// aiGlobalConfigRel 是全局配置相对用户配置目录的位置:模型配置从这里继承。
	aiGlobalConfigRel = "opencode/opencode.json"
)

// ActionName 把「server 名 + 工具名」拼成客户端动作名:只把字母、数字、_、- 之外的
// 字符换成 _,保留 - 与 _。
//
// server 名 patrol-node 的工具动作名是 patrol-node_report_finding,不是
// patrol_node_report_finding —— 写错前缀的放行规则一条也命中不了,客户端会被 deny-all 挡住。
func ActionName(server, tool string) string {
	return normalizeActionPart(server) + "_" + normalizeActionPart(tool)
}

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

type aiPermissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

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

// mergeGlobalAIConfig 把全局配置的 model / small_model / provider 抄进本轮配置 ——
// 客户端不会自动继承全局 provider(靶场实测),缺了它模型就没有来源。
//
// provider 整块原样继承:若全局把它内联成含 apiKey 的字面量,密钥就会出现在轮次
// 目录的 opencode.json 里(0600,但轮次目录无保留策略);敏感环境请改用 command 的
// --model。auth.json 里的凭据不在配置里,不抄。
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

// globalAIConfigPath 返回全局客户端配置路径;不存在时 ok=false(不是错误)。
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
