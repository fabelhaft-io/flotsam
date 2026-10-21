package main

import (
	"context"
	"fmt"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/fabelhaft-io/flotsam/internal/config"
	"github.com/fabelhaft-io/flotsam/internal/engine"
	"github.com/fabelhaft-io/flotsam/pkg/storage"
)

const backupSummary = `Usage:
  flotsam backup <url|provider>... [-o <directory>]
  flotsam backup -c <config.json> [-s <source>]

Back up the given URLs, or everything a provider's token can access, without a
config file. Or back up the sources of a config file.

`

// defaultOutputDirectory is where quick backups go without -o: a relative
// directory, so trying flotsam never writes somewhere unexpected.
const defaultOutputDirectory = "backups"

// backup runs `flotsam backup`. There are two modes, a config file or a quick
// backup of URLs and provider names, and both build a config.Config and run it
// with the same engine, so they behave identically from there on.
func (command *cli) backup(ctx context.Context, arguments []string) int {
	flags := command.newFlagSet("backup", backupSummary)
	var configPath, sourceName, outputDirectory string
	flags.StringVar(&configPath, "config", "", "back up the sources in this config `file`")
	flags.StringVar(&configPath, "c", "", "shorthand for -config")
	flags.StringVar(&sourceName, "source", "", "with -config, back up only this `source`")
	flags.StringVar(&sourceName, "s", "", "shorthand for -source")
	flags.StringVar(&outputDirectory, "output", "", "for quick backups, write to this `directory` (default \"backups\")")
	flags.StringVar(&outputDirectory, "o", "", "shorthand for -output")
	positional, code, proceed := parse(flags, arguments)
	if !proceed {
		return code
	}

	// Combinations that mix the two modes are rejected instead of letting one
	// win, because either guess could back up something other than intended.
	var file *config.File
	var err error
	switch {
	case configPath != "" && len(positional) > 0:
		return command.usageError(flags, "use either -config or URLs and provider names, not both")
	case configPath != "" && outputDirectory != "":
		return command.usageError(flags, "-output is for quick backups; set the storage path in the config file instead")
	case configPath == "" && sourceName != "":
		return command.usageError(flags, "-source needs -config")
	case configPath != "":
		file, err = config.Load(configPath)
	case len(positional) > 0:
		if outputDirectory == "" {
			outputDirectory = defaultOutputDirectory
		}
		file, err = config.FromArguments(positional, outputDirectory)
	default:
		return command.usageError(flags, "name the URLs or providers to back up, or a config file with -config")
	}
	if err != nil {
		return command.fail(err)
	}
	configuration, err := file.Resolve(config.Options{LookupEnvironment: command.lookupEnvironment, Only: sourceName})
	if err != nil {
		return command.fail(err)
	}

	store, err := configuration.Storage.Info.New(configuration.Storage.Input)
	if err != nil {
		return command.fail(err)
	}
	runner := &engine.Engine{
		Storage: store,
		Logger:  slog.New(slog.NewTextHandler(command.stderr, nil)),
		Version: resolveVersion(),
		Now:     time.Now,
	}
	results, err := runner.Run(ctx, configuration.Sources)
	if err != nil {
		return command.fail(err)
	}
	return command.printResults(store, results)
}

// printResults prints one line per source and returns exitFailure unless
// every backup is complete. The summary goes to stdout and the logs to
// stderr, so a script can capture the summary without the log noise.
func (command *cli) printResults(store storage.Storage, results []engine.Result) int {
	code := exitSuccess
	table := tabwriter.NewWriter(command.stdout, 0, 0, 2, ' ', 0)
	for _, result := range results {
		location := result.Snapshot
		if locator, isLocator := store.(storage.Locator); isLocator {
			location = locator.Location(result.Snapshot)
		}
		var detail string
		switch result.Status {
		case engine.StatusComplete:
			detail = fmt.Sprintf("%d files in %s", result.Files, location)
		case engine.StatusPartial:
			detail = fmt.Sprintf("%d files in %s, %d warnings (see manifest.json)", result.Files, location, len(result.Warnings))
			code = exitFailure
		default:
			detail = result.Failure.Error()
			code = exitFailure
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", result.Source, result.Status, detail)
	}
	_ = table.Flush()
	return code
}
