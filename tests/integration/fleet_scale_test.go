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
	"strings"
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

	agents, addrs := startScaleAgents(t, pki, n)
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
	// count 策略的"顺序"含义是**保持机器顺序**(与 importance 的分组重排不同):
	// 第 0 批必须就是机器表的前 50 台(不足 50 台时就是全部),按原序。
	wantFirst := 50
	if n < wantFirst {
		wantFirst = n
	}
	for i, id := range run.Batches[0].NodeIDs {
		if want := machineID(i); id != want {
			t.Fatalf("count 批序被打乱:批 0 第 %d 项 = %s, want %s", i, id, want)
		}
	}
	if got := len(run.Batches[0].NodeIDs); got != wantFirst {
		t.Fatalf("批 0 = %d 台, want %d", got, wantFirst)
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

// TestFleetScale_ImportanceOrdering 钉批序:importance 策略下 supported 的机器
// 必须排在 experimental 前面 —— 控制面把"有把握的先做",失败面小的那批先落地。
//
// 机器表故意把 experimental 放在**前半段**,这样"batch[0] 全 supported"不能靠
// 源顺序侥幸成立:只有真按 support_level 分组重排才会通过。
func TestFleetScale_ImportanceOrdering(t *testing.T) {
	n := scaleN(t)
	if n < 4 {
		t.Skipf("importance 用例需要至少 4 台机器,当前 DAEDALUS_SCALE_N=%d", n)
	}
	pki := newScalePKI(t)
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	agents, addrs := startScaleAgents(t, pki, n)
	registerScaleNodes(t, reg, addrs)

	plan := scalePlan(t, n, objectmodel.BatchingImportance, 0, objectmodel.KindPackage, objectmodel.RiskClassLow)
	half := n / 2
	for i := range plan.Machines {
		if i < half {
			plan.Machines[i].SupportLevel = objectmodel.SupportLevelExperimental
		}
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("改完 support_level 后 Plan 未通过校验: %v", err)
	}

	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != objectmodel.PlanStatusComplete {
		t.Fatalf("status = %v, want complete", run.Status)
	}
	if len(run.Batches) != 2 {
		t.Fatalf("batches = %d, want 2(supported 一批 + experimental 一批)", len(run.Batches))
	}

	// 按 Plan 的机器表算每台的 support_level,再逐批核对成员。
	levels := make(map[string]objectmodel.SupportLevel, n)
	for _, m := range plan.Machines {
		levels[m.MachineID] = m.SupportLevel
	}
	for _, id := range run.Batches[0].NodeIDs {
		if levels[id] != objectmodel.SupportLevelSupported {
			t.Fatalf("第 0 批出现了 %s(support_level=%s):supported 必须先跑", id, levels[id])
		}
	}
	for _, id := range run.Batches[1].NodeIDs {
		if levels[id] != objectmodel.SupportLevelExperimental {
			t.Fatalf("第 1 批出现了 %s(support_level=%s):experimental 只能在后", id, levels[id])
		}
	}
	if got := len(run.Batches[0].NodeIDs) + len(run.Batches[1].NodeIDs); got != n {
		t.Fatalf("两批合计 %d 台, want %d(每台必须恰好出现一次)", got, n)
	}

	// 两批都真的下发过:批序不是"只跑了一档"的假象。
	for i, ag := range agents {
		if got := ag.calls.Load(); got != 1 {
			t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 1", i, got)
		}
	}
	t.Logf("IMPORTANCE %d 节点:批 0(supported)=%d 台,批 1(experimental)=%d 台",
		n, len(run.Batches[0].NodeIDs), len(run.Batches[1].NodeIDs))
}

// TestFleetScale_UnreachableNodeDirtiesAndStops 钉"停手":第 60 台不可达
// (count size=50 → 它在第 2 批)。
//
//   - 前两批照跑(它之前每台各 1 次);
//   - 那一批整批 dirty,状态 dirty,StoppedAt 指向下一批;
//   - **它之后的机器一台都没跑**:同一批的后续机器与后续批的调用计数全为 0
//     —— 这才是"停手"的判据,只断言 StoppedAt 非空证明不了任何事。
func TestFleetScale_UnreachableNodeDirtiesAndStops(t *testing.T) {
	n := scaleN(t)
	pki := newScalePKI(t)
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	agents, addrs := startScaleAgents(t, pki, n)
	deadIdx := scaleIndex(n, 60)
	if deadIdx+1 >= n {
		t.Skipf("需要死节点之后还有机器,当前 DAEDALUS_SCALE_N=%d", n)
	}
	// 先起完所有 agent 再取死端口:端口的分配/释放在这之后没有新的监听了。
	addrs[deadIdx] = deadAddr(t)
	registerScaleNodes(t, reg, addrs)

	plan := scalePlan(t, n, objectmodel.BatchingCount, 50, objectmodel.KindPackage, objectmodel.RiskClassLow)
	start := time.Now()
	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if run.Status != objectmodel.PlanStatusDirty {
		t.Fatalf("status = %v, want dirty", run.Status)
	}
	if run.StoppedAt == nil {
		t.Fatal("StoppedAt 为空:整批 dirty 时必须指向没跑的那一批")
	}

	// 死节点之前:每台恰好 1 次(低危重试的是死节点自己,不许连坐)。
	for i := 0; i < deadIdx; i++ {
		if got := agents[i].calls.Load(); got != 1 {
			t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 1", i, got)
		}
	}
	// 死节点之后(同批剩余 + 后续批):一次都没跑。
	for i := deadIdx + 1; i < n; i++ {
		if got := agents[i].calls.Load(); got != 0 {
			t.Fatalf("第 %d 台在脏批之后仍收到 %d 次 ApplyIntent, want 0(停手不彻底)", i, got)
		}
	}
	// 结果里只该有死节点及其之前的记录。
	if got, want := len(run.Results), deadIdx+1; got != want {
		t.Fatalf("results = %d, want %d(只跑到死节点为止)", got, want)
	}
	res, ok := resultFor(run, machineID(deadIdx))
	if !ok {
		t.Fatalf("结果里没有死节点 %s 的记录", machineID(deadIdx))
	}
	if res.Outcome.String() != "persistent" {
		t.Fatalf("死节点 outcome = %s, want persistent", res.Outcome)
	}
	if res.Message == "" {
		t.Fatal("死节点的结果没有失败原因")
	}

	t.Logf("UNREACHABLE %d 节点:第 %d 台不可达,耗时 %s,已跑 %d 台,停在第 %d 批",
		n, deadIdx, elapsed.Round(time.Millisecond), len(run.Results), run.StoppedAt.Index)
}

// TestFleetScale_TransientRetryOnlyFailedMachine 是 spec G9「低危操作的瞬时失败
// 才允许自动重试」的字面判据:第 137 台前两次回 Unavailable,第三次成功。
//
// 判据必须是**计数**:它收到 3 次 ApplyIntent,其余 199 台各 1 次。只看最终
// status=complete 会同时掩盖"全部重试了一遍"和"重试了别的机器"两种缺陷。
func TestFleetScale_TransientRetryOnlyFailedMachine(t *testing.T) {
	n := scaleN(t)
	pki := newScalePKI(t)
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	agents, addrs := startScaleAgents(t, pki, n)
	retryIdx := scaleIndex(n, 137)
	if n < 2 {
		t.Skipf("需要至少 2 台机器,当前 DAEDALUS_SCALE_N=%d", n)
	}
	// 前两次瞬时失败,第三次成功:重试预算(policy transient_retry_max=3)足够。
	agents[retryIdx].failFirstN.Store(2)
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
	if got := len(run.Results); got != n {
		t.Fatalf("results = %d, want %d", got, n)
	}
	// 核心断言:只有那台被重试,且恰好重试 2 次(共 3 次)。
	if got := agents[retryIdx].calls.Load(); got != 3 {
		t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 3(1 次首发 + 2 次重试)", retryIdx, got)
	}
	for i, ag := range agents {
		if i == retryIdx {
			continue
		}
		if got := ag.calls.Load(); got != 1 {
			t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 1(不许被连坐重试)", i, got)
		}
	}
	res, ok := resultFor(run, machineID(retryIdx))
	if !ok {
		t.Fatalf("结果里没有第 %d 台的记录", retryIdx)
	}
	if res.Outcome.String() != "success" {
		t.Fatalf("第 %d 台 outcome = %s, want success", retryIdx, res.Outcome)
	}
	if res.TxID == "" {
		t.Fatalf("重试成功后仍须带 tx_id: %+v", res)
	}

	t.Logf("RETRY %d 节点:第 %d 台 %d 次 ApplyIntent,其余各 1 次,耗时 %s",
		n, retryIdx, agents[retryIdx].calls.Load(), elapsed.Round(time.Millisecond))
}

// TestFleetScale_HighRiskZeroRPC 钉 review focus #2:高危 intent 落在**没有回滚点**
// 的 atomic 节点上,控制面在发 RPC **之前**就拒。
//
// 判据必须数到 ApplyIntent 次数为 0 —— 文案点名"无回滚点"只证明控制面写了句话,
// 证明不了它没把 RPC 发出去;而 agent 侧自己拒也是另一种可能(两条不同的路)。
func TestFleetScale_HighRiskZeroRPC(t *testing.T) {
	n := scaleN(t)
	pki := newScalePKI(t)
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	agents, addrs := startScaleAgents(t, pki, n)
	atomicIdx := scaleIndex(n, 60)
	if atomicIdx+1 >= n {
		t.Skipf("需要无回滚点节点之后还有机器,当前 DAEDALUS_SCALE_N=%d", n)
	}
	registerScaleNodesWith(t, reg, addrs, func(i int, node *fleet.Node) {
		if i != atomicIdx {
			return
		}
		node.Kind = objectmodel.MachineKindAtomic
		node.SnapshotCap = "SNAPSHOT_CAPABILITY_NONE"
		node.SupportLevel = objectmodel.SupportLevelSupported
	})

	plan := scalePlan(t, n, objectmodel.BatchingCount, 50, objectmodel.KindPackage, objectmodel.RiskClassHigh)
	plan.Machines[atomicIdx].Kind = objectmodel.MachineKindAtomic
	if err := plan.Validate(); err != nil {
		t.Fatalf("把第 %d 台改成 atomic 后 Plan 未通过校验: %v", atomicIdx, err)
	}

	start := time.Now()
	run, err := fleet.NewScheduler(reg, policy.Default()).Run(context.Background(), plan)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 核心断言:零 RPC。
	if got := agents[atomicIdx].calls.Load(); got != 0 {
		t.Fatalf("无回滚点的 atomic 节点收到 %d 次 ApplyIntent, want 0(高危必须在下发前拒)", got)
	}
	if run.Status != objectmodel.PlanStatusDirty {
		t.Fatalf("status = %v, want dirty", run.Status)
	}
	if run.StoppedAt == nil {
		t.Fatal("StoppedAt 为空:整批 dirty 时必须指向没跑的那一批")
	}
	res, ok := resultFor(run, machineID(atomicIdx))
	if !ok {
		t.Fatalf("结果里没有第 %d 台的拒绝记录", atomicIdx)
	}
	if !strings.Contains(res.Message, "无回滚点") {
		t.Fatalf("拒绝文案应点名「无回滚点」, got %q", res.Message)
	}
	if res.Outcome.String() != "persistent" {
		t.Fatalf("拒绝记录 outcome = %s, want persistent", res.Outcome)
	}

	for i := 0; i < atomicIdx; i++ {
		if got := agents[i].calls.Load(); got != 1 {
			t.Fatalf("第 %d 台收到 %d 次 ApplyIntent, want 1", i, got)
		}
	}
	for i := atomicIdx + 1; i < n; i++ {
		if got := agents[i].calls.Load(); got != 0 {
			t.Fatalf("第 %d 台在脏批之后仍收到 %d 次 ApplyIntent, want 0", i, got)
		}
	}

	t.Logf("HIGHRISK %d 节点:第 %d 台无回滚点,ApplyIntent 0 次,耗时 %s,文案=%q",
		n, atomicIdx, elapsed.Round(time.Millisecond), res.Message)
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

// Heartbeat 回 Ack=true:调度器下发前会做一次活性预检,健康节点必须答活。
func (a *scaleAgent) Heartbeat(_ context.Context, _ *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{Ack: true}, nil
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

// startScaleAgents 起 n 个 loopback agent 桩,返回桩句柄与监听地址(下标对齐)。
func startScaleAgents(t *testing.T, pki *scalePKI, n int) ([]*scaleAgent, []string) {
	t.Helper()
	agents := make([]*scaleAgent, n)
	addrs := make([]string, n)
	for i := 0; i < n; i++ {
		agents[i] = &scaleAgent{}
		addrs[i] = startScaleAgent(t, pki, agents[i])
	}
	return agents, addrs
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

// scaleIndex 把用例想要的机器下标夹进 [0, n-1]:默认 200 台时原样返回,
// DAEDALUS_SCALE_N 调小后不至于越界。
func scaleIndex(n, want int) int {
	if want >= n {
		return n - 1
	}
	return want
}

// deadAddr 取一个"确实连不上"的 loopback 地址:绑端口后立刻关掉。
// (留一个不说话的监听会让 RPC 挂到 60s 超时,那是另一条路径。)
func deadAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("绑定临时端口: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("关闭临时端口: %v", err)
	}
	return addr
}

// resultFor 找某台机器的结果记录。
func resultFor(run *fleet.PlanRun, nodeID string) (fleet.StepResult, bool) {
	for _, r := range run.Results {
		if r.NodeID == nodeID {
			return r, true
		}
	}
	return fleet.StepResult{}, false
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
