// Package registry holds named entries, such as providers and storage backends,
// that register themselves at init time.
//
// Providers and storage backends are registered the same way, so they share
// this one implementation instead of each keeping its own map and lock.
package registry

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Registry maps names to entries. The zero value is ready to use, so a
// package-level variable needs no setup before init functions call Add. It is
// safe for concurrent use: registration happens in init, but tests may
// register entries while other tests read them.
type Registry[Entry any] struct {
	mutex   sync.RWMutex
	entries map[string]Entry
}

// Add stores entry under name, or returns an error if name is already taken.
// Replacing an entry is never allowed: two providers with the same name are
// always a mistake, and the later one silently winning would hide it.
func (registry *Registry[Entry]) Add(name string, entry Entry) error {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if _, exists := registry.entries[name]; exists {
		return fmt.Errorf("%q is already registered", name)
	}
	if registry.entries == nil {
		registry.entries = make(map[string]Entry)
	}
	registry.entries[name] = entry
	return nil
}

// Lookup returns the entry stored under name.
func (registry *Registry[Entry]) Lookup(name string) (Entry, bool) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	entry, found := registry.entries[name]
	return entry, found
}

// List returns every entry, sorted by name, so listings and error messages
// don't change order from run to run.
func (registry *Registry[Entry]) List() []Entry {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	entries := make([]Entry, 0, len(registry.entries))
	for _, name := range slices.Sorted(maps.Keys(registry.entries)) {
		entries = append(entries, registry.entries[name])
	}
	return entries
}
