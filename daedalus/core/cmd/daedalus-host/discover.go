// discover.go: 插件目录扫描与 list/inspect 子命令。
//
// 目录契约(计划决策 22,镜像构建期内建):<pluginDir>/<id>/daedalus.plugin.json,
// 一层扫描,id = 目录名。扫描是幂等的,单个损坏插件绝不拖垮整体:
// 任何缺 manifest / 解析失败 / id 与目录名不一致 / sha256 校验失败的目录
// 都被标记 degraded 并附带原因,其余条目照常输出。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/daedalus-os/daedalus/core/internal/plugin"
)

// 插件状态枚举(list 的 STATUS 列)。
const (
	statusOK       = "ok"
	statusDegraded = "degraded"
)

// pluginState 是单个已安装插件目录的发现结果。
type pluginState struct {
	id       string           // 目录名(= 期望的插件 id)
	dir      string           // 插件目录绝对路径
	manifest *plugin.Manifest // nil 表示 manifest 缺失或不可解析
	status   string           // statusOK / statusDegraded
	reason   string           // degraded 的具体原因(ok 时为空)
}

// scanPlugins 扫描 root 下的一层子目录并逐一评估。仅当 root 本身不可读
// (不存在/不是目录)时返回错误;单插件损坏不报错,只标 degraded。
func scanPlugins(root string) ([]pluginState, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("插件目录不可读: %w", err)
	}
	var out []pluginState
	for _, e := range entries {
		if !e.IsDir() {
			continue // 游离文件不是插件,忽略。
		}
		out = append(out, loadPlugin(root, e.Name()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// loadPlugin 评估单个插件目录:目录名文法 → manifest 存在/可解析/字段合法
// → manifest id 与目录名一致 → sha256 完整性(复用 plugin.VerifyDir,不放宽)。
func loadPlugin(root, dirName string) pluginState {
	st := pluginState{id: dirName, dir: filepath.Join(root, dirName), status: statusDegraded}
	if !plugin.ValidID(dirName) {
		st.reason = fmt.Sprintf("目录名 %q 不匹配插件 id 文法", dirName)
		return st
	}
	m, err := plugin.LoadManifestFile(filepath.Join(st.dir, plugin.ManifestFileName))
	if err != nil {
		st.reason = err.Error()
		return st
	}
	if err := m.Validate(); err != nil {
		st.reason = fmt.Sprintf("manifest 校验失败: %v", err)
		return st
	}
	if m.ID != dirName {
		st.reason = fmt.Sprintf("manifest id %q 与目录名 %q 不一致", m.ID, dirName)
		return st
	}
	st.manifest = m // 字段展示不依赖摘要校验:即便降级也先给出 manifest 列。
	if _, err := plugin.VerifyDir(st.dir); err != nil {
		st.reason = err.Error()
		return st
	}
	st.status = statusOK
	st.reason = ""
	return st
}

// cmdList 打印全部插件的 id/name/version/type/runtime + 状态列。
// 表头 + 每插件一行;degraded 的完整原因在表格之后单独列出
// (tabwriter 的列对齐会被行内长文本破坏,故不混排)。
func cmdList(stdout, stderr io.Writer, root string) int {
	states, err := scanPlugins(root)
	if err != nil {
		fmt.Fprintf(stderr, "daedalus-host: list: %v\n", err)
		return exitRuntime
	}
	var reasons []string
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVERSION\tTYPE\tRUNTIME\tSTATUS")
	for _, st := range states {
		name, ver, typ, rt := "-", "-", "-", "-"
		if st.manifest != nil {
			name, ver, typ, rt = st.manifest.Name, st.manifest.Version, st.manifest.Type, st.manifest.Runtime
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", st.id, name, ver, typ, rt, st.status)
		if st.status == statusDegraded {
			reasons = append(reasons, fmt.Sprintf("  %s: %s", st.id, st.reason))
		}
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "daedalus-host: list: 输出失败: %v\n", err)
		return exitRuntime
	}
	if len(reasons) > 0 {
		fmt.Fprintf(stdout, "\ndegraded 明细:\n%s\n", strings.Join(reasons, "\n"))
	}
	return exitOK
}

// cmdInspect 打印单个插件的 manifest 详情(checksums 摘要、tools、permissions)
// 与完整性结论。manifest 不可读或完整性失败都以退出码 1 结束——
// inspect 的 0 退出必须意味着"该插件当前可通过校验",不给误导性成功。
func cmdInspect(stdout, stderr io.Writer, st pluginState) int {
	if st.manifest == nil {
		fmt.Fprintf(stderr, "daedalus-host: inspect: %s: %s\n", st.id, inspectNoManifestReason(st))
		return exitRuntime
	}
	m := st.manifest
	fmt.Fprintf(stdout, "id:          %s\n", m.ID)
	fmt.Fprintf(stdout, "name:        %s\n", m.Name)
	fmt.Fprintf(stdout, "version:     %s\n", m.Version)
	fmt.Fprintf(stdout, "type:        %s\n", m.Type)
	fmt.Fprintf(stdout, "runtime:     %s\n", m.Runtime)
	fmt.Fprintf(stdout, "executable:  %s\n", m.Executable)
	fmt.Fprintf(stdout, "dir:         %s\n", st.dir)
	fmt.Fprintf(stdout, "entrypoint:  %s\n", joinOrNone(m.Entrypoint))
	fmt.Fprintf(stdout, "tools:       %s\n", joinOrNone(m.Tools))
	fmt.Fprint(stdout, renderPermissions(m.Permissions))
	fmt.Fprintf(stdout, "checksums:   %d 条\n", len(m.Checksums))
	for _, name := range sortedKeys(m.Checksums) {
		fmt.Fprintf(stdout, "  %s  %s\n", name, m.Checksums[name])
	}
	if st.status != statusOK {
		fmt.Fprintf(stderr, "daedalus-host: inspect: %s 完整性 DEGRADED: %s\n", st.id, st.reason)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "integrity:   ok(sha256 全部匹配)\n")
	return exitOK
}

// inspectNoManifestReason 给出"为什么拿不到 manifest"的可读原因。
func inspectNoManifestReason(st pluginState) string {
	if st.reason != "" {
		return st.reason
	}
	return "manifest 不可用"
}

// renderPermissions 把声明式权限渲染为可读块(nil = 未声明)。
func renderPermissions(p *plugin.Permissions) string {
	if p == nil {
		return "permissions: (未声明)\n"
	}
	var sb strings.Builder
	sb.WriteString("permissions:\n")
	fmt.Fprintf(&sb, "  read:  %s\n", joinOrNone(p.Read))
	fmt.Fprintf(&sb, "  write: %s\n", joinOrNone(p.Write))
	fmt.Fprintf(&sb, "  run:   %s\n", joinOrNone(p.Run))
	return sb.String()
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "(无)"
	}
	return strings.Join(list, " ")
}

// sortedKeys 返回 map 键的升序切片(与 Python sort_keys 的展示习惯一致)。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// marshalArgs 把审计 args 载荷序列化为 JSON 文本(键序由 Go map 排序保证确定)。
func marshalArgs(fields map[string]string) json.RawMessage {
	data, err := json.Marshal(fields)
	if err != nil { // map[string]string 不可能失败,兜底为空对象。
		return json.RawMessage("{}")
	}
	return data
}
