package pipeline

import (
	"strings"
	"testing"
	"time"
)

func parseString(t *testing.T, src string) (*File, error) {
	t.Helper()
	return Parse(strings.NewReader(src))
}

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := parseString(t, src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

const minimal = `
version: 1
steps:
  fetch:
    uses: http.request
    with:
      url: https://example.com
  decode:
    uses: json.decode
    needs: [fetch]
`

func TestParseMinimal(t *testing.T) {
	f := mustParse(t, minimal)

	if f.Version != 1 {
		t.Errorf("Version = %d, want 1", f.Version)
	}
	if len(f.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(f.Steps))
	}
	if got := f.Steps["decode"].Needs.Positional(); len(got) != 1 || got[0] != "fetch" {
		t.Errorf("decode needs = %v, want [fetch]", got)
	}
}

// A typo that is silently ignored produces a pipeline that compiles and then
// does the wrong thing, so decoding is strict everywhere.
func TestParseRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"top level": "version: 1\nnodes:\n  a: {}\nsteps:\n  a:\n    uses: x\n",
		"step":      "version: 1\nsteps:\n  a:\n    uses: x\n    nedes: [b]\n",
		"retry":     "version: 1\nsteps:\n  a:\n    uses: x\n    retry:\n      max_retries: 3\n",
	}
	for name, src := range cases {
		if _, err := parseString(t, src); err == nil {
			t.Errorf("%s: expected an unknown-field error", name)
		}
	}
}

func TestParseRejectsBadVersion(t *testing.T) {
	if _, err := parseString(t, "steps:\n  a:\n    uses: x\n"); err == nil {
		t.Error("expected a missing-version error")
	}
	_, err := parseString(t, "version: 99\nsteps:\n  a:\n    uses: x\n")
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Errorf("err = %v, want an unsupported-version error naming 99", err)
	}
}

func TestParseRejectsStructuralMistakes(t *testing.T) {
	cases := map[string]string{
		"empty document":   "",
		"no steps":         "version: 1\nsteps: {}\n",
		"missing uses":     "version: 1\nsteps:\n  a:\n    needs: []\n",
		"self dependency":  "version: 1\nsteps:\n  a:\n    uses: x\n    needs: [a]\n",
		"zero attempts":    "version: 1\nsteps:\n  a:\n    uses: x\n    retry:\n      max_attempts: 0\n",
		"negative backoff": "version: 1\nsteps:\n  a:\n    uses: x\n    retry:\n      backoff: -1s\n",
	}
	for name, src := range cases {
		if _, err := parseString(t, src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseRetryPolicy(t *testing.T) {
	f := mustParse(t, `
version: 1
steps:
  a:
    uses: x
    retry:
      max_attempts: 3
      backoff: 250ms
  b:
    uses: x
`)

	a := f.Steps["a"].RetryPolicy()
	if a.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", a.MaxAttempts)
	}
	if a.Backoff.Duration() != 250*time.Millisecond {
		t.Errorf("Backoff = %v, want 250ms", a.Backoff.Duration())
	}

	// A step that declares no policy tries once. Retrying by default would
	// silently double the side effects of every exec and http step.
	if b := f.Steps["b"].RetryPolicy(); b.MaxAttempts != 1 {
		t.Errorf("default MaxAttempts = %d, want 1", b.MaxAttempts)
	}
}

func TestParseRejectsUnparseableDuration(t *testing.T) {
	_, err := parseString(t, "version: 1\nsteps:\n  a:\n    uses: x\n    retry:\n      backoff: soon\n")
	if err == nil || !strings.Contains(err.Error(), "soon") {
		t.Errorf("err = %v, want a duration error naming the bad value", err)
	}
}

// Compilation must be deterministic: the same file has to produce the same plan
// and report the same first error every time, and YAML mappings have no order.
func TestStepIDsAreSorted(t *testing.T) {
	f := mustParse(t, `
version: 1
steps:
  zebra:
    uses: x
  alpha:
    uses: x
  mid:
    uses: x
`)

	got := f.StepIDs()
	want := []string{"alpha", "mid", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StepIDs = %v, want %v", got, want)
		}
	}
}

type httpConfig struct {
	URL    string `yaml:"url"`
	Method string `yaml:"method"`
}

func TestStepConfigDecodes(t *testing.T) {
	f := mustParse(t, minimal)

	var cfg httpConfig
	if err := f.Steps["fetch"].Config().Decode(&cfg); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", cfg.URL, "https://example.com")
	}
}

func TestStepConfigIsStrict(t *testing.T) {
	f := mustParse(t, "version: 1\nsteps:\n  a:\n    uses: x\n    with:\n      urk: nope\n")

	var cfg httpConfig
	if err := f.Steps["a"].Config().Decode(&cfg); err == nil {
		t.Error("expected an unknown-field error inside the with block")
	}
}

func TestStepConfigEmptyLeavesZeroValue(t *testing.T) {
	f := mustParse(t, minimal)

	cfg := httpConfig{Method: "GET"}
	if err := f.Steps["decode"].Config().Decode(&cfg); err != nil {
		t.Fatalf("Decode of an absent with block should succeed, got %v", err)
	}
	if cfg.Method != "GET" {
		t.Error("decoding an absent with block overwrote the caller's defaults")
	}
}
