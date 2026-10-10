package runtime

import (
	"context"
	"sync/atomic"
)

// Workqueue 接口:Enqueue 入队;Run 消费并调 handler;Len 报告当前长度。
type Workqueue interface {
	Enqueue(ev Event)
	Run(ctx context.Context, handler func(Event)) error
	Len() int
}

// channelWorkqueue 是 channel-based workqueue,backoff 留接口不实现。
type channelWorkqueue struct {
	ch chan Event
	n  atomic.Int64
}

// NewChannelWorkqueue 构造 channel-based workqueue(缓冲 256)。
func NewChannelWorkqueue() Workqueue {
	return &channelWorkqueue{ch: make(chan Event, 256)}
}

// Enqueue 非阻塞入队;满则丢弃(留 backoff 接口)。
func (w *channelWorkqueue) Enqueue(ev Event) {
	select {
	case w.ch <- ev:
		w.n.Add(1)
	default:
	}
}

// Run 阻塞消费 channel,直到 ctx cancel。
func (w *channelWorkqueue) Run(ctx context.Context, handler func(Event)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-w.ch:
			handler(ev)
			w.n.Add(-1)
		}
	}
}

// Len 返回当前未处理 event 数。
func (w *channelWorkqueue) Len() int { return int(w.n.Load()) }