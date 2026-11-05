// internal/fleet/risk.go:把一条 intent 判成 low / high。
//
// 判定表来自 policy.toml [fleet](单一事实源):内核/security 包、敏感路径、
// 关键 systemd 单元、bootc 适配器为 high。**无法判定一律折向 high**(fail-closed)
// —— 把未知当作低危会让高危操作被自动重试,风险分桶语义当场失效。
package fleet

import (
	"path"
	"slices"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// 有判定规则的 intent 种类(与 daedalus-tx 的适配器名同源)。
const (
	intentKindPackageSet = "package.set"
	intentKindFileSet    = "file.set"
	intentKindServiceSet = "service.set"
)

// ClassifyIntent 按 policy 的 [fleet] 判定表给一条 intent 定风险等级。
// 结果只有 high / low:任何"判不出来"的情形都返回 high,调用方无需处理 error。
func ClassifyIntent(pol *policy.Policy, kind, target string) objectmodel.RiskClass {
	if pol == nil {
		// 判定表读不到(策略缺失/损坏)时不得把意图当低危。
		return objectmodel.RiskClassHigh
	}
	fleet := pol.Fleet

	// 适配器是 policy 显式声明的高危操作(如 bootc 升级/回滚):无条件 high。
	if slices.Contains(fleet.HighRiskAdapters, kind) {
		return objectmodel.RiskClassHigh
	}

	switch kind {
	case intentKindPackageSet:
		if prefixHitHighRisk(fleet.HighRiskPackages, path.Base(target)) {
			return objectmodel.RiskClassHigh
		}
		return objectmodel.RiskClassLow
	case intentKindFileSet:
		if pathPrefixHitHighRisk(fleet.HighRiskPathPrefixes, target) {
			return objectmodel.RiskClassHigh
		}
		return objectmodel.RiskClassLow
	case intentKindServiceSet:
		if prefixHitHighRisk(fleet.HighRiskServices, target) {
			return objectmodel.RiskClassHigh
		}
		return objectmodel.RiskClassLow
	}
	// 表外 kind:无法判定 → high(fail-closed)。
	return objectmodel.RiskClassHigh
}

// RetryMax 返回低危 intent 遇瞬时失败时允许的自动重试上限。
// 策略缺失或数值非正时返回 0(不重试):绝不因为读不到配置就放大重试。
func RetryMax(pol *policy.Policy) int {
	if pol == nil || pol.Fleet.TransientRetryMax <= 0 {
		return 0
	}
	return int(pol.Fleet.TransientRetryMax)
}

// prefixHitHighRisk 判定包名/单元名是否等于或以候选之一为前缀。
func prefixHitHighRisk(candidates []string, name string) bool {
	for _, c := range candidates {
		if strings.HasPrefix(name, c) {
			return true
		}
	}
	return false
}

// pathPrefixHitHighRisk 判定 target 是否落在候选路径前缀之下,带 '/' 边界:
// "/etc" 命中 "/etc" 与 "/etc/...",不命中 "/etcfoo/...。
func pathPrefixHitHighRisk(candidates []string, target string) bool {
	for _, c := range candidates {
		prefix := strings.TrimSuffix(c, "/")
		if target == prefix || strings.HasPrefix(target, prefix+"/") {
			return true
		}
	}
	return false
}
