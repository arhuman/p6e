package daemon

import (
	"log/slog"
	"net/http"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/trigger"
)

// handler routes every webhook pipeline onto one listener.
//
// One socket serves them all, which is the reason a webhook trigger cannot own
// its own: the first pipeline to bind the port would lock out the rest. The
// claim key is already a ServeMux pattern ("POST /hooks/deploy"), so routing by
// method and path needs no translation, and Load has proven no two pipelines
// share one.
func (d *Daemon) handler() http.Handler {
	mux := http.NewServeMux()
	for pattern, p := range d.routes {
		mux.HandleFunc(pattern, d.handle(p))
	}
	return mux
}

// handle answers one request by running one pipeline.
//
// The reply is synchronous. Answering with an identifier and letting the caller
// collect the result later would mean keeping executions somewhere, which is a
// persistence layer, and this engine deliberately has none. Waiting costs
// nothing but the caller's patience, which the pipeline's own timeout bounds.
func (d *Daemon) handle(p *Pipeline) http.HandlerFunc {
	driven := p.Trigger().(trigger.HTTPDriven)

	return func(w http.ResponseWriter, r *http.Request) {
		// net/http recovers a panicking handler, but it does so by dropping the
		// connection. Recovering here answers instead, and keeps one bad
		// request from looking like a network fault to whoever sent it.
		defer func() {
			if rec := recover(); rec != nil {
				d.log.Error("webhook handler panicked",
					slog.String("pipeline", p.Name), slog.Any("panic", rec))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()

		values, err := driven.Values(r)
		if err != nil {
			d.log.Warn("rejected a request before running anything",
				slog.String("pipeline", p.Name), slog.String("error", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		out := d.fire(p)(r.Context(), values)
		if out.Failed() {
			http.Error(w, out.Err.Message, statusFor(out.Err))
			return
		}
		if out.Value.IsZero() {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := driven.Respond(w, out.Value); err != nil {
			d.log.Error("could not write the response",
				slog.String("pipeline", p.Name), slog.String("error", err.Error()))
		}
	}
}

// statusFor maps a failure onto the status that describes it to a caller.
//
// Slot exhaustion is deliberately absent. A run that cannot claim a slot waits
// for one rather than failing, so a burst is absorbed instead of rejected; if
// the wait outlasts the pipeline's timeout it surfaces here as a timeout, which
// is what actually happened.
func statusFor(err *node.NodeError) int {
	switch {
	case err == nil:
		return http.StatusOK
	case err.Code == codeOverlapped:
		return http.StatusTooManyRequests
	case err.Code == codeQuarantined, err.Code == codeDraining:
		return http.StatusServiceUnavailable
	case err.Kind == node.KindCancelled:
		return http.StatusGatewayTimeout
	case err.Kind == node.KindInvalidInput:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
