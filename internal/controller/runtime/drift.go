// Package runtime 提供 controller 调和循环的最小骨架:
// registry / informer / workqueue / reconciler / driver / drift 检测。
// 零副作用:仅消费 desiredview / state / audit / tx,不写文件系统。
package runtime

import (
	"fmt"

	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
)

// DriftEntry 是期望视图的一行 drift 报告。
type DriftEntry struct {
	Kind     objectmodel.Kind `json:"kind"`
	Name     string           `json:"name"`
	Desired  string           `json:"desired"`
	Observed string           `json:"observed"`
	SourceTx string           `json:"source_tx"`
	Drifts   bool             `json:"drift"`
}

// DriftReport 是 Compute 的返回:一次扫描的 drift 表。
type DriftReport struct {
	Mode    policy.OwnershipMode `json:"mode"`
	Entries []DriftEntry         `json:"entries"`
}

// Compute 比较 desired 与 observed 产出 DriftReport;mode == unmanaged 时 Entries 为空
// (零期望 = 零报告,与 desired-state-projection.md §4.2 一致)。
func Compute(
	view *desiredview.View,
	observed map[objectmodel.Kind]map[string]objectmodel.ServiceState,
	modeFor func(objectmodel.Kind, string) policy.OwnershipMode,
) DriftReport {
	var rep DriftReport
	for _, e := range view.Entries() {
		desired := e.DesiredState
		props := map[string]string{}
		if m, ok := observed[e.Kind]; ok {
			if st, ok := m[e.Name]; ok {
				props = st.Properties
			}
		}
		observedStr, _ := MapDesiredObserved(e.Kind, desired, props)
		mode := modeFor(e.Kind, e.Name)
		rep.Mode = mode
		if mode == policy.OwnershipUnmanaged {
			continue
		}
		rep.Entries = append(rep.Entries, DriftEntry{
			Kind:     e.Kind,
			Name:     e.Name,
			Desired:  desired,
			Observed: observedStr,
			SourceTx: e.SourceTx,
			Drifts:   observedStr != desired,
		})
	}
	return rep
}

// MapDesiredObserved 把 desired_state 与 observed state 映射为可比较字符串;
// kind 词汇超出 service/package 范围返回错误(表外 reject,与 desiredview
// adapterKinds 表对齐)。
func MapDesiredObserved(kind objectmodel.Kind, desired string, props map[string]string) (string, error) {
	switch kind {
	case objectmodel.KindService:
		active := props["ActiveState"] == "active"
		if desired == "active" && active {
			return "active", nil
		}
		if desired == "inactive" && !active {
			return "inactive", nil
		}
		return props["ActiveState"], nil
	case objectmodel.KindPackage:
		switch desired {
		case "present":
			if props["Version"] != "" {
				return "present", nil
			}
			return "absent", nil
		case "absent":
			if props["Version"] == "" {
				return "absent", nil
			}
			return "present", nil
		case "latest":
			return props["LatestAvailable"], nil
		}
		return "", fmt.Errorf("package: unknown desired %q", desired)
	}
	return "", fmt.Errorf("runtime: 未实现 kind %q", kind)
}