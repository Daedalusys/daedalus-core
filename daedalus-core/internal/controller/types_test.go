// types.go 的金样序列化测试:线上契约钉死,改动即破坏——任何字段/tag/键序
// 漂移都必须先改本测试再改类型(仿 tx_test.go:85 TestTx_JSONShape_Full 风格)。
package controller

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestObject_GoldenRoundTrip 钉死 Object 全字段 JSON 逐字节形态:
// 键序 = 字段声明序,snake_case tag,omit 语义,Result 两键不在其中。
func TestObject_GoldenRoundTrip(t *testing.T) {
	obj := Object{
		APIVersion: "v1",
		Kind:       "service",
		Metadata: Metadata{
			Name:        "sshd.service",
			Labels:      map[string]string{"app": "sshd", "tier": "system"},
			Annotations: map[string]string{"owner": "daedalus", "policy": "default"},
			Generation:  7,
		},
		Spec: json.RawMessage(`{"desired":"active"}`),
		Status: Status{
			ObservedGeneration: 7,
			Conditions: []Condition{{
				Type:               "Ready",
				Status:             "True",
				Reason:             "AsExpected",
				Message:            "unit active",
				LastTransitionTime: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
			}},
		},
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"api_version":"v1","kind":"service","metadata":{"name":"sshd.service","labels":{"app":"sshd","tier":"system"},"annotations":{"owner":"daedalus","policy":"default"},"generation":7},"spec":{"desired":"active"},"status":{"observed_generation":7,"conditions":[{"type":"Ready","status":"True","reason":"AsExpected","message":"unit active","last_transition_time":"2026-09-13T00:00:00Z"}]}}`
	if string(b) != want {
		t.Fatalf("Object JSON 漂移:\n got %s\nwant %s", b, want)
	}

	// 反序列化往返:逐字节金样必须无损还原为深度相等的对象。
	var back Object
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(obj, back) {
		t.Fatalf("往返不等:\n got %#v\nwant %#v", back, obj)
	}
}

// TestVerbGrammar 钉死标准动词文法的常量表(逐字):值集合恰为 7 枚、
// DesiredStateAbsent 哨兵为 "absent",且事务生命周期原语 begin/propose
// 绝不进动词表(它们不是资源操作,防顺手扩表)。
func TestVerbGrammar(t *testing.T) {
	// 测试内枚举 7 枚已声明 Op 常量(与 types.go 常量表一一对应)。
	ops := []Op{OpQuery, OpList, OpSet, OpDelete, OpApply, OpRollback, OpStatus}

	// 正例:7 枚常量值集合恰为 {query,list,set,delete,apply,rollback,status},逐字。
	want := map[Op]bool{
		"query":    true,
		"list":     true,
		"set":      true,
		"delete":   true,
		"apply":    true,
		"rollback": true,
		"status":   true,
	}
	got := map[Op]bool{}
	for _, op := range ops {
		if !want[op] {
			t.Fatalf("动词表外多出 token %q(需先改本测试再扩表)", string(op))
		}
		got[op] = true
	}
	if len(got) != len(want) {
		t.Fatalf("动词表数量漂移: got %d 枚, want %d 枚", len(got), len(want))
	}

	// 哨兵值逐字钉死:delete 语义在 desired_state 词汇中的缺席值。
	if DesiredStateAbsent != "absent" {
		t.Fatalf("DesiredStateAbsent 漂移: got %q, want \"absent\"", DesiredStateAbsent)
	}

	// 反例:事务生命周期原语 begin/propose 不是资源操作,永不进动词表。
	for _, banned := range []string{"begin", "propose"} {
		for _, op := range ops {
			if string(op) == banned {
				t.Fatalf("事务生命周期原语 %q 不得进动词表(begin/propose 不进动词表)", banned)
			}
		}
	}
}

// TestPackageHasNoFuncBodies 把"本包零函数体"契约变成可执行码:解析本目录
// 全部非 _test.go 文件,断言无任何 *ast.FuncDecl(纯类型契约层,校验归 v2
// runtime;todo 1 预告的缝契约由此钉死)。解析失败即 Fatal。
func TestPackageHasNoFuncBodies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				t.Errorf("%s 含函数声明 FuncDecl %q: 本包是纯类型契约层,零函数体(校验归 v2 runtime)", name, fn.Name.Name)
			}
		}
	}
}
