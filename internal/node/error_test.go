package node

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRetryableDerivedFromKind(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		want bool
	}{
		{KindTransient, true},
		{KindPermanent, false},
		{KindInvalidInput, false},
		{KindCancelled, false},
		{KindInternal, false},
	}
	for _, c := range cases {
		if got := Errf(c.kind, "code", "msg").Retryable; got != c.want {
			t.Errorf("%s: Retryable = %v, want %v", c.kind, got, c.want)
		}
	}
}

// A node knows things the kind alone does not, for example a 429 carrying a
// Retry-After. It reports the fact; policy still decides what to do.
func TestNodeCanOverrideRetryable(t *testing.T) {
	e := Errf(KindPermanent, "rate_limited", "429")
	e.Retryable = true

	if !e.Retryable {
		t.Error("a node must be able to override Retryable")
	}
}

func TestNormalizePassesThroughNodeError(t *testing.T) {
	original := Errf(KindTransient, "timeout", "took too long")

	if got := Normalize(fmt.Errorf("wrapped: %w", original), "fallback"); got != original {
		t.Errorf("Normalize = %+v, want the original NodeError", got)
	}
}

func TestNormalizeRecognizesCancellation(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
	}
	for _, c := range cases {
		got := Normalize(fmt.Errorf("op: %w", c.err), "fallback")
		if got.Kind != KindCancelled {
			t.Errorf("%v: Kind = %q, want %q", c.err, got.Kind, KindCancelled)
		}
		if got.Code != c.code {
			t.Errorf("%v: Code = %q, want %q", c.err, got.Code, c.code)
		}
	}
}

// Guessing that an unknown failure is retryable turns one failure into several,
// so the default is permanent.
func TestNormalizeDefaultsToPermanent(t *testing.T) {
	got := Normalize(errors.New("something odd"), "odd")

	if got.Kind != KindPermanent {
		t.Errorf("Kind = %q, want %q", got.Kind, KindPermanent)
	}
	if got.Retryable {
		t.Error("an unclassified error must not be retryable by default")
	}
}

func TestNormalizeNilIsNil(t *testing.T) {
	if got := Normalize(nil, "code"); got != nil {
		t.Errorf("Normalize(nil) = %+v, want nil", got)
	}
}

func TestNodeErrorUnwraps(t *testing.T) {
	cause := errors.New("root cause")
	e := Wrap(cause, KindTransient, "net", "connection failed")

	if !errors.Is(e, cause) {
		t.Error("NodeError should unwrap to its cause")
	}
	if got, ok := errors.AsType[*NodeError](fmt.Errorf("step: %w", e)); !ok || got != e {
		t.Error("NodeError should be recoverable with errors.AsType through wrapping")
	}
}
