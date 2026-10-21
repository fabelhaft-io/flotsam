// Package storage is the API that backup destinations implement.
//
// Where backups go (a local disk, a NAS, S3-compatible object storage) is a
// separate choice from what is backed up, so it is a separate plug point:
// every provider works with every storage backend, and neither knows about the
// other. The interface is deliberately small, just create, open and list, so
// that it fits object stores, which can't rename, append or lock, as well as
// file systems.
//
// A storage backend registers itself in an init function, in the same way as
// a provider:
//
//	func init() {
//		storage.Register("local", storage.Spec[Settings]{
//			Summary: "A directory on the local file system",
//			New:     New,
//		})
//	}
//
// Its settings come from the "storage" section of the config file and from
// FLOTSAM_STORAGE_<SETTING> environment variables.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/fabelhaft-io/flotsam/internal/registry"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
)

// Storage stores snapshot files under keys: relative paths with forward
// slashes, such as "work-github/2026-10-21T19-00-00Z/manifest.json".
// Package storagetest checks that an implementation meets this contract.
type Storage interface {
	// Create opens key for writing. The data becomes visible to Open and
	// List only after Close returns nil, so nobody ever sees half a file and
	// takes it for the whole thing. If a Write fails, Close discards the data
	// and returns an error. Create returns an error wrapping fs.ErrExist if
	// key already exists, because snapshots are never changed once written.
	//
	// Create only checks keys that are already stored. Callers must not open
	// the same key twice at the same time; the engine's snapshot makes sure
	// of that, so backends don't each need locking for it.
	Create(ctx context.Context, key string) (io.WriteCloser, error)
	// Open returns the data stored under key, or an error wrapping
	// fs.ErrNotExist.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns every key that starts with prefix, sorted. It is a plain
	// string prefix, not a directory, because object stores have no
	// directories.
	List(ctx context.Context, prefix string) ([]string, error)
}

// Locator is an optional interface for storage that can tell people where a
// key ended up, for example as a local file path. It is optional because many
// backends have no address a person could open, and the CLI then shows the
// key instead.
type Locator interface {
	Location(key string) string
}

// Spec describes a storage backend for Register.
type Spec[Settings any] struct {
	// Summary is one line about where backups go.
	Summary string
	// New creates the storage from validated settings.
	New func(configuration Settings) (Storage, error)
}

// Info describes a registered storage backend. Like provider.Info, it hides
// the backend's settings type, so the core works with any backend.
type Info struct {
	Name     string
	Summary  string
	Settings []setting.Definition

	create func(input setting.Input) (Storage, error)
}

// New fills and validates the backend's settings from input and creates the
// storage. Its errors start with input.Label.
func (information Info) New(input setting.Input) (Storage, error) {
	return information.create(input)
}

var backends registry.Registry[Info]

// Register makes a storage backend available under name. It panics if spec is
// invalid or name is already taken, for the same reason as provider.Register:
// mistakes show up when the program starts, not during a backup.
func Register[Settings any](name string, spec Spec[Settings]) {
	information, err := newInfo(name, spec)
	if err != nil {
		panic(fmt.Sprintf("storage.Register(%q): %v", name, err))
	}
	if err := backends.Add(name, information); err != nil {
		panic(fmt.Sprintf("storage.Register: %v", err))
	}
}

// newInfo checks spec and builds the Info for it. The configure function is
// created here, where the settings type is still known, and hides it.
func newInfo[Settings any](name string, spec Spec[Settings]) (Info, error) {
	// Storage names are typed into the config file's "type", so they follow
	// the same simple naming rule as sources and providers.
	if err := setting.ValidateSourceName(name); err != nil {
		return Info{}, err
	}
	if spec.Summary == "" {
		return Info{}, errors.New("spec.Summary is empty")
	}
	if spec.New == nil {
		return Info{}, errors.New("spec.New is nil")
	}
	definitions, err := setting.Describe(reflect.TypeFor[Settings]())
	if err != nil {
		return Info{}, err
	}
	return Info{
		Name:     name,
		Summary:  spec.Summary,
		Settings: definitions,
		create: func(input setting.Input) (Storage, error) {
			var configuration Settings
			if err := setting.Fill(&configuration, input); err != nil {
				return nil, err
			}
			created, err := spec.New(configuration)
			if err != nil && input.Label != "" {
				return nil, fmt.Errorf("%s: %w", input.Label, err)
			}
			return created, err
		},
	}, nil
}

// Lookup returns the storage backend registered under name.
func Lookup(name string) (Info, bool) {
	return backends.Lookup(name)
}

// List returns every registered storage backend, sorted by name.
func List() []Info {
	return backends.List()
}
