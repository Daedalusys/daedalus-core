package runtime

import (
	"context"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

// EventType 标记事件类型(本期只产出 Updated)。
type EventType string

const (
	EventUpdated EventType = "Updated"
)

// Event 是 informer 推上来的最小事件载荷:哪个资源发生了什么、source_tx 是谁。
type Event struct {
	Kind     objectmodel.Kind
	Name     string
	Type     EventType
	SourceTx string
}

// Informer 接口:Run 产出事件 channel,Stop 终止轮询。
type Informer interface {
	Run(ctx context.Context, interval time.Duration) (<-chan Event, error)
	Stop()
}

// pollingInformer 是轮询式 informer:每次 tick 扫 desiredview,对比上次 seen
// source_tx,产生 Updated event。
type pollingInformer struct {
	view *desiredview.View
	seen map[objectmodel.Kind]map[string]string
	stop chan struct{}
}

// NewPollingInformer 构造轮询式 informer(view 不可变)。
func NewPollingInformer(view *desiredview.View) Informer {
	return &pollingInformer{
		view: view,
		seen: map[objectmodel.Kind]map[string]string{},
		stop: make(chan struct{}),
	}
}

// Run 起后台 goroutine 周期性 emit;通道缓冲 64,drop on full(留 backoff 接口)。
func (p *pollingInformer) Run(ctx context.Context, interval time.Duration) (<-chan Event, error) {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-ticker.C:
				for _, e := range p.view.Entries() {
					if last, ok := p.seen[e.Kind][e.Name]; ok && last == e.SourceTx {
						continue
					}
					if p.seen[e.Kind] == nil {
						p.seen[e.Kind] = map[string]string{}
					}
					p.seen[e.Kind][e.Name] = e.SourceTx
					select {
					case ch <- Event{Kind: e.Kind, Name: e.Name, Type: EventUpdated, SourceTx: e.SourceTx}:
					default:
						// drop;非阻塞;backoff 留接口
					}
				}
			}
		}
	}()
	return ch, nil
}

// Stop 终止轮询(关闭 stop channel)。
func (p *pollingInformer) Stop() {
	close(p.stop)
}