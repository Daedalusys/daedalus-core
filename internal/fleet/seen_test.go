package fleet

// 本文件钉住 last_seen 观测侧车(seen.log)的六条行为:追加不动身份表、
// 折叠取最后一条、未注册节点仍报错、已删节点不复活、末尾半截行可容忍而中间
// 行坏必须 fail-closed、压实后仍是原子的且保持最新值。

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileSnapshot 是身份表的"内容 + 元数据"快照:任一被改写都会让快照不等。
type fileSnapshot struct {
	size int64
	mod  time.Time
	sum  [32]byte
}

// snapshotFile 取一份文件快照。
func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return fileSnapshot{size: info.Size(), mod: info.ModTime(), sum: sha256.Sum256(raw)}
}

// lineCount 数文件里的非空行;文件不存在视为 0 行(观测侧车是可有可无的)。
func lineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("读 %s: %v", path, err)
	}
	return nonEmptyLines(string(raw))
}

// appendRaw 以追加方式写入任意字节(造半截行用)。
func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("追加 %s: %v", path, err)
	}
}

// TestSeen_AppendDoesNotTouchIdentityTable 是本次改动的核心判据:MarkSeen 只
// 追加观测侧车,身份表(nodes.jsonl)的内容与 mtime 一个字节都不动。
func TestSeen_AppendDoesNotTouchIdentityTable(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.Put(testNode("n1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	before := snapshotFile(t, r.path())
	if err := r.MarkSeen("n1", 1700000500); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	after := snapshotFile(t, r.path())

	if before.sum != after.sum {
		t.Errorf("markSeen 改写了身份表内容:\n before %x\n after  %x", before.sum, after.sum)
	}
	if before.size != after.size {
		t.Errorf("markSeen 改了身份表大小: %d → %d", before.size, after.size)
	}
	if !before.mod.Equal(after.mod) {
		t.Errorf("markSeen 重写了身份表(mtime 变了): %s → %s", before.mod, after.mod)
	}
	if got := lineCount(t, r.seenPath()); got != 1 {
		t.Errorf("seen.log 行数 = %d, want 1", got)
	}
}

// TestSeen_FoldLastWins 钉住折叠语义:同一节点多次观测取**最后一条**,不是
// max —— 时钟回拨时"最后一次观测"才是真相。
func TestSeen_FoldLastWins(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.Put(testNode("n1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, v := range []int64{1700000300, 1700000200, 1700000100} {
		if err := r.MarkSeen("n1", v); err != nil {
			t.Fatalf("MarkSeen(%d): %v", v, err)
		}
	}

	got, ok, err := r.Get("n1")
	if err != nil || !ok {
		t.Fatalf("Get n1: ok=%v err=%v", ok, err)
	}
	if got.LastSeenUnix != 1700000100 {
		t.Errorf("LastSeenUnix = %d, want 1700000100(最后一条胜,不是最大值)", got.LastSeenUnix)
	}
}

// TestSeen_UnknownNodeStillErrors 钉住 fail-fast:未注册节点调 MarkSeen 必须
// 报错,且不得在侧车里凭空造出一行。
func TestSeen_UnknownNodeStillErrors(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.Put(testNode("n1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := r.MarkSeen("nope", 1700000500); err == nil {
		t.Fatal("未注册节点必须报错")
	}
	if got := lineCount(t, r.seenPath()); got != 0 {
		t.Errorf("未注册节点的观测不得落盘,seen.log 行数 = %d, want 0", got)
	}
}

// TestSeen_DeletedNodeDoesNotResurrect 钉住摘除语义:侧车里残留的旧 node_id
// 不得让已删节点复活。
func TestSeen_DeletedNodeDoesNotResurrect(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.Put(testNode("n1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.MarkSeen("n1", 1700000500); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if err := r.Delete("n1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("已删节点复活了: %+v", list)
	}
	if _, ok, err := r.Get("n1"); err != nil || ok {
		t.Errorf("Get 已删节点: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if got := lineCount(t, r.seenPath()); got != 0 {
		t.Errorf("身份写之后观测侧车应清空,行数 = %d", got)
	}
}

// TestSeen_TornTailToleratedMidFileFatal 钉住读侧的两条边界:追加写没有 fsync,
// 崩溃只可能留下**末尾**半截行(容忍并跳过);中间行坏是真损坏,必须 fail-closed。
func TestSeen_TornTailToleratedMidFileFatal(t *testing.T) {
	t.Run("末尾半截行跳过", func(t *testing.T) {
		t.Setenv(FleetDirEnv, t.TempDir())
		r, err := OpenRegistry()
		if err != nil {
			t.Fatalf("OpenRegistry: %v", err)
		}
		if err := r.Put(testNode("n1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := r.MarkSeen("n1", 1700000100); err != nil {
			t.Fatalf("MarkSeen 1: %v", err)
		}
		if err := r.MarkSeen("n1", 1700000200); err != nil {
			t.Fatalf("MarkSeen 2: %v", err)
		}
		appendRaw(t, r.seenPath(), `{"node_id":"n1","last_seen_unix":`)

		got, ok, err := r.Get("n1")
		if err != nil {
			t.Fatalf("末尾半截行不得判失败: %v", err)
		}
		if !ok {
			t.Fatal("n1 应可查到")
		}
		if got.LastSeenUnix != 1700000200 {
			t.Errorf("LastSeenUnix = %d, want 1700000200(取半截行之前的那一条)", got.LastSeenUnix)
		}
	})

	t.Run("中间行坏必须报错", func(t *testing.T) {
		t.Setenv(FleetDirEnv, t.TempDir())
		r, err := OpenRegistry()
		if err != nil {
			t.Fatalf("OpenRegistry: %v", err)
		}
		if err := r.Put(testNode("n1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		body := "{这不是合法 JSON}\n" + `{"node_id":"n1","last_seen_unix":300}` + "\n"
		if err := os.WriteFile(r.seenPath(), []byte(body), 0o600); err != nil {
			t.Fatalf("写侧车: %v", err)
		}

		if _, ok, err := r.Get("n1"); err == nil {
			t.Fatalf("中间行损坏必须 fail-closed, got ok=%v err=nil", ok)
		}
		if _, err := r.List(); err == nil {
			t.Fatal("List 也必须 fail-closed")
		}
	})
}

// fillSeenToCompaction 交替观测 nodes 直到行数刚越过压实阈值(最后一次观测
// 即触发压实):返回每个节点最后一次写入的值。
func fillSeenToCompaction(t *testing.T, r *Registry, nodeIDs []string) map[string]int64 {
	t.Helper()
	threshold := r.seenCompactThreshold(len(nodeIDs))
	last := make(map[string]int64, len(nodeIDs))
	for i := 0; i <= threshold; i++ {
		id := nodeIDs[i%len(nodeIDs)]
		v := int64(1700000000 + i)
		if err := r.MarkSeen(id, v); err != nil {
			t.Fatalf("第 %d 次 MarkSeen(%s): %v", i, id, err)
		}
		last[id] = v
	}
	return last
}

// TestSeen_CompactionKeepsLastSeen 钉住压实:行数越过阈值后侧车收缩到"每被观测
// 过的节点一行",且各节点仍拿到自己最后一次观测值。
func TestSeen_CompactionKeepsLastSeen(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	ids := []string{"n1", "n2"}
	for _, id := range ids {
		if err := r.Put(testNode(id)); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}

	last := fillSeenToCompaction(t, r, ids)

	if got, want := lineCount(t, r.seenPath()), len(ids); got > want {
		t.Errorf("压实后 seen.log 行数 = %d, want <= %d", got, want)
	}
	for _, id := range ids {
		got, ok, err := r.Get(id)
		if err != nil || !ok {
			t.Fatalf("Get %s: ok=%v err=%v", id, ok, err)
		}
		if got.LastSeenUnix != last[id] {
			t.Errorf("%s 的 LastSeenUnix = %d, want %d(压实不得丢最后一次观测)",
				id, got.LastSeenUnix, last[id])
		}
	}
}

// TestSeen_CompactionLeavesNoPartialFile 钉住压实自身也是原子替换:替换完成后
// 侧车每行都能解析,根目录不留临时文件。
func TestSeen_CompactionLeavesNoPartialFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv(FleetDirEnv, root)
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	ids := []string{"n1", "n2"}
	for _, id := range ids {
		if err := r.Put(testNode(id)); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	fillSeenToCompaction(t, r, ids)

	raw, err := os.ReadFile(r.seenPath())
	if err != nil {
		t.Fatalf("读侧车: %v", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e seenEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("压实后第 %d 行不可解析(%v): %q", i+1, err, line)
		}
		if e.NodeID == "" {
			t.Errorf("压实后第 %d 行缺 node_id: %q", i+1, line)
		}
	}

	leftovers, err := filepath.Glob(filepath.Join(root, ".seen-*.tmp"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("根目录残留压实临时文件: %v", leftovers)
	}
}
