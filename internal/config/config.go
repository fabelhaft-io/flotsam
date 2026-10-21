// Package config turns the JSON config file, or the arguments of a quick
// backup, into the storage and sources to run.
//
// Every problem is reported before anything runs, and all of them at once.
// Backups usually run unattended at night; a typo that only shows up after
// the first source has run for an hour, or one mistake per run, costs days of
// missing backups. This package checks the structure (names, providers,
// storage type, environment variable collisions); the settings themselves are
// checked when the engine creates the providers, still before any backup
// starts.
//
// Both ways of starting a backup produce a File, and Resolve checks it, so
// quick backups and scheduled ones run exactly the same code.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
	"github.com/fabelhaft-io/flotsam/pkg/storage"
)

const (
	// LocalStorage is the storage backend that quick backups write to.
	// Quick backups are for trying flotsam and one-off copies, so they use
	// the backend that needs no setup.
	LocalStorage = "local"
	// localPathSetting is the setting of local storage that names its
	// directory.
	localPathSetting = "path"
)

// File is the structure of the config file. Settings stay raw JSON here,
// because their types are only known once the provider is looked up.
type File struct {
	Storage *StorageSection `json:"storage"`
	Sources []SourceSection `json:"sources"`
}

// StorageSection is the "storage" object of the config file.
type StorageSection struct {
	Type     string                     `json:"type"`
	Settings map[string]json.RawMessage `json:"settings"`
}

// SourceSection is one entry of the "sources" list of the config file.
type SourceSection struct {
	Name     string                     `json:"name"`
	Provider string                     `json:"provider"`
	Settings map[string]json.RawMessage `json:"settings"`
}

// Config is a checked configuration, ready to run.
type Config struct {
	Storage Storage
	Sources []Source
}

// Storage is the storage backend with the input for its settings.
type Storage struct {
	Info  storage.Info
	Input setting.Input
}

// Source is one source: a provider plus the input for its settings, under a
// name of the user's choosing. The name, not the provider, identifies the
// source, so the same provider can be used several times, for example once per
// GitHub org.
type Source struct {
	Name     string
	Provider provider.Info
	Input    setting.Input
}

// Options control File.Resolve.
type Options struct {
	// LookupEnvironment reads environment variables; os.LookupEnv in production.
	LookupEnvironment func(name string) (string, bool)
	// Only limits the result to the source with this name, so one broken
	// source doesn't block running the others one at a time.
	Only string
}

// Load reads the config file at path.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	return Parse(data, filepath.Dir(path))
}

// Parse reads a config file. Unknown fields are an error, so a typo such as
// "source" instead of "sources" doesn't silently produce an empty config.
// Parse only checks the structure; Resolve checks the content.
//
// directory is the directory of the config file. A relative path of local
// storage is resolved against it, so scheduled backups land in the same place
// whatever directory cron or systemd starts flotsam in.
func Parse(data []byte, directory string) (*File, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("parse config: unexpected data after the closing brace")
	}
	if err := file.resolveLocalPath(directory); err != nil {
		return nil, err
	}
	return &file, nil
}

// resolveLocalPath makes a relative local storage path relative to directory.
// Values that aren't a string are left alone; setting.Fill reports them.
func (file *File) resolveLocalPath(directory string) error {
	if file.Storage == nil || file.Storage.Type != LocalStorage || directory == "" {
		return nil
	}
	var path string
	if json.Unmarshal(file.Storage.Settings[localPathSetting], &path) != nil || path == "" || filepath.IsAbs(path) {
		return nil
	}
	resolved, err := json.Marshal(filepath.Join(directory, path))
	if err != nil {
		return err
	}
	file.Storage.Settings[localPathSetting] = resolved
	return nil
}

// Resolve checks the config and prepares the settings input of the storage
// and of every source.
func (file *File) Resolve(options Options) (*Config, error) {
	var problems []error
	var storageInfo storage.Info
	switch {
	case file.Storage == nil:
		problems = append(problems, errors.New(`missing "storage" section`))
	case file.Storage.Type == "":
		problems = append(problems, errors.New(`storage: missing "type"`))
	default:
		information, found := storage.Lookup(file.Storage.Type)
		if !found {
			problems = append(problems, fmt.Errorf("storage: unknown type %q; available: %s", file.Storage.Type, storageNames()))
		}
		storageInfo = information
	}

	if len(file.Sources) == 0 {
		problems = append(problems, errors.New(`no sources in "sources"`))
	}
	seen := make(map[string]bool)
	providers := make([]provider.Info, len(file.Sources))
	for index, section := range file.Sources {
		// Sources without a valid name are identified by position, so the
		// error still says which entry is meant.
		label := fmt.Sprintf("source %d", index+1)
		if section.Name != "" {
			label = fmt.Sprintf("source %q", section.Name)
		}
		switch err := setting.ValidateSourceName(section.Name); {
		case section.Name == "":
			problems = append(problems, fmt.Errorf(`%s: missing "name"`, label))
		case err != nil:
			problems = append(problems, fmt.Errorf("%s: %w", label, err))
		case section.Name == "storage":
			problems = append(problems, fmt.Errorf(`%s: the name "storage" is reserved for storage settings`, label))
		case seen[section.Name]:
			problems = append(problems, fmt.Errorf("%s: the name is used twice", label))
		}
		seen[section.Name] = true

		information, found := provider.Lookup(section.Provider)
		switch {
		case section.Provider == "":
			problems = append(problems, fmt.Errorf(`%s: missing "provider"`, label))
		case !found:
			problems = append(problems, fmt.Errorf("%s: unknown provider %q; available: %s", label, section.Provider, providerNames()))
		}
		providers[index] = information
	}
	if options.Only != "" && !seen[options.Only] {
		problems = append(problems, fmt.Errorf("no source named %q in the config", options.Only))
	}
	// Stop here: collisions can't be computed for unknown providers, and
	// would only add confusing follow-up errors.
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	// Collisions are checked across all sources, even with Only set, because
	// they are a property of the config: fixing them later would rename
	// environment variables people already use.
	owners := []settingOwner{{label: "storage", prefix: setting.StorageEnvironmentPrefix, definitions: storageInfo.Settings}}
	for index, section := range file.Sources {
		owners = append(owners, settingOwner{
			label:       fmt.Sprintf("source %q", section.Name),
			prefix:      setting.EnvironmentPrefix(section.Name),
			definitions: providers[index].Settings,
		})
	}
	if err := checkEnvironmentCollisions(owners); err != nil {
		return nil, err
	}

	config := &Config{Storage: Storage{Info: storageInfo, Input: setting.Input{
		Label:             "storage",
		File:              file.Storage.Settings,
		EnvironmentPrefix: setting.StorageEnvironmentPrefix,
		LookupEnvironment: options.LookupEnvironment,
	}}}
	for index, section := range file.Sources {
		if options.Only != "" && section.Name != options.Only {
			continue
		}
		config.Sources = append(config.Sources, Source{Name: section.Name, Provider: providers[index], Input: setting.Input{
			Label:             fmt.Sprintf("source %q", section.Name),
			File:              section.Settings,
			EnvironmentPrefix: setting.EnvironmentPrefix(section.Name),
			LookupEnvironment: options.LookupEnvironment,
		}})
	}
	return config, nil
}

// FromArguments builds the config file of a quick backup. Each argument is a
// provider name, which backs up everything its credentials can access, or a
// URL whose host picks the provider. URLs of the same provider become the
// targets of one source, named after the provider. Backups go to local
// storage in outputDirectory.
func FromArguments(arguments []string, outputDirectory string) (*File, error) {
	if len(arguments) == 0 {
		return nil, errors.New("name a provider or a URL to back up")
	}
	// quickSource collects what the arguments say about one provider.
	type quickSource struct {
		name       string
		targets    []string
		everything bool
	}
	// sources keeps the order in which providers were named, so output and
	// backups follow the command line.
	var sources []*quickSource
	sourceFor := func(name string) *quickSource {
		for _, existing := range sources {
			if existing.name == name {
				return existing
			}
		}
		created := &quickSource{name: name}
		sources = append(sources, created)
		return created
	}

	var problems []error
	for _, argument := range arguments {
		// A provider name is checked first: "github" is never meant as a
		// relative URL.
		if _, found := provider.Lookup(argument); found {
			sourceFor(argument).everything = true
			continue
		}
		parsed, err := url.Parse(argument)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			problems = append(problems, fmt.Errorf(`%q is neither a provider nor an http(s) URL; run "flotsam providers" to list providers`, argument))
			continue
		}
		information, found := provider.LookupHost(parsed.Hostname())
		if !found {
			problems = append(problems, fmt.Errorf("no provider handles %s; known hosts: %s", parsed.Hostname(), hostNames()))
			continue
		}
		source := sourceFor(information.Name)
		source.targets = append(source.targets, argument)
	}

	path, err := json.Marshal(outputDirectory)
	if err != nil {
		return nil, err
	}
	file := &File{Storage: &StorageSection{Type: LocalStorage, Settings: map[string]json.RawMessage{localPathSetting: path}}}
	for _, source := range sources {
		// "Everything" plus some URLs is contradictory. Guessing either way
		// could back up far more or far less than intended.
		if source.everything && len(source.targets) > 0 {
			problems = append(problems, fmt.Errorf("%q backs up everything, so it can't be combined with URLs for the same provider", source.name))
			continue
		}
		section := SourceSection{Name: source.name, Provider: source.name}
		if len(source.targets) > 0 {
			targets, err := json.Marshal(source.targets)
			if err != nil {
				return nil, err
			}
			section.Settings = map[string]json.RawMessage{provider.TargetsSetting: targets}
		}
		file.Sources = append(file.Sources, section)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return file, nil
}

// settingOwner is the storage or one source, with the settings it reads.
type settingOwner struct {
	// label names the owner in error messages, such as `source "work"`.
	label       string
	prefix      string
	definitions []setting.Definition
}

// checkEnvironmentCollisions rejects configs where two settings read the same
// environment variable, such as source "work" with setting "github_token" and
// source "work-github" with setting "token". Without this check, one token
// would silently be used for two sources, and the user would only notice when
// one of them backs up the wrong account.
func checkEnvironmentCollisions(owners []settingOwner) error {
	users := make(map[string]string)
	var problems []error
	for _, owner := range owners {
		for _, definition := range owner.definitions {
			name := setting.EnvironmentName(owner.prefix, definition.Name)
			user := fmt.Sprintf("%s setting %q", owner.label, definition.Name)
			if existing, taken := users[name]; taken {
				problems = append(problems, fmt.Errorf("%s and %s would both read %s; rename one of the sources", existing, user, name))
				continue
			}
			users[name] = user
		}
	}
	return errors.Join(problems...)
}

// providerNames, storageNames and hostNames list what is built into this
// binary, so "unknown" errors say what would have worked.
func providerNames() string {
	var names []string
	for _, information := range provider.List() {
		names = append(names, information.Name)
	}
	return listOrNone(names)
}

// storageNames lists the built-in storage backends.
func storageNames() string {
	var names []string
	for _, information := range storage.List() {
		names = append(names, information.Name)
	}
	return listOrNone(names)
}

// hostNames lists the URL hosts that quick backups understand.
func hostNames() string {
	var hosts []string
	for _, information := range provider.List() {
		hosts = append(hosts, information.Hosts...)
	}
	return listOrNone(hosts)
}

// listOrNone joins names for error messages, with "none" for an empty list.
func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
