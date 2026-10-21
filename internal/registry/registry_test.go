package registry

import (
	"slices"
	"testing"
)

func TestRegistry(test *testing.T) {
	var registry Registry[int]
	if err := registry.Add("second", 2); err != nil {
		test.Fatal(err)
	}
	if err := registry.Add("first", 1); err != nil {
		test.Fatal(err)
	}
	if err := registry.Add("first", 3); err == nil {
		test.Error("adding a duplicate name succeeded, want an error")
	}

	if entry, found := registry.Lookup("first"); !found || entry != 1 {
		test.Errorf("Lookup(first) = %d, %t, want 1, true", entry, found)
	}
	if _, found := registry.Lookup("missing"); found {
		test.Error("Lookup(missing) found an entry")
	}
	if got := registry.List(); !slices.Equal(got, []int{1, 2}) {
		test.Errorf("List() = %v, want [1 2]", got)
	}
}
