package main

// package_set_immutable_test.go —— package.set 在 immutable(bootc)节点上的
// fail-closed 论证防线测试。
//
// 判据两条:
//   - bootc 节点: Propose 即拒, 且**零 rpm 查询 / 零 dnf 进程**(拒绝必须发生在
//     任何副作用之前, 否则「清晰拒绝」只是副作用之后的表象);
//   - 普通节点: 行为零变化(照旧查 rpm、照旧出双快照)。
//
// bootc 判定走 bootcMarkerPath 注入缝(镜像 podmanExecutable 同款契约), 故本套
// 用例在普通宿主上也能钉住 bootc 语义, 不需要真的 ostree 部署。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// TestMain 把 bootc 探测缝统一指向一个必然不存在的路径: 宿主机若恰好是
// ostree 部署, 真实标记文件会让所有既有 Propose/Apply 用例被新守卫拒掉。
// 需要 bootc 语义的用例自己覆写(见 stubBootcMarker)。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "daedalus-tx-bootc-marker-")
	if err != nil {
		panic("无法创建 bootc 标记夹具目录: " + err.Error())
	}
	bootcMarkerPath = filepath.Join(dir, "ostree-booted")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// stubBootcMarker 覆写 bootc 探测路径(注入契约的消费端)。
func stubBootcMarker(t *testing.T, path string) {
	t.Helper()
	orig := bootcMarkerPath
	t.Cleanup(func() { bootcMarkerPath = orig })
	bootcMarkerPath = path
}

// bootcMarkerFile 造一个真实存在的 bootc 标记文件, 返回其路径。
func bootcMarkerFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ostree-booted")
	if err := os.WriteFile(p, []byte("1"), 0o644); err != nil {
		t.Fatalf("写 bootc 标记失败: %v", err)
	}
	return p
}

// TestPackageSet_Propose_RejectsImmutableNode 钉 bootc 节点上 Propose 被拒 +
// 拒绝串逐字一致 + rpm 查询零调用(拒绝早于任何查询副作用)。
func TestPackageSet_Propose_RejectsImmutableNode(t *testing.T) {
	stubBootcMarker(t, bootcMarkerFile(t))
	rpmCalls := 0
	stubRpmQuery(t, func(context.Context, string) (bool, string, error) {
		rpmCalls++
		return true, "2.4.1-1.fc40", nil
	})

	_, _, err := packageSetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"htop","desired_state":"present"}`))
	if err == nil {
		t.Fatal("immutable(bootc)节点上的 Propose 必须被拒")
	}
	if err.Error() != immutableNodeRejectMsg {
		t.Errorf("拒绝串 = %q, want %q", err.Error(), immutableNodeRejectMsg)
	}
	if rpmCalls != 0 {
		t.Errorf("拒绝必须发生在 rpm 查询之前(rpm 查询次数 = %d, want 0)", rpmCalls)
	}
}

// TestPackageSet_Propose_NormalNodeUnchanged 钉普通节点行为零变化: 照旧查一次
// rpm 并出双快照(新守卫不得误伤非 bootc 节点)。
func TestPackageSet_Propose_NormalNodeUnchanged(t *testing.T) {
	stubBootcMarker(t, filepath.Join(t.TempDir(), "no-such-marker"))
	rpmCalls := 0
	stubRpmQuery(t, func(_ context.Context, name string) (bool, string, error) {
		rpmCalls++
		if name != "htop" {
			t.Errorf("查询的包名 = %q, want %q", name, "htop")
		}
		return true, "2.4.1-1.fc40", nil
	})

	before, after, err := packageSetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"htop","desired_state":"present"}`))
	if err != nil {
		t.Fatalf("普通节点上的 Propose 不应失败: %v", err)
	}
	if rpmCalls != 1 {
		t.Errorf("rpm 查询调用次数 = %d, want 1", rpmCalls)
	}
	if len(before) == 0 || len(after) == 0 {
		t.Errorf("双快照不得为空: before=%s after=%s", before, after)
	}
}

// TestPackageSet_Apply_RejectsImmutableNodeBeforeDnf 钉 Apply 侧的纵深防御:
// 手改 journal 绕过 Propose 也不该让 dnf 跑起来(拒绝串与 Propose 同串)。
func TestPackageSet_Apply_RejectsImmutableNodeBeforeDnf(t *testing.T) {
	stubBootcMarker(t, bootcMarkerFile(t))
	dnfCalls := 0
	orig := dnfExecFn
	t.Cleanup(func() { dnfExecFn = orig })
	dnfExecFn = func(context.Context, ...string) tx.OpResult {
		dnfCalls++
		return tx.OpResult{}
	}
	origEuid := geteuid
	t.Cleanup(func() { geteuid = origEuid })
	geteuid = func() int { return 0 }

	res := (packageSetAdapter{}).Apply(withTxID(context.Background(), "0123456789abcdef"),
		tx.Step{Adapter: "package.set", Index: 0,
			Args: json.RawMessage(`{"name":"htop","desired_state":"present"}`)})
	if res.Returncode == 0 {
		t.Fatal("immutable(bootc)节点上的 Apply 必须被拒")
	}
	if res.Error != immutableNodeRejectMsg {
		t.Errorf("拒绝串 = %q, want %q", res.Error, immutableNodeRejectMsg)
	}
	if dnfCalls != 0 {
		t.Errorf("拒绝必须发生在 dnf 之前(dnf 调用次数 = %d, want 0)", dnfCalls)
	}
}
