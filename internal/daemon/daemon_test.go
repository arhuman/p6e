package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/trigger"
)

// probe is the set of behaviours a daemon has to cope with, in node form: work
// that takes time, work that fails, and work that refuses to stop.
type probe struct {
	ran     atomic.Int64
	live    atomic.Int64
	peak    atomic.Int64
	hold    time.Duration
	release chan struct{}
}

func (p *probe) registry(t *testing.T) *pipeline.Registries {
	t.Helper()
	reg := node.NewRegistry()

	reg.MustRegister(node.Static("echo", node.NewTypedNode("echo",
		func(_ context.Context, _ *node.ExecutionContext, b *types.Bytes) node.Result[*types.Bytes] {
			p.ran.Add(1)
			return node.Ok(b)
		})))

	reg.MustRegister(node.Static("tick", node.NewSource("tick",
		func(context.Context, *node.ExecutionContext) node.Result[*types.Bytes] {
			p.ran.Add(1)
			return node.Ok(&types.Bytes{Value: []byte("tick")})
		})))

	// slow tracks overlap: how many runs of one pipeline were inside it at once.
	reg.MustRegister(node.Static("slow", node.NewTypedNode("slow",
		func(ctx context.Context, _ *node.ExecutionContext, b *types.Bytes) node.Result[*types.Bytes] {
			p.ran.Add(1)
			live := p.live.Add(1)
			defer p.live.Add(-1)
			for {
				peak := p.peak.Load()
				if live <= peak || p.peak.CompareAndSwap(peak, live) {
					break
				}
			}
			if p.release != nil {
				select {
				case <-p.release:
				case <-ctx.Done():
				}
			}
			if p.hold > 0 {
				select {
				case <-time.After(p.hold):
				case <-ctx.Done():
				}
			}
			return node.Ok(b)
		})))

	reg.MustRegister(node.Static("boom", node.NewTypedNode("boom",
		func(_ context.Context, _ *node.ExecutionContext, b *types.Bytes) node.Result[*types.Bytes] {
			p.ran.Add(1)
			return node.Fail[*types.Bytes](node.Errf(node.KindPermanent, "boom", "this step always fails"))
		})))

	// deaf ignores its context entirely, which is the one thing the engine
	// cannot undo and the reason quarantine exists.
	reg.MustRegister(node.Static("deaf", node.NewTypedNode("deaf",
		func(_ context.Context, _ *node.ExecutionContext, b *types.Bytes) node.Result[*types.Bytes] {
			p.ran.Add(1)
			time.Sleep(300 * time.Millisecond)
			return node.Ok(b)
		})))

	return &pipeline.Registries{Nodes: reg, Triggers: trigger.Builtins()}
}

// quiet keeps a daemon's logs out of the test output, since several tests
// deliberately provoke errors.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeDir puts each named pipeline in a temp directory and returns its path.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	return dir
}

func webhookYAML(path, step, uses string) string {
	return "version: 1\ninputs:\n  body: Bytes\ntrigger:\n  uses: trigger.webhook\n  with:\n    path: " + path +
		"\n  timeout: 2s\n  respond_with: " + step + "\nsteps:\n  " + step + ":\n    uses: " + uses + "\n    needs: [body]\n"
}

// serveOne loads a directory and returns a daemon plus an httptest server over
// its routes, which exercises routing and handling without binding a port.
func serveOne(t *testing.T, p *probe, opts Options, files map[string]string) (*Daemon, *httptest.Server, *Loaded) {
	t.Helper()

	loaded, err := Load(writeDir(t, files), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if opts.Logger == nil {
		opts.Logger = quiet()
	}
	d := New(loaded.Served, opts)

	server := httptest.NewServer(d.handler())
	t.Cleanup(server.Close)
	return d, server, loaded
}

func post(t *testing.T, server *httptest.Server, path, body string) (int, string) {
	t.Helper()

	resp, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	return resp.StatusCode, string(payload)
}

func TestWebhookRunsThePipelineAndAnswers(t *testing.T) {
	p := &probe{}
	_, server, loaded := serveOne(t, p, Options{}, map[string]string{
		"echo.yaml": webhookYAML("/echo", "out", "echo"),
	})
	if len(loaded.Served) != 1 {
		t.Fatalf("Served = %d pipelines, want 1 (rejected: %v)", len(loaded.Served), loaded.Rejected)
	}

	status, body := post(t, server, "/echo", `{"hello":"world"}`)
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body != `{"hello":"world"}` {
		t.Errorf("body = %q, want the request echoed back", body)
	}
	if p.ran.Load() != 1 {
		t.Errorf("the pipeline ran %d times, want 1", p.ran.Load())
	}
}

// The route is claimed by method as well as path, so an unmatched method must
// not fall through to the pipeline.
func TestWebhookIgnoresAnotherMethod(t *testing.T) {
	p := &probe{}
	_, server, _ := serveOne(t, p, Options{}, map[string]string{
		"echo.yaml": webhookYAML("/echo", "out", "echo"),
	})

	resp, err := server.Client().Get(server.URL + "/echo")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		t.Error("a GET should not reach a pipeline that claimed POST")
	}
	if p.ran.Load() != 0 {
		t.Error("the pipeline ran for a method it did not claim")
	}
}

func TestWebhookReportsAFailedRun(t *testing.T) {
	p := &probe{}
	_, server, _ := serveOne(t, p, Options{}, map[string]string{
		"boom.yaml": webhookYAML("/boom", "out", "boom"),
	})

	status, _ := post(t, server, "/boom", "{}")
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for a failed run", status)
	}
}

// drop is the schedule default and the safe one: an event arriving while a run
// is in progress is refused rather than piling up.
func TestOverlapDropRefusesAConcurrentEvent(t *testing.T) {
	p := &probe{release: make(chan struct{})}
	src := strings.Replace(webhookYAML("/slow", "out", "slow"),
		"  timeout: 2s", "  timeout: 2s\n  on_overlap: drop", 1)
	_, server, _ := serveOne(t, p, Options{}, map[string]string{"slow.yaml": src})

	first := make(chan int, 1)
	go func() {
		status, _ := post(t, server, "/slow", "{}")
		first <- status
	}()

	// Wait until the first run is inside the node and holding the pipeline.
	deadline := time.After(2 * time.Second)
	for p.live.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the first run never started")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	status, _ := post(t, server, "/slow", "{}")
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 while a run is in progress under drop", status)
	}

	close(p.release)
	if got := <-first; got != http.StatusOK {
		t.Errorf("the first run answered %d, want 200", got)
	}
	if peak := p.peak.Load(); peak > 1 {
		t.Errorf("%d runs overlapped under drop, want at most 1", peak)
	}
}

// allow is the webhook default: each caller waits on its own event, and
// refusing one because another is in flight would be surprising.
func TestOverlapAllowRunsConcurrently(t *testing.T) {
	p := &probe{release: make(chan struct{})}
	_, server, _ := serveOne(t, p, Options{}, map[string]string{
		"slow.yaml": webhookYAML("/slow", "out", "slow"),
	})

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if status, _ := post(t, server, "/slow", "{}"); status != http.StatusOK {
				t.Errorf("status = %d, want 200", status)
			}
		}()
	}

	deadline := time.After(2 * time.Second)
	for p.live.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d run(s) overlapped, want the default to allow it", p.live.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(p.release)
	wg.Wait()
}

func TestScheduleFiresItsPipeline(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"tick.yaml": "version: 1\ntrigger:\n  uses: trigger.schedule\n  with:\n    every: 5ms\nsteps:\n  a:\n    uses: tick\n",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Served) != 1 {
		t.Fatalf("Served = %d, want 1 (rejected: %v)", len(loaded.Served), loaded.Rejected)
	}

	d := New(loaded.Served, Options{Logger: quiet(), DrainTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- d.Serve(ctx) }()

	deadline := time.After(3 * time.Second)
	for p.ran.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("the schedule fired %d times in 3s, want at least 3", p.ran.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}

// Draining means the runs already going are allowed to finish, which is the
// whole difference between stopping and being killed.
func TestDrainWaitsForRunsInProgress(t *testing.T) {
	p := &probe{release: make(chan struct{})}
	loaded, err := Load(writeDir(t, map[string]string{
		"slow.yaml": "version: 1\ntrigger:\n  uses: trigger.schedule\n  with:\n    every: 5ms\n  on_overlap: allow\nsteps:\n  a:\n    uses: tick\n",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := New(loaded.Served, Options{Logger: quiet(), DrainTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = d.Serve(ctx); close(stopped) }()

	deadline := time.After(3 * time.Second)
	for p.ran.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the schedule never fired")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	<-stopped
	if got := d.Inflight(); got != 0 {
		t.Errorf("%d run(s) were still in flight after the drain returned, want 0", got)
	}
}

// A node that ignores its context leaks a goroutine for the life of the
// process. One is a bug; three in a row at whatever rate the trigger fires is a
// slow-motion outage, so the pipeline is taken out of service.
func TestRepeatedAbandonmentQuarantinesAPipeline(t *testing.T) {
	p := &probe{}
	src := strings.Replace(webhookYAML("/deaf", "out", "deaf"), "  timeout: 2s", "  timeout: 20ms", 1)
	_, server, loaded := serveOne(t, p, Options{AbandonAfter: 20 * time.Millisecond}, map[string]string{
		"deaf.yaml": src,
	})
	if len(loaded.Served) != 1 {
		t.Fatalf("Served = %d, want 1 (rejected: %v)", len(loaded.Served), loaded.Rejected)
	}

	for i := range QuarantineAfter {
		if status, _ := post(t, server, "/deaf", "{}"); status != http.StatusGatewayTimeout {
			t.Fatalf("run %d answered %d, want 504 from the timeout", i, status)
		}
	}

	if !loaded.Served[0].Quarantined() {
		t.Fatalf("a pipeline that abandoned a step %d times running should be quarantined", QuarantineAfter)
	}
	if status, _ := post(t, server, "/deaf", "{}"); status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 once quarantined", status)
	}
}

// A run that succeeds clears the streak, so an intermittent slow node never
// accumulates its way into quarantine.
func TestASuccessfulRunClearsTheAbandonmentStreak(t *testing.T) {
	p := &probe{}
	_, server, loaded := serveOne(t, p, Options{}, map[string]string{
		"echo.yaml": webhookYAML("/echo", "out", "echo"),
	})

	for range QuarantineAfter + 2 {
		if status, _ := post(t, server, "/echo", "{}"); status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	}
	if loaded.Served[0].Quarantined() {
		t.Error("a pipeline whose runs all succeeded should never be quarantined")
	}
}

func TestServeReportsNothingToDo(t *testing.T) {
	d := New(nil, Options{Logger: quiet()})

	if err := d.Serve(context.Background()); err == nil {
		t.Error("serving no pipelines should be reported rather than blocking forever")
	}
}
