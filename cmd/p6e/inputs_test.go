package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const parameterized = `
version: 1
inputs:
  who: Text
steps:
  greeting:
    uses: text.format
    with:
      template: "hello {{name}}"
    needs:
      name: who
`

func TestInputIsSuppliedFromTheCommandLine(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, stdout, stderr := invoke(t, "run", path, "--input", "who=ada")

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "succeeded greeting") {
		t.Errorf("stdout %q should show the step ran", stdout)
	}
}

// The joined form matters because it is what shells and CI configuration
// usually produce.
func TestInputAcceptsTheJoinedForm(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, _, stderr := invoke(t, "run", path, "--input=who=ada")

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
}

func TestInputCanBeReadFromAFile(t *testing.T) {
	path := writePipeline(t, parameterized)
	valuePath := filepath.Join(t.TempDir(), "who.txt")
	if err := os.WriteFile(valuePath, []byte("ada"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, _, stderr := invoke(t, "run", path, "--input", "who=@"+valuePath)

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
}

// check proves the pipeline without running it, so it must not need the values
// a run would be given. That is what lets a pipeline holding secrets be
// validated anywhere.
func TestCheckDoesNotRequireInputs(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, stdout, stderr := invoke(t, "check", path)

	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "1 inputs") {
		t.Errorf("stdout %q should report the declared inputs", stdout)
	}
}

// A misspelled name would otherwise look like it worked while the real input
// went unsupplied.
func TestUnknownInputIsRejected(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, _, stderr := invoke(t, "run", path, "--input", "whom=ada")

	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "no such input") || !strings.Contains(stderr, "who") {
		t.Errorf("stderr %q should reject the name and list what is declared", stderr)
	}
}

func TestMissingInputFailsTheRun(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, stdout, stderr := invoke(t, "run", path)

	if code != exitFailure {
		t.Fatalf("exit %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stdout+stderr, "was not supplied") {
		t.Errorf("output %q should say which input is missing", stdout+stderr)
	}
}

func TestMalformedInputAssignmentIsRejected(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, _, stderr := invoke(t, "run", path, "--input", "who")

	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "NAME=VALUE") {
		t.Errorf("stderr %q should show the expected form", stderr)
	}
}

func TestInputWithoutAValueIsRejected(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, _, stderr := invoke(t, "run", path, "--input")

	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--input") {
		t.Errorf("stderr %q should name the option", stderr)
	}
}

// The declared type decides how the text is read, and a value that does not fit
// is caught before the run rather than deep inside it.
func TestInputLiteralMustFitTheDeclaredType(t *testing.T) {
	path := writePipeline(t, `
version: 1
inputs:
  count: Int
steps:
  keep:
    uses: value
    with: {type: Text, value: placeholder}
`)

	code, _, stderr := invoke(t, "run", path, "--input", "count=many")

	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "whole number") {
		t.Errorf("stderr %q should say what was expected", stderr)
	}
}

func TestSupplyingAnInputTwiceIsRejected(t *testing.T) {
	path := writePipeline(t, parameterized)

	code, _, stderr := invoke(t, "run", path, "--input", "who=ada", "--input", "who=grace")

	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "more than once") {
		t.Errorf("stderr %q should reject the duplicate", stderr)
	}
}
