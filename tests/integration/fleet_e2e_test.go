// Package integration_test 的 fleet 腿:Plan C 端到端(控制面侧)。
//
// 本文件把 tests/fleet/fixtures/ 里的两份 Plan 真跑一遍——不是 mock Dispatch,
// 而是起一个**真实 mTLS 的假 agent**(loopback TCP + 双向证书),让控制面经
// fleet.NewScheduler 走完整链路:Register 填 registry → ApplyIntent → 分桶。
// 这样 5/6/7 三类判定(低危跑完 / 高危遇无回滚点当场 dirty / 结果文案点名
// snapshot 能力)在 CI 里就是可重复的判据,不依赖真机。
package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
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

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// fixturePath 返回 tests/fleet/fixtures 下的夹具路径
// (go test 的 CWD 是包目录 tests/integration)。
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "fleet", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("夹具不存在: %v", err)
	}
	return p
}

// loadFixture 按 objectmodel.Plan 解进夹具并校验(夹具必须自身合法)。
func loadFixture(t *testing.T, name string) objectmodel.Plan {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatal(err)
	}
	var plan objectmodel.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("%s 不是合法 Plan JSON: %v", name, err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("%s 未通过 Plan 校验: %v", name, err)
	}
	return plan
}

// fakeAgent 是只做 Register / ApplyIntent 的被控端桩:snapshot 能力按 agent_id
// 决定(名字带 "no-snapshot" 的机器故意回 NONE,复现 RF1 场景)。
type fakeAgent struct {
	agentv1.UnimplementedAgentServiceServer
	applied []string
}

func (a *fakeAgent) Register(_ context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	cap := agentv1.SnapshotCapability_SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME
	if strings.Contains(req.GetAgentId(), "no-snapshot") {
		cap = agentv1.SnapshotCapability_SNAPSHOT_CAPABILITY_NONE
	}
	// node_id 由控制面在响应里给出:本桩让它等于 agent_id(= 夹具的 machine_id),
	// 使 Plan 的 machine_id 可以直接引用。
	return &agentv1.RegisterResponse{
		NodeId:             req.GetAgentId(),
		SnapshotCapability: cap,
	}, nil
}

func (a *fakeAgent) ApplyIntent(_ context.Context, req *agentv1.ApplyIntentRequest) (*agentv1.ApplyIntentResponse, error) {
	a.applied = append(a.applied, req.GetIntentId())
	return &agentv1.ApplyIntentResponse{
		Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS,
		TxId:   "tx-" + req.GetTarget(),
	}, nil
}

// startFakeAgent 在 loopback 上起一个双向 TLS 的 agent 桩,并把控制面的凭证
// 环境变量指向它,返回监听地址。
func startFakeAgent(t *testing.T, agent *fakeAgent) string {
	t.Helper()
	dir := t.TempDir()
	caCert, caKey := newCA(t, dir)
	serverCert, _, _ := issueCert(t, dir, caCert, caKey, "server", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	_, clientCertPath, clientKeyPath := issueCert(t, dir, caCert, caKey, "client", nil, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 loopback: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	agentv1.RegisterAgentServiceServer(srv, agent)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	t.Setenv(fleet.EnvClientCert, clientCertPath)
	t.Setenv(fleet.EnvClientKey, clientKeyPath)
	t.Setenv(fleet.EnvCA, filepath.Join(dir, "ca.pem"))
	t.Setenv(fleet.FleetDirEnv, t.TempDir())
	return lis.Addr().String()
}

// TestFleetE2E_LowRiskPlanCompletes 是 spec §5.3 的低危腿:3 台机、1 条
// package.set nginx(low)、count size=2 → 全部落地、status=complete、
// 每条结果都带 tx_id(可回滚点存在)。
func TestFleetE2E_LowRiskPlanCompletes(t *testing.T) {
	agent := &fakeAgent{}
	addr := startFakeAgent(t, agent)
	plan := loadFixture(t, "plan-low-nginx.json")
	reg := registerFixtureMachines(t, addr, plan)

	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != objectmodel.PlanStatusComplete {
		t.Fatalf("status = %q, want complete(%+v)", run.Status, run.Results)
	}
	if len(run.Results) != len(plan.Machines) {
		t.Fatalf("结果数 = %d, want %d", len(run.Results), len(plan.Machines))
	}
	for _, res := range run.Results {
		if res.TxID == "" {
			t.Errorf("节点 %s 的结果没有 tx_id(成功落地必须有): %+v", res.NodeID, res)
		}
	}
	if len(agent.applied) != len(plan.Machines) {
		t.Errorf("agent 实际收到 %d 条 intent, want %d", len(agent.applied), len(plan.Machines))
	}
}

// TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot 是 RF1:目标机没有回滚点时,
// 高危 intent 在**下发前**就被拒,且文案点名 snapshot 能力;整批转 dirty。
func TestFleetE2E_HighRiskPlanStopsOnNodeWithoutSnapshot(t *testing.T) {
	agent := &fakeAgent{}
	addr := startFakeAgent(t, agent)
	plan := loadFixture(t, "plan-high-kernel.json")
	reg := registerFixtureMachines(t, addr, plan)

	// 把"无回滚点"那台排到第一批,确保它先被判定。
	plan.Machines = []objectmodel.Machine{plan.Machines[1], plan.Machines[0]}
	plan.BlastRadius.Count = len(plan.Machines)

	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Fatalf("status = %q, want dirty(%+v)", run.Status, run.Results)
	}
	if len(run.Results) == 0 {
		t.Fatal("至少应有一条被拒的记录")
	}
	first := run.Results[0]
	if !strings.Contains(strings.ToLower(first.Message), "snapshot") {
		t.Errorf("拒绝文案应点名 snapshot 能力, got %q", first.Message)
	}
	if len(agent.applied) != 0 {
		t.Errorf("无回滚点节点不得收到 RPC, agent 却收到 %d 条", len(agent.applied))
	}
}

// registerFixtureMachines 经真实 Register 把 Plan 里的目标机登记进 registry
// (agent_id 取 machine_id,使假 agent 能按名字决定 snapshot 能力)。
func registerFixtureMachines(t *testing.T, addr string, plan objectmodel.Plan) *fleet.Registry {
	t.Helper()
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	cfg := fleet.DialConfigFromEnv()
	cfg.Addr = addr
	for _, m := range plan.Machines {
		node, err := fleet.RegisterNode(context.Background(), cfg, fleet.RegisterOptions{
			AgentID: m.MachineID,
			Kind:    m.Kind,
			Distro:  "ubuntu",
			// 版本故意给矩阵外的值 → support_level 走分级诚实度(与夹具的
			// experimental 那台对得上;supported 那台在本测试里不参与断言)。
			DistroVersion: "99.99",
		})
		if err != nil {
			t.Fatalf("RegisterNode %s: %v", m.MachineID, err)
		}
		if err := reg.Put(node); err != nil {
			t.Fatalf("Put %s: %v", node.NodeID, err)
		}
	}
	return reg
}

// ---------------- 测试用 PKI(仅本文件用;与 internal/fleet 的测试夹具同构) ----

// newCA 生成自签 CA 并落盘 ca.pem,返回证书与私钥。
func newCA(t *testing.T, dir string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "daedalus-e2e-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", der)
	return cert, key
}

// issueCert 由 CA 签发一张证书,返回加载好的 tls.Certificate 与证书/私钥路径。
func issueCert(t *testing.T, dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, name string, dns []string, ips []net.IP) (tls.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	writePEM(t, certPath, "CERTIFICATE", der)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPath, keyPath
}

// writePEM 落一个 PEM 块。
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
