package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/Daedalusys/daedalus-core/internal/controller/runtime"
	"github.com/Daedalusys/daedalus-core/internal/desiredview"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

func TestPollingInformer_EmitsOnChange(t *testing.T) {
	v := desiredview.ViewWithForTest([]desiredview.Entry{
		{Kind: objectmodel.KindService, Name: "nginx", DesiredState: "active", SourceTx: "tx-1"},
	})
	inf := runtime.NewPollingInformer(v)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := inf.Run(ctx, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Type != runtime.EventUpdated {
			t.Errorf("got %v", ev.Type)
		}
		if ev.SourceTx != "tx-1" {
			t.Errorf("source_tx = %q", ev.SourceTx)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("应产出 event")
	}
}