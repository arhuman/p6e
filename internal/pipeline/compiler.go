package pipeline

import (
	"fmt"
	"strings"

	"github.com/arhuman/p6e/internal/node"
)

// Problem is one thing wrong with a pipeline, attributed to a step where that
// makes sense.
type Problem struct {
	Step    string
	Message string
}

func (p Problem) String() string {
	if p.Step == "" {
		return p.Message
	}
	return fmt.Sprintf("step %q: %s", p.Step, p.Message)
}

// CompileError collects everything wrong with a pipeline. Compilation reports
// as much as it can in one pass: fixing one error at a time, recompiling
// between each, is a miserable way to write a pipeline.
type CompileError struct {
	Problems []Problem
}

func (e *CompileError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0].String()
	}
	lines := make([]string, 0, len(e.Problems)+1)
	lines = append(lines, fmt.Sprintf("%d problems:", len(e.Problems)))
	for _, p := range e.Problems {
		lines = append(lines, "  "+p.String())
	}
	return strings.Join(lines, "\n")
}

// Compile turns a parsed pipeline into an execution plan, or explains why it
// cannot. Everything checkable before execution is checked here: node
// resolution, configuration, dependencies, cycles, arity, and edge types.
// Nothing it proves is re-checked at run time.
//
// name identifies the workflow in execution contexts and reports.
func Compile(f *File, reg *node.Registry, name string) (*ExecutionPlan, error) {
	c := &compiler{file: f, reg: reg, ids: f.StepIDs()}
	c.index = make(map[string]int, len(c.ids))
	for i, id := range c.ids {
		c.index[id] = i
	}

	// Resolution and dependency existence come first: the later phases index
	// into resolved nodes and would report noise without them.
	c.resolve()
	c.checkDependencies()
	if len(c.problems) > 0 {
		return nil, &CompileError{Problems: c.problems}
	}

	// A cycle makes the type check non-terminating in the general case and its
	// output meaningless, so it gates what follows.
	c.checkCycles()
	if len(c.problems) > 0 {
		return nil, &CompileError{Problems: c.problems}
	}

	c.checkEdges()
	if len(c.problems) > 0 {
		return nil, &CompileError{Problems: c.problems}
	}

	return c.plan(name), nil
}

type compiler struct {
	file  *File
	reg   *node.Registry
	ids   []string
	index map[string]int

	nodes    []node.RuntimeNode
	problems []Problem
}

func (c *compiler) fail(step, format string, args ...any) {
	c.problems = append(c.problems, Problem{Step: step, Message: fmt.Sprintf(format, args...)})
}

// resolve looks every step's capability up in the registry and builds it with
// its configuration. Constructing here, at compile time, is what lets a node
// validate its config once and lets its descriptor depend on that config.
func (c *compiler) resolve() {
	c.nodes = make([]node.RuntimeNode, len(c.ids))
	for i, id := range c.ids {
		step := c.file.Steps[id]

		def, err := c.reg.Resolve(step.Uses)
		if err != nil {
			c.fail(id, "%v", err)
			continue
		}
		n, err := def.New(step.Config())
		if err != nil {
			c.fail(id, "invalid configuration for %q: %v", step.Uses, err)
			continue
		}
		if n == nil {
			c.fail(id, "node %q built nothing", step.Uses)
			continue
		}
		c.nodes[i] = n
	}
}

func (c *compiler) checkDependencies() {
	for _, id := range c.ids {
		for _, dep := range c.file.Steps[id].Needs {
			if _, ok := c.index[dep]; !ok {
				c.fail(id, "needs %q, which is not a step in this pipeline", dep)
			}
		}
	}
}

// checkCycles reports an actual cycle path rather than the set of steps
// involved in one: "a needs b needs c needs a" is something a reader can act on.
func (c *compiler) checkCycles() {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // finished
	)
	color := make([]int, len(c.ids))
	var path []int

	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = grey
		path = append(path, i)
		for _, dep := range c.file.Steps[c.ids[i]].Needs {
			j := c.index[dep]
			switch color[j] {
			case grey:
				c.reportCycle(path, j)
				return true
			case white:
				if visit(j) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		color[i] = black
		return false
	}

	for i := range c.ids {
		if color[i] == white && visit(i) {
			return // one cycle report is enough; the rest are usually the same knot
		}
	}
}

func (c *compiler) reportCycle(path []int, back int) {
	start := 0
	for i, idx := range path {
		if idx == back {
			start = i
			break
		}
	}
	names := make([]string, 0, len(path)-start+1)
	for _, idx := range path[start:] {
		names = append(names, fmt.Sprintf("%q", c.ids[idx]))
	}
	names = append(names, fmt.Sprintf("%q", c.ids[back]))
	c.fail("", "dependency cycle: %s", strings.Join(names, " needs "))
}

// checkEdges is the type check: the reason this engine compiles at all.
func (c *compiler) checkEdges() {
	for i, id := range c.ids {
		step := c.file.Steps[id]
		desc := c.nodes[i].Descriptor()

		if len(step.Needs) != desc.Arity() {
			c.fail(id, "node %q expects %d input(s) %s but needs lists %d",
				step.Uses, desc.Arity(), desc.InputTypes(), len(step.Needs))
			continue
		}

		for port, dep := range step.Needs {
			want := desc.Inputs[port].Type
			got := c.nodes[c.index[dep]].Descriptor().Output.Type
			if want != got {
				c.fail(id, "input %q expects %s but step %q produces %s",
					desc.Inputs[port].Name, want, dep, got)
			}
		}
	}
}

// plan precomputes what the executor would otherwise have to work out on every
// run: dependency indices, the reverse edges, and where to start.
func (c *compiler) plan(name string) *ExecutionPlan {
	p := &ExecutionPlan{Name: name, Steps: make([]CompiledStep, len(c.ids))}

	for i, id := range c.ids {
		step := c.file.Steps[id]
		deps := make([]int, len(step.Needs))
		for port, dep := range step.Needs {
			deps[port] = c.index[dep]
		}
		p.Steps[i] = CompiledStep{
			ID:          id,
			Node:        c.nodes[i],
			Deps:        deps,
			InputOffset: p.TotalInputs,
			Retry:       step.RetryPolicy(),
		}
		p.TotalInputs += len(deps)
		if len(deps) == 0 {
			p.Roots = append(p.Roots, i)
		}
	}

	for i := range p.Steps {
		for _, dep := range p.Steps[i].Deps {
			p.Steps[dep].Dependents = append(p.Steps[dep].Dependents, i)
		}
	}
	return p
}
