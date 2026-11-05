// internal/fleet/client.go:控制面到 agent 的 mTLS gRPC 客户端。
//
// 硬线:CA、客户端证书、私钥三者缺一即拒——不允许 InsecureSkipVerify,
// 也不允许无 CA 的明文降级;ServerName 未显式给出时取 Addr 的 host 段。
// 传输契约见 fleet 控制面设计稿 §4.4。
package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// dialContext 是 grpc 建连的注入缝:生产走 grpc.DialContext,测试把它换成
// bufconn 的内存拨号器,避免占用真实端口。
var dialContext = grpc.DialContext

// DialConfig 是 Dial 的全部输入。Addr 为 agent 的 host:port 形态
// (测试可用 passthrough:///bufnet 之类的自定义 target)。
type DialConfig struct {
	Addr        string
	CertPath    string
	KeyPath     string
	CAPath      string
	ServerName  string
	CallTimeout time.Duration
}

// Dial 建立一条到 agent 的 mTLS 连接,返回客户端与关闭函数。
// 任一凭证缺失都在建连前拒绝,不产出半成品连接。
func Dial(ctx context.Context, cfg DialConfig) (agentv1.AgentServiceClient, func() error, error) {
	tlsCfg, err := tlsConfigFor(cfg)
	if err != nil {
		return nil, nil, err
	}
	conn, err := dialContext(ctx, cfg.Addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, fmt.Errorf("fleet: 连接 %s 失败: %w", cfg.Addr, err)
	}
	return agentv1.NewAgentServiceClient(conn), conn.Close, nil
}

// tlsConfigFor 组装严格的双向 TLS 配置:先做三项非空校验(任何一项为空即
// 拒,不做文件 I/O),再加载证书与 CA,最后补 ServerName 缺省值。
func tlsConfigFor(cfg DialConfig) (*tls.Config, error) {
	if cfg.CAPath == "" {
		return nil, fmt.Errorf("fleet: CAPath 为空:拒绝无 CA 连接(fail-closed)")
	}
	if cfg.CertPath == "" {
		return nil, fmt.Errorf("fleet: CertPath 为空:客户端证书必填(fail-closed)")
	}
	if cfg.KeyPath == "" {
		return nil, fmt.Errorf("fleet: KeyPath 为空:客户端私钥必填(fail-closed)")
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("fleet: 加载客户端证书失败: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CAPath)
	if err != nil {
		return nil, fmt.Errorf("fleet: 读取 CA %s 失败: %w", cfg.CAPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("fleet: CA %s 未含可用证书", cfg.CAPath)
	}

	serverName := cfg.ServerName
	if serverName == "" {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("fleet: Addr %q 无法解析出 host: %w", cfg.Addr, err)
		}
		serverName = host
	}

	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
		// InsecureSkipVerify 保持零值 false:跳过服务端证书校验是硬线禁止项。
	}, nil
}
