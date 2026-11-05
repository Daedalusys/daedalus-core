package fleet

// 本文件钉住节点注册表(registry)的读写语义:一行一 Node 的
// <fleet root>/nodes.jsonl、同 node_id 覆盖、list 按 node_id 升序稳定、
// 未知节点不报错、相对根目录 fail-closed。
// 根目录一律经 DAEDALUS_FLEET_DIR 隔离到 t.TempDir()。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/dirs"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// testNode 造一个字段齐全的节点(仅 node_id 变化)。
func testNode(nodeID string) Node {
	return Node{
		NodeID:           nodeID,
		AgentID:          "agent-" + nodeID,
		Kind:             objectmodel.MachineKindAgent,
		Addr:             "10.0.0.5:9443",
		Distro:           "ubuntu",
		DistroVersion:    "24.04",
		Kernel:           "6.8.0-45-generic",
		Arch:             "amd64",
		Hostname:         "host-" + nodeID,
		AgentVersion:     "0.2.1",
		SnapshotCap:      "SNAPSHOT_CAPABILITY_BTRFS_SUBVOLUME",
		SupportLevel:     objectmodel.SupportLevelSupported,
		CertFingerprint:  "sha256:" + nodeID,
		RegisteredAtUnix: 1700000000,
		LastSeenUnix:     1700000001,
	}
}

// TestRegistry_PutGetRoundTrip 证明写入后能原样读回全部字段。
func TestRegistry_PutGetRoundTrip(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	want := testNode("n1")
	if err := r.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := r.Get("n1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("刚写入的节点应可查到")
	}
	if got != want {
		t.Errorf("round-trip 不一致:\n got %+v\nwant %+v", got, want)
	}
}

// TestRegistry_PutReplacesSameNodeID 证明同 node_id 二次 Put 后者胜,
// 且文件里只留一行(覆盖而非追加)。
func TestRegistry_PutReplacesSameNodeID(t *testing.T) {
	root := t.TempDir()
	t.Setenv(FleetDirEnv, root)
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	first := testNode("n1")
	if err := r.Put(first); err != nil {
		t.Fatalf("首次 Put: %v", err)
	}
	second := testNode("n1")
	second.Hostname = "host-n1-replaced"
	second.SupportLevel = objectmodel.SupportLevelExperimental
	if err := r.Put(second); err != nil {
		t.Fatalf("二次 Put: %v", err)
	}

	got, ok, err := r.Get("n1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("节点应仍存在")
	}
	if got != second {
		t.Errorf("二次 Put 未覆盖:\n got %+v\nwant %+v", got, second)
	}

	raw, err := os.ReadFile(filepath.Join(root, "nodes.jsonl"))
	if err != nil {
		t.Fatalf("读注册表: %v", err)
	}
	if lines := nonEmptyLines(string(raw)); lines != 1 {
		t.Errorf("注册表行数 = %d, want 1(同 node_id 必须覆盖)", lines)
	}
}

// TestRegistry_ListStableOrder 证明 List 按 node_id 升序,与写入次序无关。
func TestRegistry_ListStableOrder(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	for _, id := range []string{"n3", "n1", "n2"} {
		if err := r.Put(testNode(id)); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, 0, len(list))
	for _, n := range list {
		got = append(got, n.NodeID)
	}
	want := []string{"n1", "n2", "n3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List 顺序 = %v, want %v", got, want)
	}
}

// TestRegistry_GetUnknownNode 证明未知 node_id 是"查不到"而非"出错"
// (调用方靠 ok 区分,不靠 err)。
func TestRegistry_GetUnknownNode(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	_, ok, err := r.Get("nope")
	if err != nil {
		t.Fatalf("未知节点不得报错, got %v", err)
	}
	if ok {
		t.Fatal("未知节点 ok 必须为 false")
	}
}

// TestRegistry_RejectsRelativeFleetDir 证明相对路径的 fleet 根目录 fail-closed,
// 且错误可经 errors.Is 识别(与 dirs 的哨兵约定一致)。
func TestRegistry_RejectsRelativeFleetDir(t *testing.T) {
	t.Setenv(FleetDirEnv, "relative/fleet")
	_, err := OpenRegistry()
	if !errors.Is(err, dirs.ErrEnvNotAbsolute) {
		t.Fatalf("相对根目录应报 ErrEnvNotAbsolute, got %v", err)
	}
}

// nonEmptyLines 数非空行(注册表写入不产生空行,此处仅为稳健)。
func nonEmptyLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
