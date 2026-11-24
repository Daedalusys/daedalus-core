package main

// 本文件钉住 daedalus-patrol 的子命令分发与退出码:用法错误走 2、运行期错误走 1、
// 成功走 0,--help 永远 0。清单用 testdata/ 的夹具,不依赖镜像里的 /etc/daedalus。

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	sdkversion "github.com/Daedalusys/daedalus-sdk/version"
)

func TestRun_Dispatch(t *testing.T) {
	cfg := filepath.Join("testdata", "patrol.toml")
	cases := []struct {
		name     string
		argv     []string
		wantCode int
		wantOut  []string
		denyOut  []string
		wantErr  []string
	}{
		{
			name:     "无参数按用法错误处理",
			argv:     nil,
			wantCode: exitUsage,
			wantErr:  []string{"用法"},
		},
		{
			name:     "未知子命令",
			argv:     []string{"frobnicate"},
			wantCode: exitUsage,
			wantErr:  []string{"未知子命令"},
		},
		{
			name:     "--help 成功退出",
			argv:     []string{"--help"},
			wantCode: exitOK,
			wantOut:  []string{"daedalus-patrol", "run", "checklist", "version"},
		},
		{
			name:     "-h 成功退出",
			argv:     []string{"-h"},
			wantCode: exitOK,
			wantOut:  []string{"用法"},
		},
		{
			name:     "version 打印版本",
			argv:     []string{"version"},
			wantCode: exitOK,
			wantOut:  []string{"daedalus-patrol " + sdkversion.Version},
		},
		{
			name:     "checklist 不带节点只有全局项",
			argv:     []string{"checklist", "--config", cfg},
			wantCode: exitOK,
			wantOut:  []string{"disk-health", "disabled-one", "全局"},
			denyOut:  []string{"kvm-storage"},
		},
		{
			name:     "checklist 带节点看得到本机项与停用项",
			argv:     []string{"checklist", "--config", cfg, "--node", "lab-a"},
			wantCode: exitOK,
			wantOut:  []string{"disk-health", "disabled-one", "kvm-storage", "停用"},
		},
		{
			name:     "run 只列启用项",
			argv:     []string{"run", "--config", cfg, "--node", "lab-a"},
			wantCode: exitOK,
			wantOut:  []string{"disk-health", "kvm-storage"},
			denyOut:  []string{"disabled-one"},
		},
		{
			name:     "run 未知节点只跑全局启用项",
			argv:     []string{"run", "--config", cfg, "--node", "lab-zzz"},
			wantCode: exitOK,
			wantOut:  []string{"disk-health"},
			denyOut:  []string{"kvm-storage", "disabled-one"},
		},
		{
			name:     "run 缺 --node 是用法错误",
			argv:     []string{"run", "--config", cfg},
			wantCode: exitUsage,
			wantErr:  []string{"--node"},
		},
		{
			name:     "run --node 缺取值是用法错误",
			argv:     []string{"run", "--node"},
			wantCode: exitUsage,
		},
		{
			name:     "清单文件不存在是运行期错误",
			argv:     []string{"run", "--config", filepath.Join("testdata", "no-such-file.toml"), "--node", "lab-a"},
			wantCode: exitRuntime,
			wantErr:  []string{"清单"},
		},
		{
			name:     "未知旗标是用法错误",
			argv:     []string{"checklist", "--bogus"},
			wantCode: exitUsage,
		},
		{
			name:     "子命令 --help 也成功退出",
			argv:     []string{"run", "--help"},
			wantCode: exitOK,
			wantOut:  []string{"用法", "--node"},
		},
		{
			name:     "出厂默认配置可读且清单为空",
			argv:     []string{"checklist", "--config", filepath.Join("..", "..", "files", "system", "etc", "daedalus", "patrol.toml")},
			wantCode: exitOK,
			wantOut:  []string{"0 项"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(c.argv, &stdout, &stderr)
			if code != c.wantCode {
				t.Fatalf("退出码 = %d,期望 %d;stdout=%q stderr=%q", code, c.wantCode, stdout.String(), stderr.String())
			}
			for _, want := range c.wantOut {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout 缺少 %q:\n%s", want, stdout.String())
				}
			}
			for _, deny := range c.denyOut {
				if strings.Contains(stdout.String(), deny) {
					t.Errorf("stdout 不该出现 %q:\n%s", deny, stdout.String())
				}
			}
			for _, want := range c.wantErr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr 缺少 %q:\n%s", want, stderr.String())
				}
			}
		})
	}
}
