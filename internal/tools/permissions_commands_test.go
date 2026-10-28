package tools

import (
	"reflect"
	"testing"
)

// Dynamic grants must be mutable on the active registry rather than only on a
// one-shot permission context. This test deliberately describes the runtime
// contract used by slash commands without exercising the network.
func TestRegistrySupportsPersistentPermissionGrantMutation(t *testing.T) {
	registry := &Registry{}
	for _, field := range []string{"ReadableRoots", "WritableRoots", "ReadOnlyPaths", "NetworkAllowlist"} {
		if _, ok := reflect.TypeOf(*registry).FieldByName(field); !ok {
			t.Fatalf("active tool registry has no persistent %s state", field)
		}
	}
	method := reflect.ValueOf(registry).MethodByName("AddPermissionGrant")
	if !method.IsValid() {
		t.Fatalf("active tool registry has no AddPermissionGrant mutation API")
	}
	if got := method.Type().NumIn(); got != 3 {
		t.Fatalf("AddPermissionGrant accepts %d arguments, want capability, target, recursive", got)
	}
	for i, want := range []reflect.Kind{reflect.String, reflect.String, reflect.Bool} {
		if got := method.Type().In(i).Kind(); got != want {
			t.Fatalf("AddPermissionGrant argument %d has kind %s, want %s", i, got, want)
		}
	}
}
