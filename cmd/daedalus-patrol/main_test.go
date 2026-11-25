package main

// 本文件钉住 daedalus-patrol 的子命令分发与退出码:用法错误走 2、运行期错误走 1、
// 成功走 0,--help 永远 0。清单用 testdata/ 的夹具,不依赖镜像里的 /etc/daedalus。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkversion "github.com/Daedalusys/daedalus-sdk/version"

	"github.com/Daedalusys/daedalus-core/internal/patrol"
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
			name:     "run 缺 --node 是用法错误",
			argv:     []string{"run", "--config", cfg},
			wantCode: exitUsage,
			wantErr:  []string{"--node"},
		},
		{
			name:     "run 的节点 id 含路径形状是用法错误",
			argv:     []string{"run", "--config", cfg, "--node", "../etc"},
			wantCode: exitUsage,
			wantErr:  []string{"非法"},
		},
		{
			name:     "checklist 的节点 id 含路径形状是用法错误",
			argv:     []string{"checklist", "--config", cfg, "--node", "a/b"},
			wantCode: exitUsage,
			wantErr:  []string{"非法"},
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
			wantErr:  []string{"读取配置失败"},
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

func TestRun_UnregisteredNodeFailsButRecordsFinding(t *testing.T) {
	// 节点未注册就是节点不可达:本轮按运行期失败退出,但巡检级发现必须落盘 ——
	// 「这台机器这轮没查成」也要有证据。SMTP 未配置,投递失败同样在本轮暴露。
	t.Setenv("DAEDALUS_FLEET_DIR", t.TempDir())
	roundRoot := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"run",
		"--config", filepath.Join("testdata", "patrol.toml"),
		"--node", "lab-a",
		"--state-dir", t.TempDir(),
		"--round-root", roundRoot,
	}, &stdout, &stderr)
	if code != exitRuntime {
		t.Fatalf("退出码 = %d,期望 %d;stdout=%q stderr=%q", code, exitRuntime, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "本轮巡检失败") {
		t.Errorf("stderr 必须点明本轮失败:\n%s", stderr.String())
	}

	found, err := filepath.Glob(filepath.Join(roundRoot, "*", "findings.jsonl"))
	if err != nil || len(found) != 1 {
		t.Fatalf("期望 1 份发现文件,实际 %v(err=%v)", found, err)
	}
	data, err := os.ReadFile(found[0])
	if err != nil {
		t.Fatalf("读发现文件失败: %v", err)
	}
	if !strings.Contains(string(data), patrol.SelfCheckItemID) {
		t.Errorf("发现文件里必须有巡检级发现:\n%s", data)
	}
}

// node_timeout 的上限是硬边界:超过它 systemd 会在收尾前把整轮杀掉。
func TestNodeTimeout_Bounds(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", defaultNodeTimeout, false},
		{"10m", 10 * time.Minute, false},
		{"12m", 12 * time.Minute, false},
		{"12m1s", 0, true},
		{"20m", 0, true},
		{"0s", 0, true},
		{"-5m", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := nodeTimeout(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("nodeTimeout(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Fatalf("nodeTimeout(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// 单元里的 TimeoutStartSec 与 CLI 的 unitStartTimeout 是两处手抄的数值:
// 漂了之后现场表现是「正常轮次被 systemd 杀掉」,而不是配置错误。
func TestUnitTimeoutMatchesNodeTimeoutCap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "files", "system", "usr", "lib", "systemd", "system", "daedalus-patrol.service"))
	if err != nil {
		t.Fatalf("读单元文件失败: %v", err)
	}
	var unitRaw string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "TimeoutStartSec="); ok {
			unitRaw = v
		}
	}
	if unitRaw == "" {
		t.Fatal("单元文件里找不到 TimeoutStartSec=")
	}
	// systemd 的时间单位写成 min;time.ParseDuration 认 m。
	unit, err := time.ParseDuration(strings.ReplaceAll(unitRaw, "min", "m"))
	if err != nil {
		t.Fatalf("解析单元 TimeoutStartSec=%q 失败: %v", unitRaw, err)
	}
	if unit != unitStartTimeout {
		t.Fatalf("单元 TimeoutStartSec=%s 与 CLI unitStartTimeout=%s 不一致", unit, unitStartTimeout)
	}
	if _, err := nodeTimeout((unitStartTimeout - 3*time.Minute).String()); err != nil {
		t.Fatalf("留 3 分钟收尾余量的 node_timeout 必须被接受: %v", err)
	}
}
