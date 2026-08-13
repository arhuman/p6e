package daemon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rejectionFor returns the reason the named file was refused, or "".
func rejectionFor(loaded *Loaded, name string) string {
	for _, r := range loaded.Rejected {
		if filepath.Base(r.Path) == name {
			return r.Err.Error()
		}
	}
	return ""
}

func servedNames(loaded *Loaded) []string {
	names := make([]string, len(loaded.Served))
	for i, p := range loaded.Served {
		names[i] = p.Name
	}
	slices.Sort(names)
	return names
}

// One file that will not compile must not take down every unrelated pipeline
// beside it: a directory is a deployment surface, and a typo in one webhook is
// not a reason to stop answering the others.
func TestLoadServesTheRestWhenOneFileIsBroken(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"good.yaml":   webhookYAML("/good", "out", "echo"),
		"broken.yaml": "version: 1\ninputs:\n  body: Bytes\ntrigger:\n  uses: trigger.webhook\n  with:\n    path: /broken\n  timeout: 2s\nsteps:\n  out:\n    uses: no.such.node\n    needs: [body]\n",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := servedNames(loaded); !slices.Equal(got, []string{"good"}) {
		t.Errorf("Served = %v, want just the good pipeline", got)
	}
	if reason := rejectionFor(loaded, "broken.yaml"); !strings.Contains(reason, "no.such.node") {
		t.Errorf("broken.yaml was rejected for %q, want the compile error", reason)
	}
}

func TestLoadRejectsAnUnparseableFile(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"bad.yaml": "version: 1\nsteps: [this is not a mapping]\n",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded.Served) != 0 {
		t.Errorf("Served = %v, want nothing", servedNames(loaded))
	}
	if rejectionFor(loaded, "bad.yaml") == "" {
		t.Error("an unparseable file should be rejected with a reason")
	}
}

// This is the loading filter the daemon exists to apply: a pipeline with no
// trigger is not broken, it is simply meant to be run by hand.
func TestLoadSkipsUntriggeredPipelines(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"served.yaml": webhookYAML("/served", "out", "echo"),
		"byhand.yaml": "version: 1\nsteps:\n  a:\n    uses: tick\n",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := servedNames(loaded); !slices.Equal(got, []string{"served"}) {
		t.Errorf("Served = %v, want only the triggered pipeline", got)
	}
	if len(loaded.Untriggered) != 1 || filepath.Base(loaded.Untriggered[0]) != "byhand.yaml" {
		t.Errorf("Untriggered = %v, want byhand.yaml", loaded.Untriggered)
	}
	if reason := rejectionFor(loaded, "byhand.yaml"); reason != "" {
		t.Errorf("an untriggered pipeline was rejected (%q); it is not an error", reason)
	}
}

// Both claimants lose. Serving whichever sorted first would mean one pipeline
// quietly answering requests meant for the other, and the only symptom would be
// the other never running, with nothing saying why.
func TestLoadRejectsBothSidesOfAClaimCollision(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"first.yaml":  webhookYAML("/hooks/deploy", "out", "echo"),
		"second.yaml": webhookYAML("/hooks/deploy", "out", "echo"),
		"other.yaml":  webhookYAML("/unrelated", "out", "echo"),
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := servedNames(loaded); !slices.Equal(got, []string{"other"}) {
		t.Errorf("Served = %v, want only the uncontested pipeline", got)
	}
	for _, name := range []string{"first.yaml", "second.yaml"} {
		reason := rejectionFor(loaded, name)
		if reason == "" {
			t.Errorf("%s should have been rejected for colliding", name)
			continue
		}
		if !strings.Contains(reason, "POST /hooks/deploy") {
			t.Errorf("%s was rejected for %q, want the contested claim named", name, reason)
		}
	}
	// Each side should name the other, so one message is enough to fix it.
	if reason := rejectionFor(loaded, "first.yaml"); !strings.Contains(reason, "second.yaml") {
		t.Errorf("first.yaml's rejection %q should name the other claimant", reason)
	}
}

// Two pipelines on one path but different methods do not collide, which is why
// the claim carries the method.
func TestLoadAllowsOnePathWithTwoMethods(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"post.yaml": webhookYAML("/hooks/deploy", "out", "echo"),
		"put.yaml": strings.Replace(webhookYAML("/hooks/deploy", "out", "echo"),
			"path: /hooks/deploy", "path: /hooks/deploy\n    method: PUT", 1),
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := servedNames(loaded); !slices.Equal(got, []string{"post", "put"}) {
		t.Errorf("Served = %v, want both (rejected: %v)", got, loaded.Rejected)
	}
}

// Schedules claim nothing, so any number of them coexist.
func TestLoadAllowsManySchedules(t *testing.T) {
	p := &probe{}
	every := "version: 1\ntrigger:\n  uses: trigger.schedule\n  with:\n    every: 1s\nsteps:\n  a:\n    uses: tick\n"
	loaded, err := Load(writeDir(t, map[string]string{
		"one.yaml": every,
		"two.yaml": every,
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := servedNames(loaded); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("Served = %v, want both schedules (rejected: %v)", got, loaded.Rejected)
	}
}

func TestLoadIgnoresNonPipelineFiles(t *testing.T) {
	p := &probe{}
	loaded, err := Load(writeDir(t, map[string]string{
		"served.yaml": webhookYAML("/served", "out", "echo"),
		"README.md":   "not a pipeline",
		"notes.txt":   "also not a pipeline",
	}), p.registry(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded.Rejected) != 0 {
		t.Errorf("Rejected = %v, want nothing: unrelated files are not pipelines", loaded.Rejected)
	}
	if got := servedNames(loaded); !slices.Equal(got, []string{"served"}) {
		t.Errorf("Served = %v, want the one pipeline", got)
	}
}

func TestLoadReportsAnUnreadableDirectory(t *testing.T) {
	p := &probe{}
	if _, err := Load(filepath.Join(t.TempDir(), "absent"), p.registry(t)); err == nil {
		t.Error("loading a directory that does not exist should fail")
	}
}
