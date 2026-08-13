package trigger

import (
	"context"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// ScheduleName is the capability a pipeline references with
// "uses: trigger.schedule".
const ScheduleName = "trigger.schedule"

// MinScheduleInterval is the shortest interval a schedule may declare. It is a
// guard against "every: 1ns", which would spend the process firing rather than
// working, not a considered opinion about useful periods.
const MinScheduleInterval = time.Millisecond

// scheduleConfig is a trigger.schedule `with` block.
//
// Every is parsed here rather than through pipeline.Duration because this
// package must not import pipeline: the compiler holds a built trigger in its
// plan, so the dependency runs the other way.
type scheduleConfig struct {
	// Every is the interval between firings, such as "30s".
	Every string `yaml:"every"`
}

// ScheduleDefinition is the "trigger.schedule" capability: a pipeline runs once
// per interval.
//
// There is no cron syntax. Cron means a parser and a timezone database, which
// is a dependency and a decision of its own; an interval covers what a daemon
// of this size is for, and "run at 03:00 in Paris" can be asked for when
// something actually needs it.
func ScheduleDefinition() Definition {
	return Definition{Name: ScheduleName, New: newSchedule}
}

func newSchedule(cfg node.Config) (Trigger, error) {
	var c scheduleConfig
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}
	if c.Every == "" {
		return nil, node.Errf(node.KindInvalidInput, "missing_every",
			"trigger.schedule requires an interval such as \"30s\"")
	}
	every, err := time.ParseDuration(c.Every)
	if err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "bad_every",
			"invalid interval %q", c.Every)
	}
	if every < MinScheduleInterval {
		return nil, node.Errf(node.KindInvalidInput, "every_too_short",
			"interval %q is shorter than %s", c.Every, MinScheduleInterval)
	}

	return &schedule{
		every: every,
		desc: Descriptor{
			Name: ScheduleName,
			Provides: []node.PortDescriptor{
				{Name: "fired_at", Type: node.TypeOf[*types.Time]()},
			},
		},
	}, nil
}

type schedule struct {
	every time.Duration
	desc  Descriptor
}

func (s *schedule) Descriptor() Descriptor { return s.desc }

// Claim is zero: any number of schedules coexist, unlike routes.
func (s *schedule) Claim() Claim { return Claim{} }

// Listen fires on every tick until ctx ends.
//
// Each firing gets its own goroutine because fire runs the pipeline to
// completion. Calling it inline would make a pipeline slower than its interval
// silently reshape the schedule, since a Ticker drops ticks nobody is waiting
// on. Overlap is a declared policy the daemon applies inside fire, and it
// cannot apply it if the ticker loop has already hidden the overlap.
func (s *schedule) Listen(ctx context.Context, fire Fire) error {
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case t := <-ticker.C:
			values := map[string]node.Value{
				"fired_at": node.NewValue(&types.Time{Value: t}),
			}
			go fire(ctx, values)
		}
	}
}
