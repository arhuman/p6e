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
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/trigger"
)

// Every other benchmark in this repo measures the engine. Nothing measured the
// layer that now faces load: an event arriving on the socket, being routed and
// admitted, running, and being answered. This is that path, with a node that
// does nothing, so what it reports is the daemon's own overhead per request
// rather than a pipeline's work.
//
// Set up by hand rather than through the test helpers, which take *testing.T:
// widening them would touch tests that define the contract, for a benchmark's
// convenience.

const benchWebhook = `version: 1
inputs:
  body: Bytes
trigger:
  uses: trigger.webhook
  with:
    path: /bench
  timeout: 5s
  respond_with: out
steps:
  out:
    uses: bench.echo
    needs: [body]
`

func benchRegistries(b *testing.B) *pipeline.Registries {
	b.Helper()

	reg := node.NewRegistry()
	reg.MustRegister(node.Static("bench.echo", node.NewTypedNode("bench.echo",
		func(_ context.Context, _ *node.ExecutionContext, in *types.Bytes) node.Result[*types.Bytes] {
			return node.Ok(in)
		})))
	return &pipeline.Registries{Nodes: reg, Triggers: trigger.Builtins()}
}

func benchServer(b *testing.B, opts Options) *httptest.Server {
	b.Helper()

	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(benchWebhook), 0o600); err != nil {
		b.Fatalf("WriteFile: %v", err)
	}

	loaded, err := Load(dir, benchRegistries(b))
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	if len(loaded.Served) != 1 {
		b.Fatalf("Served = %d, want 1 (rejected: %v)", len(loaded.Served), loaded.Rejected)
	}

	// Discard rather than the default handler: a benchmark that also measures
	// slog writing to stderr is measuring the terminal.
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	d := New(loaded.Served, opts)

	server := httptest.NewServer(d.handler())
	b.Cleanup(server.Close)
	return server
}

// call is one webhook round trip, body read to completion so the connection is
// reused rather than reopened on the next iteration.
func call(b *testing.B, server *httptest.Server, body string) {
	b.Helper()

	resp, err := server.Client().Post(server.URL+"/bench", "application/json", strings.NewReader(body))
	if err != nil {
		b.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		b.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// BenchmarkWebhookRoundTrip is the whole path, one caller at a time.
func BenchmarkWebhookRoundTrip(b *testing.B) {
	server := benchServer(b, Options{})
	body := `{"ref":"refs/heads/main"}`

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		call(b, server, body)
	}
}

// BenchmarkWebhookRoundTripParallel is the same path under concurrent callers,
// which is what a webhook actually sees and what the admission path exists for.
func BenchmarkWebhookRoundTripParallel(b *testing.B) {
	server := benchServer(b, Options{})
	body := `{"ref":"refs/heads/main"}`

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, server, body)
		}
	})
}

// BenchmarkAdmit isolates the admission path from the HTTP round trip, so a
// regression in the quarantine, drain, overlap and ceiling checks is visible
// separately from anything the network does.
func BenchmarkAdmit(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(benchWebhook), 0o600); err != nil {
		b.Fatalf("WriteFile: %v", err)
	}
	loaded, err := Load(dir, benchRegistries(b))
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	d := New(loaded.Served, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	p := loaded.Served[0]

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if refusal := d.admit(p); refusal != nil {
			b.Fatalf("admit refused: %v", refusal)
		}
		// Release what admit took, which is what fire's deferred close does.
		p.inflight.Add(-1)
		d.inflight.Add(-1)
		d.wg.Done()
	}
}
