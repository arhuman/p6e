package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// invoke runs the CLI the way main does, capturing what a user would see.
func invoke(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	var out, errOut bytes.Buffer
	code = run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// writePipeline puts a pipeline in a temp file and returns its path.
func writePipeline(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pipeline.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// The shipped examples are documentation, and documentation that does not
// compile is worse than none.
func TestExamplesCompile(t *testing.T) {
	for _, name := range []string{
		"json.yaml", "exec.yaml", "http.yaml",
		"chaining.yaml", "monitor.yaml", "parameterized.yaml",
	} {
		code, stdout, stderr := invoke(t, "check", filepath.Join("..", "..", "examples", name))
		if code != exitOK {
			t.Errorf("check %s: exit %d, stderr: %s", name, code, stderr)
		}
		if !strings.Contains(stdout, "ok:") {
			t.Errorf("check %s: stdout %q should confirm success", name, stdout)
		}
	}
}

// The broken example exists to demonstrate the engine's central claim: an
// incompatible edge is caught before anything runs, and the message says which
// edge and why.
func TestBrokenExampleIsRejectedWithAUsefulMessage(t *testing.T) {
	code, _, stderr := invoke(t, "check", filepath.Join("..", "..", "examples", "broken.yaml"))

	if code != exitFailure {
		t.Fatalf("exit %d, want %d", code, exitFailure)
	}
	for _, want := range []string{`step "document"`, "Bytes", `"payload"`, "Text"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q should contain %q", stderr, want)
		}
	}
}

func TestRunExecutesAPipeline(t *testing.T) {
	code, stdout, stderr := invoke(t, "run", filepath.Join("..", "..", "examples", "json.yaml"))

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, step := range []string{"payload", "document", "is_ada", "has_three"} {
		if !strings.Contains(stdout, step) {
			t.Errorf("output should report step %q:\n%s", step, stdout)
		}
	}
	if strings.Contains(stdout, "failed") || strings.Contains(stdout, "skipped") {
		t.Errorf("every step should have succeeded:\n%s", stdout)
	}
}

// check must not execute anything. A pipeline that would fail at run time still
// passes check when it is structurally sound.
func TestCheckDoesNotExecute(t *testing.T) {
	path := writePipeline(t, `
version: 1
steps:
  cmd:
    uses: exec.command
    with:
      name: /definitely/not/a/real/binary
  run:
    uses: exec
    needs: [cmd]
`)

	if code, _, stderr := invoke(t, "check", path); code != exitOK {
		t.Fatalf("check: exit %d, stderr: %s", code, stderr)
	}
	if code, _, _ := invoke(t, "run", path); code != exitFailure {
		t.Errorf("run: exit %d, want %d: the binary does not exist", code, exitFailure)
	}
}

func TestFailingStepReportsWhereAndWhy(t *testing.T) {
	path := writePipeline(t, `
version: 1
steps:
  cmd:
    uses: exec.command
    with:
      name: /definitely/not/a/real/binary
  run:
    uses: exec
    needs: [cmd]
`)

	code, stdout, stderr := invoke(t, "run", path)

	if code != exitFailure {
		t.Fatalf("exit %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, `step "run"`) {
		t.Errorf("stderr %q should name the failing step", stderr)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("the step report should mark the failure:\n%s", stdout)
	}
}

func TestFailureSkipsTheRestAndSaysSo(t *testing.T) {
	path := writePipeline(t, `
version: 1
steps:
  cmd:
    uses: exec.command
    with:
      name: /definitely/not/a/real/binary
  run:
    uses: exec
    needs: [cmd]
  never:
    uses: exec
    needs: [cmd]
`)

	_, stdout, _ := invoke(t, "run", path)

	if !strings.Contains(stdout, "skipped") && !strings.Contains(stdout, "cancelled") {
		t.Errorf("a step downstream of a failure should be reported as skipped:\n%s", stdout)
	}
}

func TestMissingFileIsReported(t *testing.T) {
	code, _, stderr := invoke(t, "check", "no/such/pipeline.yaml")

	if code != exitFailure {
		t.Errorf("exit %d, want %d", code, exitFailure)
	}
	if stderr == "" {
		t.Error("a missing file should be explained on stderr")
	}
}

// A usage mistake and a broken pipeline are different problems, so they get
// different exit codes.
func TestUsageErrorsAreDistinctFromPipelineFailures(t *testing.T) {
	cases := map[string][]string{
		"no arguments":  {},
		"unknown verb":  {"compile", "x.yaml"},
		"missing file":  {"check"},
		"too many args": {"run", "a.yaml", "b.yaml"},
	}
	for name, args := range cases {
		if code, _, _ := invoke(t, args...); code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
	}
}

func TestNodesListsCapabilities(t *testing.T) {
	code, stdout, _ := invoke(t, "nodes")

	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, name := range []string{"value", "json.decode", "condition", "exec", "http.request"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("nodes output should list %q:\n%s", name, stdout)
		}
	}
}

func TestHelpIsAvailable(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		code, stdout, _ := invoke(t, arg)
		if code != exitOK {
			t.Errorf("%s: exit %d, want %d", arg, code, exitOK)
		}
		if !strings.Contains(stdout, "p6e check") {
			t.Errorf("%s: usage should describe check", arg)
		}
	}
}

func TestRunAcceptsDetectMutation(t *testing.T) {
	code, stdout, stderr := invoke(t, "run", "--detect-mutation", filepath.Join("..", "..", "examples", "json.yaml"))

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ok:") {
		t.Errorf("a clean pipeline should still report success:\n%s", stdout)
	}
}

// The flag order must not matter, and an unrecognized option is a usage error
// rather than a filename.
func TestRunOptionHandling(t *testing.T) {
	example := filepath.Join("..", "..", "examples", "json.yaml")

	if code, _, _ := invoke(t, "run", example, "--detect-mutation"); code != exitOK {
		t.Errorf("trailing option: exit %d, want %d", code, exitOK)
	}
	if code, _, stderr := invoke(t, "run", "--nope", example); code != exitUsage {
		t.Errorf("unknown option: exit %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
}

func TestRunAcceptsInline(t *testing.T) {
	example := filepath.Join("..", "..", "examples", "json.yaml")

	code, stdout, stderr := invoke(t, "run", "--inline", example)
	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ok:") {
		t.Errorf("inlining should not change the outcome:\n%s", stdout)
	}

	// The options compose, since one is a debugging aid and the other a
	// scheduling choice.
	if code, _, _ := invoke(t, "run", "--inline", "--detect-mutation", example); code != exitOK {
		t.Errorf("combined options: exit %d, want %d", code, exitOK)
	}
}
