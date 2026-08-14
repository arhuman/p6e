package main

import (
	"os"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/nodes"
	"github.com/arhuman/p6e/internal/trigger"
)

// The README's sample output drifted once already: it listed 8 capabilities
// against a registry of 22, so it read as the capability list while omitting
// every node added since. Nothing would have noticed, because a code sample in
// a markdown file is not compiled, run, or diffed by anything.
//
// This is that missing gate. It lives in a test rather than a CI step so it
// also fails locally, in the same `make test` that a contributor already runs.
const readmePath = "../../README.md"

func TestReadmeNodeListingMatchesTheRegistry(t *testing.T) {
	assertListedBlock(t, "$ ./bin/p6e nodes", nodes.Registry().Names())
}

func TestReadmeTriggerListingMatchesTheRegistry(t *testing.T) {
	// Only checked when the README shows the command at all: the trigger
	// listing is not currently part of it, and a guard that invented a
	// requirement would be worse than no guard.
	if !strings.Contains(readme(t), "$ ./bin/p6e triggers") {
		t.Skip("the README does not show p6e triggers output")
	}
	assertListedBlock(t, "$ ./bin/p6e triggers", trigger.Builtins().Names())
}

func readme(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("reading %s: %v", readmePath, err)
	}
	return string(raw)
}

// assertListedBlock compares the lines a fenced block shows after command with
// what the registry actually holds.
func assertListedBlock(t *testing.T, command string, want []string) {
	t.Helper()

	_, after, found := strings.Cut(readme(t), command)
	if !found {
		t.Fatalf("%s does not show %q, so its output cannot be checked", readmePath, command)
	}
	block, _, closed := strings.Cut(after, "```")
	if !closed {
		t.Fatalf("the block after %q in %s is never closed", command, readmePath)
	}

	got := strings.Fields(block)
	if len(got) != len(want) {
		t.Errorf("%s lists %d capabilities, the registry has %d", readmePath, len(got), len(want))
	}

	listed := map[string]bool{}
	for _, name := range got {
		listed[name] = true
	}
	for _, name := range want {
		if !listed[name] {
			t.Errorf("%q is registered but missing from the %q output in %s."+
				"\nRegenerate it with: go run ./cmd/p6e %s",
				name, command, readmePath, strings.TrimPrefix(command, "$ ./bin/p6e "))
		}
		delete(listed, name)
	}
	for name := range listed {
		t.Errorf("%q is shown in %s but is not registered", name, readmePath)
	}
}
