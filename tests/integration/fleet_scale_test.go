// Package integration_test 的 fleet scale 腿(issue #156):把 spec G1 的
// 「P0.5 控制面 scale 验证 200 台」从文档承诺变成可复跑的证据。
//
// 造法:进程内起 200 个 loopback 监听 + 真双向 TLS 的 gRPC 桩,共用一张服务器
// 证书(不逐台签证书 —— 这里量的是控制面规模行为,不是每台机的身份);节点行
// 直接 reg.Put 种进注册表(不走 RegisterNode,那条要活 agent)。
//
// 生产代码一行不改:调度器 fleet.NewScheduler(reg, pol).Run 已经是全部需要的
// 接口,本文件只观测它的行为。
package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// TestFleetScale_200Nodes_AllSucceed 是 #156 的主干:200 台假被控机、1 条低危
// package.set、count size=50 → 4 批 → 全部落地。
//
// 断言分三层:结果正确(200 条、每条带 tx_id、status=complete)、
// 真的发了(每台恰好 1 次 ApplyIntent)、数字可量化(耗时与 JSON 字节数打出来)。
func TestFleetScale_200Nodes_AllSucceed(t *testing.T) {
	n := scaleN(t)
	pki := newScalePKI(t)
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	agents := make([]*scaleAgent, n)
	addrs := make([]string, n)
	for i := 0; i < n; i++ {
		agents[i] = &scaleAgent{}
		addrs[i] = startScaleAgent(t, pki, agents[i])
	}
	registerScaleNodes(t, reg, addrs)

	plan := scalePlan(t, n, objectmodel.BatchingCount, 50, objectmodel.KindPackage, objectmodel.RiskClassLow)
	start := time.Now()
	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if run.Status != objectmodel.PlanStatusComplete {
		t.Fatalf("status = %v, want complete", run.Status)
	}
	if len(run.Results) != n {
		t.Fatalf("results = %d, want %d", len(run.Results), n)
	}
	for _, r := range run.Results {
		if r.TxID == "" {
			t.Fatalf("结果缺 tx_id: %+v", r)
		}
	}
	wantBatches := (n + 49) / 50
	if len(run.Batches) != wantBatches {
		t.Fatalf("batches = %d, want %d", len(run.Batches), wantBatches)
	}
	for i, ag := range agents {
		if got := ag.calls.Load(); got != 1 {
			t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 1", i, got)
		}
	}
	js, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("序列化 PlanRun: %v", err)
	}
	t.Logf("SCALE %d 节点:耗时 %s,返回 JSON %d 字节,批次 %d,结果 %d 条",
		n, elapsed.Round(time.Millisecond), len(js), len(run.Batches), len(run.Results))
}

// ---------------- 助手 ----------------

// scaleN 是本次 scale 测量的节点数:默认 200(spec G1 的数),
// DAEDALUS_SCALE_N 可覆盖(给慢机器一条退路);-short 下整层跳过。
func scaleN(t *testing.T) int {
	t.Helper()
	if testing.Short() {
		t.Skip("scale 测量在 -short 下跳过(200 个 loopback 监听太重)")
	}
	n := 200
	if v := os.Getenv("DAEDALUS_SCALE_N"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			t.Fatalf("DAEDALUS_SCALE_N=%q 非法:必须是正整数", v)
		}
		n = parsed
	}
	return n
}

// scalePKI 是本层 scale 测量用的凭证:一张共享服务器证书(200 台共用)、
// 一张客户端证书、一个 CA。逐台签 200 张证书对本测量没有意义 —— 这里量的是
// 控制面规模行为,不是每台机的身份。
type scalePKI struct {
	caCert         *x509.Certificate
	serverCert     tls.Certificate
	clientCertPath string
	clientKeyPath  string
	caPath         string
}

// newScalePKI 签一次 CA + 两张证书,并把控制面凭证环境变量指向它们。
// fleet 根目录也在这里定一次(t.Setenv 只能在测试内用)。
func newScalePKI(t *testing.T) *scalePKI {
	t.Helper()
	dir := t.TempDir()
	caCert, caKey := newCA(t, dir)
	serverCert, _, _ := issueCert(t, dir, caCert, caKey, "scale-server", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	_, clientCertPath, clientKeyPath := issueCert(t, dir, caCert, caKey, "scale-client", nil, nil)
	caPath := filepath.Join(dir, "ca.pem")

	t.Setenv(fleet.EnvClientCert, clientCertPath)
	t.Setenv(fleet.EnvClientKey, clientKeyPath)
	t.Setenv(fleet.EnvCA, caPath)
	t.Setenv(fleet.FleetDirEnv, t.TempDir())

	return &scalePKI{
		caCert:         caCert,
		serverCert:     serverCert,
		clientCertPath: clientCertPath,
		clientKeyPath:  clientKeyPath,
		caPath:         caPath,
	}
}

// scaleAgent 是被控端桩:只实现 Register 与 ApplyIntent。计数用 sync/atomic
// (既有 fakeAgent.applied 是裸切片,调度器一旦并发就会 race —— 本层不能照抄)。
type scaleAgent struct {
	agentv1.UnimplementedAgentServiceServer
	calls      atomic.Int64
	failFirstN atomic.Int32
}

// Register 回一个 node_id 等于 agent_id 的注册响应(与既有 fakeAgent 同口径)。
func (a *scaleAgent) Register(_ context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	return &agentv1.RegisterResponse{
		NodeId:             req.GetAgentId(),
		SnapshotCapability: agentv1.SnapshotCapability_SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME,
	}, nil
}

// ApplyIntent 记一次调用;前 failFirstN 次回 Unavailable(瞬时失败),之后成功。
func (a *scaleAgent) ApplyIntent(_ context.Context, req *agentv1.ApplyIntentRequest) (*agentv1.ApplyIntentResponse, error) {
	a.calls.Add(1)
	for {
		left := a.failFirstN.Load()
		if left <= 0 {
			break
		}
		if a.failFirstN.CompareAndSwap(left, left-1) {
			return nil, status.Error(codes.Unavailable, "模拟瞬时不可用")
		}
	}
	return &agentv1.ApplyIntentResponse{
		Result: agentv1.ApplyIntentResult_APPLY_INTENT_RESULT_SUCCESS,
		TxId:   "tx-" + req.GetTarget(),
	}, nil
}

// startScaleAgent 在 loopback 上起一个双向 TLS 的 agent 桩,返回监听地址。
func startScaleAgent(t *testing.T, pki *scalePKI, ag *scaleAgent) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 loopback: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(pki.caCert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	agentv1.RegisterAgentServiceServer(srv, ag)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// machineID 是第 i 台机器的稳定标识(注册表唯一键 + intent_id 的组成段)。
func machineID(i int) string {
	return fmt.Sprintf("m%03d", i)
}

// scalePlan 造一份合法的 Plan:1 条 intent、n 台 agent 节点、support_level 全
// supported。爆炸半径按规则算(count = 机器数、max_risk = intents 的最大风险),
// 不手写常量 —— 否则 Plan.Validate() 会直接拒。
func scalePlan(t *testing.T, n int, strategy objectmodel.BatchingStrategy, size int, kind objectmodel.Kind, risk objectmodel.RiskClass) objectmodel.Plan {
	t.Helper()
	machines := make([]objectmodel.Machine, n)
	for i := range machines {
		machines[i] = objectmodel.Machine{
			MachineID:    machineID(i),
			Kind:         objectmodel.MachineKindAgent,
			SupportLevel: objectmodel.SupportLevelSupported,
		}
	}
	plan := objectmodel.Plan{
		ID:       fmt.Sprintf("scale-%d-%d", n, time.Now().UnixNano()),
		Intents:  []objectmodel.Intent{{Kind: kind, Target: "nginx", RiskClass: risk}},
		Machines: machines,
		Batching: objectmodel.Batching{Strategy: strategy, Size: size},
		BlastRadius: objectmodel.BlastRadius{
			Count:         n,
			KindBreakdown: map[string]int{string(kind): 1},
			MaxDowntime:   30 * time.Second,
			MaxRisk:       risk,
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("构造的 Plan 未通过校验: %v", err)
	}
	return plan
}

// registerScaleNodes 把 n 台假被控机直接种进注册表(不经 RegisterNode ——
// 那条要活 agent)。mutate 供个别用例改节点形态(如 atomic 节点)。
func registerScaleNodes(t *testing.T, reg *fleet.Registry, addrs []string) []fleet.Node {
	t.Helper()
	return registerScaleNodesWith(t, reg, addrs, nil)
}

// registerScaleNodesWith 是 registerScaleNodes 的可变形态:mutate 可逐台改节点行。
func registerScaleNodesWith(t *testing.T, reg *fleet.Registry, addrs []string, mutate func(i int, n *fleet.Node)) []fleet.Node {
	t.Helper()
	nodes := make([]fleet.Node, len(addrs))
	for i, addr := range addrs {
		node := fleet.Node{
			NodeID:           machineID(i),
			AgentID:          machineID(i),
			Kind:             objectmodel.MachineKindAgent,
			Addr:             addr,
			Distro:           "ubuntu",
			DistroVersion:    "24.04",
			SnapshotCap:      "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME",
			SupportLevel:     objectmodel.SupportLevelSupported,
			RegisteredAtUnix: time.Now().Unix(),
		}
		if mutate != nil {
			mutate(i, &node)
		}
		if err := reg.Put(node); err != nil {
			t.Fatalf("Put %s: %v", node.NodeID, err)
		}
		nodes[i] = node
	}
	return nodes
}
