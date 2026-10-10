//go:build integration

package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
	"github.com/Daedalusys/daedalus-sdk/state"
)

// TestE2E_NginxActive 端到端测试:view 注入 + state 空 → managed/reconcile 模式
// 下 controller 经 daedalus-tx(stub) 触发一次 begin 调用。
func TestE2E_NginxActive(t *testing.T) {
	tmp := t.TempDir()
	auditPath := filepath.Join(tmp, "audit.jsonl")
	t.Setenv(audit.EnvLogPath, auditPath)
	pf := filepath.Join(tmp, "policy.toml")
	if err := os.WriteFile(pf, []byte(`
[objectmodel]
enabled_kinds = ["service"]
[ownership]
default = "managed/reconcile"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEDALUS_POLICY_PATH", pf)

	logPath := filepath.Join(tmp, "tx.log")
	txBin := writeShellScript(t, `echo "$@" >> "$LOG_FILE"`)
	t.Setenv("LOG_FILE", logPath)

	view := desiredview.ViewWithForTest([]desiredview.Entry{
		{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"},
	})
	runtime.SetViewLoaderForTest(func() (*desiredview.View, error) { return view, nil })
	runtime.SetStateReaderForTest(func() ([]state.StateEntry, error) { return nil, nil })
	t.Cleanup(runtime.ResetInjections)

	reg := &runtime.Registry{}
	reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		d := &runtime.Driver{TxBinary: txBin}
		return controller.Result{}, d.ApplyDesired(ctx, obj)
	})

	modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
		return policy.OwnershipManagedReconcile
	}
	objectFor := func(k objectmodel.Kind, n string) (controller.Object, error) {
		return controller.Object{
			Kind:     k,
			Metadata: controller.Metadata{Name: n, Generation: 5},
			Spec:     json.RawMessage(`{"desired_state":"active"}`),
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = runtime.RunLoop(ctx, 100*time.Millisecond, reg, modeFor, objectFor)

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "begin") {
		t.Errorf("应触发一次 daedalus-tx begin, tx.log=%q", string(data))
	}
	if !strings.Contains(string(data), "service.set") {
		t.Errorf("应含 service.set adapter, tx.log=%q", string(data))
	}
	if !strings.Contains(string(data), "nginx.service") {
		t.Errorf("应含 nginx.service, tx.log=%q", string(data))
	}
}