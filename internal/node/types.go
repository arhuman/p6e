package node

import (
	"fmt"
	"reflect"
	"sync"
)

// TypeID is the runtime identity of a Go type on a port. The compiler compares
// TypeIDs to decide whether an edge is legal; the executor never looks at one.
//
// Two types are compatible if and only if their TypeIDs are equal. There is no
// subtyping and no implicit conversion: converting between unrelated types is a
// node's job, visible in the graph, not a hidden engine behavior.
type TypeID string

var (
	typesMu sync.RWMutex
	byType  = map[reflect.Type]TypeID{}
	byID    = map[TypeID]reflect.Type{}
)

// RegisterType gives T a short, stable TypeID for use in pipeline files and
// error messages, for example RegisterType[*HTTPResponse]("HTTPResponse").
//
// Call it from an init function in the package that owns T, before any pipeline
// is compiled. Registering the same type under two names, or two types under
// one name, panics: that is a programming error, not a run-time condition.
func RegisterType[T any](name string) TypeID {
	id := TypeID(name)
	t := reflect.TypeFor[T]()

	typesMu.Lock()
	defer typesMu.Unlock()
	if existing, ok := byType[t]; ok && existing != id {
		panic(fmt.Sprintf("node: type %s already registered as %q, cannot also be %q", t, existing, id))
	}
	if existing, ok := byID[id]; ok && existing != t {
		panic(fmt.Sprintf("node: type name %q already registered for %s, cannot also be %s", id, existing, t))
	}
	byType[t] = id
	byID[id] = t
	return id
}

// TypeOf returns T's TypeID: its registered name if it has one, otherwise a
// canonical fully qualified name derived from the Go type.
//
// It is called when a node is constructed, never during execution.
func TypeOf[T any]() TypeID {
	t := reflect.TypeFor[T]()

	typesMu.RLock()
	id, ok := byType[t]
	typesMu.RUnlock()
	if ok {
		return id
	}
	return TypeID(canonicalName(t))
}

// LookupType resolves a TypeID back to its Go type. Only registered types
// resolve, which is what a pipeline file may name (see the value node).
func LookupType(id TypeID) (reflect.Type, bool) {
	typesMu.RLock()
	defer typesMu.RUnlock()
	t, ok := byID[id]
	return t, ok
}

// canonicalName builds a collision-free name for an unregistered type.
// reflect's own String uses the short package name, which two packages can
// share; the import path cannot.
func canonicalName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + canonicalName(t.Elem())
	case reflect.Slice:
		return "[]" + canonicalName(t.Elem())
	}
	if path := t.PkgPath(); path != "" {
		return path + "." + t.Name()
	}
	return t.String()
}
