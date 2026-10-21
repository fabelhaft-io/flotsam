// Package provider is the API that SaaS integrations implement.
//
// # Why providers look like this
//
// flotsam is meant to grow through community contributions: each service
// (GitHub, Jira, ...) is written by someone who knows that service, not
// flotsam's internals. So a provider only does the part that is specific to
// its service, which is talking to its API and deciding what to save, and
// everything that is the same for every service lives in the core:
//
//   - reading and validating settings (package setting)
//   - retries, rate limits and secret redaction (Env)
//   - where and how files are stored, checksums and the manifest (Snapshot)
//
// That keeps providers small, makes every backup look the same, and means a
// fix in the core helps every provider at once.
//
// A provider is a package that registers itself in an init function, in the
// same way database/sql drivers do. Adding a service is then one new
// directory plus one import line, without changing any core code:
//
//	func init() {
//		provider.Register("example", provider.Spec[Config]{
//			Summary: "Everything in an Example workspace",
//			Hosts:   []string{"example.com"},
//			New:     New,
//		})
//	}
//
// Config is a settings struct as described in package setting. flotsam fills
// it from the config file and environment variables, validates it and passes
// it to New together with an Env.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/fabelhaft-io/flotsam/internal/registry"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
)

// TargetsSetting is the name of the optional setting that limits a backup to
// some URLs. Without targets, a provider backs up everything its credentials
// can access.
//
// The name is fixed for every provider so the core can fill it without
// knowing the service: `flotsam backup <url>` puts the URLs into this setting.
const TargetsSetting = "targets"

// Provider backs up one service.
type Provider interface {
	// Backup copies everything in scope into snapshot. Return an error only
	// when the backup as a whole failed; report problems with single items
	// with snapshot.Warnf and continue. One inaccessible repository shouldn't
	// cost the backup of all the others.
	Backup(ctx context.Context, snapshot Snapshot) error
}

// Snapshot receives what a provider backs up. Paths are relative and use
// forward slashes, for example "fabelhaft-io/flotsam/issues/42.json". Every
// path can be written once. Its methods are safe for concurrent use, so a
// provider may back up several items in parallel.
//
// Providers never see the storage behind a snapshot. That way every provider
// works with every storage backend, and the engine can add checksums and the
// manifest in one place.
type Snapshot interface {
	// WriteJSON writes value as indented JSON to path. Indented, because
	// people read backups with ordinary tools.
	WriteJSON(ctx context.Context, path string, value any) error
	// Create opens path for streaming large data, such as a git bundle, so it
	// never has to fit in memory. The file is only part of the snapshot once
	// Close returns nil, and every writer must be closed before Backup returns.
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	// Warnf records a problem that didn't stop the backup, such as one
	// inaccessible repository. The snapshot is then marked partial, and the
	// warning is listed in its manifest so the user knows what is missing.
	Warnf(format string, arguments ...any)
}

// Env gives a provider what it shares with the rest of flotsam. It is passed
// to New instead of being created by the provider, so that retries, rate
// limits and redaction behave the same in every provider, and so tests can
// swap in a client that replays recorded responses.
type Env interface {
	// HTTPClient returns a client that retries transient errors and waits out
	// rate limits. Use it for every request.
	HTTPClient() *http.Client
	// Logger returns a logger that removes secrets from what it writes.
	Logger() *slog.Logger
}

// Spec describes a provider for Register.
type Spec[Settings any] struct {
	// Summary is one line about what gets backed up, shown by
	// `flotsam providers`.
	Summary string
	// Hosts are the URL hosts this provider handles, such as "github.com", so
	// "flotsam backup <url>" can pick it. Only providers with a targets
	// setting can have hosts, because the URLs are passed as targets.
	Hosts []string
	// New creates the provider from validated settings. It may return an
	// error for invalid combinations of settings, but must not do any I/O:
	// the engine creates every provider before the first backup starts, so a
	// configuration mistake in one source stops the run before any API call.
	New func(configuration Settings, environment Env) (Provider, error)
}

// Info describes a registered provider. The rest of flotsam only works with
// Info, never with a provider's settings type, so the core doesn't need to
// know any provider at compile time.
type Info struct {
	Name     string
	Summary  string
	Hosts    []string
	Settings []setting.Definition

	// create is made by Register, where the settings type is still known. It
	// hides that type behind a plain function.
	create func(input setting.Input, environment Env) (Provider, error)
}

// SupportsTargets reports whether the provider has a targets setting, and so
// whether it can back up single URLs.
func (information Info) SupportsTargets() bool {
	return slices.ContainsFunc(information.Settings, func(definition setting.Definition) bool {
		return definition.Name == TargetsSetting
	})
}

// New fills and validates the provider's settings from input and creates the
// provider. Its errors start with input.Label, so with several sources each
// one says which source it is about.
func (information Info) New(input setting.Input, environment Env) (Provider, error) {
	return information.create(input, environment)
}

var (
	providers registry.Registry[Info]
	// hosts maps each URL host to the name of the provider that handles it,
	// for quick backups of URLs.
	hosts registry.Registry[string]
)

// Register makes a provider available under name. It panics if spec is
// invalid or name or one of its hosts is already taken. A panic in init stops
// the program right away, so a broken provider is found by whoever builds it,
// not by a user in the middle of a backup.
func Register[Settings any](name string, spec Spec[Settings]) {
	information, err := newInfo(name, spec)
	if err != nil {
		panic(fmt.Sprintf("provider.Register(%q): %v", name, err))
	}
	if err := providers.Add(name, information); err != nil {
		panic(fmt.Sprintf("provider.Register: %v", err))
	}
	for _, host := range information.Hosts {
		if err := hosts.Add(host, name); err != nil {
			panic(fmt.Sprintf("provider.Register(%q): host %v", name, err))
		}
	}
}

// newInfo checks spec and builds the Info for it.
func newInfo[Settings any](name string, spec Spec[Settings]) (Info, error) {
	// The provider name doubles as the source name of quick backups, so it
	// follows the source name rules.
	if err := setting.ValidateSourceName(name); err != nil {
		return Info{}, err
	}
	if name == "storage" {
		return Info{}, errors.New(`"storage" is reserved for storage settings`)
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
	information := Info{Name: name, Summary: spec.Summary, Settings: definitions}
	// The core writes URLs into targets as a list, so a targets setting of
	// any other type would break quick backups.
	for _, definition := range definitions {
		if definition.Name == TargetsSetting && (definition.Type != setting.TypeList || definition.Secret) {
			return Info{}, fmt.Errorf("the %q setting must be a []string and not secret", TargetsSetting)
		}
	}
	// Hosts are compared case-insensitively, like hosts in URLs.
	for _, host := range spec.Hosts {
		host = strings.ToLower(host)
		if host == "" || strings.ContainsAny(host, "/:") {
			return Info{}, fmt.Errorf("host %q must be a bare host name such as \"github.com\"", host)
		}
		information.Hosts = append(information.Hosts, host)
	}
	if len(information.Hosts) > 0 && !information.SupportsTargets() {
		return Info{}, fmt.Errorf("spec.Hosts needs a %q setting to receive the URLs", TargetsSetting)
	}

	information.create = func(input setting.Input, environment Env) (Provider, error) {
		var configuration Settings
		if err := setting.Fill(&configuration, input); err != nil {
			return nil, err
		}
		created, err := spec.New(configuration, environment)
		if err != nil && input.Label != "" {
			return nil, fmt.Errorf("%s: %w", input.Label, err)
		}
		return created, err
	}
	return information, nil
}

// Lookup returns the provider registered under name.
func Lookup(name string) (Info, bool) {
	return providers.Lookup(name)
}

// LookupHost returns the provider that handles URLs on host.
func LookupHost(host string) (Info, bool) {
	name, found := hosts.Lookup(strings.ToLower(host))
	if !found {
		return Info{}, false
	}
	return providers.Lookup(name)
}

// List returns every registered provider, sorted by name so output is stable.
func List() []Info {
	return providers.List()
}
