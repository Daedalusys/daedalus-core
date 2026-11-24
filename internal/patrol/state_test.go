package patrol_test

// 本文件钉住跨轮状态与变化四分类:身份是 (node, item_id),problem 才是「有问题」,
// 同一时刻一条清单项最多一条活跃发现;新增 / 恶化 / 恢复 / 持续 四类的边界都不许含糊。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

func TestClassify_FourKinds(t *testing.T) {
	prev := map[string]patrol.Finding{
		"disk-health": {ItemID: "disk-health", Status: "problem", Severity: "warning"},
		"fixed-one":   {ItemID: "fixed-one", Status: "problem", Severity: "warning"},
	}
	cur := []patrol.Finding{
		{ItemID: "disk-health", Status: "problem", Severity: "critical"}, // 恶化
		{ItemID: "fixed-one", Status: "ok"},                              // 恢复
		{ItemID: "new-one", Status: "problem", Severity: "warning"},      // 新增
	}
	ch := patrol.Classify(prev, cur)

	if ids := findingIDs(ch.Added); len(ids) != 1 || ids[0] != "new-one" {
		t.Errorf("Added = %v,期望 [new-one]", ids)
	}
	if ids := findingIDs(ch.Worsened); len(ids) != 1 || ids[0] != "disk-health" {
		t.Errorf("Worsened = %v,期望 [disk-health]", ids)
	}
	if ids := findingIDs(ch.Recovered); len(ids) != 1 || ids[0] != "fixed-one" {
		t.Errorf("Recovered = %v,期望 [fixed-one]", ids)
	}
	if len(ch.Ongoing) != 0 {
		t.Errorf("Ongoing 应为空,实际 %v", findingIDs(ch.Ongoing))
	}
	if ch.Worsened[0].Severity != "critical" {
		t.Errorf("恶化条目应带本轮的严重级,实际 %q", ch.Worsened[0].Severity)
	}
}

func TestClassify_OngoingWhenUnchanged(t *testing.T) {
	prev := map[string]patrol.Finding{
		"disk-health": {ItemID: "disk-health", Status: "problem", Severity: "warning", Summary: "旧描述"},
	}
	cur := []patrol.Finding{
		{ItemID: "disk-health", Status: "problem", Severity: "warning", Summary: "新描述"},
	}
	ch := patrol.Classify(prev, cur)

	if ids := findingIDs(ch.Ongoing); len(ids) != 1 || ids[0] != "disk-health" {
		t.Errorf("Ongoing = %v,期望 [disk-health]", ids)
	}
	if len(ch.Added)+len(ch.Worsened)+len(ch.Recovered) != 0 {
		t.Error("未变化的问题不该出现在新增/恶化/恢复里")
	}
	if ch.Ongoing[0].Summary != "新描述" {
		t.Errorf("持续条目应带本轮描述,实际 %q", ch.Ongoing[0].Summary)
	}
}

func TestClassify_SeverityDowngradeIsOngoing(t *testing.T) {
	prev := map[string]patrol.Finding{
		"kvm-storage": {ItemID: "kvm-storage", Status: "problem", Severity: "critical"},
	}
	cur := []patrol.Finding{
		{ItemID: "kvm-storage", Status: "problem", Severity: "warning"},
	}
	ch := patrol.Classify(prev, cur)

	if ids := findingIDs(ch.Ongoing); len(ids) != 1 || ids[0] != "kvm-storage" {
		t.Errorf("Ongoing = %v,期望 [kvm-storage]", ids)
	}
	if len(ch.Worsened) != 0 {
		t.Error("严重级下调不是恶化")
	}
	if len(ch.Recovered) != 0 {
		t.Error("严重级下调不是恢复")
	}
	// 下调以条目自身的严重级标注:持续条目带本轮(更低)的严重级。
	if ch.Ongoing[0].Severity != "warning" {
		t.Errorf("持续条目的严重级应为本轮的 warning,实际 %q", ch.Ongoing[0].Severity)
	}
}

// 本轮清单项压根没出现(= 模型没再报它)也算恢复:身份消失即恢复。
func TestClassify_VanishedItemIsRecovered(t *testing.T) {
	prev := map[string]patrol.Finding{
		"disk-health":    {ItemID: "disk-health", Status: "problem", Severity: "warning", Summary: "磁盘快满了"},
		"journal-errors": {ItemID: "journal-errors", Status: "problem", Severity: "critical"},
	}
	ch := patrol.Classify(prev, nil)

	if ids := findingIDs(ch.Recovered); len(ids) != 2 || ids[0] != "disk-health" || ids[1] != "journal-errors" {
		t.Errorf("Recovered = %v,期望按 id 升序的 [disk-health journal-errors]", ids)
	}
	if ch.Recovered[0].Summary != "磁盘快满了" {
		t.Error("恢复条目应带上一轮的描述(说明是什么恢复了)")
	}
	if len(ch.Added)+len(ch.Worsened)+len(ch.Ongoing) != 0 {
		t.Error("本轮没有发现时不该出现新增/恶化/持续")
	}
}

// 上一轮正常(ok)的项本轮出问题,算新增;ok 不该被当成活跃问题。
func TestClassify_PreviouslyOKNowProblemIsAdded(t *testing.T) {
	prev := map[string]patrol.Finding{
		"disk-health": {ItemID: "disk-health", Status: "ok", Severity: "warning"},
	}
	cur := []patrol.Finding{
		{ItemID: "disk-health", Status: "problem", Severity: "warning"},
	}
	ch := patrol.Classify(prev, cur)

	if ids := findingIDs(ch.Added); len(ids) != 1 || ids[0] != "disk-health" {
		t.Errorf("Added = %v,期望 [disk-health]", ids)
	}
	if len(ch.Recovered)+len(ch.Ongoing)+len(ch.Worsened) != 0 {
		t.Error("ok 的旧状态不该产生恢复或持续")
	}
}

// 本轮 ok 且上一轮也没有活跃问题:没有任何变化,四类都为空。
func TestClassify_QuietRound(t *testing.T) {
	ch := patrol.Classify(nil, []patrol.Finding{{ItemID: "disk-health", Status: "ok"}})
	if len(ch.Added)+len(ch.Worsened)+len(ch.Recovered)+len(ch.Ongoing) != 0 {
		t.Errorf("干净的一轮不该有变化: %+v", ch)
	}
}

// 状态文件里带上一轮的节点标识:节点不同就不是同一条发现(身份 = node + item_id)。
func TestClassify_DifferentNodeIsNotSameIdentity(t *testing.T) {
	prev := map[string]patrol.Finding{
		"disk-health": {Node: "lab-a", ItemID: "disk-health", Status: "problem", Severity: "warning"},
	}
	cur := []patrol.Finding{
		{Node: "lab-b", ItemID: "disk-health", Status: "problem", Severity: "warning"},
	}
	ch := patrol.Classify(prev, cur)

	if len(ch.Added) != 1 || ch.Added[0].Node != "lab-b" {
		t.Errorf("Added = %+v,期望 lab-b 的新增项", ch.Added)
	}
	if len(ch.Recovered) != 1 || ch.Recovered[0].Node != "lab-a" {
		t.Errorf("Recovered = %+v,期望 lab-a 的旧项按恢复处理", ch.Recovered)
	}
	if len(ch.Ongoing) != 0 {
		t.Error("不同节点不该算同一条发现的持续")
	}
}

func TestLoadState_MissingFileIsEmpty(t *testing.T) {
	st, err := patrol.LoadState(filepath.Join(t.TempDir(), "state", "lab-a.json"))
	if err != nil {
		t.Fatalf("首轮没有状态文件不该报错: %v", err)
	}
	if len(st) != 0 {
		t.Errorf("缺失的状态文件应读成空状态,实际 %d 条", len(st))
	}
}

func TestLoadState_CorruptFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lab-a.json")
	if err := os.WriteFile(path, []byte("{ 这不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := patrol.LoadState(path); err == nil {
		t.Fatal("损坏的状态文件必须报错,不能当成空状态继续跑")
	}
}

func TestSaveState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "lab-a.json")
	want := map[string]patrol.Finding{
		"disk-health": {
			Node:     "lab-a",
			ItemID:   "disk-health",
			Status:   "problem",
			Severity: "warning",
			Summary:  "根分区剩 3%",
			Evidence: []patrol.Evidence{{Tool: "shell_exec", Input: `{"command":"df"}`, Output: "/dev/vda1 97%", ReturnCode: 0}},
		},
	}
	if err := patrol.SaveState(path, want); err != nil {
		t.Fatalf("SaveState 失败: %v", err)
	}
	got, err := patrol.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState 失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 条活跃发现,实际 %d 条", len(got))
	}
	f := got["disk-health"]
	if f.Severity != "warning" || f.Summary != "根分区剩 3%" {
		t.Errorf("落盘再读回丢了字段: %+v", f)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Output != "/dev/vda1 97%" {
		t.Errorf("证据没有原样保留: %+v", f.Evidence)
	}
}

// 状态文件整体替换:上一轮的键不会残留在新一轮里。
func TestSaveState_ReplacesWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab-a.json")
	if err := patrol.SaveState(path, map[string]patrol.Finding{
		"old": {ItemID: "old", Status: "problem", Severity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := patrol.SaveState(path, map[string]patrol.Finding{
		"new": {ItemID: "new", Status: "problem", Severity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := patrol.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["old"]; ok {
		t.Error("上一轮的状态不该残留")
	}
	if _, ok := got["new"]; !ok {
		t.Error("新一轮的状态没有落盘")
	}
}

// 空状态落盘后读回来仍是空状态:nil map 不该序列化成含义含糊的 null。
func TestSaveState_NilMapWritesEmptyObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab-a.json")
	if err := patrol.SaveState(path, nil); err != nil {
		t.Fatalf("SaveState 失败: %v", err)
	}
	got, err := patrol.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState 失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空状态应有 0 条,实际 %d 条", len(got))
	}
}

func TestActiveFindings_KeepsOnlyProblems(t *testing.T) {
	cur := []patrol.Finding{
		{ItemID: "disk-health", Status: "problem", Severity: "warning"},
		{ItemID: "journal-patterns", Status: "ok"},
	}
	st := patrol.ActiveFindings(cur)
	if len(st) != 1 {
		t.Fatalf("活跃状态只应含 problem,实际 %d 条: %+v", len(st), st)
	}
	if _, ok := st["disk-health"]; !ok {
		t.Error("problem 项应进活跃状态")
	}
}

func TestReadFindings_MissingFileIsEmpty(t *testing.T) {
	fs, err := patrol.ReadFindings(filepath.Join(t.TempDir(), "findings.jsonl"))
	if err != nil {
		t.Fatalf("本轮没有任何发现时文件可以不存在: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("缺失的发现文件应读成空,实际 %d 条", len(fs))
	}
}

func TestReadFindings_CorruptLineRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.jsonl")
	body := `{"item_id":"disk-health","status":"problem"}` + "\n{坏的\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := patrol.ReadFindings(path); err == nil {
		t.Fatal("坏行必须报错,不能静默跳过(否则发现会被悄悄吞掉)")
	}
}

func TestReadFindings_BlankLinesSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	body := "\n" + `{"item_id":"disk-health","status":"problem","severity":"warning"}` + "\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := patrol.ReadFindings(path)
	if err != nil {
		t.Fatalf("空行不是坏行: %v", err)
	}
	if len(fs) != 1 || fs[0].ItemID != "disk-health" {
		t.Errorf("应有 1 条发现,实际 %+v", fs)
	}
}

// 同一清单项一轮只能有一条发现:重复的 item_id 会让四分类把同一项同时算成
// 恢复与持续(或新增两条),邮件自相矛盾。读侧 fail-closed 拒掉。
func TestReadFindings_DuplicateItemIDRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	body := `{"item_id":"disk-health","status":"ok"}` + "\n" +
		`{"item_id":"disk-health","status":"problem","severity":"warning"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := patrol.ReadFindings(path); err == nil {
		t.Fatal("重复 item_id 必须拒绝")
	}
}

// 读侧拒空 item_id,写侧也必须拒:库不能写出自己读不回的文件。
func TestWriteFindings_EmptyItemIDRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	if err := patrol.WriteFindings(path, []patrol.Finding{{Status: "problem", Severity: "warning"}}); err == nil {
		t.Fatal("空 item_id 必须拒绝")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("被拒的发现不该留下文件")
	}
}

// 同理:读侧拒重复 item_id,写侧也必须拒 —— 否则网关落盘的这一轮发现,
// 下一轮 ReadFindings 整轮读不回来。
func TestWriteFindings_DuplicateItemIDRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	err := patrol.WriteFindings(path, []patrol.Finding{
		{ItemID: "disk-health", Status: "ok"},
		{ItemID: "disk-health", Status: "problem", Severity: "warning"},
	})
	if err == nil {
		t.Fatal("重复 item_id 必须拒绝")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("被拒的发现不该留下文件")
	}
}

// 同理:SaveState 与 LoadState 对「键 == item_id」的口径必须一致。
func TestSaveState_KeyItemIDMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab-a.json")
	err := patrol.SaveState(path, map[string]patrol.Finding{
		"disk-health": {ItemID: "journal-patterns", Status: "problem", Severity: "warning"},
	})
	if err == nil {
		t.Fatal("键与 item_id 不一致必须拒绝")
	}
}

// 0 字节/纯空白的状态文件是最可能的截断形状:按「文件损坏」处理,不当空状态。
func TestLoadState_EmptyFileRejected(t *testing.T) {
	for _, body := range []string{"", " \n\t\n"} {
		path := filepath.Join(t.TempDir(), "lab-a.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := patrol.LoadState(path); err == nil {
			t.Errorf("空状态文件(%q)必须报错", body)
		}
	}
}

func TestWriteFindings_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round", "findings.jsonl")
	want := []patrol.Finding{
		{
			Node:     "lab-a",
			ItemID:   "disk-health",
			Status:   "problem",
			Severity: "warning",
			Summary:  "inode 快用尽",
			Evidence: []patrol.Evidence{{Tool: "shell_exec", Input: `{"command":"df","args":["-i"]}`, Output: "/dev/vda1 93%", ReturnCode: 0}},
		},
		{Node: "lab-a", ItemID: "journal-patterns", Status: "ok"},
	}
	if err := patrol.WriteFindings(path, want); err != nil {
		t.Fatalf("WriteFindings 失败: %v", err)
	}
	got, err := patrol.ReadFindings(path)
	if err != nil {
		t.Fatalf("ReadFindings 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 条发现,实际 %d 条", len(got))
	}
	if got[0].Summary != "inode 快用尽" || len(got[0].Evidence) != 1 {
		t.Errorf("落盘再读回丢了字段: %+v", got[0])
	}
	if got[1].Status != "ok" {
		t.Errorf("第 2 条状态 = %q,期望 ok", got[1].Status)
	}
}

// findingIDs 抽出条目 id,失败信息里直接可比对。
func findingIDs(fs []patrol.Finding) []string {
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ItemID)
	}
	return ids
}
