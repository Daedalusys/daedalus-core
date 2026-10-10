// Package runtime 提供 controller 调和循环的最小骨架:
// registry / informer / workqueue / reconciler / driver / drift 检测。
// 零副作用:仅消费 desiredview / state / audit / tx,不写文件系统。
package runtime

import (
	"fmt"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

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