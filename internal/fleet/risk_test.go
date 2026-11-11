package fleet

// 本文件钉住控制面的 intent 风险分类:判定表来自 policy.Fleet(单一事实源),
// 无法判定一律折向 high(fail-closed)——"未知 kind 判低风险"会让高危操作被当成
// 低危自动重试,是越界。

import (
	"testing"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// TestClassifyIntent_KernelPackageIsHigh 证明内核包按 HighRiskPackages 前缀命中。
func TestClassifyIntent_KernelPackageIsHigh(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "package.set", "kernel-core"); got != objectmodel.RiskClassHigh {
		t.Errorf("package.set kernel-core = %q, want %q", got, objectmodel.RiskClassHigh)
	}
}

// TestClassifyIntent_NginxPackageIsLow 证明普通包不在判定表内即低危。
func TestClassifyIntent_NginxPackageIsLow(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "package.set", "nginx"); got != objectmodel.RiskClassLow {
		t.Errorf("package.set nginx = %q, want %q", got, objectmodel.RiskClassLow)
	}
}

// TestClassifyIntent_FileUnderEtcIsHigh 证明 /etc 下的一切文件变更都是高危。
func TestClassifyIntent_FileUnderEtcIsHigh(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "file.set", "/etc/nginx/nginx.conf"); got != objectmodel.RiskClassHigh {
		t.Errorf("file.set /etc/nginx/nginx.conf = %q, want %q", got, objectmodel.RiskClassHigh)
	}
}

// TestClassifyIntent_FileUnderHomeIsLow 证明高危路径之外的文件变更是低危。
func TestClassifyIntent_FileUnderHomeIsLow(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "file.set", "/home/a/b.conf"); got != objectmodel.RiskClassLow {
		t.Errorf("file.set /home/a/b.conf = %q, want %q", got, objectmodel.RiskClassLow)
	}
}

// TestClassifyIntent_DebianUbuntuKernelPackagesAreHigh 证明 Debian / Ubuntu 的
// 内核与 polkit 包名同样命中高危表(只看 RHEL 系包名会让这两类发行上的内核
// 升级被判低危而被自动重试)。
func TestClassifyIntent_DebianUbuntuKernelPackagesAreHigh(t *testing.T) {
	for _, name := range []string{"linux-image-amd64", "linux-image-generic", "linux-headers-amd64", "policykit-1"} {
		if got := ClassifyIntent(policy.Default(), "package.set", name); got != objectmodel.RiskClassHigh {
			t.Errorf("package.set %s = %q, want high", name, got)
		}
	}
}

// TestClassifyIntent_OrdinaryLinuxPackagesStayLow 是上一条的反面控制组:
// `linux-` 不能整体进表,否则 linux-base / linux-tools 这类无害包会被判高危。
func TestClassifyIntent_OrdinaryLinuxPackagesStayLow(t *testing.T) {
	for _, name := range []string{"linux-base", "linux-tools"} {
		if got := ClassifyIntent(policy.Default(), "package.set", name); got != objectmodel.RiskClassLow {
			t.Errorf("package.set %s = %q, want low", name, got)
		}
	}
}

// TestClassifyIntent_FileKindUsesPathTable 证明 file kind 经 adapterKind 桥接后
// 落到路径判定表,而不是表外 fail-closed 恒判 high:/etc 下高危,其余低危。
// (直接传 "file.set" 测不到桥接——判定表认的是适配器名,Kind→适配器名的映射
// 由 adapterKind 负责。)
func TestClassifyIntent_FileKindUsesPathTable(t *testing.T) {
	adapter := adapterKind(objectmodel.KindFile)
	if adapter != intentKindFileSet {
		t.Fatalf("adapterKind(file) = %q, want %q", adapter, intentKindFileSet)
	}
	if got := ClassifyIntent(policy.Default(), adapter, "/etc/nginx/nginx.conf"); got != objectmodel.RiskClassHigh {
		t.Errorf("/etc 下的 file = %q, want high", got)
	}
	if got := ClassifyIntent(policy.Default(), adapter, "/home/u/app.conf"); got != objectmodel.RiskClassLow {
		t.Errorf("/home 下的 file = %q, want low", got)
	}
}

// TestClassifyIntent_SystemdServiceIsHigh 证明关键单元按 HighRiskServices 前缀命中。
func TestClassifyIntent_SystemdServiceIsHigh(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "service.set", "systemd-logind"); got != objectmodel.RiskClassHigh {
		t.Errorf("service.set systemd-logind = %q, want %q", got, objectmodel.RiskClassHigh)
	}
}

// TestClassifyIntent_SSHServiceIsHigh 证明 sshd 是精确命中的高危单元。
func TestClassifyIntent_SSHServiceIsHigh(t *testing.T) {
	if got := ClassifyIntent(policy.Default(), "service.set", "sshd"); got != objectmodel.RiskClassHigh {
		t.Errorf("service.set sshd = %q, want %q", got, objectmodel.RiskClassHigh)
	}
}

// TestClassifyIntent_UnknownKindFailsClosedToHigh 证明表外 kind 不被当成低危
// (fail-closed:无法判定 = 高危)。
func TestClassifyIntent_UnknownKindFailsClosedToHigh(t *testing.T) {
	for _, kind := range []string{"container.set", "task.set", "", "PACKAGE.SET"} {
		if got := ClassifyIntent(policy.Default(), kind, "whatever"); got != objectmodel.RiskClassHigh {
			t.Errorf("未知 kind %q = %q, want %q", kind, got, objectmodel.RiskClassHigh)
		}
	}
}

// TestClassifyIntent_NilPolicyFailsClosedToHigh 证明策略缺失时同样折向 high
// (绝不因为"读不到判定表"就把意图当低危)。
func TestClassifyIntent_NilPolicyFailsClosedToHigh(t *testing.T) {
	if got := ClassifyIntent(nil, "package.set", "nginx"); got != objectmodel.RiskClassHigh {
		t.Errorf("nil policy = %q, want %q", got, objectmodel.RiskClassHigh)
	}
}

// TestClassifyIntent_HighRiskAdapterIsHigh 证明 policy 显式声明的高危适配器
// 无条件判 high。
func TestClassifyIntent_HighRiskAdapterIsHigh(t *testing.T) {
	for _, kind := range policy.Default().Fleet.HighRiskAdapters {
		if got := ClassifyIntent(policy.Default(), kind, "latest"); got != objectmodel.RiskClassHigh {
			t.Errorf("适配器 %q = %q, want %q", kind, got, objectmodel.RiskClassHigh)
		}
	}
}

// TestClassifyIntent_PathPrefixBoundary 证明路径前缀必须带 '/' 边界:
// "/etcfoo/x" 不算落在 "/etc" 之下。
func TestClassifyIntent_PathPrefixBoundary(t *testing.T) {
	cases := []struct {
		target string
		want   objectmodel.RiskClass
	}{
		{"/etc/nginx/nginx.conf", objectmodel.RiskClassHigh},
		{"/etc", objectmodel.RiskClassHigh},
		{"/etcfoo/x", objectmodel.RiskClassLow},
		{"/etcfoo", objectmodel.RiskClassLow},
		{"/usrlocal/bin/x", objectmodel.RiskClassLow},
		{"/var/lib/daedalus/tx", objectmodel.RiskClassHigh},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			if got := ClassifyIntent(policy.Default(), "file.set", tc.target); got != tc.want {
				t.Errorf("file.set %q = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

// TestRetryMax_ReadsPolicy 证明重试上限来自 policy 而非硬编码。
func TestRetryMax_ReadsPolicy(t *testing.T) {
	if got := RetryMax(policy.Default()); got != 3 {
		t.Errorf("RetryMax(Default()) = %d, want 3", got)
	}
	custom := policy.Default()
	custom.Fleet.TransientRetryMax = 7
	if got := RetryMax(custom); got != 7 {
		t.Errorf("RetryMax(自定义) = %d, want 7", got)
	}
	if got := RetryMax(nil); got != 0 {
		t.Errorf("RetryMax(nil) = %d, want 0(读不到判定表不得重试)", got)
	}
}
