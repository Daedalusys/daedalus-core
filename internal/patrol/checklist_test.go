package patrol_test

// 本文件钉住清单解析的两件事:合并语义(全局基线 + 节点专属段逐字段覆盖)
// 与 fail-closed 校验(未知键 / 非法 id / 空 prompt / 重复 id / 非法 severity /
// 空工具名一律拒)。夹具在 testdata/ 下,一个夹具一种坏,便于定位是哪条规则被触发。

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
)

func TestLoadChecklist_GlobalPlusPerNode(t *testing.T) {
	c, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-full.toml"), "lab-a")
	if err != nil {
		t.Fatalf("LoadChecklist 失败: %v", err)
	}
	if len(c.Items) != 3 {
		t.Fatalf("合并后应有 3 项,实际 %d 项: %+v", len(c.Items), c.Items)
	}

	// 同 id 的全局项:位置不动,被本机段逐字段覆盖;本机没声明的字段(tools)沿用全局值。
	got := c.Items[0]
	if got.ID != "disk-health" {
		t.Errorf("第 1 项 id = %q,期望 disk-health", got.ID)
	}
	if got.Prompt != "lab-a 的磁盘(含 KVM 阵列)有哪些健康问题迹象?" {
		t.Errorf("第 1 项 prompt 未被本机段覆盖: %q", got.Prompt)
	}
	if got.Severity != "critical" {
		t.Errorf("第 1 项 severity = %q,期望 critical(本机段覆盖)", got.Severity)
	}
	if !slices.Equal(got.Tools, []string{"shell_exec", "fs_read"}) {
		t.Errorf("第 1 项 tools = %v,期望沿用全局值 [shell_exec fs_read]", got.Tools)
	}
	if !got.Enabled {
		t.Error("第 1 项应默认启用")
	}

	// 全局独有的项:原样保留。
	if c.Items[1].ID != "journal-patterns" {
		t.Errorf("第 2 项 id = %q,期望 journal-patterns", c.Items[1].ID)
	}
	if c.Items[1].Severity != "warning" {
		t.Errorf("第 2 项 severity = %q,期望 warning", c.Items[1].Severity)
	}

	// 本机新增项:追加在末尾。
	if c.Items[2].ID != "kvm-storage" {
		t.Errorf("第 3 项 id = %q,期望 kvm-storage(本机新项应在末尾)", c.Items[2].ID)
	}
}

func TestLoadChecklist_UnknownNodeHasNoExtraItems(t *testing.T) {
	c, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-full.toml"), "lab-b")
	if err != nil {
		t.Fatalf("LoadChecklist 失败: %v", err)
	}
	if len(c.Items) != 2 {
		t.Fatalf("未知节点只应有全局 2 项,实际 %d 项: %+v", len(c.Items), c.Items)
	}
	if c.Items[0].Prompt == "lab-a 的磁盘(含 KVM 阵列)有哪些健康问题迹象?" {
		t.Error("lab-b 不该继承 lab-a 的覆盖值")
	}
	if c.Items[0].Severity != "warning" {
		t.Errorf("lab-b 的 severity = %q,期望全局值 warning", c.Items[0].Severity)
	}
}

func TestLoadChecklist_UnknownKeyRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-unknown-key.toml"), "lab-a")
	if err == nil {
		t.Fatal("未知键必须拒绝")
	}
}

func TestLoadChecklist_EmptyPromptRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-empty-prompt.toml"), "lab-a")
	if err == nil {
		t.Fatal("空 prompt 必须拒绝")
	}
}

func TestLoadChecklist_DuplicateIDRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-dup-id.toml"), "lab-a")
	if err == nil {
		t.Fatal("合并后 id 重复必须拒绝")
	}
}

func TestLoadChecklist_NodeDuplicateIDRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-node-dup-id.toml"), "lab-a")
	if err == nil {
		t.Fatal("节点段内 id 重复必须拒绝")
	}
}

func TestLoadChecklist_InvalidIDRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-invalid-id.toml"), "lab-a")
	if err == nil {
		t.Fatal("非法 id 必须拒绝")
	}
}

func TestLoadChecklist_BadSeverityRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-bad-severity.toml"), "lab-a")
	if err == nil {
		t.Fatal("表外 severity 必须拒绝")
	}
}

func TestLoadChecklist_EmptyToolRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-empty-tool.toml"), "lab-a")
	if err == nil {
		t.Fatal("空工具名必须拒绝")
	}
}

// 节点段里独立存在(全局没有同名项)的清单项:severity 不会被继承,缺失即非法。
func TestLoadChecklist_NodeOnlyItemNeedsSeverity(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-node-missing-severity.toml"), "lab-a")
	if err == nil {
		t.Fatal("节点专属项缺 severity 必须拒绝")
	}
}

// 显式 disabled 的项照旧进清单(清单是配置,开关是配置的一部分),
// 由调用方决定跑不跑 —— 巡检链路按 enabled 过滤。
func TestLoadChecklist_DisabledItemKept(t *testing.T) {
	c, err := patrol.LoadChecklist(filepath.Join("testdata", "checklist-disabled.toml"), "lab-a")
	if err != nil {
		t.Fatalf("LoadChecklist 失败: %v", err)
	}
	if len(c.Items) != 1 {
		t.Fatalf("应有 1 项,实际 %d 项", len(c.Items))
	}
	if c.Items[0].Enabled {
		t.Error("enabled = false 的项不该是启用状态")
	}
}

func TestLoadChecklist_MissingFileRejected(t *testing.T) {
	_, err := patrol.LoadChecklist(filepath.Join("testdata", "no-such-file.toml"), "lab-a")
	if err == nil {
		t.Fatal("清单文件缺失必须报错(不能当成空清单静默跑一轮)")
	}
}

// 出厂默认配置必须永远可解析:它是「清单为空」的合法形态,不是坏文件。
func TestLoadChecklist_ShippedDefaultParses(t *testing.T) {
	c, err := patrol.LoadChecklist(filepath.Join("..", "..", "files", "system", "etc", "daedalus", "patrol.toml"), "lab-a")
	if err != nil {
		t.Fatalf("出厂默认配置解析失败: %v", err)
	}
	if len(c.Items) != 0 {
		t.Errorf("出厂默认配置的清单应为空,实际 %d 项", len(c.Items))
	}
}

// 节点 id 会被后续任务拼进状态文件路径(/var/lib/daedalus-patrol/state/<node>.json),
// 含路径分隔符或 .. 的形状必须在读清单这一步就拒掉。
func TestLoadChecklist_NodeIDWhitelist(t *testing.T) {
	full := filepath.Join("testdata", "checklist-full.toml")
	good := []string{"", "lab-a", "lab.a_1-2", "LAB01"}
	for _, id := range good {
		if _, err := patrol.LoadChecklist(full, id); err != nil {
			t.Errorf("节点 id %q 应被接受,却报错: %v", id, err)
		}
	}
	bad := []string{"..", ".", "../etc", "a/b", "lab a", "lab-a\n", "lab/../a"}
	for _, id := range bad {
		if _, err := patrol.LoadChecklist(full, id); err == nil {
			t.Errorf("节点 id %q 必须拒绝", id)
		}
	}
}

// 配置文件里的节点键同样会被拼进路径:键非法比「将来某次请求恰好用到」更早拒。
func TestLoadConfig_InvalidNodeKeyRejected(t *testing.T) {
	dir := t.TempDir()
	body := `[[node."../etc".checklist.item]]
id = "disk-health"
prompt = "磁盘健康?"
severity = "warning"
`
	path := filepath.Join(dir, "patrol.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := patrol.LoadConfig(path); err == nil {
		t.Fatal("非法节点键必须拒绝")
	}
}

// Config 是只读快照:调用方改了拿到的清单,不该回写进下一次查询。
func TestChecklistFor_ReturnsCopy(t *testing.T) {
	cfg, err := patrol.LoadConfig(filepath.Join("testdata", "checklist-full.toml"))
	if err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}
	mutated := cfg.ChecklistFor("lab-a")
	mutated.Items[0].Prompt = "被改坏"
	mutated.Items[0].Tools[0] = "evil_tool"

	again := cfg.ChecklistFor("lab-a")
	if again.Items[0].Prompt == "被改坏" {
		t.Error("调用方改到了 Config 内部的清单项")
	}
	if again.Items[0].Tools[0] == "evil_tool" {
		t.Error("调用方改到了 Config 内部的 tools 切片")
	}
}
