package monitors

import (
	"context"
	"testing"
	"time"
)

func TestWaitForWorkersJoinsSafeSubgoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	exited := make(chan struct{})
	goSafe("safe_shutdown_test", func() {
		close(started)
		<-ctx.Done()
		close(exited)
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("safe worker did not start")
	}
	cancel()
	WaitForWorkers()
	select {
	case <-exited:
	default:
		t.Fatal("wait returned before safe worker exited")
	}
}
