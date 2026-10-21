package setting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Input holds the raw values for one settings struct. It keeps the sources of
// values abstract, so the same Fill serves config files, quick backups (whose
// "file" values come from the command line) and tests.
type Input struct {
	// Label names the owner of the settings in error messages, such as
	// `source "work-github"` or "storage". With several sources, an error
	// without it wouldn't say which source to fix.
	Label string
	// File holds the values from the config file, by setting name. They stay
	// raw JSON until the setting's type is known.
	File map[string]json.RawMessage
	// EnvironmentPrefix is the result of EnvironmentPrefix for a source, or
	// StorageEnvironmentPrefix for storage.
	EnvironmentPrefix string
	// LookupEnvironment reads an environment variable; os.LookupEnv in
	// production. It is a function so tests don't have to change the real
	// environment, which is shared by all tests in a process. An empty value
	// counts as not set, because tools such as Docker Compose often define
	// variables that are left empty.
	LookupEnvironment func(name string) (string, bool)
}

// Fill sets the fields of the struct that target points to. Environment
// variables win over the config file, so the same file can be used in
// different environments, and secrets never have to be in it.
//
// Fill reports every problem at once rather than stopping at the first, so a
// user fixing a config doesn't have to run flotsam once per mistake. Each
// problem starts with input.Label. Errors never contain a setting's value: a
// value in the wrong place may be a secret, and error messages end up in logs
// and CI output.
func Fill(target any, input Input) error {
	pointer := reflect.ValueOf(target)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return errors.New("settings target must be a non-nil pointer to a struct")
	}
	structure := pointer.Elem()
	definitions, err := Describe(structure.Type())
	if err != nil {
		return err
	}

	var problems []error
	known := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		known = append(known, definition.Name)
	}
	// Unknown keys are an error, not ignored: a typo such as "tokn" would
	// otherwise leave the real setting empty without any hint why.
	for _, name := range slices.Sorted(maps.Keys(input.File)) {
		if !slices.Contains(known, name) {
			problems = append(problems, fmt.Errorf("unknown setting %q; known settings: %s", name, listOrNone(known)))
		}
	}
	for _, definition := range definitions {
		if err := fill(structure.Field(definition.fieldIndex), definition, input); err != nil {
			problems = append(problems, err)
		}
	}
	if input.Label != "" {
		for index, problem := range problems {
			problems[index] = fmt.Errorf("%s: %w", input.Label, problem)
		}
	}
	return errors.Join(problems...)
}

// fill sets one field and checks it. Errors name the environment variable,
// because that is what users most often need to set.
func fill(field reflect.Value, definition Definition, input Input) error {
	environmentName := EnvironmentName(input.EnvironmentPrefix, definition.Name)
	environmentValue, inEnvironment := lookup(input, environmentName)
	fileValue, inFile := input.File[definition.Name]
	// JSON null means "not set", so a template can list a setting without
	// giving it a value.
	if inFile && string(bytes.TrimSpace(fileValue)) == "null" {
		inFile = false
	}

	// Rejected even if the environment variable is set as well: a token in
	// the file has already leaked wherever the file is stored or committed,
	// and the user needs to know.
	if definition.Secret && inFile {
		return fmt.Errorf("setting %q is secret and can't be in the config file: remove it and set %s instead", definition.Name, environmentName)
	}
	switch {
	case inEnvironment:
		if err := setFromText(field, definition.Type, environmentValue); err != nil {
			return fmt.Errorf("%s: %w", environmentName, err)
		}
	case inFile:
		if err := setFromJSON(field, definition.Type, fileValue); err != nil {
			return fmt.Errorf("setting %q in the config file: %w", definition.Name, err)
		}
	}

	if definition.Required && isEmpty(field) {
		// Secrets can only come from the environment, so only that is
		// suggested for them.
		if definition.Secret {
			return fmt.Errorf("missing required setting %q: set %s", definition.Name, environmentName)
		}
		return fmt.Errorf("missing required setting %q: set it in the config file or with %s", definition.Name, environmentName)
	}
	return nil
}

// lookup reads one environment variable through input.LookupEnvironment.
func lookup(input Input, name string) (string, bool) {
	if input.LookupEnvironment == nil {
		return "", false
	}
	value, found := input.LookupEnvironment(name)
	return value, found && value != ""
}

// setFromText parses an environment variable value. Errors describe the
// expected form without echoing the value, which could be a secret.
func setFromText(field reflect.Value, settingType Type, text string) error {
	switch settingType {
	case TypeString:
		// Strings are kept exactly, including spaces, since tokens and paths
		// can contain them.
		field.SetString(text)
	case TypeBool:
		value, err := strconv.ParseBool(strings.TrimSpace(text))
		if err != nil {
			return errors.New("expected true or false")
		}
		field.SetBool(value)
	case TypeList:
		// Comma-separated, because environment variables can't hold arrays.
		// Empty items are dropped so "a,,b" and a trailing comma work.
		var items []string
		for item := range strings.SplitSeq(text, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		field.Set(reflect.ValueOf(items))
	}
	return nil
}

// setFromJSON parses a config file value. It is strict about types (a number
// for a string setting is an error, not converted), so mistakes in the file
// show up instead of being guessed at. The json errors are replaced by our own
// messages, which are clearer and can't contain the value.
func setFromJSON(field reflect.Value, settingType Type, raw json.RawMessage) error {
	switch settingType {
	case TypeString:
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("expected a string")
		}
		field.SetString(value)
	case TypeBool:
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("expected true or false")
		}
		field.SetBool(value)
	case TypeList:
		var items []string
		if json.Unmarshal(raw, &items) != nil {
			return errors.New("expected a list of strings")
		}
		field.Set(reflect.ValueOf(items))
	}
	return nil
}

// isEmpty reports whether a field has no usable value. An empty list counts
// as empty even if it isn't nil, since "targets": [] means "no targets".
func isEmpty(field reflect.Value) bool {
	if field.Kind() == reflect.Slice {
		return field.Len() == 0
	}
	return field.IsZero()
}

// SecretValues returns the values of the secret settings in definitions, read
// from the environment of input. The engine feeds them to the redactor, which
// removes them from everything flotsam logs or writes, as a safety net for
// providers that accidentally include a token in an error message.
//
// Secrets can only come from the environment, so reading them from there
// gives the same values Fill would, without needing the settings struct.
func SecretValues(definitions []Definition, input Input) []string {
	var secrets []string
	for _, definition := range definitions {
		if !definition.Secret {
			continue
		}
		if value, found := lookup(input, EnvironmentName(input.EnvironmentPrefix, definition.Name)); found {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// listOrNone joins names for error messages, so an empty list reads as "none"
// instead of nothing.
func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
