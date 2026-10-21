package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

// errNotFound means git couldn't find the repository. For a wiki that means it
// was never created: GitHub reports has_wiki for every repository with the
// wiki turned on, whether it has pages or not.
var errNotFound = errors.New("repository not found")

// bundle mirrors the repository at cloneURL and writes it to path as a git
// bundle: one file holding every branch and tag, which `git clone` reads
// directly. A repository without any commits can't be bundled and is skipped.
//
// The clone goes to a temporary directory that is removed afterwards. The
// bundle is streamed into the snapshot, so large repositories never have to
// fit in memory.
func (github *GitHub) bundle(ctx context.Context, snapshot provider.Snapshot, cloneURL, path string) error {
	directory, err := os.MkdirTemp("", "flotsam-git-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()

	environment := github.gitEnvironment(cloneURL)
	// "--" keeps a URL that starts with "-" from being read as an option.
	if err := runGit(ctx, environment, nil, "clone", "--mirror", "--quiet", "--", cloneURL, directory); err != nil {
		return err
	}
	var references bytes.Buffer
	if err := runGit(ctx, environment, &references, "-C", directory, "for-each-ref", "--count=1"); err != nil {
		return err
	}
	if references.Len() == 0 {
		return nil
	}

	writer, err := snapshot.Create(ctx, path)
	if err != nil {
		return err
	}
	if err := runGit(ctx, environment, writer, "-C", directory, "bundle", "create", "--quiet", "-", "--all"); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

// gitEnvironment returns the environment for git commands on cloneURL.
//
// The token is passed as configuration in environment variables, never on the
// command line or in the URL, where it would show up in process lists and in
// the clone's config file. The header is limited to the host being cloned, so
// no other server git talks to sees it. GitHub accepts a token as the
// password of HTTP basic authentication.
//
// Prompts and credential helpers are turned off: flotsam runs unattended, and
// a helper such as Git Credential Manager could otherwise open a login window
// or use another account than the configured token.
func (github *GitHub) gitEnvironment(cloneURL string) []string {
	settings := [][2]string{{"credential.helper", ""}}
	if parsed, err := url.Parse(cloneURL); err == nil && parsed.Host != "" && github.token != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + github.token))
		settings = append(settings, [2]string{
			"http." + parsed.Scheme + "://" + parsed.Host + "/.extraHeader",
			"Authorization: Basic " + credentials,
		})
	}
	environment := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(settings)))
	for index, setting := range settings {
		environment = append(environment,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, setting[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, setting[1]),
		)
	}
	return environment
}

// runGit runs git with arguments, sending its output to stdout if that isn't
// nil. Errors include what git printed, which says what went wrong.
func runGit(ctx context.Context, environment []string, stdout io.Writer, arguments ...string) error {
	command := exec.CommandContext(ctx, "git", arguments...) //nolint:gosec // fixed git subcommands; no shell is involved
	command.Env = environment
	command.Stdout = stdout
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(message), "not found") {
			return fmt.Errorf("%w: %s", errNotFound, message)
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}
