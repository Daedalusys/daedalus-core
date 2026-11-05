// internal/fleet/support.go:被控节点的 support_level 分级诚实度。
//
// supportMatrix 是 supported 集合的唯一事实源(distro 小写基名 → 版本前缀列表),
// 逐字来自 fleet 控制面设计稿 §7.1 已拍 #1:Ubuntu LTS 22.04/24.04 + RHEL 9/10 +
// Debian 12。扩矩阵必须先开 issue,禁止默默扩;矩阵外发行版一律 experimental。
package fleet

import (
	"fmt"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// supportMatrix 是 supported 集合;版本按前缀匹配(如 rhel 的 "9" 命中 "9.4")。
var supportMatrix = map[string][]string{
	"ubuntu": {"22.04", "24.04"},
	"rhel":   {"9", "10"},
	"debian": {"12"},
}

// SupportLevelFor 判定 (distro, version) 的支持分级:distro 小写命中且 version
// 以某项为前缀 = supported;distro 命中但版本不符、或 distro 不在表内 = experimental。
// distro 或 version 为空属于误用,显式报错(不猜)。
func SupportLevelFor(distro, version string) (objectmodel.SupportLevel, error) {
	if distro == "" || version == "" {
		return "", fmt.Errorf("fleet: support_level 需 distro 与 version 均非空, got distro=%q version=%q", distro, version)
	}
	for _, prefix := range supportMatrix[strings.ToLower(distro)] {
		if strings.HasPrefix(version, prefix) {
			return objectmodel.SupportLevelSupported, nil
		}
	}
	return objectmodel.SupportLevelExperimental, nil
}
