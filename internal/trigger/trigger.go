// Package trigger defines what starts a pipeline run in a long-lived process.
//
// A trigger is deliberately not a node. A node is pulled: the executor invokes
// it once its dependencies are met and it returns a value. A trigger is pushed:
// the world invokes it, it fires an unbounded number of times, it lives as long
// as the daemon rather than as long as a run, and it may own a resource shared
// across pipelines. Nothing about that fits an interface whose Execute returns
// once, and a trigger that blocked inside Execute would hold a concurrency slot
// while idle, be indistinguishable from a wedged node, and deadlock against the
// executor's inline fast path.
//
// What a trigger does instead is supply a run's inputs. A pipeline declares the
// typed values it consumes under `inputs`, the compiler proves the trigger
// provides every one of them at the declared type, and the daemon hands them to
// runtime.Run. The injection path is the ordinary one, so the executor needs no
// notion of triggers at all.
//
// This package depends only on node and the domain types, never on pipeline or
// runtime: a trigger reports what an event carried, and knows nothing about how
// a plan is compiled or how a run is scheduled.
package trigger

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/arhuman/p6e/internal/node"
)

// Descriptor is everything the compiler needs to know about a trigger without
// starting it: what it is called, and what one event supplies.
type Descriptor struct {
	// Name is the capability a pipeline references, for example
	// "trigger.webhook".
	Name string
	// Provides are the named, typed values one event carries. A pipeline
	// declares the subset it consumes under `inputs`; providing more than a
	// pipeline declares is normal and the surplus is simply unused.
	Provides []node.PortDescriptor
}

// Provided reports the type this trigger supplies under name.
func (d Descriptor) Provided(name string) (node.TypeID, bool) {
	for _, p := range d.Provides {
		if p.Name == name {
			return p.Type, true
		}
	}
	return "", false
}

// ProvidedNames renders what this trigger supplies, for the error a pipeline
// author sees when an input is not among them.
func (d Descriptor) ProvidedNames() string {
	if len(d.Provides) == 0 {
		return "nothing"
	}
	names := make([]string, len(d.Provides))
	for i, p := range d.Provides {
		names[i] = fmt.Sprintf("%s %s", strconv.Quote(p.Name), p.Type)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Claim is the process-wide resource a trigger needs to itself. Two loaded
// pipelines making the same claim cannot both be served, and the daemon rejects
// both rather than letting one silently shadow the other: a hijacked route is
// found in production by absence, which is the hardest kind of bug to find.
//
// The zero Claim claims nothing, which is the schedule's case: any number of
// schedules coexist.
type Claim struct {
	// Kind groups claims that can collide with each other, for example "http".
	Kind string
	// Key identifies the resource within that kind, for example
	// "POST /hooks/deploy".
	Key string
}

// IsZero reports whether this trigger claims nothing exclusive.
func (c Claim) IsZero() bool { return c.Kind == "" && c.Key == "" }

// String renders the claim for logs and collision reports.
func (c Claim) String() string {
	if c.IsZero() {
		return "none"
	}
	return c.Kind + " " + c.Key
}

// Trigger is a compiled, configured source of runs. It is built once, at
// compile time, and must be safe for concurrent use: events do not wait for
// each other.
type Trigger interface {
	Descriptor() Descriptor
	Claim() Claim
}

// SelfDriven is a trigger that runs its own loop, such as a schedule waiting on
// a timer. Listen owns the goroutine until ctx ends.
type SelfDriven interface {
	Trigger
	// Listen fires once per event until ctx is done, then returns. It returns
	// an error only when it could not listen at all; an individual run's
	// failure is reported in the Outcome that fire returns.
	Listen(ctx context.Context, fire Fire) error
}

// HTTPDriven is a trigger the daemon's own listener drives, such as a webhook.
//
// It exists as a separate interface rather than a Listen implementation because
// a webhook genuinely cannot own its socket: one listener serves every webhook
// pipeline in the process and the daemon routes by claim. Giving the webhook a
// Listen method would hide that, and the first pipeline to bind the port would
// lock out the rest.
type HTTPDriven interface {
	Trigger
	// Values extracts what this event supplies. An error means the request is
	// not usable, and the daemon rejects it without starting a run.
	Values(r *http.Request) (map[string]node.Value, error)
	// ResponseTypes lists the output types this trigger can write back as a
	// response body. The compiler checks a pipeline's respond_with against it,
	// which is what keeps the pipeline package free of any knowledge of domain
	// types: the trigger that has to write the bytes is the one that says what
	// it can write.
	ResponseTypes() []node.TypeID
	// Respond writes a successful run's output. The value is of one of the
	// ResponseTypes, proven by the compiler, so this never has to reject it.
	Respond(w http.ResponseWriter, value node.Value) error
}

// Authenticating is implemented by an HTTPDriven trigger that can verify an
// event came from who it claims to.
//
// It is a separate, optional interface rather than a method on HTTPDriven
// because a trigger that cannot authenticate at all is a coherent thing to
// write, and forcing it to answer would mean forcing it to lie. A trigger that
// does not implement this, or that reports false, is serving an open route,
// and the daemon says so at startup rather than leaving it to be discovered.
type Authenticating interface {
	Authenticated() bool
}

// Fire runs the pipeline once with the values one event supplied. The daemon
// gives it to a trigger, and it is safe for concurrent use: a trigger firing
// again before the previous run finished is normal, and what happens then is
// the pipeline's declared overlap policy, not the trigger's business.
type Fire func(ctx context.Context, values map[string]node.Value) Outcome

// Outcome is what a run did, in the only terms a trigger needs. This package
// deliberately does not expose an execution: a webhook needs a status and a
// body, not a step-by-step report, and keeping it that way is what stops the
// trigger layer growing a dependency on the runtime.
type Outcome struct {
	// Err is the failure that ended the run, or nil.
	Err *node.Error
	// Value is the output of the step the pipeline named in respond_with. It is
	// zero when the pipeline named none, or when the run failed before
	// producing it.
	Value node.Value
}

// Failed reports whether the run did not complete successfully.
func (o Outcome) Failed() bool { return o.Err != nil }

// Definition is a registered trigger capability: the name a pipeline
// references, and how to build a configured trigger from a `with` block.
//
// New is called once per pipeline at compile time, never while serving, which
// is what makes a malformed `with` block a `p6e check` failure rather than a
// surprise on the first event.
type Definition struct {
	Name string
	New  func(cfg node.Config) (Trigger, error)
}

// Registry resolves the trigger names a pipeline references. It mirrors
// node.Registry rather than sharing it, because a trigger is not a RuntimeNode
// and a pipeline may not use one where the other belongs.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]Definition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

// Register adds a definition. Registering a name twice is an error: silently
// replacing a trigger implementation is never what the caller meant.
func (r *Registry) Register(d Definition) error {
	if d.Name == "" {
		return fmt.Errorf("trigger: definition has no name")
	}
	if d.New == nil {
		return fmt.Errorf("trigger: definition %q has no constructor", d.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[d.Name]; exists {
		return fmt.Errorf("trigger: %q is already registered", d.Name)
	}
	r.defs[d.Name] = d
	return nil
}

// MustRegister is Register for package init, where a failure is a build error
// in disguise.
func (r *Registry) MustRegister(d Definition) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// Resolve looks up a trigger capability by name.
func (r *Registry) Resolve(name string) (Definition, error) {
	r.mu.RLock()
	d, ok := r.defs[name]
	r.mu.RUnlock()
	if !ok {
		return Definition{}, fmt.Errorf("unknown trigger %q (known: %v)", name, r.Names())
	}
	return d, nil
}

// Names lists registered trigger capabilities in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
