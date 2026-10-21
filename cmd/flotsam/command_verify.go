package main

import (
	"fmt"
	"os"

	"github.com/fabelhaft-io/flotsam/internal/manifest"
)

const verifySummary = `Usage:
  flotsam verify <snapshot-directory>

Check every file of a snapshot against the sizes and checksums in its
manifest.json, and report files that are missing, changed or not listed.

`

// verify runs `flotsam verify`. A backup is only useful if it is intact when
// it is needed, and storage can silently damage files over the years. verify
// lets people check snapshots regularly, before a restore depends on them.
func (command *cli) verify(arguments []string) int {
	flags := command.newFlagSet("verify", verifySummary)
	positional, code, proceed := parse(flags, arguments)
	if !proceed {
		return code
	}
	if len(positional) != 1 {
		return command.usageError(flags, "name exactly one snapshot directory")
	}
	directory := positional[0]

	report, err := manifest.Verify(os.DirFS(directory))
	if err != nil {
		return command.fail(fmt.Errorf("verify %s: %w", directory, err))
	}
	if len(report.Problems) > 0 {
		_, _ = fmt.Fprintf(command.stdout, "%s has %d problem(s):\n", directory, len(report.Problems))
		for _, problem := range report.Problems {
			_, _ = fmt.Fprintln(command.stdout, "  "+problem)
		}
		return exitFailure
	}

	// A partial snapshot is intact but incomplete. verify exits 0 for it,
	// since the files are fine, but lists the warnings so nobody mistakes it
	// for a full backup.
	snapshot := report.Manifest
	_, _ = fmt.Fprintf(command.stdout, "OK: all %d files match the manifest (source %s, provider %s, status %s)\n",
		len(snapshot.Files), snapshot.Source, snapshot.Provider, snapshot.Status)
	if len(snapshot.Warnings) > 0 {
		_, _ = fmt.Fprintf(command.stdout, "The backup recorded %d warning(s):\n", len(snapshot.Warnings))
		for _, warning := range snapshot.Warnings {
			_, _ = fmt.Fprintln(command.stdout, "  "+warning)
		}
	}
	return exitSuccess
}
