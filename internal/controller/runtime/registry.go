package runtime

import (
	"sync"

	"github.com/Daedalusys/daedalus-core/internal/controller"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// Registry 是 ReconcileFunc 的按 kind 注册表(并发安全)。
type Registry struct {
	mu    sync.RWMutex
	funcs map[objectmodel.Kind]controller.ReconcileFunc
}

// Register 注册(或覆盖)某 kind 的 ReconcileFunc。
func (r *Registry) Register(k objectmodel.Kind, f controller.ReconcileFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.funcs == nil {
		r.funcs = map[objectmodel.Kind]controller.ReconcileFunc{}
	}
	r.funcs[k] = f
}

// Get 取出某 kind 的 ReconcileFunc;ok=false 表示未注册。
func (r *Registry) Get(k objectmodel.Kind) (controller.ReconcileFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.funcs[k]
	return f, ok
}