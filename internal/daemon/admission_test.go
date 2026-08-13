package daemon

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Under allow, a burst is absorbed up to the ceiling and shed above it. The
// slot budget bounds steps process-wide; nothing bounded runs, so a burst grew
// goroutines and retained plan state instead of answering.
func TestMaxRunsPerPipelineShedsAboveTheCeiling(t *testing.T) {
	p := &probe{release: make(chan struct{})}
	_, server, _ := serveOne(t, p, Options{MaxRunsPerPipeline: 2}, map[string]string{
		"slow.yaml": webhookYAML("/slow", "out", "slow"),
	})

	const callers = 6
	var (
		wg       sync.WaitGroup
		accepted atomic.Int64
		shed     atomic.Int64
		other    atomic.Int64
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch status, _ := post(t, server, "/slow", "{}"); status {
			case http.StatusOK:
				accepted.Add(1)
			case http.StatusTooManyRequests:
				shed.Add(1)
			default:
				other.Add(1)
			}
		}()
	}

	// Let the callers pile up against the ceiling before releasing the runs.
	deadline := time.After(2 * time.Second)
	for p.live.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d run(s) started, want the ceiling of 2 to be reached", p.live.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Nothing may exceed the ceiling while the runs are still held.
	if live := p.live.Load(); live > 2 {
		t.Errorf("%d runs in flight, want at most the ceiling of 2", live)
	}
	close(p.release)
	wg.Wait()

	if other.Load() != 0 {
		t.Errorf("%d caller(s) got neither 200 nor 429", other.Load())
	}
	if shed.Load() == 0 {
		t.Error("no caller was shed: the ceiling did not bite")
	}
	if accepted.Load()+shed.Load() != callers {
		t.Errorf("accounted for %d of %d callers", accepted.Load()+shed.Load(), callers)
	}
	if peak := p.peak.Load(); peak > 2 {
		t.Errorf("%d runs overlapped, want at most the ceiling of 2", peak)
	}
}

// A negative ceiling is the documented way to ask for the pre-cap behaviour,
// and it must not be confused with the zero that selects the default.
func TestNegativeMaxRunsRemovesTheCap(t *testing.T) {
	d := New(nil, Options{MaxRunsPerPipeline: -1, Logger: quiet()})
	if d.maxRuns != 0 {
		t.Errorf("maxRuns = %d, want 0 (uncapped)", d.maxRuns)
	}

	d = New(nil, Options{Logger: quiet()})
	if d.maxRuns != DefaultMaxRunsPerPipeline {
		t.Errorf("maxRuns = %d, want the default %d", d.maxRuns, DefaultMaxRunsPerPipeline)
	}

	d = New(nil, Options{MaxRunsPerPipeline: 5, Logger: quiet()})
	if d.maxRuns != 5 {
		t.Errorf("maxRuns = %d, want 5", d.maxRuns)
	}
}

// max_body bounds bytes, not time. Without a read timeout a caller that sends
// headers promptly and then dribbles the body holds a connection and a handler
// goroutine for as long as it likes.
func TestServerTimeoutsAreSet(t *testing.T) {
	d := New(nil, Options{Logger: quiet()})

	if d.readTimeout != DefaultReadTimeout {
		t.Errorf("readTimeout = %v, want %v", d.readTimeout, DefaultReadTimeout)
	}
	if d.idleTimeout != DefaultIdleTimeout {
		t.Errorf("idleTimeout = %v, want %v", d.idleTimeout, DefaultIdleTimeout)
	}
	// Zero rather than defaulted, and deliberately so: the webhook handler runs
	// the pipeline synchronously, and WriteTimeout is measured from the end of
	// the request headers, so any value below the trigger's own timeout would
	// truncate responses the run was about to produce.
	if d.writeTimeout != 0 {
		t.Errorf("writeTimeout = %v, want 0 so the webhook listener leaves it unset", d.writeTimeout)
	}

	custom := New(nil, Options{
		ReadTimeout:  5 * time.Second,
		IdleTimeout:  7 * time.Second,
		WriteTimeout: 9 * time.Second,
		Logger:       quiet(),
	})
	if custom.readTimeout != 5*time.Second || custom.idleTimeout != 7*time.Second || custom.writeTimeout != 9*time.Second {
		t.Errorf("Options did not carry through: read=%v idle=%v write=%v",
			custom.readTimeout, custom.idleTimeout, custom.writeTimeout)
	}
}
