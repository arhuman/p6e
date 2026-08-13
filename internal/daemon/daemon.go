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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arhuman/p6e/internal/runtime"
	"github.com/arhuman/p6e/internal/trigger"
)

// Defaults for Options.
const (
	DefaultAddr           = ":8080"
	DefaultAdminAddr      = "127.0.0.1:8081"
	DefaultMaxConcurrency = 256
	DefaultDrainTimeout   = 30 * time.Second
	DefaultReadTimeout    = 30 * time.Second
	DefaultIdleTimeout    = 120 * time.Second
	// DefaultMaxRunsPerPipeline bounds concurrent runs of one pipeline under
	// the allow overlap policy, which is the default for a webhook. Steps are
	// bounded process-wide by the slot budget; runs were not bounded by
	// anything, so a burst grew goroutines and retained plan state instead of
	// shedding load.
	DefaultMaxRunsPerPipeline = 64
)

// AdminDisabled is the AdminAddr that serves no admin endpoints.
const AdminDisabled = "-"

// Options configures a daemon. Everything is optional.
type Options struct {
	// Addr is the address the webhook listener binds. Unused when no served
	// pipeline has an HTTP trigger.
	Addr string
	// AdminAddr is the address for liveness, readiness and metrics. Zero
	// selects DefaultAdminAddr, which is loopback: this surface describes every
	// pipeline in the process and should not be exposed by accident. Set it to
	// "-" to serve no admin endpoints at all.
	AdminAddr string
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
	// MaxRunsPerPipeline caps concurrent runs of a single pipeline under the
	// allow overlap policy. Zero selects DefaultMaxRunsPerPipeline; a negative
	// value removes the cap, which is the pre-cap behaviour and is only sane
	// behind something that already limits the rate.
	//
	// Over the cap the daemon answers 429 rather than queueing, because a queue
	// with no bound is the same problem one indirection further away.
	MaxRunsPerPipeline int
	// ReadTimeout bounds reading a whole request, headers and body. Zero
	// selects DefaultReadTimeout.
	//
	// It is what stops a caller sending headers promptly and then dribbling the
	// body forever: max_body bounds bytes, not time, so without this a slow
	// sender holds a connection and a handler goroutine for as long as it likes.
	ReadTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit unused. Zero
	// selects DefaultIdleTimeout.
	IdleTimeout time.Duration
	// WriteTimeout bounds the response write, measured from the end of the
	// request headers, which means it also bounds the handler. Zero leaves it
	// unset on the webhook listener, deliberately: that handler runs the
	// pipeline synchronously, and a run is already bounded by the trigger's own
	// required timeout. Setting this below that timeout cuts off responses the
	// pipeline was about to produce.
	//
	// The admin listener always sets it, since those handlers are trivial.
	WriteTimeout time.Duration
	// Logger receives one record per run. Zero selects slog's default.
	Logger *slog.Logger
}

// Daemon serves a set of loaded pipelines.
type Daemon struct {
	pipelines []*Pipeline
	slots     chan struct{}
	log       *slog.Logger
	addr      string
	adminAddr string
	drain     time.Duration
	abandon   time.Duration

	// maxRuns caps concurrent runs of one pipeline. Zero means no cap.
	maxRuns      int64
	readTimeout  time.Duration
	idleTimeout  time.Duration
	writeTimeout time.Duration

	server *http.Server
	admin  *http.Server
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
		adminAddr: opts.AdminAddr,
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
	if d.adminAddr == "" {
		d.adminAddr = DefaultAdminAddr
	}
	if d.drain <= 0 {
		d.drain = DefaultDrainTimeout
	}

	d.readTimeout = orDefault(opts.ReadTimeout, DefaultReadTimeout)
	d.idleTimeout = orDefault(opts.IdleTimeout, DefaultIdleTimeout)
	// Not defaulted: zero means "leave unset on the webhook listener", which is
	// a meaningful choice rather than an omission. See Options.WriteTimeout.
	d.writeTimeout = opts.WriteTimeout

	switch {
	case opts.MaxRunsPerPipeline < 0:
		d.maxRuns = 0 // explicitly uncapped
	case opts.MaxRunsPerPipeline == 0:
		d.maxRuns = DefaultMaxRunsPerPipeline
	default:
		d.maxRuns = int64(opts.MaxRunsPerPipeline)
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

func orDefault(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
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

	serverErr := make(chan error, 2)
	// The admin listener runs whether or not any pipeline is a webhook, because
	// a schedule-only daemon otherwise offers no way at all to tell whether it
	// is alive or has quarantined everything it was given.
	if d.adminAddr != AdminDisabled {
		d.admin = &http.Server{
			Addr:              d.adminAddr,
			Handler:           d.adminHandler(),
			ReadHeaderTimeout: d.readTimeout,
			ReadTimeout:       d.readTimeout,
			IdleTimeout:       d.idleTimeout,
			// These handlers do a little arithmetic over in-memory counters, so
			// a write that has not finished within the read budget is a stuck
			// peer rather than slow work.
			WriteTimeout: orDefault(d.writeTimeout, d.readTimeout),
		}
		go func() { serverErr <- listenAndServe(d.admin, "admin listener") }()
		d.log.Info("serving admin endpoints",
			slog.String("addr", d.adminAddr),
			slog.String("paths", strings.Join([]string{pathHealth, pathReady, pathMetrics}, " ")))
	}

	if len(d.routes) > 0 {
		d.server = &http.Server{
			Addr:              d.addr,
			Handler:           d.handler(),
			ReadHeaderTimeout: d.readTimeout,
			ReadTimeout:       d.readTimeout,
			IdleTimeout:       d.idleTimeout,
			// Deliberately whatever the caller asked for, usually unset: this
			// handler runs the pipeline synchronously and WriteTimeout is
			// measured from the end of the request headers, so any value below
			// the trigger's timeout truncates responses the run was about to
			// produce. The run is already bounded by that timeout.
			WriteTimeout: d.writeTimeout,
		}
		go func() { serverErr <- listenAndServe(d.server, "webhook listener") }()
		d.log.Info("serving webhooks", slog.String("addr", d.addr), slog.Int("routes", len(d.routes)))
		if open := d.openRoutes(); len(open) > 0 {
			d.log.Warn("serving webhook routes that authenticate nothing: anyone who can reach the listener can start these runs",
				slog.String("routes", strings.Join(open, ", ")),
				slog.String("remedy", "give the trigger an auth block, or front the listener with an authenticating proxy"))
		}
	}

	// Either listener failing ends the daemon: a process that answers webhooks
	// but cannot report its own health, or reports health while answering
	// nothing, is worse than one that stopped and said why.
	var failure error
	select {
	case <-ctx.Done():
	case failure = <-serverErr:
	}

	stopListeners()
	d.shutdownServer()
	listeners.Wait()
	d.drainRuns()
	// The admin listener closes last, so readiness reports "draining" for as
	// long as the drain actually lasts rather than going dark at the start of
	// it. That is the window a rolling deploy needs to see.
	d.shutdown(d.admin, "admin listener")
	return failure
}

// openRoutes lists the claim keys of webhook routes that verify nothing, in
// sorted order so the warning does not shuffle between restarts.
func (d *Daemon) openRoutes() []string {
	var open []string
	for key, p := range d.routes {
		auth, ok := p.Trigger().(trigger.Authenticating)
		if !ok || !auth.Authenticated() {
			open = append(open, key)
		}
	}
	sort.Strings(open)
	return open
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
func (d *Daemon) shutdownServer() { d.shutdown(d.server, "webhook listener") }

// listenAndServe runs one listener, naming it in any failure it reports. A
// closed server is an ordinary shutdown, not a failure.
func listenAndServe(server *http.Server, what string) error {
	err := server.ListenAndServe()
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (d *Daemon) shutdown(server *http.Server, what string) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.drain)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		d.log.Warn(what+" did not shut down cleanly", slog.String("error", err.Error()))
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
