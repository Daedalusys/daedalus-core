package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// cmdController 实现子命令(controller start [--foreground] [--poll-interval D])。
func cmdController(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 || args[0] != "start" {
		fmt.Fprintln(stderr, "usage: daedalus-host controller start [--foreground] [--poll-interval D]")
		return exitUsage
	}
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	intervalF := fs.Duration("poll-interval", 2*time.Second, "informer poll interval")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}

	pol, err := policy.LoadOrDefault()
	if err != nil {
		fmt.Fprintln(stderr, "controller: policy:", err)
		return exitRuntime
	}
	if err := pol.ValidateOwnership(); err != nil {
		fmt.Fprintln(stderr, "controller: ownership:", err)
		return exitRuntime
	}

	modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
		if m, ok := pol.Ownership.ByKind[string(k)]; ok {
			return m
		}
		return pol.Ownership.Default
	}
	objectFor := func(k objectmodel.Kind, n string) (controller.Object, error) {
		return runtime.ObjectFromViewAndState(k, n)
	}

	reg := &runtime.Registry{}
	reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		d := &runtime.Driver{}
		return controller.Result{}, d.ApplyDesired(ctx, obj)
	})
	reg.Register(objectmodel.KindPackage, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		d := &runtime.Driver{}
		return controller.Result{}, d.ApplyDesired(ctx, obj)
	})

	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	argsJSON := fmt.Sprintf(`{"interval_ns":%d}`, intervalF.Nanoseconds())
	if v, err := audit.ParseValue(argsJSON); err == nil {
		_, _ = audit.LogAudit(audit.Entry{
			Identity: hostIdentity,
			Tool:     "host_controller_start",
			Args:     v,
			Outcome:  "success",
		})
	}

	if err := runtime.RunLoop(runCtx, *intervalF, reg, modeFor, objectFor); err != nil && runCtx.Err() == nil {
		fmt.Fprintln(stderr, "controller: loop:", err)
		return exitRuntime
	}
	return exitOK
}