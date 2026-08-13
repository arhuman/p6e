package trigger

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func TestScheduleDescriptor(t *testing.T) {
	trg := build(t, ScheduleName, "every: 30s")

	got, ok := trg.Descriptor().Provided("fired_at")
	if !ok {
		t.Fatal("schedule should provide fired_at")
	}
	if got != "Time" {
		t.Errorf("fired_at is %s, want Time", got)
	}
}

// Schedules do not compete for anything, unlike routes, so any number coexist.
func TestScheduleClaimsNothing(t *testing.T) {
	trg := build(t, ScheduleName, "every: 30s")

	if !trg.Claim().IsZero() {
		t.Errorf("Claim() = %v, want nothing claimed", trg.Claim())
	}
}

func TestScheduleRejectsBadConfig(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, want config }{
		"no interval":      {"{}", "requires an interval"},
		"unparseable":      {"every: soon", "invalid interval"},
		"shorter than min": {"every: 1ns", "shorter than"},
		"unknown field":    {"{every: 1s, jitter: 2s}", "jitter"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := buildErr(t, ScheduleName, tc.cfg); !strings.Contains(got, string(tc.want)) {
				t.Errorf("error %q should mention %q", got, string(tc.want))
			}
		})
	}
}

func TestScheduleFiresUntilCancelled(t *testing.T) {
	trg := build(t, ScheduleName, "every: 5ms").(SelfDriven)

	var fired atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- trg.Listen(ctx, func(context.Context, map[string]node.Value) Outcome {
			fired.Add(1)
			return Outcome{}
		})
	}()

	deadline := time.After(2 * time.Second)
	for fired.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("schedule fired %d times in 2s, want at least 3", fired.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Listen returned %v, want nil on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return after its context was cancelled")
	}
}

// fired_at carries the instant the tick happened, which is the one thing a
// scheduled pipeline cannot work out for itself after the fact.
func TestScheduleProvidesFiredAt(t *testing.T) {
	trg := build(t, ScheduleName, "every: 5ms").(SelfDriven)

	ctx := t.Context()
	got := make(chan node.Value, 1)
	go func() {
		_ = trg.Listen(ctx, func(_ context.Context, values map[string]node.Value) Outcome {
			select {
			case got <- values["fired_at"]:
			default:
			}
			return Outcome{}
		})
	}()

	select {
	case value := <-got:
		if value.Type() != "Time" {
			t.Errorf("fired_at is supplied as %s, want Time", value.Type())
		}
		if _, ok := value.Interface().(*types.Time); !ok {
			t.Errorf("fired_at holds %T, want *types.Time", value.Interface())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("schedule did not fire within 2s")
	}
}
