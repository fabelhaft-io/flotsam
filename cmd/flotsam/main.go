// Command flotsam backs up SaaS data to storage you control.
//
// The command only parses the command line and prints results; everything
// else lives in packages, so it can be tested without running a binary. Each
// command is in its own command_<name>.go file. Which providers and storage
// backends exist is decided by the blank imports in providers.go and
// storage.go, and nowhere else.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3". It
// is recorded in every manifest, so a snapshot always says which flotsam
// wrote it.
var version = ""

// Exit codes. Scripts and schedulers only see the exit code, so a backup that
// skipped data must not exit 0: otherwise a cron job would report success for
// months while a repository silently goes missing from every backup.
const (
	exitSuccess = 0
	// exitFailure means a backup failed or was partial, a snapshot didn't
	// verify, or the configuration is invalid.
	exitFailure = 1
	// exitUsage means the command line was wrong.
	exitUsage = 2
)

const usage = `flotsam backs up SaaS data to storage you control.

Usage:
  flotsam backup <url|provider>... [-o <directory>]   quick backup, no config file
  flotsam backup -c <config.json> [-s <source>]       back up the configured sources
  flotsam providers [<name>]                           list providers, or show one's settings
  flotsam verify <snapshot-directory>                  check a snapshot against its manifest
  flotsam version                                      print the version

Run "flotsam <command> -h" for the options of a command.
`

// main wires run to the real process: arguments, output, environment and
// signals.
func main() {
	// Ctrl-C and the SIGTERM that schedulers and containers send cancel the
	// context, so a backup stops cleanly: open files are discarded and no
	// manifest is written for the unfinished snapshot.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv)
	stop()
	os.Exit(code)
}

// cli holds what every command writes to and reads from, so tests can
// replace it.
type cli struct {
	stdout            io.Writer
	stderr            io.Writer
	lookupEnvironment func(name string) (string, bool)
}

// run executes one command and returns its exit code. main is the only
// caller outside tests; tests call run directly with their own output buffers
// and environment, which is much faster than building and starting the binary.
func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, lookupEnvironment func(string) (string, bool)) int {
	command := &cli{stdout: stdout, stderr: stderr, lookupEnvironment: lookupEnvironment}
	if len(arguments) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}
	name, rest := arguments[0], arguments[1:]
	switch name {
	case "backup":
		return command.backup(ctx, rest)
	case "providers":
		return command.providers(rest)
	case "verify":
		return command.verify(rest)
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(stdout, "flotsam", resolveVersion())
		return exitSuccess
	case "help", "-h", "-help", "--help":
		_, _ = io.WriteString(stdout, usage)
		return exitSuccess
	}
	_, _ = fmt.Fprintf(stderr, "flotsam: unknown command %q\n\n%s", name, usage)
	return exitUsage
}

// newFlagSet returns a flag set for a command that prints summary and the
// flags as its help. ContinueOnError makes the flag package return errors
// instead of calling os.Exit, so tests can check them and run decides the exit
// code.
func (command *cli) newFlagSet(name, summary string) *flag.FlagSet {
	flags := flag.NewFlagSet("flotsam "+name, flag.ContinueOnError)
	flags.SetOutput(command.stderr)
	flags.Usage = func() {
		_, _ = io.WriteString(command.stderr, summary)
		flags.PrintDefaults()
	}
	return flags
}

// parse parses flags that may appear before, between and after positional
// arguments, which the flag package alone doesn't allow: it stops at the first
// positional argument. People naturally type
// "flotsam backup https://github.com/x -o /mnt/backups", and silently treating
// -o as a URL would be a nasty surprise. It returns the positional arguments,
// or an exit code if parsing should stop.
func parse(flags *flag.FlagSet, arguments []string) ([]string, int, bool) {
	var positional []string
	for {
		if err := flags.Parse(arguments); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitSuccess, false
			}
			return nil, exitUsage, false
		}
		arguments = flags.Args()
		if len(arguments) == 0 {
			return positional, 0, true
		}
		positional = append(positional, arguments[0])
		arguments = arguments[1:]
	}
}

// fail prints every line of err and returns exitFailure. Each line gets the
// "flotsam:" prefix, so joined errors (one problem per line) stay readable
// and can be found with grep in a log.
func (command *cli) fail(err error) int {
	for line := range strings.SplitSeq(err.Error(), "\n") {
		_, _ = fmt.Fprintln(command.stderr, "flotsam:", line)
	}
	return exitFailure
}

// usageError prints message and the command's help, and returns exitUsage,
// which tells scripts the command line is wrong rather than the backup.
func (command *cli) usageError(flags *flag.FlagSet, message string) int {
	_, _ = fmt.Fprintln(command.stderr, "flotsam:", message)
	flags.Usage()
	return exitUsage
}

// resolveVersion returns the version set at release time, then the module
// version recorded by `go install ...@vX.Y.Z` or a build from a git checkout,
// and "dev" if neither is known. The fallbacks mean people who install with go
// install, rather than downloading a release, still get a meaningful version
// in their manifests and bug reports.
func resolveVersion() string {
	if version != "" {
		return version
	}
	buildInformation, ok := debug.ReadBuildInfo()
	if ok && buildInformation.Main.Version != "" && buildInformation.Main.Version != "(devel)" {
		return buildInformation.Main.Version
	}
	return "dev"
}
