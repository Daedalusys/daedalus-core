package runtime_test

import (
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestMapDesiredObserved_ServiceActive(t *testing.T) {
	got, err := runtime.MapDesiredObserved(objectmodel.KindService, "active", map[string]string{"ActiveState": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "active" {
		t.Errorf("got %q, want active", got)
	}
}

func TestMapDesiredObserved_ServiceInactiveMapsFailed(t *testing.T) {
	got, err := runtime.MapDesiredObserved(objectmodel.KindService, "inactive", map[string]string{"ActiveState": "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "inactive" {
		t.Errorf("failed→inactive, got %q", got)
	}
}

func TestMapDesiredObserved_PackagePresent(t *testing.T) {
	got, err := runtime.MapDesiredObserved(objectmodel.KindPackage, "present", map[string]string{"Version": "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "present" {
		t.Errorf("got %q, want present", got)
	}
}

func TestMapDesiredObserved_PackageAbsentEmptyVersion(t *testing.T) {
	got, err := runtime.MapDesiredObserved(objectmodel.KindPackage, "absent", map[string]string{"Version": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got != "absent" {
		t.Errorf("got %q, want absent", got)
	}
}

func TestMapDesiredObserved_UnknownKindErrors(t *testing.T) {
	_, err := runtime.MapDesiredObserved(objectmodel.KindContainer, "active", map[string]string{})
	if err == nil {
		t.Fatal("未实现 kind 应报错")
	}
}