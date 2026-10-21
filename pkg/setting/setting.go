// Package setting turns a tagged struct into a list of documented settings and
// fills it from the config file and environment variables.
//
// # Why a struct instead of reading values directly
//
// flotsam is extended by people who don't know its internals, and every
// provider needs the same things from its settings: values from the config
// file or the environment, validation, clear errors, docs for users, and
// secrets kept out of logs. If each provider read os.Getenv or parsed JSON on
// its own, each would do this slightly differently and some would get it
// wrong. Instead a provider only declares a struct, and this package does the
// rest the same way for everyone. The struct is the single definition: the
// same tags drive parsing, validation, `flotsam providers <name>` and the
// generated docs, so they can't drift apart.
//
// Providers and storage backends declare their settings like this:
//
//	type Config struct {
//		Token   string   `setting:"token,required,secret" help:"API token with read access"`
//		Targets []string `setting:"targets"               help:"URLs to back up (default: everything)"`
//	}
//
// Every field needs a setting tag with a lower_snake_case name, optionally
// followed by "required" and "secret", and a help tag. Supported field types
// are string, bool and []string. Only strings can be secret. More types can be
// added when a provider needs one.
//
// # Environment variables
//
// Every setting can also be set with FLOTSAM_<SOURCE>_<SETTING>, which wins
// over the config file. The source name, not the provider name, is part of the
// variable, so two sources of the same provider (two GitHub orgs with two
// tokens) don't fight over one variable. Secrets can only come from the
// environment, so nobody commits a token by putting it in the config file.
package setting

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Type is a setting's value type, as shown in docs and error messages.
type Type string

// The supported setting types. The list is deliberately short: every type
// needs a text form for environment variables and a JSON form for the config
// file, and both must give clear errors.
const (
	TypeString Type = "string"
	TypeBool   Type = "bool"
	// TypeList is a []string: a JSON array in the config file, and a
	// comma-separated value in an environment variable.
	TypeList Type = "list"
)

// StorageEnvironmentPrefix is the environment variable prefix for storage
// settings, so the storage "path" setting reads FLOTSAM_STORAGE_PATH. Because
// of it, no source may be named "storage".
const StorageEnvironmentPrefix = "FLOTSAM_STORAGE"

// Definition describes one setting of a settings struct. Definitions are what
// the rest of flotsam sees of a provider's settings: the CLI prints them, the
// config loader checks environment variable names against them.
type Definition struct {
	// Name is the key in the config file, such as "api_url".
	Name string
	// Help is the description shown by `flotsam providers <name>`.
	Help string
	Type Type
	// Required settings must have a non-empty value, from the file or the
	// environment.
	Required bool
	// Secret settings can only come from environment variables and are
	// removed from logs and error messages.
	Secret bool

	// fieldIndex is the struct field the setting is stored in. It is
	// unexported so that only this package writes to settings structs.
	fieldIndex int
}

var (
	// Setting names become environment variable suffixes, so they are limited
	// to characters that are valid there once upper-cased.
	settingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	// Source names appear in snapshot paths and environment variable names,
	// so they must be safe in both. Single hyphens map one-to-one to
	// underscores, which keeps the environment names readable.
	sourceNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	stringType        = reflect.TypeFor[string]()
)

// Describe returns the settings declared by the struct type settingsType, in
// field order. It returns an error for any mistake in the struct, so that
// provider.Register can reject a broken provider when the program starts
// instead of when a user first runs it.
func Describe(settingsType reflect.Type) ([]Definition, error) {
	if settingsType == nil || settingsType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("settings type %v must be a struct", settingsType)
	}
	definitions := make([]Definition, 0, settingsType.NumField())
	seen := make(map[string]bool)
	for index := range settingsType.NumField() {
		field := settingsType.Field(index)
		definition, err := describeField(field)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", settingsType.Name(), field.Name, err)
		}
		if seen[definition.Name] {
			return nil, fmt.Errorf("%s.%s: setting %q is declared twice", settingsType.Name(), field.Name, definition.Name)
		}
		seen[definition.Name] = true
		definition.fieldIndex = index
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

// describeField reads the tags of one field. Every field must be a setting:
// an untagged field would silently never be filled, which is almost always a
// forgotten tag rather than intent.
func describeField(field reflect.StructField) (Definition, error) {
	tag, tagged := field.Tag.Lookup("setting")
	switch {
	case !tagged:
		return Definition{}, errors.New(`missing "setting" tag`)
	case !field.IsExported():
		// reflect can't set unexported fields.
		return Definition{}, errors.New("settings fields must be exported")
	}

	name, options, _ := strings.Cut(tag, ",")
	if !settingNamePattern.MatchString(name) {
		return Definition{}, fmt.Errorf("setting name %q must be lower_snake_case", name)
	}
	definition := Definition{Name: name, Help: field.Tag.Get("help")}
	// Help is required because it is the only documentation users get for
	// the setting.
	if definition.Help == "" {
		return Definition{}, errors.New(`missing "help" tag`)
	}
	if options != "" {
		for option := range strings.SplitSeq(options, ",") {
			switch option {
			case "required":
				definition.Required = true
			case "secret":
				definition.Secret = true
			default:
				return Definition{}, fmt.Errorf("unknown setting option %q; use required or secret", option)
			}
		}
	}

	settingType, err := typeOf(field.Type)
	if err != nil {
		return Definition{}, err
	}
	definition.Type = settingType
	// Redaction works on strings, so a secret of any other type couldn't be
	// kept out of logs.
	if definition.Secret && settingType != TypeString {
		return Definition{}, errors.New("only string settings can be secret")
	}
	// A bool is always either true or false, so "required" would mean
	// nothing, or force users to write "false" explicitly.
	if definition.Required && settingType == TypeBool {
		return Definition{}, errors.New("a bool setting can't be required")
	}
	return definition, nil
}

// typeOf maps a Go field type to a setting type. Only exact []string is
// accepted for lists, because a slice of a named string type can't be set from
// a plain []string.
func typeOf(fieldType reflect.Type) (Type, error) {
	switch {
	case fieldType.Kind() == reflect.String:
		return TypeString, nil
	case fieldType.Kind() == reflect.Bool:
		return TypeBool, nil
	case fieldType.Kind() == reflect.Slice && fieldType.Elem() == stringType:
		return TypeList, nil
	}
	return "", fmt.Errorf("unsupported type %s; use string, bool or []string", fieldType)
}

// ValidateSourceName returns an error unless name is a valid source name:
// lowercase letters and digits, separated by single hyphens. Provider and
// storage names follow the same rule, because quick backups use the provider
// name as the source name.
func ValidateSourceName(name string) error {
	if !sourceNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must use only lowercase letters, digits and single hyphens, like \"work-github\"", name)
	}
	return nil
}

// EnvironmentPrefix returns the environment variable prefix for a source, for
// example FLOTSAM_WORK_GITHUB for "work-github".
func EnvironmentPrefix(sourceName string) string {
	return "FLOTSAM_" + strings.ToUpper(strings.ReplaceAll(sourceName, "-", "_"))
}

// EnvironmentName returns the environment variable for one setting, for
// example FLOTSAM_WORK_GITHUB_TOKEN. Two different source and setting pairs
// can produce the same name; the config loader checks for that.
func EnvironmentName(prefix, settingName string) string {
	return prefix + "_" + strings.ToUpper(settingName)
}
