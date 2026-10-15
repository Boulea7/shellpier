package netutil

import (
	"context"
	"testing"
	"time"
)

func TestDirectDialerRespectsContextBudget(t *testing.T) {
	if got := directDialer(context.Background()).Timeout; got != 10*time.Second {
		t.Fatalf("default timeout %v", got)
	}
	for _, limit := range []time.Duration{time.Millisecond, time.Minute} {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		if got := directDialer(ctx).Timeout; got != 0 {
			t.Fatalf("context budget was capped at %v", got)
		}
		cancel()
	}
}
