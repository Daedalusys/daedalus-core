package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// cmdOwnership 实现子命令(ownership get <kind>/<name>,查询模式)。
func cmdOwnership(stdout, stderr io.Writer, args []string) int {
	if len(args) < 2 || args[0] != "get" {
		fmt.Fprintln(stderr, "usage: daedalus-host ownership get <kind>/<name>")
		return exitUsage
	}
	target := args[1]
	kind, name, ok := strings.Cut(target, "/")
	if !ok || kind == "" || name == "" {
		fmt.Fprintf(stderr, "daedalus-host ownership get: 期望 <kind>/<name>, got %q\n", target)
		return exitUsage
	}

	pol, err := policy.LoadOrDefault()
	if err != nil {
		fmt.Fprintln(stderr, "ownership: policy:", err)
		return exitRuntime
	}
	if err := pol.ValidateOwnership(); err != nil {
		fmt.Fprintln(stderr, "ownership: ownership:", err)
		return exitRuntime
	}

	var mode policy.OwnershipMode
	source := "default"
	if m, ok := pol.Ownership.ByKind[kind]; ok {
		mode = m
		source = "by_kind"
	} else {
		mode = pol.Ownership.Default
	}

	fmt.Fprintf(stdout, "mode=%s source=%s\n", mode, source)

	argsJSON := fmt.Sprintf(`{"kind":%q,"name":%q,"mode":%q,"source":%q}`, kind, name, mode, source)
	if v, err := audit.ParseValue(argsJSON); err == nil {
		_, _ = audit.LogAudit(audit.Entry{
			Identity: hostIdentity,
			Tool:     "host_ownership_get",
			Args:     v,
			Outcome:  "success",
		})
	}
	return exitOK
}