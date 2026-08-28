// Command daedalus-plugin-pack 是 daedalus-plugin 插件包的打包/校验 CLI。
//
// 用法:
//
//	daedalus-plugin-pack -in <插件目录> -out <输出.zip>   # 打包
//	daedalus-plugin-pack -verify <插件包.zip> [--keep <解压目录>]  # 校验
//
// 打包:读取目录内 daedalus.plugin.json,校验后把全部文件写入 zip,
// 并自动注入逐条目 sha256 checksums(manifest 自身按规范化 JSON 摘要)。
// 校验:带 zip-slip 防护地解压到临时目录(或 --keep 指定目录),
// 逐条核对 manifest 字段、checksums 与实际字节、executable 可执行位,
// 通过后打印 manifest 摘要。退出码:0 成功,1 运行期失败,2 用法错误。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/daedalus-os/daedalus/core/internal/plugin"
	"github.com/daedalus-os/daedalus/core/internal/version"
)

const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run 解析旗标并分派到打包/校验两条路径,返回进程退出码。
func run(argv []string, stdout *os.File) int {
	fs := flag.NewFlagSet("daedalus-plugin-pack", flag.ContinueOnError)
	fs.SetOutput(stdout) // 用法/错误信息统一走 stdout 由调用方观察,行为与 flag 默认一致。
	fs.Usage = func() {
		fmt.Fprintf(stdout, "usage: daedalus-plugin-pack -in <dir> -out <zip>\n"+
			"       daedalus-plugin-pack -verify <zip> [--keep <dir>]\n\n"+
			"Daedalus plugin packer/verifier (core %s)\n\nFlags:\n", version.Version)
		fs.PrintDefaults()
	}
	inDir := fs.String("in", "", "插件源目录(含 daedalus.plugin.json),打包模式")
	outZip := fs.String("out", "", "输出 zip 路径,须与 -in 同时给出")
	verifyZip := fs.String("verify", "", "待校验的插件包路径(校验模式)")
	keepDir := fs.String("keep", "", "校验模式:解压到指定空目录而非临时目录(调试用)")

	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	packMode := *inDir != "" || *outZip != ""
	if packMode && *verifyZip != "" {
		fmt.Fprintln(stdout, "daedalus-plugin-pack: error: -in/-out 与 -verify 互斥,只能选一种模式")
		return exitUsage
	}
	switch {
	case packMode:
		if *inDir == "" || *outZip == "" {
			fmt.Fprintln(stdout, "daedalus-plugin-pack: error: 打包模式必须同时给出 -in 与 -out")
			return exitUsage
		}
		return doPack(stdout, *inDir, *outZip)
	case *verifyZip != "":
		return doVerify(stdout, *verifyZip, *keepDir)
	default:
		fs.Usage()
		return exitUsage
	}
}

// doPack 执行打包并打印结果摘要。
func doPack(stdout *os.File, inDir, outZip string) int {
	m, err := plugin.Pack(inDir, outZip)
	if err != nil {
		fmt.Fprintf(stdout, "打包失败: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "已打包 %d 个 checksum 条目 → %s\n", len(m.Checksums), outZip)
	printSummary(stdout, m)
	return exitOK
}

// doVerify 执行校验:未指定 --keep 时解压到临时目录并在结束后清理。
func doVerify(stdout *os.File, zipPath, keepDir string) int {
	dest := keepDir
	cleanup := false
	if dest == "" {
		tmp, err := os.MkdirTemp("", "daedalus-plugin-verify-")
		if err != nil {
			fmt.Fprintf(stdout, "校验失败:无法创建临时解压目录: %v\n", err)
			return exitRuntime
		}
		dest = tmp
		cleanup = true
	} else if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(stdout, "校验失败:无法创建 --keep 目录 %s: %v\n", dest, err)
		return exitRuntime
	}
	if cleanup {
		defer os.RemoveAll(dest)
	}

	m, err := plugin.Verify(zipPath, dest)
	if err != nil {
		fmt.Fprintf(stdout, "校验失败: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "校验通过: %s (%d 字节)\n", zipPath, fileSize(zipPath))
	if cleanup {
		fmt.Fprintf(stdout, "  临时解压目录已清理: %s\n", dest)
	} else {
		fmt.Fprintf(stdout, "  解压目录保留: %s\n", dest)
	}
	printSummary(stdout, m)
	return exitOK
}

// fileSize 返回文件大小(仅用于摘要展示,失败时显示 0)。
func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// printSummary 打印 manifest 的人读摘要(任务 7 的 host inspect 是正式界面)。
func printSummary(stdout *os.File, m *plugin.Manifest) {
	fmt.Fprintf(stdout, "  id:         %s\n", m.ID)
	fmt.Fprintf(stdout, "  name:       %s\n", m.Name)
	fmt.Fprintf(stdout, "  version:    %s\n", m.Version)
	fmt.Fprintf(stdout, "  type:       %s\n", m.Type)
	fmt.Fprintf(stdout, "  runtime:    %s\n", m.Runtime)
	fmt.Fprintf(stdout, "  executable: %s\n", m.Executable)
	if len(m.Entrypoint) > 0 {
		fmt.Fprintf(stdout, "  entrypoint: %v\n", m.Entrypoint)
	}
	if len(m.Tools) > 0 {
		fmt.Fprintf(stdout, "  tools:      %v\n", m.Tools)
	}
	if m.Permissions != nil {
		fmt.Fprintf(stdout, "  perms:      read=%v write=%v run=%v\n",
			m.Permissions.Read, m.Permissions.Write, m.Permissions.Run)
	}
	fmt.Fprintf(stdout, "  checksums:  %d 个条目\n", len(m.Checksums))
}
