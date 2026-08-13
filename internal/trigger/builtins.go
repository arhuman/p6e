package trigger

import "sync"

var (
	once     sync.Once
	builtins *Registry
)

// Builtins returns the shared registry of built-in triggers.
//
// It is built once and reused: a trigger instance is created per pipeline at
// compile time, so what lives here is the definition, not any per-pipeline
// state.
func Builtins() *Registry {
	once.Do(func() {
		builtins = NewRegistry()
		for _, d := range Definitions() {
			builtins.MustRegister(d)
		}
	})
	return builtins
}

// Definitions lists every built-in trigger. Tests use it to build an isolated
// registry rather than sharing the process-wide one.
func Definitions() []Definition {
	return []Definition{
		WebhookDefinition(),
		ScheduleDefinition(),
	}
}
