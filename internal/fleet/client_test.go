package fleet

// 本文件是 internal/fleet 的 mTLS 客户端测试(内部测试包:要断言未导出的
// tlsConfigFor,并把 grpc 建连注入缝换成 bufconn,故不能用外部测试包)。
// 硬线:缺 CA / 缺客户端证书 / 缺私钥一律拒;InsecureSkipVerify 恒为 false。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
)

// TestDial_RejectsMissingCA 钉住 fail-closed 硬线:没有 CA 就绝不建连
// (不得回落成明文或 InsecureSkipVerify)。
func TestDial_RejectsMissingCA(t *testing.T) {
	cfg := DialConfig{
		Addr:     "10.0.0.5:9443",
		CertPath: "/tmp/does-not-matter.crt",
		KeyPath:  "/tmp/does-not-matter.key",
	}
	client, closer, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("缺 CAPath 必须拒绝建连")
	}
	if !strings.HasPrefix(err.Error(), "fleet: ") {
		t.Errorf("错误文案前缀 = %q, want %q", err.Error(), "fleet: ")
	}
	if client != nil {
		t.Errorf("失败时 client 必须为 nil, got %v", client)
	}
	if closer != nil {
		t.Error("失败时不得返回关闭函数")
	}
}

// TestDial_RejectsMissingCertOrKey 表驱动覆盖客户端证书与私钥的缺失组合:
// 缺 CertPath / 缺 KeyPath / 两者都缺,一律拒。
func TestDial_RejectsMissingCertOrKey(t *testing.T) {
	cases := []struct {
		name     string
		certPath string
		keyPath  string
	}{
		{"缺 CertPath", "", "/tmp/does-not-matter.key"},
		{"缺 KeyPath", "/tmp/does-not-matter.crt", ""},
		{"两者都缺", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DialConfig{
				Addr:     "10.0.0.5:9443",
				CAPath:   "/tmp/does-not-matter-ca.pem",
				CertPath: tc.certPath,
				KeyPath:  tc.keyPath,
			}
			client, _, err := Dial(context.Background(), cfg)
			if err == nil {
				t.Fatal("缺客户端证书或私钥必须拒绝建连")
			}
			if !strings.HasPrefix(err.Error(), "fleet: ") {
				t.Errorf("错误文案前缀 = %q, want %q", err.Error(), "fleet: ")
			}
			if client != nil {
				t.Errorf("失败时 client 必须为 nil, got %v", client)
			}
		})
	}
}

// TestDial_TLSConfigNeverInsecureSkipVerify 钉住 TLS 配置的硬线字段:
// InsecureSkipVerify 恒 false、RootCAs 非空、客户端证书恰好一份、
// 最低版本 TLS 1.2。
func TestDial_TLSConfigNeverInsecureSkipVerify(t *testing.T) {
	pki := newTestPKI(t)
	cfg := DialConfig{
		Addr:     "10.0.0.5:9443",
		CAPath:   pki.caPath,
		CertPath: pki.clientCertPath,
		KeyPath:  pki.clientKeyPath,
	}
	tlsCfg, err := tlsConfigFor(cfg)
	if err != nil {
		t.Fatalf("tlsConfigFor: %v", err)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify 必须为 false——跳过服务端证书校验是硬线禁止的")
	}
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs 不得为 nil")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("客户端证书数 = %d, want 1", len(tlsCfg.Certificates))
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want %#x(TLS 1.2)", tlsCfg.MinVersion, tls.VersionTLS12)
	}
}

// TestDial_ServerNameFallsBackToAddrHost 钉住 ServerName 的缺省来源:
// 未显式给出时取 Addr 的 host 段。
func TestDial_ServerNameFallsBackToAddrHost(t *testing.T) {
	pki := newTestPKI(t)
	cfg := DialConfig{
		Addr:     "10.0.0.5:9443",
		CAPath:   pki.caPath,
		CertPath: pki.clientCertPath,
		KeyPath:  pki.clientKeyPath,
	}
	tlsCfg, err := tlsConfigFor(cfg)
	if err != nil {
		t.Fatalf("tlsConfigFor: %v", err)
	}
	if tlsCfg.ServerName != "10.0.0.5" {
		t.Errorf("ServerName = %q, want %q", tlsCfg.ServerName, "10.0.0.5")
	}
}

// TestDial_SucceedsOverBufconnWithMutualTLS 端到端证明双向 TLS 真的通:
// 自签 CA + 服务端/客户端证书,bufconn 上跑一次 Heartbeat 往返。
func TestDial_SucceedsOverBufconnWithMutualTLS(t *testing.T) {
	pki := newTestPKI(t)

	pool := x509.NewCertPool()
	pemBytes, err := os.ReadFile(pki.caPath)
	if err != nil {
		t.Fatalf("读 CA: %v", err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("CA 文件未含可用证书")
	}

	lis := bufconn.Listen(1024 * 1024)
	defer lis.Close()
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	agentv1.RegisterAgentServiceServer(srv, heartbeatStub{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	orig := dialContext
	dialContext = func(ctx context.Context, target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}))
		return grpc.DialContext(ctx, target, opts...)
	}
	defer func() { dialContext = orig }()

	client, closer, err := Dial(context.Background(), DialConfig{
		Addr:       "passthrough:///bufnet",
		ServerName: pki.serverName,
		CAPath:     pki.caPath,
		CertPath:   pki.clientCertPath,
		KeyPath:    pki.clientKeyPath,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer closer()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Heartbeat(ctx, &agentv1.HeartbeatRequest{NodeId: "n1", StatusSummary: "ok"})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !resp.GetAck() {
		t.Error("Heartbeat 应回 ack=true")
	}
}

// heartbeatStub 是只实现 Heartbeat 的 agent 桩(其余 RPC 走 Unimplemented)。
type heartbeatStub struct {
	agentv1.UnimplementedAgentServiceServer
}

func (heartbeatStub) Heartbeat(_ context.Context, _ *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{Ack: true, NextHeartbeatSeconds: 30}, nil
}

// testPKI 是一次性测试证书材料:自签 CA + 由它签发的服务端/客户端证书。
type testPKI struct {
	caPath         string
	serverName     string
	serverCert     tls.Certificate
	clientCertPath string
	clientKeyPath  string
}

// newTestPKI 在临时目录里生成自签 CA 与两端证书。
func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 CA 私钥: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "daedalus-fleet-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发 CA 证书: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("解析 CA 证书: %v", err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("写 CA: %v", err)
	}

	const serverName = "daedalus-agent.test"
	serverCertPath, serverKeyPath := issueTestCert(t, dir, "server", caCert, caKey, serverName, []string{serverName}, x509.ExtKeyUsageServerAuth)
	clientCertPath, clientKeyPath := issueTestCert(t, dir, "client", caCert, caKey, "daedalus-control-plane", nil, x509.ExtKeyUsageClientAuth)

	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("加载服务端证书: %v", err)
	}
	return testPKI{
		caPath:         caPath,
		serverName:     serverName,
		serverCert:     serverCert,
		clientCertPath: clientCertPath,
		clientKeyPath:  clientKeyPath,
	}
}

// issueTestCert 由 CA 签发一张证书,返回证书与私钥的路径。
func issueTestCert(t *testing.T, dir, name string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, dnsNames []string, eku x509.ExtKeyUsage) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 %s 私钥: %v", name, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发 %s 证书: %v", name, err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("写 %s 证书: %v", name, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("序列化 %s 私钥: %v", name, err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("写 %s 私钥: %v", name, err)
	}
	return certPath, keyPath
}
