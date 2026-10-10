package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestReconciler_GenerationGating(t *testing.T) {
	reg := &runtime.Registry{}
	called := atomic.Int32{}
	reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		called.Add(1)
		return controller.Result{RequeueAfter: time.Second}, nil
	})
	r := runtime.NewReconciler(reg, func(k objectmodel.Kind, n string) (controller.Object, error) {
		return controller.Object{
			Metadata: controller.Metadata{Generation: 5},
			Status:   controller.Status{ObservedGeneration: 3},
		}, nil
	})
	res, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindService, Name: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Errorf("handler 应被调 1 次, got %d", called.Load())
	}
	if res.RequeueAfter != time.Second {
		t.Errorf("requeue after 透传失败: %v", res.RequeueAfter)
	}
}

func TestReconciler_GenerationUpToDateSkips(t *testing.T) {
	reg := &runtime.Registry{}
	called := atomic.Int32{}
	reg.Register(objectmodel.KindService, func(ctx context.Context, obj controller.Object) (controller.Result, error) {
		called.Add(1)
		return controller.Result{}, nil
	})
	r := runtime.NewReconciler(reg, func(k objectmodel.Kind, n string) (controller.Object, error) {
		return controller.Object{
			Metadata: controller.Metadata{Generation: 3},
			Status:   controller.Status{ObservedGeneration: 3},
		}, nil
	})
	if _, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindService, Name: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 0 {
		t.Errorf("持平不应调 handler, got %d", called.Load())
	}
}

func TestReconciler_UnregisteredKindReturnsError(t *testing.T) {
	r := runtime.NewReconciler(&runtime.Registry{}, func(objectmodel.Kind, string) (controller.Object, error) {
		return controller.Object{}, nil
	})
	_, err := r.Reconcile(context.Background(), runtime.Event{Kind: objectmodel.KindPackage})
	if err == nil {
		t.Fatal("未注册 kind 应报错")
	}
}