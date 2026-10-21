package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

const fakeToken = "fake-secret-token"

// fakeSettings has a required secret, so tests can check that secrets are
// required, read from the environment and never written anywhere.
type fakeSettings struct {
	Token   string   `setting:"token,required,secret" help:"Token for the fake service"`
	Targets []string `setting:"targets"               help:"URLs to back up"`
	Warn    bool     `setting:"warn"                  help:"Record a warning"`
}

// fakeProvider stands in for a real provider, so the whole command can be
// tested end to end without network access or a real service.
type fakeProvider struct {
	settings fakeSettings
}

// Backup writes one JSON file per target and one streamed file.
func (fake *fakeProvider) Backup(ctx context.Context, snapshot provider.Snapshot) error {
	for index, target := range fake.settings.Targets {
		item := map[string]any{"index": index, "target": target}
		if err := snapshot.WriteJSON(ctx, "items/"+string(rune('a'+index))+".json", item); err != nil {
			return err
		}
	}
	writer, err := snapshot.Create(ctx, "data.bin")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, "streamed data"); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if fake.settings.Warn {
		snapshot.Warnf("one item was skipped (token %s)", fake.settings.Token)
	}
	return nil
}

// init registers the fake provider for this test binary only.
func init() {
	provider.Register("fake", provider.Spec[fakeSettings]{
		Summary: "Fake service for tests",
		Hosts:   []string{"fake.example"},
		New: func(settings fakeSettings, _ provider.Env) (provider.Provider, error) {
			return &fakeProvider{settings: settings}, nil
		},
	})
}

// invocation is the outcome of one run of the command.
type invocation struct {
	code   int
	stdout string
	stderr string
}

// invoke runs the command with arguments and only the given environment
// variables, so tests don't depend on the developer's real environment.
func invoke(test *testing.T, environment map[string]string, arguments ...string) invocation {
	test.Helper()
	var stdout, stderr bytes.Buffer
	code := run(test.Context(), arguments, &stdout, &stderr, func(name string) (string, bool) {
		value, found := environment[name]
		return value, found
	})
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// onlySnapshot returns the single snapshot directory of source below directory.
func onlySnapshot(test *testing.T, directory, source string) string {
	test.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, source))
	if err != nil {
		test.Fatal(err)
	}
	if len(entries) != 1 {
		test.Fatalf("%d snapshots for %s, want 1", len(entries), source)
	}
	return filepath.Join(directory, source, entries[0].Name())
}

// assertNoSecret fails the test if the token shows up in the output or in any
// file below directory. It checks the promise that secrets are never written.
func assertNoSecret(test *testing.T, directory string, outputs ...invocation) {
	test.Helper()
	for _, output := range outputs {
		if strings.Contains(output.stdout+output.stderr, fakeToken) {
			test.Errorf("output contains the token:\n%s\n%s", output.stdout, output.stderr)
		}
	}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // test reads files it just wrote below a temporary directory
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(fakeToken)) {
			test.Errorf("%s contains the token", path)
		}
		return nil
	})
	if err != nil {
		test.Fatal(err)
	}
}

func TestQuickBackupThenVerify(test *testing.T) {
	directory := test.TempDir()
	environment := map[string]string{"FLOTSAM_FAKE_TOKEN": fakeToken}

	// Flags may come after URLs.
	backup := invoke(test, environment, "backup", "https://fake.example/one", "-o", directory, "https://fake.example/two")
	if backup.code != exitSuccess {
		test.Fatalf("backup exit code = %d\nstdout: %s\nstderr: %s", backup.code, backup.stdout, backup.stderr)
	}
	if !strings.Contains(backup.stdout, "fake") || !strings.Contains(backup.stdout, "complete") || !strings.Contains(backup.stdout, "3 files") {
		test.Errorf("backup output = %q, want a complete line with 3 files", backup.stdout)
	}

	snapshot := onlySnapshot(test, directory, "fake")
	verify := invoke(test, nil, "verify", snapshot)
	if verify.code != exitSuccess || !strings.Contains(verify.stdout, "OK: all 3 files match") {
		test.Errorf("verify = %d, %q, %q", verify.code, verify.stdout, verify.stderr)
	}
	assertNoSecret(test, directory, backup, verify)
}

func TestConfigBackupWithWarnings(test *testing.T) {
	directory := test.TempDir()
	configPath := filepath.Join(test.TempDir(), "flotsam.json")
	content := `{
		"storage": {"type": "local", "settings": {"path": "` + filepath.ToSlash(directory) + `"}},
		"sources": [{"name": "work-fake", "provider": "fake", "settings": {"targets": ["https://fake.example/one"], "warn": true}}]
	}`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		test.Fatal(err)
	}

	backup := invoke(test, map[string]string{"FLOTSAM_WORK_FAKE_TOKEN": fakeToken}, "backup", "-c", configPath, "-s", "work-fake")
	if backup.code != exitFailure || !strings.Contains(backup.stdout, "partial") {
		test.Fatalf("backup = %d, %q, %q; want exit 1 and a partial result", backup.code, backup.stdout, backup.stderr)
	}

	verify := invoke(test, nil, "verify", onlySnapshot(test, directory, "work-fake"))
	if verify.code != exitSuccess || !strings.Contains(verify.stdout, "recorded 1 warning") {
		test.Errorf("verify = %d, %q", verify.code, verify.stdout)
	}
	assertNoSecret(test, directory, backup, verify)
}

func TestMissingTokenFailsBeforeAnyBackup(test *testing.T) {
	directory := test.TempDir()
	backup := invoke(test, nil, "backup", "-o", directory, "https://fake.example/one")
	if backup.code != exitFailure {
		test.Errorf("exit code = %d, want %d", backup.code, exitFailure)
	}
	if !strings.Contains(backup.stderr, `missing required setting "token": set FLOTSAM_FAKE_TOKEN`) {
		test.Errorf("stderr = %q, want it to name FLOTSAM_FAKE_TOKEN", backup.stderr)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		test.Errorf("something was written: %v", entries)
	}
}

func TestVerifyDetectsChangedFiles(test *testing.T) {
	directory := test.TempDir()
	if backup := invoke(test, map[string]string{"FLOTSAM_FAKE_TOKEN": fakeToken}, "backup", "-o", directory, "fake"); backup.code != exitSuccess {
		test.Fatalf("backup = %d, %s", backup.code, backup.stderr)
	}
	snapshot := onlySnapshot(test, directory, "fake")
	if err := os.WriteFile(filepath.Join(snapshot, "data.bin"), []byte("STREAMED DATA"), 0o600); err != nil {
		test.Fatal(err)
	}
	verify := invoke(test, nil, "verify", snapshot)
	if verify.code != exitFailure || !strings.Contains(verify.stdout, "data.bin: checksum doesn't match") {
		test.Errorf("verify = %d, %q; want a checksum problem", verify.code, verify.stdout)
	}
}

func TestProvidersCommand(test *testing.T) {
	list := invoke(test, nil, "providers")
	if list.code != exitSuccess || !strings.Contains(list.stdout, "fake") || !strings.Contains(list.stdout, "fake.example") {
		test.Errorf("providers = %d, %q", list.code, list.stdout)
	}

	show := invoke(test, nil, "providers", "fake")
	for _, want := range []string{"Fake service for tests", "FLOTSAM_<SOURCE>_TOKEN", "FLOTSAM_FAKE_TOKEN", "Token for the fake service"} {
		if !strings.Contains(show.stdout, want) {
			test.Errorf("providers fake output doesn't contain %q:\n%s", want, show.stdout)
		}
	}

	if unknown := invoke(test, nil, "providers", "gitlab"); unknown.code != exitFailure {
		test.Errorf("providers gitlab exit code = %d, want %d", unknown.code, exitFailure)
	}
}

func TestUsage(test *testing.T) {
	tests := []struct {
		name       string
		arguments  []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", arguments: []string{"version"}, wantCode: exitSuccess, wantStdout: "flotsam "},
		{name: "version flag", arguments: []string{"--version"}, wantCode: exitSuccess, wantStdout: "flotsam "},
		{name: "help", arguments: []string{"help"}, wantCode: exitSuccess, wantStdout: "flotsam backs up"},
		{name: "no arguments", arguments: nil, wantCode: exitUsage, wantStderr: "flotsam backs up"},
		{name: "unknown command", arguments: []string{"restore"}, wantCode: exitUsage, wantStderr: `unknown command "restore"`},
		{name: "backup without anything", arguments: []string{"backup"}, wantCode: exitUsage, wantStderr: "name the URLs or providers"},
		{name: "backup with config and URLs", arguments: []string{"backup", "-c", "x.json", "fake"}, wantCode: exitUsage, wantStderr: "not both"},
		{name: "source without config", arguments: []string{"backup", "-s", "x", "fake"}, wantCode: exitUsage, wantStderr: "-source needs -config"},
		{name: "output with config", arguments: []string{"backup", "-c", "x.json", "-o", "dir"}, wantCode: exitUsage, wantStderr: "-output is for quick backups"},
		{name: "backup help", arguments: []string{"backup", "-h"}, wantCode: exitSuccess, wantStderr: "Usage:"},
		{name: "unknown flag", arguments: []string{"backup", "-x"}, wantCode: exitUsage, wantStderr: "flag provided but not defined"},
		{name: "verify without directory", arguments: []string{"verify"}, wantCode: exitUsage, wantStderr: "exactly one snapshot directory"},
		{name: "verify missing manifest", arguments: []string{"verify", "."}, wantCode: exitFailure, wantStderr: "no manifest.json"},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			output := invoke(test, nil, testCase.arguments...)
			if output.code != testCase.wantCode {
				test.Errorf("exit code = %d, want %d\nstderr: %s", output.code, testCase.wantCode, output.stderr)
			}
			if !strings.Contains(output.stdout, testCase.wantStdout) {
				test.Errorf("stdout = %q, want it to contain %q", output.stdout, testCase.wantStdout)
			}
			if !strings.Contains(output.stderr, testCase.wantStderr) {
				test.Errorf("stderr = %q, want it to contain %q", output.stderr, testCase.wantStderr)
			}
		})
	}
}

func TestResolveVersionPrefersReleaseVersion(test *testing.T) {
	previous := version
	test.Cleanup(func() { version = previous })

	version = "v1.2.3"
	if got := resolveVersion(); got != "v1.2.3" {
		test.Errorf("resolveVersion() = %q, want %q", got, "v1.2.3")
	}
}
