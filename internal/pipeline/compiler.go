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
	// Inputs lead, then steps, each group sorted. They share one namespace and
	// one index space, so a dependency on an input needs no special case
	// anywhere after this point.
	inputs := f.InputNames()
	c := &compiler{file: f, reg: reg, inputs: inputs}
	c.ids = append(append(make([]string, 0, len(inputs)+len(f.Steps)), inputs...), f.StepIDs()...)
	c.index = make(map[string]int, len(c.ids))
	for i, id := range c.ids {
		c.index[id] = i
	}

	// Every phase runs, and each skips only the steps it genuinely cannot judge:
	// a step whose node failed to resolve has no descriptor to type check
	// against, but its neighbours still do. Returning after the first phase that
	// found anything would hide a type error behind an unrelated typo in a with
	// block, which defeats the point of collecting problems at all.
	c.resolveInputs()
	c.resolve()
	c.checkDependencies()
	c.checkCycles()
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

	// inputs are the leading entries of ids, and inputTypes holds the type each
	// one declares, indexed the same way.
	inputs     []string
	inputTypes []node.TypeID

	nodes []node.RuntimeNode
	// deps holds each step's dependencies as indices in port order, resolved
	// from either needs form by checkEdges and reused when building the plan.
	deps     [][]int
	problems []Problem
}

func (c *compiler) fail(step, format string, args ...any) {
	c.problems = append(c.problems, Problem{Step: step, Message: fmt.Sprintf(format, args...)})
}

// resolveInputs checks that each declared type exists. A type is a name in a
// registry, so a typo like "Byte" is caught here rather than at the first run
// that supplies a value nothing can consume.
func (c *compiler) resolveInputs() {
	c.inputTypes = make([]node.TypeID, len(c.inputs))
	for i, name := range c.inputs {
		declared := node.TypeID(c.file.Inputs[name])
		if _, ok := node.LookupType(declared); !ok {
			c.fail("", "input %q declares type %q, which is not a registered type",
				name, declared)
			continue
		}
		c.inputTypes[i] = declared
	}
}

// isInput reports whether an index addresses a declared input rather than a
// step. Inputs occupy the leading positions of ids.
func (c *compiler) isInput(i int) bool { return i < len(c.inputs) }

// outputType is what an edge leaving this graph node carries. The empty TypeID
// means it could not be determined, which only happens after a failure that was
// already reported.
func (c *compiler) outputType(i int) node.TypeID {
	if c.isInput(i) {
		return c.inputTypes[i]
	}
	if c.nodes[i] == nil {
		return ""
	}
	return c.nodes[i].Descriptor().Output.Type
}

// resolve looks every step's capability up in the registry and builds it with
// its configuration. Constructing here, at compile time, is what lets a node
// validate its config once and lets its descriptor depend on that config.
func (c *compiler) resolve() {
	c.nodes = make([]node.RuntimeNode, len(c.ids))
	for i, id := range c.ids {
		if c.isInput(i) {
			continue // an input supplies a value; there is nothing to build
		}
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
		for _, dep := range c.file.Steps[id].Needs.Steps() {
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
		for _, dep := range c.file.Steps[c.ids[i]].Needs.Steps() {
			j, ok := c.index[dep]
			if !ok {
				continue // checkDependencies already reported it
			}
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

// checkEdges resolves each step's needs to dependency indices in port order,
// then type checks every edge. This is the reason the engine compiles at all.
func (c *compiler) checkEdges() {
	c.deps = make([][]int, len(c.ids))

	for i, id := range c.ids {
		if c.isInput(i) {
			continue // an input consumes nothing, so it has no edges to check
		}
		step := c.file.Steps[id]
		// A step whose node did not resolve has no ports to check against, and
		// one that names a missing step has nothing to check on the other end.
		// Both were already reported; checking anyway would only add noise.
		if c.nodes[i] == nil || !c.dependenciesResolve(step) {
			continue
		}
		desc := c.nodes[i].Descriptor()

		deps, ok := c.resolveNeeds(id, step, desc)
		if !ok {
			continue
		}
		c.deps[i] = deps

		for port, dep := range deps {
			want := desc.Inputs[port].Type
			got := c.outputType(dep)
			if got == "" {
				continue // already reported: nothing to compare against
			}
			if want != got {
				// Naming the producer for what it is: "step" for a computed
				// value, "pipeline input" for a supplied one, so the reader
				// knows where to go and looks in the right half of the file.
				if c.isInput(dep) {
					c.fail(id, "input %q expects %s but pipeline input %q supplies %s",
						desc.Inputs[port].Name, want, c.ids[dep], got)
				} else {
					c.fail(id, "input %q expects %s but step %q produces %s",
						desc.Inputs[port].Name, want, c.ids[dep], got)
				}
			}
		}
	}
}

// dependenciesResolve reports whether every step this one names exists.
func (c *compiler) dependenciesResolve(step Step) bool {
	for _, dep := range step.Needs.Steps() {
		if _, ok := c.index[dep]; !ok {
			return false
		}
	}
	return true
}

// resolveNeeds turns either needs form into dependency indices ordered by input
// port, which is what the rest of the compiler and the executor work with.
//
// The list form is rejected for a node whose input ports are not pairwise
// type-distinct: that is the one shape where a swapped list type checks and
// silently means something else, so the mapping form is mandatory there.
func (c *compiler) resolveNeeds(id string, step Step, desc node.Descriptor) ([]int, bool) {
	if step.Needs.Named() {
		return c.resolveNamedNeeds(id, step, desc)
	}

	// Checked before arity, because a node whose ports are not pairwise
	// type-distinct needs the mapping form whatever the list contains. Reporting
	// a count first would send the author back to a form they cannot use.
	if typ, names, ambiguous := desc.AmbiguousInputs(); ambiguous {
		c.fail(id, "node %q has inputs of identical type %s (%s), so a positional swap would type check: bind needs by name instead",
			step.Uses, typ, names)
		return nil, false
	}

	list := step.Needs.Positional()
	if len(list) != desc.Arity() {
		c.fail(id, "node %q expects %d input(s) %s but needs lists %d",
			step.Uses, desc.Arity(), desc.InputTypes(), len(list))
		return nil, false
	}
	deps := make([]int, len(list))
	for port, dep := range list {
		deps[port] = c.index[dep]
	}
	return deps, true
}

// resolveNamedNeeds binds by input port name. Every port must be bound and
// every binding must name a real port, so a typo cannot silently leave an input
// unconnected.
func (c *compiler) resolveNamedNeeds(id string, step Step, desc node.Descriptor) ([]int, bool) {
	deps := make([]int, desc.Arity())
	bound := make(map[string]bool, desc.Arity())
	ok := true

	for port, in := range desc.Inputs {
		dep, found := step.Needs.Port(in.Name)
		if !found {
			c.fail(id, "input %q of node %q is not bound by needs (inputs: %s)",
				in.Name, step.Uses, desc.InputNames())
			ok = false
			continue
		}
		bound[in.Name] = true
		deps[port] = c.index[dep]
	}

	for _, name := range step.Needs.PortNames() {
		if !bound[name] {
			c.fail(id, "needs binds %q, but node %q has no such input (inputs: %s)",
				name, step.Uses, desc.InputNames())
			ok = false
		}
	}
	return deps, ok
}

// plan precomputes what the executor would otherwise have to work out on every
// run: dependency indices, the reverse edges, and where to start.
func (c *compiler) plan(name string) *ExecutionPlan {
	p := &ExecutionPlan{Name: name, Steps: make([]CompiledStep, len(c.ids))}

	for i, id := range c.ids {
		if c.isInput(i) {
			// An input is a step with no node and no dependencies. It is not a
			// root: the executor records its supplied value before scheduling
			// anything, so it is never waiting to run.
			p.Steps[i] = CompiledStep{ID: id, InputOffset: p.TotalInputs, Retry: DefaultRetry}
			p.Inputs = append(p.Inputs, PlanInput{Name: id, Type: c.inputTypes[i], Step: i})
			continue
		}

		step := c.file.Steps[id]
		deps := c.deps[i]
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
