package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// admin returns a server over the admin endpoints of a daemon loaded from files.
func admin(t *testing.T, p *probe, files map[string]string) (*Daemon, *httptest.Server, *Loaded) {
	t.Helper()

	loaded, err := Load(writeDir(t, files), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := New(loaded.Served, Options{Logger: quiet()})

	server := httptest.NewServer(d.adminHandler())
	t.Cleanup(server.Close)
	return d, server, loaded
}

func get(t *testing.T, server *httptest.Server, path string) (int, string) {
	t.Helper()

	resp, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

func TestHealthzReportsTheProcessIsUp(t *testing.T) {
	p := &probe{}
	_, server, _ := admin(t, p, map[string]string{"echo.yaml": webhookYAML("/echo", "out", "echo")})

	if status, _ := get(t, server, pathHealth); status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
}

// Liveness must not follow pipeline health. A daemon whose pipelines are all
// quarantined is broken, but restarting it is a person's decision, and a
// liveness probe that failed would make an orchestrator loop instead.
func TestHealthzStaysUpWhenEveryPipelineIsQuarantined(t *testing.T) {
	p := &probe{}
	_, server, loaded := admin(t, p, map[string]string{"echo.yaml": webhookYAML("/echo", "out", "echo")})
	loaded.Served[0].quarantined.Store(true)

	if status, _ := get(t, server, pathHealth); status != http.StatusOK {
		t.Errorf("liveness = %d, want 200 even with everything quarantined", status)
	}
	if status, body := get(t, server, pathReady); status != http.StatusServiceUnavailable {
		t.Errorf("readiness = %d (%q), want 503", status, body)
	}
}

func TestReadyzIsReadyWhenPipelinesAreHealthy(t *testing.T) {
	p := &probe{}
	_, server, _ := admin(t, p, map[string]string{
		"a.yaml": webhookYAML("/a", "out", "echo"),
		"b.yaml": webhookYAML("/b", "out", "echo"),
	})

	if status, body := get(t, server, pathReady); status != http.StatusOK {
		t.Errorf("status = %d (%q), want 200", status, body)
	}
}

// One quarantined pipeline out of two still leaves work the daemon can do.
func TestReadyzToleratesOneQuarantinedPipeline(t *testing.T) {
	p := &probe{}
	_, server, loaded := admin(t, p, map[string]string{
		"a.yaml": webhookYAML("/a", "out", "echo"),
		"b.yaml": webhookYAML("/b", "out", "echo"),
	})
	loaded.Served[0].quarantined.Store(true)

	if status, body := get(t, server, pathReady); status != http.StatusOK {
		t.Errorf("status = %d (%q), want 200 while another pipeline still works", status, body)
	}
}

func TestReadyzReportsDraining(t *testing.T) {
	p := &probe{}
	d, server, _ := admin(t, p, map[string]string{"echo.yaml": webhookYAML("/echo", "out", "echo")})

	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()

	status, body := get(t, server, pathReady)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 while draining", status)
	}
	if !strings.Contains(body, "draining") {
		t.Errorf("body = %q, want it to say the daemon is draining", body)
	}
}

// The reason this surface exists: a quarantined pipeline used to be visible
// only as one log line, so a half-dead daemon looked healthy to monitoring.
func TestMetricsExposeQuarantine(t *testing.T) {
	p := &probe{}
	_, server, loaded := admin(t, p, map[string]string{"echo.yaml": webhookYAML("/echo", "out", "echo")})

	status, body := get(t, server, pathMetrics)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, `p6e_pipeline_quarantined{pipeline="echo"} 0`) {
		t.Errorf("metrics should report echo as healthy:\n%s", body)
	}

	loaded.Served[0].quarantined.Store(true)
	_, body = get(t, server, pathMetrics)
	if !strings.Contains(body, `p6e_pipeline_quarantined{pipeline="echo"} 1`) {
		t.Errorf("metrics should report echo as quarantined:\n%s", body)
	}
	if !strings.Contains(body, "p6e_pipelines_quarantined 1") {
		t.Errorf("metrics should count quarantined pipelines:\n%s", body)
	}
}

func TestMetricsCountRunsAndFailures(t *testing.T) {
	p := &probe{}
	d, ops, loaded := admin(t, p, map[string]string{"boom.yaml": webhookYAML("/boom", "out", "boom")})

	hooks := httptest.NewServer(d.handler())
	defer hooks.Close()
	for range 2 {
		post(t, hooks, "/boom", "{}")
	}

	_, body := get(t, ops, pathMetrics)
	if !strings.Contains(body, `p6e_pipeline_runs_total{pipeline="boom"} 2`) {
		t.Errorf("metrics should count 2 runs:\n%s", body)
	}
	if !strings.Contains(body, `p6e_pipeline_failures_total{pipeline="boom"} 2`) {
		t.Errorf("metrics should count 2 failures:\n%s", body)
	}
	if got := loaded.Served[0].Runs(); got != 2 {
		t.Errorf("Runs() = %d, want 2", got)
	}
}

// Abandonment is what quarantine acts on, so the total has to be visible
// separately from the streak that drives the policy.
func TestMetricsExposeAbandonedRuns(t *testing.T) {
	p := &probe{}
	src := strings.Replace(webhookYAML("/deaf", "out", "deaf"), "  timeout: 2s", "  timeout: 20ms", 1)

	loaded, err := Load(writeDir(t, map[string]string{"deaf.yaml": src}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := New(loaded.Served, Options{Logger: quiet(), AbandonAfter: 20 * time.Millisecond})

	hooks := httptest.NewServer(d.handler())
	defer hooks.Close()
	ops := httptest.NewServer(d.adminHandler())
	defer ops.Close()

	post(t, hooks, "/deaf", "{}")

	_, body := get(t, ops, pathMetrics)
	if !strings.Contains(body, `p6e_pipeline_abandoned_runs_total{pipeline="deaf"} 1`) {
		t.Errorf("metrics should report the abandoned run:\n%s", body)
	}
}

func TestMetricsReportTheSharedBudget(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{"echo.yaml": webhookYAML("/echo", "out", "echo")}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := New(loaded.Served, Options{Logger: quiet(), MaxConcurrency: 7})

	server := httptest.NewServer(d.adminHandler())
	defer server.Close()

	_, body := get(t, server, pathMetrics)
	if !strings.Contains(body, "p6e_slots_total 7") {
		t.Errorf("metrics should report the configured budget:\n%s", body)
	}
	if !strings.Contains(body, "p6e_pipelines_served 1") {
		t.Errorf("metrics should report how many pipelines are served:\n%s", body)
	}
}

// Pipeline names come from filenames, so a name carrying a quote must not be
// able to corrupt the exposition format.
func TestMetricsEscapeLabelValues(t *testing.T) {
	if got := escapeLabel(`odd"name\here`); got != `odd\"name\\here` {
		t.Errorf("escapeLabel = %q, want the quote and backslash escaped", got)
	}
}

// A pipeline cannot shadow an admin route or be shadowed by one, because the
// two are not on the same listener at all.
func TestAdminAndWebhookListenersAreSeparate(t *testing.T) {
	p := &probe{}
	d, hooks, _ := serveOne(t, p, Options{}, map[string]string{
		"metrics.yaml": webhookYAML(pathMetrics, "out", "echo"),
	})

	// The pipeline owns /metrics on the webhook listener.
	if status, body := post(t, hooks, pathMetrics, `{"a":1}`); status != http.StatusOK || body != `{"a":1}` {
		t.Errorf("webhook /metrics answered %d %q, want the pipeline's own reply", status, body)
	}

	// The admin listener still serves its own, unaffected.
	ops := httptest.NewServer(d.adminHandler())
	defer ops.Close()
	if status, body := get(t, ops, pathMetrics); status != http.StatusOK || !strings.Contains(body, "p6e_pipelines_served") {
		t.Errorf("admin /metrics answered %d %q, want the daemon's metrics", status, body)
	}
}
