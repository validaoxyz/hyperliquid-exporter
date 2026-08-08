package metrics

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProviderOwnerShutdownExactlyOnce(t *testing.T) {
	wantErr := errors.New("flush failed")
	var calls atomic.Int64
	owner := newProviderOwner(func(context.Context) error {
		calls.Add(1)
		return wantErr
	})

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	results := make(chan error, callers)
	for range callers {
		go func() {
			defer wg.Done()
			results <- owner.Shutdown(context.Background())
		}()
	}
	wg.Wait()
	close(results)

	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying shutdown calls = %d, want 1", got)
	}
	for err := range results {
		if !errors.Is(err, wantErr) {
			t.Fatalf("Shutdown() error = %v, want %v", err, wantErr)
		}
	}
}

func TestProviderOwnerUsesCallerContext(t *testing.T) {
	applicationCtx, cancelApplication := context.WithCancel(context.Background())
	cancelApplication()

	var sawFreshContext atomic.Bool
	owner := newProviderOwner(func(ctx context.Context) error {
		if ctx.Err() == nil {
			sawFreshContext.Store(true)
		}
		return nil
	})

	// Process shutdown must not reuse applicationCtx, which is already
	// cancelled. This models the fresh bounded context supplied by main.
	if applicationCtx.Err() == nil {
		t.Fatal("application context was not cancelled")
	}
	if err := owner.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !sawFreshContext.Load() {
		t.Fatal("provider did not receive the fresh shutdown context")
	}
}

func TestNilProviderOwnerShutdownIsSafe(t *testing.T) {
	var owner *ProviderOwner
	if err := owner.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil owner Shutdown() error = %v", err)
	}
}
