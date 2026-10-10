package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// writeShellScript 写一个 stub 脚本(可执行),返回路径。
func writeShellScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nset -e\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// recordFromAuditLog 解析 audit.jsonl 拿 (tool, outcome) 元组列表。
func recordFromAuditLog(t *testing.T, path string) []audit.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []audit.Record
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		v, err := audit.ParseValue(line)
		if err != nil {
			continue
		}
		rec := audit.Record{}
		if s, ok := v.LookupString("tool"); ok {
			rec.Tool = s
		}
		if s, ok := v.LookupString("outcome"); ok {
			rec.Outcome = s
		}
		out = append(out, rec)
	}
	return out
}

func TestDriver_ApplyDesired_InvokesTxBinary(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tx.log")
	txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
	t.Setenv("LOG_FILE", logPath)

	d := &runtime.Driver{TxBinary: txBin}
	specJSON, _ := json.Marshal(objectmodel.ResourceSpec{DesiredState: "active"})
	obj := controller.Object{
		Kind:     objectmodel.KindService,
		Metadata: controller.Metadata{Name: "nginx.service"},
		Spec:     specJSON,
	}
	if err := d.ApplyDesired(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "begin") {
		t.Errorf("tx 二进制应被以 begin 调用, got %q", string(data))
	}
	if !strings.Contains(string(data), "service.set") {
		t.Errorf("应含 service.set adapter, got %q", string(data))
	}
}

func TestDriver_ApplyDesired_AuditsReconcileDrive(t *testing.T) {
	txBin := writeShellScript(t, `exit 0`)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, auditPath)

	d := &runtime.Driver{TxBinary: txBin}
	obj := controller.Object{Kind: objectmodel.KindService, Metadata: controller.Metadata{Name: "nginx.service"}}
	if err := d.ApplyDesired(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	entries := recordFromAuditLog(t, auditPath)
	found := false
	for _, e := range entries {
		if e.Tool == "controller_reconcile_drive" {
			found = true
		}
	}
	if !found {
		t.Errorf("应写一条 controller_reconcile_drive 审计, got %+v", entries)
	}
}

func TestDriver_ApplyDesired_TxFailureAuditsError(t *testing.T) {
	txBin := writeShellScript(t, `exit 7`)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, auditPath)

	d := &runtime.Driver{TxBinary: txBin}
	obj := controller.Object{Kind: objectmodel.KindService, Metadata: controller.Metadata{Name: "nginx.service"}}
	if err := d.ApplyDesired(context.Background(), obj); err == nil {
		t.Fatal("非零 rc 应报错")
	}
	entries := recordFromAuditLog(t, auditPath)
	found := false
	for _, e := range entries {
		if e.Tool == "controller_reconcile_drive" && e.Outcome == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("应写一条 outcome=error 的 reconcile_drive 审计, got %+v", entries)
	}
}