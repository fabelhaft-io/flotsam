package config_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabelhaft-io/flotsam/internal/config"
	"github.com/fabelhaft-io/flotsam/pkg/provider"
	_ "github.com/fabelhaft-io/flotsam/pkg/storage/local"
)

// fakeSettings belongs to a provider with hosts and targets, for quick
// backups.
type fakeSettings struct {
	Token   string   `setting:"token,required,secret" help:"Token"`
	Targets []string `setting:"targets"               help:"URLs to back up"`
}

// colliding belongs to a provider whose setting name, combined with source
// "work", gives the same environment variable as source "work-github"
// setting "token".
type colliding struct {
	GithubToken string `setting:"github_token" help:"Collides with work-github's token"`
}

// fakeProvider does nothing; config tests only need providers to exist.
type fakeProvider struct{}

// Backup implements provider.Provider.
func (fakeProvider) Backup(context.Context, provider.Snapshot) error { return nil }

// init registers the test providers for this test binary only.
func init() {
	provider.Register("fake", provider.Spec[fakeSettings]{
		Summary: "Fake service",
		Hosts:   []string{"fake.example"},
		New:     func(fakeSettings, provider.Env) (provider.Provider, error) { return fakeProvider{}, nil },
	})
	provider.Register("plain", provider.Spec[colliding]{
		Summary: "Provider without targets",
		New:     func(colliding, provider.Env) (provider.Provider, error) { return fakeProvider{}, nil },
	})
}

// resolve parses and resolves a config file given as a string.
func resolve(test *testing.T, content, only string) (*config.Config, error) {
	test.Helper()
	file, err := config.Parse([]byte(content), "")
	if err != nil {
		return nil, err
	}
	return file.Resolve(config.Options{Only: only})
}

// storagePath returns the local storage path a config resolved to.
func storagePath(test *testing.T, configuration *config.Config) string {
	test.Helper()
	var path string
	if err := json.Unmarshal(configuration.Storage.Input.File["path"], &path); err != nil {
		test.Fatal(err)
	}
	return path
}

func TestResolve(test *testing.T) {
	configuration, err := resolve(test, `{
		"storage": {"type": "local", "settings": {"path": "backups"}},
		"sources": [
			{"name": "work-fake", "provider": "fake", "settings": {"targets": ["https://fake.example/a"]}},
			{"name": "other-fake", "provider": "fake"}
		]
	}`, "")
	if err != nil {
		test.Fatal(err)
	}
	if len(configuration.Sources) != 2 || configuration.Sources[0].Name != "work-fake" || configuration.Sources[1].Provider.Name != "fake" {
		test.Errorf("sources = %+v", configuration.Sources)
	}
	other := configuration.Sources[1].Input
	if other.Label != `source "other-fake"` || other.EnvironmentPrefix != "FLOTSAM_OTHER_FAKE" {
		test.Errorf("input of other-fake = %+v", other)
	}
	if configuration.Storage.Info.Name != "local" || configuration.Storage.Input.Label != "storage" {
		test.Errorf("storage = %+v", configuration.Storage)
	}
}

func TestRelativeStoragePathIsRelativeToTheConfigFile(test *testing.T) {
	directory := filepath.Join("etc", "flotsam")
	file, err := config.Parse([]byte(`{"storage": {"type": "local", "settings": {"path": "backups"}}, "sources": [{"name": "x", "provider": "fake"}]}`), directory)
	if err != nil {
		test.Fatal(err)
	}
	configuration, err := file.Resolve(config.Options{})
	if err != nil {
		test.Fatal(err)
	}
	if got, want := storagePath(test, configuration), filepath.Join(directory, "backups"); got != want {
		test.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveOnlyReturnsTheSelectedSource(test *testing.T) {
	configuration, err := resolve(test, `{
		"storage": {"type": "local", "settings": {"path": "backups"}},
		"sources": [
			{"name": "broken", "provider": "fake"},
			{"name": "working", "provider": "fake"}
		]
	}`, "working")
	if err != nil {
		test.Fatal(err)
	}
	if len(configuration.Sources) != 1 || configuration.Sources[0].Name != "working" {
		test.Errorf("sources = %+v, want only working", configuration.Sources)
	}
}

func TestResolveErrors(test *testing.T) {
	tests := []struct {
		name       string
		content    string
		only       string
		wantErrors []string
	}{
		{
			name:       "unknown field",
			content:    `{"storage": {"type": "local"}, "sources": [], "extra": 1}`,
			wantErrors: []string{`unknown field "extra"`},
		},
		{
			name:       "trailing data",
			content:    `{"storage": {"type": "local"}, "sources": []} {}`,
			wantErrors: []string{"unexpected data"},
		},
		{
			name:       "missing everything",
			content:    `{}`,
			wantErrors: []string{`missing "storage" section`, `no sources in "sources"`},
		},
		{
			name:       "unknown storage and provider",
			content:    `{"storage": {"type": "tape"}, "sources": [{"name": "x", "provider": "nope"}]}`,
			wantErrors: []string{`storage: unknown type "tape"; available: local`, `source "x": unknown provider "nope"; available: fake, plain`},
		},
		{
			name: "invalid, reserved and duplicate names",
			content: `{"storage": {"type": "local", "settings": {"path": "b"}}, "sources": [
				{"name": "Work_GitHub", "provider": "fake"},
				{"name": "storage", "provider": "fake"},
				{"name": "twice", "provider": "fake"},
				{"name": "twice", "provider": "fake"},
				{"provider": "fake"}
			]}`,
			wantErrors: []string{`source "Work_GitHub": name "Work_GitHub" must use only lowercase`, `source "storage": the name "storage" is reserved`, `source "twice": the name is used twice`, `source 5: missing "name"`},
		},
		{
			name:       "unknown selected source",
			content:    `{"storage": {"type": "local", "settings": {"path": "b"}}, "sources": [{"name": "a", "provider": "fake"}]}`,
			only:       "b",
			wantErrors: []string{`no source named "b"`},
		},
		{
			name: "environment variable collision",
			content: `{"storage": {"type": "local", "settings": {"path": "b"}}, "sources": [
				{"name": "work-github", "provider": "fake"},
				{"name": "work", "provider": "plain"}
			]}`,
			wantErrors: []string{`source "work-github" setting "token" and source "work" setting "github_token" would both read FLOTSAM_WORK_GITHUB_TOKEN`},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			_, err := resolve(test, testCase.content, testCase.only)
			if err == nil {
				test.Fatal("Resolve() succeeded, want an error")
			}
			for _, want := range testCase.wantErrors {
				if !strings.Contains(err.Error(), want) {
					test.Errorf("error = %q\nwant it to contain %q", err, want)
				}
			}
		})
	}
}

func TestFromArguments(test *testing.T) {
	file, err := config.FromArguments([]string{"https://fake.example/a", "plain", "https://FAKE.example/b"}, "out")
	if err != nil {
		test.Fatal(err)
	}
	// The quick backup goes through the same Resolve as a config file.
	configuration, err := file.Resolve(config.Options{})
	if err != nil {
		test.Fatal(err)
	}
	if len(configuration.Sources) != 2 || configuration.Sources[0].Name != "fake" || configuration.Sources[1].Name != "plain" {
		test.Fatalf("sources = %+v, want fake and plain", configuration.Sources)
	}
	var targets []string
	if err := json.Unmarshal(configuration.Sources[0].Input.File["targets"], &targets); err != nil {
		test.Fatal(err)
	}
	if len(targets) != 2 || targets[1] != "https://FAKE.example/b" {
		test.Errorf("targets = %v, want both fake.example URLs", targets)
	}
	if configuration.Sources[1].Input.File != nil {
		test.Errorf("plain got settings %v, want none (back up everything)", configuration.Sources[1].Input.File)
	}
	if configuration.Storage.Info.Name != config.LocalStorage || storagePath(test, configuration) != "out" {
		test.Errorf("storage = %+v", configuration.Storage)
	}
}

func TestFromArgumentsErrors(test *testing.T) {
	tests := []struct {
		name       string
		arguments  []string
		wantErrors []string
	}{
		{name: "nothing", wantErrors: []string{"name a provider or a URL"}},
		{name: "unknown word", arguments: []string{"gitlab"}, wantErrors: []string{`"gitlab" is neither a provider nor an http(s) URL`}},
		{name: "unknown host", arguments: []string{"https://unknown.example/x"}, wantErrors: []string{"no provider handles unknown.example; known hosts: fake.example"}},
		{name: "everything and URLs", arguments: []string{"fake", "https://fake.example/a"}, wantErrors: []string{`"fake" backs up everything, so it can't be combined with URLs`}},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			_, err := config.FromArguments(testCase.arguments, "out")
			if err == nil {
				test.Fatal("FromArguments() succeeded, want an error")
			}
			for _, want := range testCase.wantErrors {
				if !strings.Contains(err.Error(), want) {
					test.Errorf("error = %q\nwant it to contain %q", err, want)
				}
			}
		})
	}
}
