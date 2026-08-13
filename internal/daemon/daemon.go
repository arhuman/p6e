// Package daemon serves compiled pipelines: it loads a directory of them,
// keeps the ones that declare a trigger, and runs each one when its trigger
// fires.
//
// It is the only part of p6e that knows a process can outlive a single run, and
// everything that follows from that lives here rather than in the engine:
// bounding the whole process rather than one run, what to do when an event
// arrives while the last one is still going, taking a misbehaving pipeline out
// of service, and draining in-flight work on the way down.
//
// The engine below it is unchanged. A trigger supplies a run's declared inputs
// and the ordinary executor does the rest, so nothing here reaches into
// scheduling.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arhuman/p6e/internal/runtime"
	"github.com/arhuman/p6e/internal/trigger"
)

// Defaults for Options.
const (
	DefaultAddr           = ":8080"
	DefaultMaxConcurrency = 256
	DefaultDrainTimeout   = 30 * time.Second
	DefaultReadTimeout    = 30 * time.Second
)

// Options configures a daemon. Everything is optional.
type Options struct {
	// Addr is the address the webhook listener binds. Unused when no served
	// pipeline has an HTTP trigger.
	Addr string
	// MaxConcurrency bounds steps in flight across every pipeline at once, not
	// pipelines. Zero selects DefaultMaxConcurrency.
	MaxConcurrency int
	// DrainTimeout bounds how long Serve waits for in-flight runs once it is
	// shutting down. Zero selects DefaultDrainTimeout.
	DrainTimeout time.Duration
	// AbandonAfter is how long a run waits for steps still going once it has
	// stopped, passed through to the executor. Zero takes the executor's own
	// default. Lowering it makes a wedged node surface sooner, at the cost of
	// giving a merely slow one less room.
	AbandonAfter time.Duration
	// Logger receives one record per run. Zero selects slog's default.
	Logger *slog.Logger
}

// Daemon serves a set of loaded pipelines.
type Daemon struct {
	pipelines []*Pipeline
	slots     chan struct{}
	log       *slog.Logger
	addr      string
	drain     time.Duration
	abandon   time.Duration

	server *http.Server
	// routes maps a claim key such as "POST /hooks/deploy" to the pipeline that
	// answers it. Load has already proven no two pipelines share one.
	routes map[string]*Pipeline

	// mu guards draining and orders it against run registration, so a run
	// cannot start after the drain has decided what to wait for.
	mu       sync.RWMutex
	draining bool
	wg       sync.WaitGroup
	inflight atomic.Int64
}

// New builds a daemon over the pipelines a Load produced.
func New(served []*Pipeline, opts Options) *Daemon {
	d := &Daemon{
		pipelines: served,
		log:       opts.Logger,
		addr:      opts.Addr,
		drain:     opts.DrainTimeout,
		abandon:   opts.AbandonAfter,
		routes:    map[string]*Pipeline{},
	}
	if d.log == nil {
		d.log = slog.Default()
	}
	if d.addr == "" {
		d.addr = DefaultAddr
	}
	if d.drain <= 0 {
		d.drain = DefaultDrainTimeout
	}

	concurrency := opts.MaxConcurrency
	if concurrency <= 0 {
		concurrency = DefaultMaxConcurrency
	}
	d.slots = runtime.NewSlots(concurrency)

	for _, p := range served {
		if _, ok := p.Trigger().(trigger.HTTPDriven); ok {
			d.routes[p.Trigger().Claim().Key] = p
		}
	}
	return d
}

// Inflight reports how many runs are in progress across every pipeline.
func (d *Daemon) Inflight() int64 { return d.inflight.Load() }

// Addr reports the address the webhook listener uses.
func (d *Daemon) Addr() string { return d.addr }

// Serve runs until ctx is done, then drains.
//
// Draining means: stop accepting events, let the runs already going finish, and
// give up on them after DrainTimeout. A run abandoned by the drain is reported,
// not waited for, for the same reason the executor abandons a stuck step.
//
// It returns nil on a clean shutdown, and an error only when it could not
// listen at all.
func (d *Daemon) Serve(ctx context.Context) error {
	if len(d.pipelines) == 0 {
		return errors.New("no pipelines to serve")
	}

	listenCtx, stopListeners := context.WithCancel(ctx)
	defer stopListeners()

	var listeners sync.WaitGroup
	for _, p := range d.pipelines {
		driven, ok := p.Trigger().(trigger.SelfDriven)
		if !ok {
			continue
		}
		listeners.Add(1)
		go func() {
			defer listeners.Done()
			d.listen(listenCtx, p, driven)
		}()
	}

	serverErr := make(chan error, 1)
	if len(d.routes) > 0 {
		d.server = &http.Server{
			Addr:              d.addr,
			Handler:           d.handler(),
			ReadHeaderTimeout: DefaultReadTimeout,
		}
		go func() {
			err := d.server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serverErr <- err
		}()
		d.log.Info("serving webhooks", slog.String("addr", d.addr), slog.Int("routes", len(d.routes)))
	}

	var failure error
	select {
	case <-ctx.Done():
	case failure = <-serverErr:
		if failure != nil {
			failure = fmt.Errorf("webhook listener: %w", failure)
		}
	}

	stopListeners()
	d.shutdownServer()
	listeners.Wait()
	d.drainRuns()
	return failure
}

// listen runs one self-driven trigger's loop, and survives it panicking.
//
// The recovery is here because this goroutine is new surface: the executor
// recovers a panicking node, but nothing was ever watching a trigger's own
// loop, and one malformed event should not take the process down with it.
func (d *Daemon) listen(ctx context.Context, p *Pipeline, driven trigger.SelfDriven) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("trigger listener panicked and has stopped",
				slog.String("pipeline", p.Name), slog.Any("panic", r))
		}
	}()

	if err := driven.Listen(ctx, d.fire(p)); err != nil && !errors.Is(err, context.Canceled) {
		d.log.Error("trigger listener stopped",
			slog.String("pipeline", p.Name), slog.String("error", err.Error()))
	}
}

// shutdownServer stops accepting requests and lets the handlers already running
// finish, which is the same bargain drainRuns makes for schedules.
func (d *Daemon) shutdownServer() {
	if d.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.drain)
	defer cancel()

	if err := d.server.Shutdown(ctx); err != nil {
		d.log.Warn("webhook listener did not shut down cleanly", slog.String("error", err.Error()))
	}
}

// drainRuns refuses new runs and waits for the ones in progress.
func (d *Daemon) drainRuns() {
	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		d.log.Info("all runs finished")
	case <-time.After(d.drain):
		d.log.Warn("gave up waiting for runs still in progress",
			slog.Int64("inflight", d.inflight.Load()), slog.Duration("after", d.drain))
	}
}
