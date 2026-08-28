// Command daedalus-smoke 是 Daedalus Go 工作区的构建冒烟验证程序(计划 todo 1)。
//
// 它的唯一目的,是在当前工具链下"真实编译并执行"本工作区的两个关键依赖:
//  1. github.com/modelcontextprotocol/go-sdk v1.7.0 —— 构造 MCP 服务器、注册
//     demo 工具,并通过内存内传输完成一次真实的 initialize + tools/call 往返;
//  2. github.com/BurntSushi/toml —— 解析一段内嵌 TOML 配置,证明后续任务
//     (policy.toml)所需的配置解析能力可用。
//
// 本程序不包含任何产品逻辑;服务器/审计等真实实现属于后续 todo。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/daedalus-os/daedalus/core/internal/version"
)

// smokeConfig 是冒烟测试的 TOML 配置模式:证明 BurntSushi/toml
// 可以解析后续任务(policy.toml 等)所需形式的键值配置。
type smokeConfig struct {
	Name     string `toml:"name"`
	ReadOnly bool   `toml:"read_only"`
}

// embeddedTOML 是内嵌的示例配置:一行 Go 字符串承载的合法 TOML
// (TOML 规范要求每个键值对独占一行,故用转义换行符编码两条配置)。
const embeddedTOML = "name = \"daedalus-smoke\"\nread_only = true\n"

// pingIn / pingOut 分别是 demo 工具的输入与输出 JSON Schema 载体类型。
type (
	pingIn struct {
		Message string `json:"message"`
	}
	pingOut struct {
		Echo string `json:"echo"`
	}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke FAILED: %v\n", err)
		os.Exit(1)
	}
}

// run 按"解析配置 → 注册工具 → 内存内往返调用 → 校验回包"的顺序执行冒烟流程,
// 任何一步失败都以 error 返回并由 main 转为非零退出码。
func run() error {
	// —— 第 1 步:BurntSushi/toml 解析 ——
	var cfg smokeConfig
	if _, err := toml.Decode(embeddedTOML, &cfg); err != nil {
		return fmt.Errorf("解析内嵌 TOML 失败: %w", err)
	}
	fmt.Printf("toml : name=%s read_only=%v\n", cfg.Name, cfg.ReadOnly)

	// —— 第 2 步:go-sdk 构造 MCP 服务器并注册 demo 工具 ——
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "daedalus-smoke",
		Version: version.Version,
	}, nil)

	// v1.7.0 的 ToolAnnotations.DestructiveHint 是 *bool 且 SDK 未导出
	// 取地址助手,这里用一个显式局部变量取地址(只读工具必然非破坏性)。
	nonDestructive := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "smoke_ping",
		Description: "冒烟测试工具:回显输入消息,验证 go-sdk 与工具注册链路可用",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Smoke Ping",
			ReadOnlyHint:    true,
			DestructiveHint: &nonDestructive,
			IdempotentHint:  true,
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pingIn) (*mcp.CallToolResult, pingOut, error) {
		echo := "pong: " + in.Message
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: echo}},
		}, pingOut{Echo: echo}, nil
	})

	// —— 第 3 步:内存内客户端真实往返(initialize + tools/call)——
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 一次调用产生互联的客户端/服务器两端传输(基于 net.Pipe,零网络依赖)。
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "daedalus-smoke-client",
		Version: version.Version,
	}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return fmt.Errorf("MCP 客户端握手失败: %w", err)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "smoke_ping",
		Arguments: pingIn{Message: cfg.Name},
	})
	if err != nil {
		return fmt.Errorf("tools/call 调用失败: %w", err)
	}
	if res.IsError {
		return fmt.Errorf("tools/call 返回错误结果: %+v", res.Content)
	}
	if len(res.Content) != 1 {
		return fmt.Errorf("tools/call 回包内容块数量异常: got %d, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return fmt.Errorf("tools/call 回包不是文本内容: %T", res.Content[0])
	}
	if want := "pong: " + cfg.Name; text.Text != want {
		return fmt.Errorf("tools/call 回显不符: got %q, want %q", text.Text, want)
	}
	fmt.Printf("mcp  : tool=smoke_ping echo=%q (server %s v%s)\n", text.Text, version.ModulePath, version.Version)

	// —— 第 4 步:确定性收尾(关闭会话 → 服务器随连接断开而退出)——
	if err := session.Close(); err != nil {
		return fmt.Errorf("关闭 MCP 客户端会话失败: %w", err)
	}
	if err := <-serverDone; err != nil {
		return fmt.Errorf("MCP 服务器退出异常: %w", err)
	}

	fmt.Println("SMOKE OK")
	return nil
}
