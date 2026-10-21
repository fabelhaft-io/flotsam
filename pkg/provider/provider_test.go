package provider

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/setting"
)

// testSettings has a secret and targets, the two settings Register treats
// specially.
type testSettings struct {
	Token   string   `setting:"token,required,secret" help:"API token"`
	Targets []string `setting:"targets"               help:"URLs to back up"`
}

// testProvider keeps its settings so tests can check what New received.
type testProvider struct {
	configuration testSettings
}

// Backup implements Provider.
func (fake *testProvider) Backup(context.Context, Snapshot) error { return nil }

// newTestProvider is the New function of the test provider.
func newTestProvider(configuration testSettings, _ Env) (Provider, error) {
	return &testProvider{configuration: configuration}, nil
}

func TestRegisterAndNew(test *testing.T) {
	Register("provider-test", Spec[testSettings]{
		Summary: "Test provider",
		Hosts:   []string{"Provider-Test.Example"},
		New:     newTestProvider,
	})

	information, found := Lookup("provider-test")
	if !found {
		test.Fatal("Lookup() didn't find the registered provider")
	}
	if !information.SupportsTargets() {
		test.Error("SupportsTargets() = false, want true")
	}
	if byHost, found := LookupHost("provider-test.example"); !found || byHost.Name != "provider-test" {
		test.Errorf("LookupHost() = %q, %t", byHost.Name, found)
	}
	if !slices.ContainsFunc(List(), func(listed Info) bool { return listed.Name == "provider-test" }) {
		test.Error("List() doesn't contain the registered provider")
	}

	created, err := information.New(setting.Input{
		EnvironmentPrefix: setting.EnvironmentPrefix("provider-test"),
		LookupEnvironment: func(name string) (string, bool) {
			return "secret-token", name == "FLOTSAM_PROVIDER_TEST_TOKEN"
		},
	}, nil)
	if err != nil {
		test.Fatal(err)
	}
	if token := created.(*testProvider).configuration.Token; token != "secret-token" {
		test.Errorf("provider token = %q, want secret-token", token)
	}

	withoutToken := setting.Input{Label: `source "x"`, EnvironmentPrefix: setting.EnvironmentPrefix("provider-test")}
	_, err = information.New(withoutToken, nil)
	if err == nil || !strings.Contains(err.Error(), `source "x": missing required setting "token": set FLOTSAM_PROVIDER_TEST_TOKEN`) {
		test.Errorf("New() without token = %v, want a labeled error naming the environment variable", err)
	}
}

func TestRegisterPanics(test *testing.T) {
	Register("provider-duplicate", Spec[testSettings]{Summary: "x", Hosts: []string{"duplicate.example"}, New: newTestProvider})

	type noTargets struct {
		Token string `setting:"token" help:"x"`
	}
	type secretTargets struct {
		Targets string `setting:"targets,secret" help:"x"`
	}
	tests := []struct {
		name     string
		register func()
	}{
		{name: "duplicate name", register: func() {
			Register("provider-duplicate", Spec[testSettings]{Summary: "x", New: newTestProvider})
		}},
		{name: "duplicate host", register: func() {
			Register("provider-other", Spec[testSettings]{Summary: "x", Hosts: []string{"duplicate.example"}, New: newTestProvider})
		}},
		{name: "invalid name", register: func() {
			Register("Provider", Spec[testSettings]{Summary: "x", New: newTestProvider})
		}},
		{name: "reserved name", register: func() {
			Register("storage", Spec[testSettings]{Summary: "x", New: newTestProvider})
		}},
		{name: "missing summary", register: func() {
			Register("provider-no-summary", Spec[testSettings]{New: newTestProvider})
		}},
		{name: "missing New", register: func() {
			Register("provider-no-new", Spec[testSettings]{Summary: "x"})
		}},
		{name: "host with scheme", register: func() {
			Register("provider-bad-host", Spec[testSettings]{Summary: "x", Hosts: []string{"https://x.example"}, New: newTestProvider})
		}},
		{name: "hosts without targets", register: func() {
			Register("provider-no-targets", Spec[noTargets]{Summary: "x", Hosts: []string{"x.example"}, New: func(noTargets, Env) (Provider, error) { return nil, nil }})
		}},
		{name: "targets of the wrong type", register: func() {
			Register("provider-secret-targets", Spec[secretTargets]{Summary: "x", New: func(secretTargets, Env) (Provider, error) { return nil, nil }})
		}},
		{name: "invalid settings struct", register: func() {
			Register("provider-bad-settings", Spec[string]{Summary: "x", New: func(string, Env) (Provider, error) { return nil, nil }})
		}},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			defer func() {
				if recover() == nil {
					test.Error("Register() didn't panic")
				}
			}()
			testCase.register()
		})
	}
}
