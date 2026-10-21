package setting

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// exampleSettings uses every supported type and option.
type exampleSettings struct {
	Token   string   `setting:"token,required,secret" help:"API token"`
	Owner   string   `setting:"owner,required"        help:"Owner to back up"`
	Targets []string `setting:"targets"               help:"URLs to back up"`
	Forks   bool     `setting:"skip_forks"            help:"Skip forks"`
}

// environment returns a lookup function for a fixed set of variables, so
// tests don't read or change the real environment.
func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	}
}

// file turns Go values into the raw JSON values that a config file would
// hold.
func file(test *testing.T, values map[string]any) map[string]json.RawMessage {
	test.Helper()
	raw := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			test.Fatal(err)
		}
		raw[name] = data
	}
	return raw
}

func TestDescribe(test *testing.T) {
	definitions, err := Describe(reflect.TypeFor[exampleSettings]())
	if err != nil {
		test.Fatal(err)
	}
	var names []string
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	if want := []string{"token", "owner", "targets", "skip_forks"}; !slices.Equal(names, want) {
		test.Errorf("names = %v, want %v", names, want)
	}
	token := definitions[0]
	if !token.Required || !token.Secret || token.Type != TypeString || token.Help != "API token" {
		test.Errorf("token definition = %+v", token)
	}
	if definitions[2].Type != TypeList {
		test.Errorf("targets type = %s, want %s", definitions[2].Type, TypeList)
	}
}

func TestDescribeRejectsInvalidStructs(test *testing.T) {
	tests := []struct {
		name         string
		settingsType reflect.Type
	}{
		{name: "not a struct", settingsType: reflect.TypeFor[string]()},
		{name: "missing setting tag", settingsType: reflect.TypeFor[struct {
			Token string `help:"x"`
		}]()},
		{name: "missing help tag", settingsType: reflect.TypeFor[struct {
			Token string `setting:"token"`
		}]()},
		{name: "unexported field", settingsType: reflect.TypeFor[struct {
			token string `setting:"token" help:"x"`
		}]()},
		{name: "invalid name", settingsType: reflect.TypeFor[struct {
			Token string `setting:"apiToken" help:"x"`
		}]()},
		{name: "unknown option", settingsType: reflect.TypeFor[struct {
			Token string `setting:"token,optional" help:"x"`
		}]()},
		{name: "unsupported type", settingsType: reflect.TypeFor[struct {
			Limit int `setting:"limit" help:"x"`
		}]()},
		{name: "secret list", settingsType: reflect.TypeFor[struct {
			Tokens []string `setting:"tokens,secret" help:"x"`
		}]()},
		{name: "required bool", settingsType: reflect.TypeFor[struct {
			Enabled bool `setting:"enabled,required" help:"x"`
		}]()},
		{name: "duplicate name", settingsType: reflect.TypeFor[struct {
			First  string `setting:"name" help:"x"`
			Second string `setting:"name" help:"x"`
		}]()},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			if _, err := Describe(testCase.settingsType); err == nil {
				test.Error("Describe() succeeded, want an error")
			}
		})
	}
}

func TestFill(test *testing.T) {
	var settings exampleSettings
	err := Fill(&settings, Input{
		File: file(test, map[string]any{
			"owner":      "from-file",
			"targets":    []string{"https://github.com/fabelhaft-io"},
			"skip_forks": false,
		}),
		EnvironmentPrefix: "FLOTSAM_WORK_GITHUB",
		LookupEnvironment: environment(map[string]string{
			"FLOTSAM_WORK_GITHUB_TOKEN":      "secret-token",
			"FLOTSAM_WORK_GITHUB_SKIP_FORKS": "true",
		}),
	})
	if err != nil {
		test.Fatal(err)
	}
	want := exampleSettings{
		Token:   "secret-token",
		Owner:   "from-file",
		Targets: []string{"https://github.com/fabelhaft-io"},
		Forks:   true,
	}
	if !reflect.DeepEqual(settings, want) {
		test.Errorf("settings = %+v, want %+v", settings, want)
	}
}

func TestFillFromEnvironmentOnly(test *testing.T) {
	var settings exampleSettings
	err := Fill(&settings, Input{
		EnvironmentPrefix: "FLOTSAM_GITHUB",
		LookupEnvironment: environment(map[string]string{
			"FLOTSAM_GITHUB_TOKEN":   "secret-token",
			"FLOTSAM_GITHUB_OWNER":   "fabelhaft-io",
			"FLOTSAM_GITHUB_TARGETS": " https://github.com/a , ,https://github.com/b ",
		}),
	})
	if err != nil {
		test.Fatal(err)
	}
	if want := []string{"https://github.com/a", "https://github.com/b"}; !slices.Equal(settings.Targets, want) {
		test.Errorf("targets = %v, want %v", settings.Targets, want)
	}
}

func TestFillKeepsValuesWithAngleBrackets(test *testing.T) {
	settings := struct {
		Query string `setting:"jql" help:"x"`
	}{}
	query := "created >= -7d AND priority > Low AND votes < 3"
	err := Fill(&settings, Input{File: file(test, map[string]any{"jql": query})})
	if err != nil || settings.Query != query {
		test.Errorf("Fill() = %v, query = %q; want the query unchanged", err, settings.Query)
	}
}

func TestFillErrors(test *testing.T) {
	tests := []struct {
		name        string
		file        map[string]any
		environment map[string]string
		wantErrors  []string
	}{
		{
			name:       "missing required settings name their environment variables",
			wantErrors: []string{`missing required setting "token": set FLOTSAM_WORK_TOKEN`, `missing required setting "owner": set it in the config file or with FLOTSAM_WORK_OWNER`},
		},
		{
			name:        "secret in the config file",
			file:        map[string]any{"token": "ghp_leaked", "owner": "x"},
			environment: map[string]string{"FLOTSAM_WORK_TOKEN": "ghp_real"},
			wantErrors:  []string{`setting "token" is secret and can't be in the config file: remove it and set FLOTSAM_WORK_TOKEN instead`},
		},
		{
			name:        "unknown setting",
			file:        map[string]any{"owner": "x", "tokn": "x"},
			environment: map[string]string{"FLOTSAM_WORK_TOKEN": "t"},
			wantErrors:  []string{`unknown setting "tokn"; known settings: token, owner, targets, skip_forks`},
		},
		{
			name:        "wrong type in the file",
			file:        map[string]any{"owner": 42, "targets": "not-a-list"},
			environment: map[string]string{"FLOTSAM_WORK_TOKEN": "t"},
			wantErrors:  []string{`setting "owner" in the config file: expected a string`, `setting "targets" in the config file: expected a list of strings`},
		},
		{
			name:        "wrong type in the environment",
			file:        map[string]any{"owner": "x"},
			environment: map[string]string{"FLOTSAM_WORK_TOKEN": "t", "FLOTSAM_WORK_SKIP_FORKS": "maybe"},
			wantErrors:  []string{"FLOTSAM_WORK_SKIP_FORKS: expected true or false"},
		},
		{
			name:        "empty string counts as missing",
			file:        map[string]any{"owner": ""},
			environment: map[string]string{"FLOTSAM_WORK_TOKEN": ""},
			wantErrors:  []string{`missing required setting "token"`, `missing required setting "owner"`},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			var settings exampleSettings
			err := Fill(&settings, Input{
				Label:             `source "work"`,
				File:              file(test, testCase.file),
				EnvironmentPrefix: "FLOTSAM_WORK",
				LookupEnvironment: environment(testCase.environment),
			})
			if err == nil {
				test.Fatal("Fill() succeeded, want an error")
			}
			// Every problem carries the label, so each line says which source to fix.
			for _, want := range testCase.wantErrors {
				if !strings.Contains(err.Error(), `source "work": `+want) {
					test.Errorf("error = %q\nwant it to contain %q with its label", err, want)
				}
			}
			for _, secret := range []string{"ghp_leaked", "ghp_real"} {
				if strings.Contains(err.Error(), secret) {
					test.Errorf("error contains the secret value %q: %v", secret, err)
				}
			}
		})
	}
}

func TestFillTreatsNullAsUnset(test *testing.T) {
	settings := struct {
		Name string `setting:"name" help:"x"`
	}{}
	err := Fill(&settings, Input{File: map[string]json.RawMessage{"name": json.RawMessage("null")}})
	if err != nil || settings.Name != "" {
		test.Errorf("Fill() = %v, name = %q; want no error and an empty name", err, settings.Name)
	}
}

func TestFillRejectsNonPointer(test *testing.T) {
	if err := Fill(exampleSettings{}, Input{}); err == nil {
		test.Error("Fill() with a struct value succeeded, want an error")
	}
}

func TestSecretValues(test *testing.T) {
	definitions, err := Describe(reflect.TypeFor[exampleSettings]())
	if err != nil {
		test.Fatal(err)
	}
	input := Input{
		EnvironmentPrefix: "FLOTSAM_WORK",
		LookupEnvironment: environment(map[string]string{"FLOTSAM_WORK_TOKEN": "secret-token", "FLOTSAM_WORK_OWNER": "public"}),
	}
	if got := SecretValues(definitions, input); !slices.Equal(got, []string{"secret-token"}) {
		test.Errorf("SecretValues() = %v, want [secret-token]", got)
	}
	if got := SecretValues(definitions, Input{EnvironmentPrefix: "FLOTSAM_WORK"}); len(got) != 0 {
		test.Errorf("SecretValues() without environment = %v, want none", got)
	}
}

func TestNames(test *testing.T) {
	for _, name := range []string{"github", "work-github", "a1-b2"} {
		if err := ValidateSourceName(name); err != nil {
			test.Errorf("ValidateSourceName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "Work", "work_github", "-work", "work-", "work--github", "work github"} {
		if ValidateSourceName(name) == nil {
			test.Errorf("ValidateSourceName(%q) succeeded, want an error", name)
		}
	}
	if got := EnvironmentName(EnvironmentPrefix("work-github"), "api_url"); got != "FLOTSAM_WORK_GITHUB_API_URL" {
		test.Errorf("environment name = %q", got)
	}
}
