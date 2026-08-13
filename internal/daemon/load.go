package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/trigger"
)

// Pipeline is one loaded, servable pipeline: a compiled plan plus the state a
// long-lived process keeps about it.
type Pipeline struct {
	// Name identifies the pipeline in logs and execution contexts. It is the
	// file's base name without its extension.
	Name string
	// Path is where it was loaded from.
	Path string
	// Plan is the compiled pipeline. It is immutable and shared by every run.
	Plan *pipeline.ExecutionPlan

	state
}

// Trigger returns the pipeline's trigger, which Load has already proven is
// present.
func (p *Pipeline) Trigger() trigger.Trigger { return p.Plan.Trigger.Trigger }

// Rejection is one pipeline file the daemon will not serve, and why.
type Rejection struct {
	Path string
	Err  error
}

func (r Rejection) String() string { return r.Path + ": " + r.Err.Error() }

// Loaded is the result of reading a directory of pipelines.
//
// Partial failure is normal and deliberate: a directory is a deployment
// surface, and one file that will not compile must not take down every
// unrelated pipeline beside it. What a caller does with Rejected is report it
// loudly; what it does with Served is run it.
type Loaded struct {
	// Served are the pipelines that compiled, declare a trigger, and hold an
	// uncontested claim.
	Served []*Pipeline
	// Rejected are files that did not compile, or whose claim collided.
	Rejected []Rejection
	// Untriggered are files that compiled but declare no trigger. They are not
	// a problem: they are the pipelines meant to be run by hand, and skipping
	// them is what "load only triggered pipelines" means.
	Untriggered []string
}

// Load compiles every pipeline in dir and sorts the results into what can be
// served and what cannot.
//
// It reads dir itself and does not descend into subdirectories: a served
// pipeline should be findable by listing one directory.
//
// It returns an error only when dir cannot be read. A directory where nothing
// compiles is a valid, if useless, configuration, and reporting it as a
// per-file rejection says far more than one opaque failure would.
func Load(dir string, reg *pipeline.Registries) (*Loaded, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline directory: %w", err)
	}

	loaded := &Loaded{}
	var candidates []*Pipeline

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, entry.Name())

		file, err := pipeline.ParseFile(path)
		if err != nil {
			loaded.Rejected = append(loaded.Rejected, Rejection{Path: path, Err: err})
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ext)
		plan, err := pipeline.Compile(file, reg, name)
		if err != nil {
			loaded.Rejected = append(loaded.Rejected, Rejection{Path: path, Err: err})
			continue
		}
		if plan.Trigger == nil {
			loaded.Untriggered = append(loaded.Untriggered, path)
			continue
		}
		candidates = append(candidates, &Pipeline{Name: name, Path: path, Plan: plan})
	}

	served, collisions := resolveClaims(candidates)
	loaded.Served = served
	loaded.Rejected = append(loaded.Rejected, collisions...)

	sort.Slice(loaded.Rejected, func(i, j int) bool {
		return loaded.Rejected[i].Path < loaded.Rejected[j].Path
	})
	return loaded, nil
}

// resolveClaims removes every pipeline whose trigger competes with another's.
//
// Both claimants are rejected rather than one being picked. Serving whichever
// happened to sort first would mean a pipeline silently answering requests
// meant for its neighbour, and the symptom of that is an absence: the other
// pipeline simply never runs, with nothing anywhere saying why. Refusing both
// converts a silent hijack into a loud, obvious failure that names both files.
//
// This is the one check a single-file compile cannot make, which is why
// `p6e check --dir` exists and why the daemon is not the only place it runs.
func resolveClaims(candidates []*Pipeline) (served []*Pipeline, rejected []Rejection) {
	byClaim := map[trigger.Claim][]*Pipeline{}
	for _, p := range candidates {
		claim := p.Trigger().Claim()
		if claim.IsZero() {
			continue // claims nothing, so it can never collide
		}
		byClaim[claim] = append(byClaim[claim], p)
	}

	contested := map[*Pipeline]bool{}
	for claim, claimants := range byClaim {
		if len(claimants) < 2 {
			continue
		}
		paths := make([]string, len(claimants))
		for i, p := range claimants {
			paths[i] = p.Path
		}
		sort.Strings(paths)
		for _, p := range claimants {
			contested[p] = true
			others := slices.DeleteFunc(slices.Clone(paths), func(s string) bool { return s == p.Path })
			rejected = append(rejected, Rejection{
				Path: p.Path,
				Err: fmt.Errorf("claims %s, which is also claimed by %s; neither is served",
					claim, strings.Join(others, ", ")),
			})
		}
	}

	for _, p := range candidates {
		if !contested[p] {
			served = append(served, p)
		}
	}
	return served, rejected
}
