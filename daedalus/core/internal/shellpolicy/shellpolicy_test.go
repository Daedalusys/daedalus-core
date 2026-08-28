// shellpolicy 包的表驱动测试:逐条钉死 shell_server.ts:13-209 的白名单语义。
package shellpolicy

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestConstantsPinDenoSource 钉死所有规格常量:数量与内容必须与
// shell_server.ts 逐字一致(任何漂移都必须先改此测试并过规格评审)。
func TestConstantsPinDenoSource(t *testing.T) {
	wantCommands := []string{
		"df", "ls", "cat", "pwd", "uname", "free", "ps", "uptime",
		"whoami", "ip", "arch", "hostname", "date", "ping", "systemctl",
	}
	if len(DefaultAllowCommands) != len(wantCommands) {
		t.Fatalf("DEFAULT_ALLOW_COMMANDS 数量漂移: got %d, want %d", len(DefaultAllowCommands), len(wantCommands))
	}
	for _, c := range wantCommands {
		if _, ok := DefaultAllowCommands[c]; !ok {
			t.Errorf("DEFAULT_ALLOW_COMMANDS 缺少 %q", c)
		}
	}

	wantPrefixes := []string{
		"/home", "/var/log", "/tmp", "/proc", "/sys",
		"/etc/os-release", "/usr/lib/os-release", "/etc/fedora-release", "/etc/almalinux-release",
	}
	if !slices.Equal(AllowedPathPrefixes, wantPrefixes) {
		t.Errorf("ALLOWED_PATH_PREFIXES 漂移: got %v", AllowedPathPrefixes)
	}

	wantBlocked := []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d", "/root"}
	if !slices.Equal(BlockedPaths, wantBlocked) {
		t.Errorf("BLOCKED_PATHS 漂移: got %v", BlockedPaths)
	}

	wantCleanEnv := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C.UTF-8"}
	if !slices.Equal(CleanEnv, wantCleanEnv) {
		t.Errorf("CLEAN_ENV 漂移: got %v", CleanEnv)
	}

	// 数值语义:TIMEOUT_MS=30000;验证失败 126;超时 124;启动失败 1。
	if TimeoutSeconds != 30 || Timeout.Milliseconds() != 30000 {
		t.Errorf("超时漂移: TimeoutSeconds=%d Timeout=%v, want 30s/30000ms", TimeoutSeconds, Timeout)
	}
	if ValidationRejectionCode != 126 || TimeoutRejectionCode != 124 || ExecutionFailureCode != 1 {
		t.Errorf("退出码漂移: got %d/%d/%d, want 126/124/1",
			ValidationRejectionCode, TimeoutRejectionCode, ExecutionFailureCode)
	}
	if AuditPathEnv != "DAEDALUS_AUDIT_LOG_PATH" || DefaultAuditPath != "/var/log/daedalus/audit.jsonl" {
		t.Errorf("审计路径常量漂移: %s / %s", AuditPathEnv, DefaultAuditPath)
	}
}

func TestResolveAllowCommands(t *testing.T) {
	t.Run("缺省用默认 15 项", func(t *testing.T) {
		got := ResolveAllowCommands("")
		if len(got) != 15 {
			t.Fatalf("默认白名单数量 = %d, want 15", len(got))
		}
	})
	t.Run("环境变量整体替换而非并集", func(t *testing.T) {
		got := ResolveAllowCommands("df,ls")
		if len(got) != 2 {
			t.Fatalf("替换后数量 = %d, want 2(REPLACE 语义不得并入默认集)", len(got))
		}
		if _, ok := got["cat"]; ok {
			t.Error("cat 不应出现在替换后的白名单中")
		}
		// 生效替换的端到端证明:cat 被拒。
		if _, err := ValidateCommand("cat", got); err == nil {
			t.Error("ALLOW_COMMANDS=df,ls 时 cat 竟然通过校验")
		}
		if _, err := ValidateCommand("df", got); err != nil {
			t.Errorf("df 应通过: %v", err)
		}
	})
	t.Run("逗号项 trim 且丢弃空项", func(t *testing.T) {
		got := ResolveAllowCommands(" df , ls ,")
		if len(got) != 2 {
			t.Fatalf("数量 = %d, want 2 (%v)", len(got), got)
		}
	})
	t.Run("空白值替换为空集", func(t *testing.T) {
		got := ResolveAllowCommands("  ")
		if len(got) != 0 {
			t.Fatalf("纯空白必须替换为空集(ts 真值分支), got %v", got)
		}
	})
}

func TestValidateCommand(t *testing.T) {
	def := ResolveAllowCommands("")
	tests := []struct {
		name      string
		command   string
		wantBase  string
		wantError string
	}{
		{"裸 df", "df", "df", ""},
		{"裸 rm 拒绝", "rm", "", "not in ALLOW_COMMANDS allowlist"},
		{"裸 bash 拒绝", "bash", "", "not in ALLOW_COMMANDS allowlist"},
		{"sudo 拒绝", "sudo", "", "not in ALLOW_COMMANDS allowlist"},
		{"空命令", "", "", "non-empty string"},
		{"空字节", "df\x00", "", "Null bytes are not allowed in command"},
		{"usr-bin-df", "/usr/bin/df", "df", ""},
		{"bin-df 经 realpath", "/bin/df", "df", ""},
		{"sbin-ip", "/sbin/ip", "ip", ""},
		{"usr-sbin-uname", "/usr/sbin/uname", "uname", ""},
		{"local-bin-df 拒绝", "/usr/local/bin/df", "", "not in a valid system bin directory"},
		{"首尾空白容忍", "  df  ", "df", ""},
		{"带参尾巴非 argv", "df -h", "", "not in ALLOW_COMMANDS allowlist"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateCommand(tc.command, def)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateCommand(%q) 意外失败: %v", tc.command, err)
				}
				if got != tc.wantBase {
					t.Errorf("ValidateCommand(%q) = %q, want %q", tc.command, got, tc.wantBase)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCommand(%q) 意外成功: %q", tc.command, got)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("ValidateCommand(%q) 错误 %q 不含 %q", tc.command, err, tc.wantError)
			}
		})
	}
}

func TestIsPathLike(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"/etc/passwd", true},
		{"a/b", true},
		{".", true},
		{"..", true},
		{"../x", true},
		{"a\x00b", true},
		{"plain", false},
		{"", false},
		{"-l", false},
		{"-file=x", false}, // 不含 '/' 的取值:整体非路径形态
	}
	for _, tc := range tests {
		if got := IsPathLike(tc.arg); got != tc.want {
			t.Errorf("IsPathLike(%q) = %v, want %v", tc.arg, got, tc.want)
		}
	}
}

func TestValidateArg(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		wantError string
	}{
		{"普通参数", "-l", ""},
		{"空字节", "a\x00b", "Null bytes are not allowed in arguments."},
		{"flag等号受阻路径", "--file=/etc/shadow", "Access to blocked path"},
		{"短flag等号受阻路径", "-f=/etc/sudoers.d/90x", "Access to blocked path"},
		{"flag等号允许路径", "--file=/tmp/x", ""},
		{"flag等号非路径值", "--format=json", ""},
		{"裸受阻路径", "/etc/gshadow", "Access to blocked path"},
		{"裸root家目录", "/root/.ssh/id_rsa", "Access to blocked path"},
		{"白名单目录", "/var/log", ""},
		{"白名单子路径", "/proc/meminfo", ""},
		{"前缀边界外", "/home2", "outside allowed directories:"},
		{"白名单外系统路径", "/usr/bin", "outside allowed directories:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArg(tc.arg)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateArg(%q) 意外失败: %v", tc.arg, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateArg(%q) 意外通过", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("ValidateArg(%q) 错误 %q 不含 %q", tc.arg, err, tc.wantError)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	// 消息形态与 ts 逐字对齐(注意 shell 版 resolved 不带引号、结尾无句点,
	// 与 fs 版消息风格不同)。
	_, err := ValidatePath("/etc/shadow")
	if err == nil || !strings.Contains(err.Error(), "Access to blocked path '/etc/shadow'") {
		t.Errorf("受阻消息形态漂移: %v", err)
	}
	_, err = ValidatePath("/usr/bin")
	if err == nil || !strings.Contains(err.Error(), "outside allowed directories: /home, /var/log, /tmp, /proc, /sys, /etc/os-release, /usr/lib/os-release, /etc/fedora-release, /etc/almalinux-release") {
		t.Errorf("越界消息形态漂移: %v", err)
	}
	if _, err := ValidatePath(""); err == nil || !strings.Contains(err.Error(), "non-empty string") {
		t.Errorf("空路径消息漂移: %v", err)
	}
	if _, err := ValidatePath("/tmp/a\x00"); err == nil || !strings.Contains(err.Error(), "Null bytes are not allowed in path arguments.") {
		t.Errorf("空字节消息漂移: %v", err)
	}

	// 相对路径:先拼 cwd 再规范化(对应 ts:104-108)。
	cwd := mustGetwd(t)
	if !strings.HasPrefix(cwd, "/tmp") && !strings.HasPrefix(cwd, "/home") {
		t.Skipf("当前工作目录 %q 不在白名单内,跳过相对路径正例", cwd)
	}
	if _, err := ValidatePath("./relative_ok"); err != nil {
		t.Errorf("白名单 cwd 下的相对路径应通过: %v", err)
	}

	// /etc/os-release 在本机 realpath 到 /usr/lib/os-release,两前缀均在白名单。
	resolved, err := ValidatePath("/etc/os-release")
	if err != nil {
		t.Fatalf("/etc/os-release 应允许: %v", err)
	}
	if resolved != "/etc/os-release" && resolved != "/usr/lib/os-release" {
		t.Errorf("解析异常: %q", resolved)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取 cwd 失败: %v", err)
	}
	return cwd
}

func TestNormalizePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/tmp/./a", "/tmp/a"},
		{"/tmp/a/../b", "/tmp/b"},
		{"/a/../../b", "/b"},
		{"/", "/"},
	}
	for _, tc := range tests {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
