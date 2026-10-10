package runtime_test

import (
	"context"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := &runtime.Registry{}
	fn := func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		return controller.Result{}, nil
	}
	reg.Register(objectmodel.KindService, fn)
	got, ok := reg.Get(objectmodel.KindService)
	if !ok {
		t.Fatal("应能取出")
	}
	if got == nil {
		t.Fatal("handler 不应为 nil")
	}
}