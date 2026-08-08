package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type metricsShutdownFunc func(context.Context) error

func (f metricsShutdownFunc) Shutdown(ctx context.Context) error { return f(ctx) }

func TestShutdownMetricsUsesFreshBoundedContext(t *testing.T) {
	applicationCtx, cancelApplication := context.WithCancel(context.Background())
	cancelApplication()
	if applicationCtx.Err() == nil {
		t.Fatal("application context was not cancelled")
	}

	owner := metricsShutdownFunc(func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Fatalf("shutdown context was already cancelled: %v", ctx.Err())
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > metricsShutdownTimeout {
			t.Fatalf("shutdown deadline remaining = %v", remaining)
		}
		return nil
	})

	if err := shutdownMetrics(owner); err != nil {
		t.Fatalf("shutdownMetrics() error = %v", err)
	}
}

func TestShutdownMetricsPropagatesTimeout(t *testing.T) {
	owner := metricsShutdownFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	err := shutdownMetricsWithin(owner, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdownMetricsWithin() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("bounded shutdown took %v", elapsed)
	}
}
