package fleet

// 本文件钉住 support_level 的分级诚实度矩阵:矩阵内 = supported,其余
// (含矩阵内 distro 的矩阵外版本) = experimental;distro 或 version 为空
// 是误用,显式报错。
// 矩阵本体(supportMatrix)是唯一事实源,扩矩阵必须先开 issue;本文件用
// 集合相等钉死它,多一行少一行都红。

import (
	"reflect"
	"testing"

	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// TestSupportLevelFor_Matrix 表驱动覆盖已拍矩阵的全部命中/落空分支。
func TestSupportLevelFor_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		distro  string
		version string
		want    objectmodel.SupportLevel
	}{
		{"ubuntu 24.04", "ubuntu", "24.04", objectmodel.SupportLevelSupported},
		{"ubuntu 24.04 点版本", "ubuntu", "24.04.1", objectmodel.SupportLevelSupported},
		{"ubuntu 22.04", "ubuntu", "22.04", objectmodel.SupportLevelSupported},
		{"ubuntu 20.04 出矩阵", "ubuntu", "20.04", objectmodel.SupportLevelExperimental},
		{"rhel 9 前缀", "rhel", "9.", objectmodel.SupportLevelSupported},
		{"rhel 9.4", "rhel", "9.4", objectmodel.SupportLevelSupported},
		{"rhel 10", "rhel", "10", objectmodel.SupportLevelSupported},
		{"rhel 8 出矩阵", "rhel", "8.10", objectmodel.SupportLevelExperimental},
		{"debian 12", "debian", "12", objectmodel.SupportLevelSupported},
		{"debian 13 出矩阵", "debian", "13", objectmodel.SupportLevelExperimental},
		{"fedora 未列", "fedora", "42", objectmodel.SupportLevelExperimental},
		{"centos stream 未列", "centos", "stream", objectmodel.SupportLevelExperimental},
		{"distro 大小写不敏感", "Ubuntu", "24.04", objectmodel.SupportLevelSupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SupportLevelFor(tc.distro, tc.version)
			if err != nil {
				t.Fatalf("SupportLevelFor(%q, %q): %v", tc.distro, tc.version, err)
			}
			if got != tc.want {
				t.Errorf("SupportLevelFor(%q, %q) = %q, want %q", tc.distro, tc.version, got, tc.want)
			}
		})
	}
}

// TestSupportLevelFor_EmptyVersionErrors 证明 distro/version 任一为空即误用。
func TestSupportLevelFor_EmptyVersionErrors(t *testing.T) {
	cases := []struct {
		name    string
		distro  string
		version string
	}{
		{"两者都空", "", ""},
		{"缺 distro", "", "24.04"},
		{"缺 version", "ubuntu", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SupportLevelFor(tc.distro, tc.version); err == nil {
				t.Fatalf("SupportLevelFor(%q, %q) 应报错", tc.distro, tc.version)
			}
		})
	}
}

// TestSupportLevelFor_UnknownDistroIsExperimental 证明矩阵外发行版不给 supported
// (分级诚实度:不打肿脸充胖子)。
func TestSupportLevelFor_UnknownDistroIsExperimental(t *testing.T) {
	got, err := SupportLevelFor("alpine", "3.20")
	if err != nil {
		t.Fatalf("SupportLevelFor: %v", err)
	}
	if got != objectmodel.SupportLevelExperimental {
		t.Errorf("矩阵外发行版 = %q, want %q", got, objectmodel.SupportLevelExperimental)
	}
}

// TestSupportLevelFor_SupportedSetMatchesSpec 钉死 supported 集合恰好是
// {ubuntu 22.04, ubuntu 24.04, rhel 9, rhel 10, debian 12}(spec §7.1 已拍 #1);
// 任何增删都会在这里红——扩矩阵必须先开 issue。
func TestSupportLevelFor_SupportedSetMatchesSpec(t *testing.T) {
	got := map[string]bool{}
	for distro, prefixes := range supportMatrix {
		for _, p := range prefixes {
			got[distro+"/"+p] = true
		}
	}
	want := map[string]bool{
		"ubuntu/22.04": true,
		"ubuntu/24.04": true,
		"rhel/9":       true,
		"rhel/10":      true,
		"debian/12":    true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("support_level 矩阵漂移:\n got %v\nwant %v", got, want)
	}
}
