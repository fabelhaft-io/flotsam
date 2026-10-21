package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
)

const providersSummary = `Usage:
  flotsam providers [<name>]

List the providers built into this binary, or show the settings of one of them
and the environment variables that set them.

`

// providers runs `flotsam providers`. It is the user-facing documentation of
// each provider's settings, generated from the same struct tags that the
// settings are read with, so it is always in sync with the code that is
// actually running.
func (command *cli) providers(arguments []string) int {
	flags := command.newFlagSet("providers", providersSummary)
	positional, code, proceed := parse(flags, arguments)
	if !proceed {
		return code
	}
	switch len(positional) {
	case 0:
		command.listProviders()
		return exitSuccess
	case 1:
		return command.showProvider(positional[0])
	}
	return command.usageError(flags, "name at most one provider")
}

// listProviders prints every provider built into this binary. Custom builds
// may have more or fewer than the official release, so the list comes from
// the registry rather than from docs.
func (command *cli) listProviders() {
	providers := provider.List()
	if len(providers) == 0 {
		_, _ = fmt.Fprintln(command.stdout, "No providers are built into this binary.")
		return
	}
	table := tabwriter.NewWriter(command.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "NAME\tHOSTS\tSUMMARY")
	for _, information := range providers {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", information.Name, strings.Join(information.Hosts, ", "), information.Summary)
	}
	_ = table.Flush()
	_, _ = fmt.Fprintln(command.stdout, "\nRun \"flotsam providers <name>\" to see the settings of a provider.")
}

// showProvider prints one provider's settings with the environment variable
// for each. <SOURCE> stands for the source name, because the variable depends
// on what the user calls the source.
func (command *cli) showProvider(name string) int {
	information, found := provider.Lookup(name)
	if !found {
		return command.fail(fmt.Errorf("unknown provider %q; run \"flotsam providers\" to list them", name))
	}
	_, _ = fmt.Fprintf(command.stdout, "%s: %s\n", information.Name, information.Summary)
	if len(information.Hosts) > 0 {
		_, _ = fmt.Fprintf(command.stdout, "Hosts: %s\n", strings.Join(information.Hosts, ", "))
	}
	if len(information.Settings) == 0 {
		_, _ = fmt.Fprintln(command.stdout, "\nThis provider has no settings.")
		return exitSuccess
	}

	_, _ = fmt.Fprintln(command.stdout, "\nSettings:")
	table := tabwriter.NewWriter(command.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "  SETTING\tTYPE\tREQUIRED\tSECRET\tENVIRONMENT VARIABLE\tDESCRIPTION")
	for _, definition := range information.Settings {
		_, _ = fmt.Fprintf(table, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			definition.Name,
			definition.Type,
			yesOrEmpty(definition.Required),
			yesOrEmpty(definition.Secret),
			setting.EnvironmentName("FLOTSAM_<SOURCE>", definition.Name),
			definition.Help,
		)
	}
	_ = table.Flush()

	example := setting.EnvironmentName(setting.EnvironmentPrefix(information.Name), information.Settings[0].Name)
	_, _ = fmt.Fprintf(command.stdout, "\n<SOURCE> is the source name in upper case, with \"-\" replaced by \"_\".\n"+
		"Quick backups name the source after the provider, so they read %s and so on.\n"+
		"Secret settings can only be set with environment variables.\n", example)
	return exitSuccess
}

// yesOrEmpty prints booleans in tables as "yes" or nothing, which is easier
// to scan than columns of true and false.
func yesOrEmpty(value bool) string {
	if value {
		return "yes"
	}
	return ""
}
