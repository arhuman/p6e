package node

import (
	"strings"
	"testing"
)

type registered struct{ N int }

type unregistered struct{ N int }

func init() {
	RegisterType[*registered]("Registered")
}

func TestTypeOfUsesRegisteredName(t *testing.T) {
	if got := TypeOf[*registered](); got != "Registered" {
		t.Errorf("TypeOf = %q, want %q", got, "Registered")
	}
}

func TestTypeOfFallsBackToQualifiedName(t *testing.T) {
	got := string(TypeOf[*unregistered]())
	if !strings.HasPrefix(got, "*github.com/arhuman/p6e/internal/node.") {
		t.Errorf("TypeOf = %q, want an import-path-qualified name", got)
	}
}

// A short package name is not unique across a program, so an unregistered type
// must not be identified by one.
func TestCanonicalNameIsNotAmbiguous(t *testing.T) {
	if got := string(TypeOf[*unregistered]()); got == "*node.unregistered" {
		t.Error("TypeOf used the short package name, which two packages can share")
	}
}

func TestTypeOfDistinguishesValueFromPointer(t *testing.T) {
	if TypeOf[registered]() == TypeOf[*registered]() {
		t.Error("value and pointer types share a TypeID")
	}
}

func TestRegisterTypeRejectsConflict(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a second name for one type should panic")
		}
	}()
	RegisterType[*registered]("SomethingElse")
}

func TestLookupType(t *testing.T) {
	if _, ok := LookupType("Registered"); !ok {
		t.Error("registered type does not resolve by name")
	}
	if _, ok := LookupType("NeverRegistered"); ok {
		t.Error("unregistered name resolved")
	}
}
