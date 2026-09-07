// manifest.go resources 字段(todo 2)的专项测试:接受面/拒绝面/JSON 边界,
// 以及对仓库内 5 个官方插件 manifest 源文件的向后兼容硬断言。
package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daedalus-os/daedalus/core/internal/objectmodel"
)

// TestManifest_Resources_Accepts 钉死 todo 2 的接受面:合法 resources 条目
// (含 "*" 通配与空 desired_state)、字段缺席/为空数组时行为与旧版完全一致
// (向后兼容:5 个官方 manifest 均无该字段,必须照常通过)。
func TestManifest_Resources_Accepts(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Manifest)
	}{
		{"省略 resources(向后兼容)", func(m *Manifest) {}},
		{"空数组", func(m *Manifest) { m.Resources = []*objectmodel.Resource{} }},
		{"service 通配声明", func(m *Manifest) {
			m.Resources = []*objectmodel.Resource{{Kind: objectmodel.KindService, Name: "*", DesiredState: ""}}
		}},
		{"含期望状态的条目", func(m *Manifest) {
			m.Resources = []*objectmodel.Resource{{Kind: objectmodel.KindService, Name: "sshd", DesiredState: "active"}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mut(m)
			if err := m.Validate(); err != nil {
				t.Fatalf("合法 resources 被拒绝: %v", err)
			}
		})
	}
}

// TestManifest_Resources_Rejects 钉死 todo 2 的拒绝面:每类畸形输入都必须
// 被拒,且错误消息带 resources[i] 字段路径(与 tools[1] 的既有风格同源)。
func TestManifest_Resources_Rejects(t *testing.T) {
	tests := []struct {
		name      string
		res       []*objectmodel.Resource
		wantInMsg string
	}{
		{"未知 kind", []*objectmodel.Resource{{Kind: "machine", Name: "*"}}, "resources[0]"},
		{"kind 大小写变体(线协议 token 严格小写)", []*objectmodel.Resource{{Kind: "Service", Name: "*"}}, "resources[0]"},
		{"kind 为空", []*objectmodel.Resource{{Kind: "", Name: "*"}}, "resources[0]"},
		{"name 为空", []*objectmodel.Resource{{Kind: objectmodel.KindService, Name: ""}}, "resources[0]"},
		{"name 含空字节", []*objectmodel.Resource{{Kind: objectmodel.KindService, Name: "a\x00b"}}, "resources[0]"},
		{"name 含路径分隔符", []*objectmodel.Resource{{Kind: objectmodel.KindService, Name: "etc/passwd"}}, "resources[0]"},
		{"数组元素为 null", []*objectmodel.Resource{nil}, "resources[0]"},
		{"第二个元素非法(索引路径)", []*objectmodel.Resource{
			{Kind: objectmodel.KindService, Name: "*"},
			{Kind: "bogus", Name: "*"},
		}, "resources[1]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			m.Resources = tc.res
			err := m.Validate()
			if err == nil {
				t.Fatalf("非法 resources 被接受(期望错误包含 %q)", tc.wantInMsg)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Errorf("错误消息缺少字段路径: got %q, want 包含 %q", err.Error(), tc.wantInMsg)
			}
		})
	}
}

// TestParseManifest_Resources 钉死 resources 的 JSON 边界:合法数组解析进字段;
// 条目内未知字段与顶层未知字段同样被 DisallowUnknownFields 拒绝(拼错的
// 声明键不得静默丢弃);字段缺席时保持 nil(omitempty 往返)。
func TestParseManifest_Resources(t *testing.T) {
	const base = `{"id":"a.b","name":"N","version":"1.0.0","type":"capability","runtime":"native","executable":"bin/x"`

	t.Run("合法数组解析", func(t *testing.T) {
		m, err := ParseManifest([]byte(base + `,"resources":[{"kind":"service","name":"*","desired_state":""}]}`))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(m.Resources) != 1 {
			t.Fatalf("resources 未解析进字段: %#v", m.Resources)
		}
		if m.Resources[0].Kind != objectmodel.KindService || m.Resources[0].Name != "*" {
			t.Errorf("resources 字段值错误: %+v", *m.Resources[0])
		}
	})
	t.Run("null 元素解析后可被 Validate 拒绝", func(t *testing.T) {
		m, err := ParseManifest([]byte(base + `,"resources":[null]}`))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "resources[0]") {
			t.Fatalf("null 元素未被拒绝: %v", err)
		}
	})
	t.Run("条目内未知字段", func(t *testing.T) {
		_, err := ParseManifest([]byte(base + `,"resources":[{"kind":"service","name":"*","vendor":"evil"}]}`))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("resources 条目内未知字段未被拒绝: %v", err)
		}
	})
	t.Run("字段缺席时为 nil", func(t *testing.T) {
		m, err := ParseManifest([]byte(base + `}`))
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if m.Resources != nil {
			t.Errorf("字段缺席应得 nil,得 %#v", m.Resources)
		}
	})
}

// TestValidate_OfficialPluginManifests 向后兼容硬断言:仓库内 5 个官方插件
// manifest 源文件(fs/shell/pkg/sysinfo/copilot)在新字段缺席下必须照常
// ParseManifest + Validate 全绿(计划 todo 2 acceptance 的向后兼容条款)。
func TestValidate_OfficialPluginManifests(t *testing.T) {
	ids := []string{"fs", "shell", "pkg", "sysinfo", "copilot"}
	for _, id := range ids {
		t.Run("daedalus."+id, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "plugin", id, ManifestFileName)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", path, err)
			}
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatalf("官方 manifest 解析失败: %v", err)
			}
			if err := m.Validate(); err != nil {
				t.Fatalf("官方 manifest Validate 失败: %v", err)
			}
		})
	}
}
