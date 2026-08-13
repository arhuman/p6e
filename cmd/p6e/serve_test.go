package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func webhookYAML(path string) string {
	return "version: 1\ninputs:\n  body: Bytes\ntrigger:\n  uses: trigger.webhook\n  with:\n    path: " + path +
		"\n  timeout: 5s\n  respond_with: out\nsteps:\n  out:\n    uses: json.encode\n    needs: [doc]\n" +
		"  doc:\n    uses: json.decode\n    needs: [body]\n"
}

func TestCheckDirAcceptsAHealthyDirectory(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"a.yaml":      webhookYAML("/a"),
		"b.yaml":      webhookYAML("/b"),
		"byhand.yaml": "version: 1\nsteps:\n  a:\n    uses: value\n    with:\n      type: Text\n      value: hi\n",
	})

	code, stdout, stderr := invoke(t, "check", "--dir", dir)
	if code != exitOK {
		t.Fatalf("exit %d, want 0. stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "2 servable") || !strings.Contains(stdout, "1 to run by hand") {
		t.Errorf("stdout = %q, want it to count both kinds", stdout)
	}
}

// The reason check --dir exists: a route collision is invisible to any
// single-file check, so without this there is no way to catch one before a
// deploy.
func TestCheckDirRejectsAClaimCollision(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"first.yaml":  webhookYAML("/hooks/deploy"),
		"second.yaml": webhookYAML("/hooks/deploy"),
	})

	code, _, stderr := invoke(t, "check", "--dir", dir)
	if code != exitFailure {
		t.Fatalf("exit %d, want 1: a collision has to fail the gate", code)
	}
	for _, name := range []string{"first", "second"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("stderr = %q, want both claimants named", stderr)
		}
	}
	if !strings.Contains(stderr, "POST /hooks/deploy") {
		t.Errorf("stderr = %q, want the contested claim named", stderr)
	}
}

// Unlike serve, check --dir is a gate: any problem fails it.
func TestCheckDirFailsOnABrokenFile(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"good.yaml":   webhookYAML("/good"),
		"broken.yaml": "version: 1\nsteps:\n  a:\n    uses: no.such.node\n",
	})

	code, _, stderr := invoke(t, "check", "--dir", dir)
	if code != exitFailure {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "no.such.node") {
		t.Errorf("stderr = %q, want the compile error", stderr)
	}
}

func TestCheckDirAcceptsTheJoinedForm(t *testing.T) {
	dir := writeDir(t, map[string]string{"a.yaml": webhookYAML("/a")})

	if code, _, stderr := invoke(t, "check", "--dir="+dir); code != exitOK {
		t.Errorf("--dir=<path> gave exit %d, want 0. stderr: %s", code, stderr)
	}
}

func TestCheckDirWantsExactlyOneDirectory(t *testing.T) {
	if code, _, _ := invoke(t, "check", "--dir"); code != exitUsage {
		t.Errorf("exit %d, want 2 for a missing directory", code)
	}
}

func TestServeReportsAnUnreadableDirectory(t *testing.T) {
	code, _, stderr := invoke(t, "serve", filepath.Join(t.TempDir(), "absent"))
	if code != exitFailure {
		t.Fatalf("exit %d, want 1", code)
	}
	if stderr == "" {
		t.Error("a missing directory should be explained")
	}
}

// Starting a daemon that would answer nothing is a mistake worth reporting
// rather than a process that sits there looking healthy.
func TestServeRefusesWhenNothingIsServable(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"byhand.yaml": "version: 1\nsteps:\n  a:\n    uses: value\n    with:\n      type: Text\n      value: hi\n",
	})

	code, _, stderr := invoke(t, "serve", dir)
	if code != exitFailure {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "nothing to serve") {
		t.Errorf("stderr = %q, want it to say nothing is servable", stderr)
	}
}

func TestServeRejectsBadOptions(t *testing.T) {
	dir := writeDir(t, map[string]string{"a.yaml": webhookYAML("/a")})

	for name, args := range map[string][]string{
		"unknown option":       {"serve", dir, "--nope"},
		"non-numeric limit":    {"serve", dir, "--max-concurrency", "lots"},
		"zero limit":           {"serve", dir, "--max-concurrency", "0"},
		"unparseable drain":    {"serve", dir, "--drain", "soon"},
		"non-numeric max-runs": {"serve", dir, "--max-runs", "many"},
		// Zero is rejected rather than taken as "no cap": that is what a
		// negative value means, and silently defaulting a zero would be the
		// kind of quiet reinterpretation this CLI avoids everywhere else.
		"zero max-runs":        {"serve", dir, "--max-runs", "0"},
		"two directories":      {"serve", dir, dir},
		"no directory":         {"serve"},
		"option without value": {"serve", dir, "--listen"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := invoke(t, args...); code != exitUsage {
				t.Errorf("exit %d, want 2", code)
			}
		})
	}
}

func TestTriggersListsTheBuiltIns(t *testing.T) {
	code, stdout, _ := invoke(t, "triggers")
	if code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	for _, want := range []string{"trigger.webhook", "trigger.schedule"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to list %q", stdout, want)
		}
	}
}

// The claim that a triggered pipeline needs no daemon to test is load bearing:
// it is why there is no separate way to fire one synthetically.
func TestTriggeredPipelineRunsFromTheCommandLine(t *testing.T) {
	dir := writeDir(t, map[string]string{"hook.yaml": webhookYAML("/hook")})
	payload := filepath.Join(dir, "event.json")
	if err := os.WriteFile(payload, []byte(`{"ref":"refs/heads/main"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, stdout, stderr := invoke(t, "run", "--input", "body=@"+payload, filepath.Join(dir, "hook.yaml"))
	if code != exitOK {
		t.Fatalf("exit %d, want 0. stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ok:") {
		t.Errorf("stdout = %q, want a successful run", stdout)
	}
}
