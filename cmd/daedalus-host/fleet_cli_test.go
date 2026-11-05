package main

// 控制面子命令(plan / node)测试的共享夹具:fleet 根与 policy 隔离、Plan JSON
// 造件、运行记录落盘、stdout JSON 解析。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/fleet"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// setupFleetCLI 隔离控制面的两个根:DAEDALUS_FLEET_DIR 指向临时目录;
// policy 走 development 回退(与开发机是否装了生产 policy.toml 无关);
// 清掉 mTLS 凭证环境变量,使下发在建连前就失败——测试不打真实网络。
func setupFleetCLI(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAEDALUS_FLEET_DIR", root)
	t.Setenv("DAEDALUS_POLICY_MODE", "development")
	t.Setenv("DAEDALUS_POLICY_PATH", "")
	t.Setenv(fleet.EnvClientCert, "")
	t.Setenv(fleet.EnvClientKey, "")
	t.Setenv(fleet.EnvCA, "")
	return root
}

// testMachine 造一台目标机。
func testMachine(nodeID string) objectmodel.Machine {
	return objectmodel.Machine{
		MachineID:    nodeID,
		Kind:         objectmodel.MachineKindAgent,
		SupportLevel: objectmodel.SupportLevelSupported,
	}
}

// testNodeRow 造一行节点名单记录。
func testNodeRow(nodeID string, level objectmodel.SupportLevel) fleet.Node {
	return fleet.Node{
		NodeID:           nodeID,
		AgentID:          "agent-" + nodeID,
		Kind:             objectmodel.MachineKindAgent,
		Addr:             "10.0.0.11:9443",
		Distro:           "ubuntu",
		DistroVersion:    "24.04",
		SnapshotCap:      "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME",
		SupportLevel:     level,
		RegisteredAtUnix: 1700000000,
		LastSeenUnix:     1700000000,
	}
}

// lowIntent 是判定表判低危的 intent(package.set + 非内核包)。
func lowIntent() objectmodel.Intent {
	return objectmodel.Intent{
		Kind:      objectmodel.KindPackage,
		Target:    "nginx",
		RiskClass: objectmodel.RiskClassLow,
	}
}

// testPlan 造一份能过 Validate 的 Plan。
func testPlan(t *testing.T, planID string, machines []objectmodel.Machine, intents []objectmodel.Intent, size int) objectmodel.Plan {
	t.Helper()
	plan := objectmodel.Plan{
		ID:       planID,
		Intents:  intents,
		Machines: machines,
		Batching: objectmodel.Batching{Strategy: objectmodel.BatchingCount, Size: size},
		BlastRadius: objectmodel.BlastRadius{
			Count:       len(machines),
			MaxRisk:     objectmodel.Plan{Intents: intents}.MaxRisk(),
			MaxDowntime: 30 * time.Second,
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("夹具 Plan 不合法: %v", err)
	}
	return plan
}

// writeTempFile 落一个临时文件,返回路径。
func writeTempFile(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// planFile 把 Plan 序列化到临时文件,返回路径。
func planFile(t *testing.T, plan objectmodel.Plan) string {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return writeTempFile(t, "plan.json", b)
}

// seedNode 把一行节点写进 fleet 根。
func seedNode(t *testing.T, node fleet.Node) {
	t.Helper()
	reg, err := fleet.OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Put(node); err != nil {
		t.Fatalf("Put %s: %v", node.NodeID, err)
	}
}

// seedPlanRecord 按 plans.jsonl 的线上形状追加一行运行记录。
func seedPlanRecord(t *testing.T, root string, rec planRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, planRecordFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// decodePlanStatus 解析 stdout 的单行 JSON。
func decodePlanStatus(t *testing.T, out string) planStatusOutput {
	t.Helper()
	var got planStatusOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout 不是合法 JSON(%v): %s", err, out)
	}
	return got
}
