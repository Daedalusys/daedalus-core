package runtime_test

import (
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
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
// === Task 5: drift compute with four-mode semantics ===

func TestCompute_UnmanagedEmpty(t *testing.T) {
	v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
	state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
		objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
	}
	rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipUnmanaged })
	if len(rep.Entries) != 0 {
		t.Errorf("unmanaged 应空, got %d", len(rep.Entries))
	}
}

func TestCompute_ObserveReportsButNotDrives(t *testing.T) {
	v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
	state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
		objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
	}
	rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipObserve })
	if len(rep.Entries) != 1 {
		t.Fatalf("应有 1 条 drift, got %d", len(rep.Entries))
	}
	if !rep.Entries[0].Drifts {
		t.Error("active vs inactive 应 drift")
	}
}

func TestCompute_ManagedReconcileAllowsDrive(t *testing.T) {
	v := desiredview.ViewWithForTest([]desiredview.Entry{{Kind: objectmodel.KindService, Name: "nginx.service", DesiredState: "active", SourceTx: "tx-1"}})
	state := map[objectmodel.Kind]map[string]objectmodel.ServiceState{
		objectmodel.KindService: {"nginx.service": {Properties: map[string]string{"ActiveState": "inactive"}}},
	}
	rep := runtime.Compute(v, state, func(objectmodel.Kind, string) policy.OwnershipMode { return policy.OwnershipManagedReconcile })
	if !rep.Entries[0].Drifts {
		t.Error("managed/reconcile 应识别 drift")
	}
}
