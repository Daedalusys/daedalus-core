package fleet

// 本文件钉住 issue #184 的热路径契约:markSeenKnown 假定调用方已持有身份表快照
// 并校验过节点已注册,因此**绝不**重放 nodes.jsonl;它仍要保留观测侧车的全部
// 语义 —— 追加、按 last_seen 阈值压实、压实原子替换。
//
// 判据用"身份表被写成非法 JSON"来证伪:只要热路径真去读表,就必然报错。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeen_MarkSeenKnownSkipsIdentityTableRead 是核心判据:身份表不可解析时,
// MarkSeen(带未注册校验)必须报错,而 markSeenKnown 必须成功并照常追加一行
// —— 证明热路径一次都没打开 nodes.jsonl。
func TestSeen_MarkSeenKnownSkipsIdentityTableRead(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	// 调用方已持有的身份表快照(Run 里就是 List 的结果)。
	all := []Node{testNode("n1")}
	if err := os.WriteFile(r.path(), []byte("{这不是合法 JSON}\n"), 0o600); err != nil {
		t.Fatalf("写非法身份表: %v", err)
	}

	if err := r.MarkSeen("n1", 1700000500); err == nil {
		t.Fatal("MarkSeen 仍须读身份表做未注册校验,非法表必须报错")
	}
	if err := r.markSeenKnown("n1", 1700000500, all); err != nil {
		t.Fatalf("markSeenKnown 不得读身份表, got %v", err)
	}
	if got := lineCount(t, r.seenPath()); got != 1 {
		t.Errorf("seen.log 行数 = %d, want 1(热路径照常追加一条观测)", got)
	}
}

// TestSeen_MarkSeenKnownDoesNotValidateRegistration 把契约的另一半钉死:调用方
// 已校验过,热路径不再校验 —— 未注册 id 不报错、照常落一行。同时确认读侧仍
// 拒绝复活未知节点(侧车里的陌生 id 不会变成身份表的一行)。
func TestSeen_MarkSeenKnownDoesNotValidateRegistration(t *testing.T) {
	t.Setenv(FleetDirEnv, t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.Put(testNode("n1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	all, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if err := r.markSeenKnown("ghost", 1700000500, all); err != nil {
		t.Fatalf("markSeenKnown 不做未注册校验, got %v", err)
	}
	if got := lineCount(t, r.seenPath()); got != 1 {
		t.Errorf("seen.log 行数 = %d, want 1", got)
	}
	if _, ok, err := r.Get("ghost"); err != nil || ok {
		t.Errorf("侧车里的陌生 id 不得让节点复活: ok=%v err=%v", ok, err)
	}
	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].NodeID != "n1" {
		t.Errorf("身份表 = %+v, want 只剩 n1", list)
	}
}

// TestSeen_HotPathCompactionStillTriggers 钉住热路径没丢掉压实:用 markSeenKnown
// 反复追加越过阈值后,侧车仍收缩到"每个被观测过的节点一行"、保留最后一次观测,
// 且替换仍是原子的(每行可解析、不留临时文件)。
func TestSeen_HotPathCompactionStillTriggers(t *testing.T) {
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
	all, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	threshold := r.seenCompactThreshold(len(all))
	last := make(map[string]int64, len(ids))
	for i := 0; i <= threshold; i++ {
		id := ids[i%len(ids)]
		v := int64(1700000000 + i)
		if err := r.markSeenKnown(id, v, all); err != nil {
			t.Fatalf("第 %d 次 markSeenKnown(%s): %v", i, id, err)
		}
		last[id] = v
	}

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
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".seen-*.tmp"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("根目录残留压实临时文件: %v", leftovers)
	}
}
