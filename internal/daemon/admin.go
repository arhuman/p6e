package daemon

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Admin endpoints. They live on their own listener rather than beside the
// webhooks, for two reasons that both matter:
//
//   - A pipeline claims a method and a path. Sharing a mux would let one claim
//     "POST /metrics" and shadow this, or be shadowed by it, and the loser would
//     simply never fire.
//   - The webhook listener is the one exposed to whatever sends the events.
//     Operational detail about every pipeline in the process does not belong on
//     it, and defaulting the admin listener to loopback means it is not exposed
//     by accident.
//
// A schedule-only daemon has no webhook listener at all, and this is then the
// only way to see whether it is alive.
const (
	pathHealth  = "/healthz"
	pathReady   = "/readyz"
	pathMetrics = "/metrics"
)

// adminHandler serves liveness, readiness and metrics.
func (d *Daemon) adminHandler() http.Handler {
	mux := http.NewServeMux()

	// Liveness answers whether the process is running, and nothing else. It
	// must not consult pipeline health: a daemon whose pipelines are all
	// quarantined is broken, but restarting it is a decision for a person, and
	// a liveness probe that fails would make the orchestrator loop instead.
	mux.HandleFunc("GET "+pathHealth, func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok\n")
	})

	mux.HandleFunc("GET "+pathReady, func(w http.ResponseWriter, _ *http.Request) {
		if reason, ready := d.readiness(); !ready {
			writeText(w, http.StatusServiceUnavailable, reason+"\n")
			return
		}
		writeText(w, http.StatusOK, "ready\n")
	})

	mux.HandleFunc("GET "+pathMetrics, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		d.writeMetrics(w)
	})

	return mux
}

// readiness reports whether the daemon can still do useful work.
//
// Draining is not ready, so a rolling deploy stops sending to a process that is
// on its way out. Every pipeline quarantined is not ready either: the process
// is up and answering, but nothing it was asked to serve will ever run again,
// and that is precisely the failure that would otherwise be invisible.
func (d *Daemon) readiness() (reason string, ready bool) {
	d.mu.RLock()
	draining := d.draining
	d.mu.RUnlock()
	if draining {
		return "draining", false
	}

	quarantined := 0
	for _, p := range d.pipelines {
		if p.Quarantined() {
			quarantined++
		}
	}
	if len(d.pipelines) > 0 && quarantined == len(d.pipelines) {
		return fmt.Sprintf("all %d pipeline(s) quarantined", quarantined), false
	}
	return "", true
}

// writeMetrics emits the Prometheus text exposition format by hand.
//
// Hand-written because it is a dozen lines of printf and the alternative is a
// dependency. This module has exactly one, and a metrics client would be a
// large one to take on for output this simple.
func (d *Daemon) writeMetrics(w io.Writer) {
	quarantined := 0
	for _, p := range d.pipelines {
		if p.Quarantined() {
			quarantined++
		}
	}

	metric(w, "p6e_pipelines_served", "gauge",
		"Pipelines this daemon is serving.", float64(len(d.pipelines)))
	metric(w, "p6e_pipelines_quarantined", "gauge",
		"Pipelines taken out of service after repeatedly abandoning a step.", float64(quarantined))
	metric(w, "p6e_runs_inflight", "gauge",
		"Runs in progress across every pipeline.", float64(d.inflight.Load()))
	metric(w, "p6e_slots_held", "gauge",
		"Steps holding a slot in the shared budget.", float64(len(d.slots)))
	metric(w, "p6e_slots_total", "gauge",
		"Size of the shared step budget.", float64(cap(d.slots)))

	perPipeline(w, d.pipelines, "p6e_pipeline_runs_total", "counter",
		"Runs started, by pipeline.", func(p *Pipeline) float64 { return float64(p.runs.Load()) })
	perPipeline(w, d.pipelines, "p6e_pipeline_failures_total", "counter",
		"Runs that failed, by pipeline.", func(p *Pipeline) float64 { return float64(p.failures.Load()) })
	perPipeline(w, d.pipelines, "p6e_pipeline_abandoned_runs_total", "counter",
		"Runs that left a step running because a node ignored its context.",
		func(p *Pipeline) float64 { return float64(p.abandonedRuns.Load()) })
	perPipeline(w, d.pipelines, "p6e_pipeline_runs_inflight", "gauge",
		"Runs in progress, by pipeline.", func(p *Pipeline) float64 { return float64(p.inflight.Load()) })
	perPipeline(w, d.pipelines, "p6e_pipeline_quarantined", "gauge",
		"1 when the pipeline has been taken out of service.", func(p *Pipeline) float64 {
			if p.Quarantined() {
				return 1
			}
			return 0
		})
}

func metric(w io.Writer, name, kind, help string, value float64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %g\n", name, help, name, kind, name, value)
}

func perPipeline(w io.Writer, pipelines []*Pipeline, name, kind, help string, value func(*Pipeline) float64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	for _, p := range pipelines {
		fmt.Fprintf(w, "%s{pipeline=\"%s\"} %g\n", name, escapeLabel(p.Name), value(p))
	}
}

// escapeLabel quotes a label value per the exposition format. Pipeline names
// come from filenames, so they are not guaranteed to be free of the three
// characters that would otherwise corrupt the output.
func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
