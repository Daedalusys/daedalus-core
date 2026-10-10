package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestChannelWorkqueue_Roundtrip(t *testing.T) {
	wq := runtime.NewChannelWorkqueue()
	wq.Enqueue(runtime.Event{Kind: objectmodel.KindService, Name: "nginx"})
	if wq.Len() != 1 {
		t.Errorf("Len=%d", wq.Len())
	}
	done := make(chan runtime.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := wq.Run(ctx, func(ev runtime.Event) { done <- ev })
	if err != nil && err != context.Canceled {
		t.Fatal(err)
	}
	select {
	case ev := <-done:
		if ev.Name != "nginx" {
			t.Errorf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("handler 未调用")
	}
}