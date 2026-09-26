package desiredview

// desiredview_test.go —— 钉死 docs/desired-state-projection.md 的投影语义:
// T1/T2/T3 锚例、五类失败路径、封闭映射与 fail-closed 解析门、
// List→Project 全链路(临时 journal 根)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

func appliedTx(id string, at time.Time, steps ...tx.Step) *tx.Transaction {
	return &tx.Transaction{ID: id, CreatedAt: at, Status: tx.StatusApplied, Steps: steps}
}

func setStep(t *testing.T, adapter, name, desired string) tx.Step {
	t.Helper()
	raw, err := json.Marshal(stepArgs{Name: name, DesiredState: &desired})
	if err != nil {
		t.Fatal(err)
	}
	return tx.Step{Adapter: adapter, Args: raw}
}

// TestT1T2T3 锚例:T1 active、T2 inactive、rollback T2(状态翻 rolled_back)
// ⇒ 当前期望回到 active,source_tx 指回 T1。
func TestT1T2T3(t *testing.T) {
	t0 := time.Unix(1767225600, 0).UTC()
	t1 := appliedTx("1111111111111111", t0, setStep(t, "service.set", "nginx", "active"))
	t2 := appliedTx("2222222222222222", t0.Add(time.Hour), setStep(t, "service.set", "nginx", "inactive"))
	t2.Status = tx.StatusRolledBack // 模拟 tx rollback 后的最新状态

	v, err := Project([]*tx.Transaction{t1, t2})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := v.Get(objectmodel.KindService, "nginx")
	if !ok {
		t.Fatal("nginx 应有期望")
	}
	if e.DesiredState != "active" || e.SourceTx != t1.ID {
		t.Fatalf("rollback T2 后期望应为 active@T1, got %s@%s", e.DesiredState, e.SourceTx)
	}
}

// TestOnlyAppliedContributes 钉失败路径:proposed/applying/failed 一律不进视图
// (apply failed、partial apply、abandoned 共用这条规则)。
func TestOnlyAppliedContributes(t *testing.T) {
	t0 := time.Unix(1767225600, 0).UTC()
	step := setStep(t, "service.set", "nginx", "inactive")
	var txs []*tx.Transaction
	for i, st := range []tx.Status{tx.StatusProposed, tx.StatusApplying, tx.StatusFailed} {
		txs = append(txs, &tx.Transaction{
			ID: strings.Repeat(string('a'+rune(i)), 16), CreatedAt: t0, Status: st,
			Steps: []tx.Step{step},
		})
	}
	v, err := Project(txs)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(v.Entries()); n != 0 {
		t.Fatalf("非 applied 状态不得贡献期望, got %d 行", n)
	}
}

// TestLastWriteWins 同事务内多步 + 跨事务:同资源后写覆盖前写;
// superseded 无需显式状态。
func TestLastWriteWins(t *testing.T) {
	t0 := time.Unix(1767225600, 0).UTC()
	a := appliedTx("aaaaaaaaaaaaaaaa", t0,
		setStep(t, "service.set", "nginx", "inactive"),
		setStep(t, "service.set", "nginx", "active"),
		setStep(t, "package.set", "curl", "present"),
	)
	b := appliedTx("bbbbbbbbbbbbbbbb", t0.Add(time.Minute),
		setStep(t, "service.set", "nginx", "unmanaged-nope")) // 词汇表外也照投:provider 层管词表
	v, err := Project([]*tx.Transaction{a, b})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := v.Get(objectmodel.KindService, "nginx")
	if svc.DesiredState != "unmanaged-nope" || svc.SourceTx != b.ID {
		t.Fatalf("后写应胜出: %s@%s", svc.DesiredState, svc.SourceTx)
	}
	pkg, ok := v.Get(objectmodel.KindPackage, "curl")
	if !ok || pkg.DesiredState != "present" || pkg.SourceTx != a.ID {
		t.Fatalf("同事务其他资源行丢失或错源: %+v", pkg)
	}
	// Entries 确定性排序:(kind,name) 升序 → package 在 service 前。
	es := v.Entries()
	if len(es) != 2 || es[0].Kind != objectmodel.KindPackage {
		t.Fatalf("Entries 排序异常: %+v", es)
	}
}

// TestClosedMapping 封闭映射:表外 adapter 忽略;表内 args 损坏/未知键/空名
// 一律整体报错(fail-closed,不静默丢期望)。
func TestClosedMapping(t *testing.T) {
	t0 := time.Unix(1767225600, 0).UTC()

	ok := appliedTx("cccccccccccccccc", t0, tx.Step{
		Adapter: "blueprint.write",
		Args:    json.RawMessage(`{"path":"/etc/nginx/conf.d/x.conf"}`),
	})
	v, err := Project([]*tx.Transaction{ok})
	if err != nil || len(v.Entries()) != 0 {
		t.Fatalf("表外 adapter 应被忽略: %v %v", err, v.Entries())
	}

	for _, bad := range []string{`{"name":"nginx"}`, `{"name":"","desired_state":"active"}`, `{`, `{"name":"nginx","desired_state":"active","evil":1}`} {
		broken := appliedTx("dddddddddddddddd", t0, tx.Step{Adapter: "service.set", Args: json.RawMessage(bad)})
		if _, err := Project([]*tx.Transaction{broken}); err == nil {
			t.Fatalf("损坏 args 应整体报错(fail-closed): %s", bad)
		}
	}
}

// TestListAndLoadRoundTrip 全链路:真实 journal 根(temp dir)下
// begin→propose→apply 两笔 + 一笔 rolled_back,Load 投影正确。
func TestListAndLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAEDALUS_TX_DIR", root)

	newApplied := func(t *testing.T, adapter, name, desired string) *tx.Transaction {
		t.Helper()
		tr, err := tx.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.Append(setStep(t, adapter, name, desired)); err != nil {
			t.Fatal(err)
		}
		if err := tr.MarkApplying(); err != nil {
			t.Fatal(err)
		}
		if err := tr.MarkApplied(); err != nil {
			t.Fatal(err)
		}
		return tr
	}

	nginx := newApplied(t, "service.set", "nginx", "active")
	curl := newApplied(t, "package.set", "curl", "latest")
	docker := newApplied(t, "service.set", "docker", "inactive")
	if err := docker.MarkRolledBack(); err != nil {
		t.Fatal(err)
	}

	v, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := v.Get(objectmodel.KindService, "nginx")
	if !ok || e.SourceTx != nginx.ID {
		t.Fatalf("nginx 投影缺失或错源: %+v", e)
	}
	if _, ok := v.Get(objectmodel.KindService, "docker"); ok {
		t.Fatal("rolled_back 事务不得进视图")
	}
	if e2, ok := v.Get(objectmodel.KindPackage, "curl"); !ok || e2.SourceTx != curl.ID {
		t.Fatalf("curl 投影缺失或错源: %+v", e2)
	}
}

// TestListOrderingAndStrays 钉 List:杂散文件跳过、同刻按 id 升序、
// 合法命名但内容损坏 → fail-closed 报错。
func TestListOrderingAndStrays(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAEDALUS_TX_DIR", root)

	write := func(id, created string, status tx.Status) {
		doc := map[string]any{
			"id": id, "created_at": created, "status": status,
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, id+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := "2026-01-01T00:00:00Z"
	write("bbbbbbbbbbbbbbbb", now, tx.StatusApplied)
	write("aaaaaaaaaaaaaaaa", now, tx.StatusApplied)
	write("0000000000000000", "2025-06-01T00:00:00Z", tx.StatusApplied)

	for name, content := range map[string]string{
		"README.md":           "杂散",
		"notahexdigitid.json": "杂散命名",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	txs, err := tx.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{txs[0].ID, txs[1].ID, txs[2].ID}; !slices.Equal(got,
		[]string{"0000000000000000", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}) {
		t.Fatalf("回放序应为 created_at 升序 + 同刻 id 升序, got %v", got)
	}

	if err := os.WriteFile(filepath.Join(root, "cccccccccccccccc.json"), []byte("{ 撕裂"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.List(); err == nil {
		t.Fatal("合法命名的损坏 journal 必须报错(fail-closed)")
	}
}
