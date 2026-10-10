package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// Driver 把 ReconcileFunc 的写动作翻译为一次 daedalus-tx 子进程调用。
// 命令:daedalus-tx begin --steps '<json>' --auto-apply。
type Driver struct {
	TxBinary  string        // 默认 "daedalus-tx"
	TxTimeout time.Duration // 默认 30s
}

// ApplyDesired 调起 daedalus-tx 子进程;非零 rc 写 error 审计。
func (d *Driver) ApplyDesired(ctx context.Context, obj controller.Object) error {
	bin := d.TxBinary
	if bin == "" {
		bin = "daedalus-tx"
	}
	timeout := d.TxTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	var desired string
	var rs objectmodel.ResourceSpec
	if err := json.Unmarshal(obj.Spec, &rs); err == nil {
		desired = rs.DesiredState
	}
	step := map[string]any{
		"adapter": adapterForKind(obj.Kind),
		"args": map[string]any{
			"name":          obj.Metadata.Name,
			"desired_state": desired,
		},
	}
	stepsJSON, _ := json.Marshal([]any{step})
	args := []string{"begin", "--steps", string(stepsJSON), "--auto-apply"}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = timeout
	out, err := cmd.CombinedOutput()

	outcome := "success"
	rc := -1
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		outcome = "error"
	}

	argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q,"tx_rc":%d,"output":%q}`,
		obj.Kind, obj.Metadata.Name, rc, string(out))
	if v, perr := audit.ParseValue(argsJSON); perr == nil {
		_, _ = audit.LogAudit(audit.Entry{
			Identity:      "controller",
			Tool:          "controller_reconcile_drive",
			Args:          v,
			Outcome:       outcome,
			PolicyVersion: audit.DefaultPolicyVersion,
		})
	}
	if err != nil {
		return fmt.Errorf("driver: daedalus-tx apply failed (rc=%d output=%s): %w", rc, string(out), err)
	}
	return nil
}

// adapterForKind 把 kind 翻译为 daedalus-tx 适配器名。
func adapterForKind(k objectmodel.Kind) string {
	switch k {
	case objectmodel.KindService:
		return "service.set"
	case objectmodel.KindPackage:
		return "package.set"
	}
	return string(k)
}