package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
	"github.com/Daedalusys/daedalus-sdk/state"
)

// cmdDrift 实现子命令(drift,扫描 desiredview 与 state.jsonl 比对)。
// 签名沿用 host main dispatch:`cmdXxx(stdout, stderr, args) int`。
func cmdDrift(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	kindF := fs.String("kind", "", "filter by kind")
	nameF := fs.String("name", "", "filter by name")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "daedalus-host drift:", err)
		return exitUsage
	}

	pol, err := policy.LoadOrDefault()
	if err != nil {
		fmt.Fprintln(stderr, "drift: policy:", err)
		return exitRuntime
	}
	if err := pol.ValidateOwnership(); err != nil {
		fmt.Fprintln(stderr, "drift: ownership:", err)
		return exitRuntime
	}

	view, err := desiredview.Load()
	if err != nil {
		fmt.Fprintln(stderr, "drift: desiredview:", err)
		return exitRuntime
	}
	entries, err := state.Read(time.Time{})
	if err != nil {
		fmt.Fprintln(stderr, "drift: state:", err)
		return exitRuntime
	}
	observed := indexState(entries)
	modeFor := func(k objectmodel.Kind, n string) policy.OwnershipMode {
		if m, ok := pol.Ownership.ByKind[string(k)]; ok {
			return m
		}
		return pol.Ownership.Default
	}
	rep := runtime.Compute(view, observed, modeFor)

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tNAME\tDESIRED\tOBSERVED\tSOURCE_TX\tDRIFT")
	for _, e := range rep.Entries {
		if *kindF != "" && string(e.Kind) != *kindF {
			continue
		}
		if *nameF != "" && e.Name != *nameF {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\n",
			e.Kind, e.Name, e.Desired, e.Observed, e.SourceTx, e.Drifts)
	}
	_ = tw.Flush()

	argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q}`, *kindF, *nameF)
	if v, err := audit.ParseValue(argsJSON); err == nil {
		_, _ = audit.LogAudit(audit.Entry{
			Identity: hostIdentity,
			Tool:     "host_drift",
			Args:     v,
			Outcome:  "success",
		})
	}
	return exitOK
}

// indexState 把 state.Read 的扁平列表索引为 map[Kind]map[string]ServiceState;
// 同一 (kind, name) 多次出现时取 ObservedAt 最晚的(state 是 append-only 缓存)。
func indexState(entries []state.StateEntry) map[objectmodel.Kind]map[string]objectmodel.ServiceState {
	out := map[objectmodel.Kind]map[string]objectmodel.ServiceState{}
	lastSeen := map[objectmodel.Kind]map[string]time.Time{}
	for _, e := range entries {
		var st objectmodel.ServiceState
		if err := json.Unmarshal(e.Payload, &st); err != nil {
			continue // 损坏行静默跳过(design RF4)
		}
		if out[objectmodel.Kind(e.Kind)] == nil {
			out[objectmodel.Kind(e.Kind)] = map[string]objectmodel.ServiceState{}
			lastSeen[objectmodel.Kind(e.Kind)] = map[string]time.Time{}
		}
		if prev, ok := lastSeen[objectmodel.Kind(e.Kind)][e.Name]; !ok || e.ObservedAt.After(prev) {
			out[objectmodel.Kind(e.Kind)][e.Name] = st
			lastSeen[objectmodel.Kind(e.Kind)][e.Name] = e.ObservedAt
		}
	}
	return out
}