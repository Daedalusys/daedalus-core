package runtime

import (
	"context"
	"fmt"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// Reconciler 把 Event 喂给注册的 ReconcileFunc,并施加 generation 门控:
// 仅当 metadata.generation > status.observed_generation 才触发。
type Reconciler struct {
	reg       *Registry
	objectFor func(objectmodel.Kind, string) (controller.Object, error)
}

// NewReconciler 构造 Reconciler;objectFor 提供资源当前 Object。
func NewReconciler(reg *Registry, objectFor func(objectmodel.Kind, string) (controller.Object, error)) *Reconciler {
	return &Reconciler{reg: reg, objectFor: objectFor}
}

// Reconcile 处理一个 event:未注册 kind 报错;generation 已持平则 skip;否则调 handler。
func (r *Reconciler) Reconcile(ctx context.Context, ev Event) (controller.Result, error) {
	fn, ok := r.reg.Get(ev.Kind)
	if !ok {
		return controller.Result{}, fmt.Errorf("runtime: 未注册 kind %q", ev.Kind)
	}
	obj, err := r.objectFor(ev.Kind, ev.Name)
	if err != nil {
		return controller.Result{}, err
	}
	if obj.Metadata.Generation <= obj.Status.ObservedGeneration {
		return controller.Result{}, nil // up-to-date, skip
	}
	return fn(ctx, obj)
}