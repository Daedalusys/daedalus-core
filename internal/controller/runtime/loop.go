package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
	"github.com/Daedalusys/daedalus-sdk/policy"
	"github.com/Daedalusys/daedalus-sdk/state"
)

// viewLoader / stateReader 是包级可注入函数(测试替身用);生产代码保持默认指向
// desiredview.Load 与 state.Read(t0)。
var (
	viewLoader  = func() (*desiredview.View, error) { return desiredview.Load() }
	stateReader = func() ([]state.StateEntry, error) { return state.Read(time.Time{}) }
)

// SetViewLoaderForTest / SetStateReaderForTest / ResetInjections 是三个测试 hook。
// 生产代码不许调;测试文件 t.Cleanup(ResetInjections) 复原默认实现。
func SetViewLoaderForTest(fn func() (*desiredview.View, error))     { viewLoader = fn }
func SetStateReaderForTest(fn func() ([]state.StateEntry, error)) { stateReader = fn }
func ResetInjections() {
	viewLoader = func() (*desiredview.View, error) { return desiredview.Load() }
	stateReader = func() ([]state.StateEntry, error) { return state.Read(time.Time{}) }
}

// ObjectFromViewAndState 从 viewLoader + stateReader 读取数据,构造一个 controller.Object。
// Generation = view size(占位;首次 reconcile 后由 provider apply 后回填);
// ObservedGeneration = 0。
func ObjectFromViewAndState(kind objectmodel.Kind, name string) (controller.Object, error) {
	v, err := viewLoader()
	if err != nil {
		return controller.Object{}, fmt.Errorf("runtime: %w", err)
	}
	for _, e := range v.Entries() {
		if e.Kind == kind && e.Name == name {
			spec, _ := json.Marshal(objectmodel.ResourceSpec{DesiredState: e.DesiredState})
			return controller.Object{
				Kind:     kind,
				Metadata: controller.Metadata{Name: name, Generation: int64(len(v.Entries()))},
				Spec:     spec,
			}, nil
		}
	}
	return controller.Object{}, fmt.Errorf("runtime: view 中无 kind=%s name=%s 条目", kind, name)
}

// RunLoop 是 controller 主循环:起 view + observed → informer → 过滤
// (mode != managed/reconcile drop) → 同步调 reconciler 调 driver。
func RunLoop(
	ctx context.Context,
	interval time.Duration,
	reg *Registry,
	modeFor func(objectmodel.Kind, string) policy.OwnershipMode,
	objectFor func(objectmodel.Kind, string) (controller.Object, error),
) error {
	view, err := viewLoader()
	if err != nil {
		return fmt.Errorf("runtime: load view: %w", err)
	}
	inf := NewPollingInformer(view)
	defer inf.Stop()
	evCh, err := inf.Run(ctx, interval)
	if err != nil {
		return fmt.Errorf("runtime: informer: %w", err)
	}
	rec := NewReconciler(reg, objectFor)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-evCh:
			if !ok {
				return nil
			}
			if modeFor(ev.Kind, ev.Name) != policy.OwnershipManagedReconcile {
				continue
			}
			if _, err := rec.Reconcile(ctx, ev); err != nil {
				// 留接口为 P4.x backoff;本骨架仅静默(不阻断 pipeline)
				_ = err
			}
		}
	}
}