package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fabelhaft-io/flotsam/internal/config"
	"github.com/fabelhaft-io/flotsam/internal/manifest"
	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
	"github.com/fabelhaft-io/flotsam/pkg/storage/local"
)

const secretToken = "very-secret-token"

// testSettings choose what the test provider does with Mode, so one provider
// covers every success and failure case.
type testSettings struct {
	Token string `setting:"token,required,secret" help:"Token"`
	Mode  string `setting:"mode"                  help:"What the test provider does"`
}

// testProvider behaves according to its Mode setting.
type testProvider struct {
	settings testSettings
}

// Backup does what Mode says: fail, panic, leave a file open, write the same
// path twice, write paths differing only in case, write manifest.json, or
// (by default) write two files and optionally warn.
func (fake *testProvider) Backup(ctx context.Context, snapshot provider.Snapshot) error {
	switch fake.settings.Mode {
	case "fail":
		return fmt.Errorf("request with %s failed", fake.settings.Token)
	case "panic":
		panic("provider bug")
	case "leave-open":
		_, err := snapshot.Create(ctx, "never-closed.bin")
		return err
	case "duplicate":
		if err := snapshot.WriteJSON(ctx, "item.json", 1); err != nil {
			return err
		}
		return snapshot.WriteJSON(ctx, "item.json", 2)
	case "case":
		if err := snapshot.WriteJSON(ctx, "Item.json", 1); err != nil {
			return err
		}
		return snapshot.WriteJSON(ctx, "item.json", 2)
	case "manifest":
		return snapshot.WriteJSON(ctx, manifest.FileName, "{}")
	}

	if err := snapshot.WriteJSON(ctx, "repository.json", map[string]string{"html": "<b>&</b>"}); err != nil {
		return err
	}
	writer, err := snapshot.Create(ctx, "nested/git.bundle")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, "bundle data"); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if fake.settings.Mode == "warn" {
		snapshot.Warnf("skipped one item that needed %s", fake.settings.Token)
	}
	return nil
}

// init registers the test provider. Mode "invalid" makes New fail, to test
// that the engine stops before any backup.
func init() {
	provider.Register("engine-test", provider.Spec[testSettings]{
		Summary: "Test provider",
		New: func(settings testSettings, _ provider.Env) (provider.Provider, error) {
			if settings.Mode == "invalid" {
				return nil, fmt.Errorf("token %s doesn't fit mode invalid", settings.Token)
			}
			return &testProvider{settings: settings}, nil
		},
	})
}

// source configures the test provider as source name with the given mode,
// through the real settings code.
func source(name, mode string) config.Source {
	information, _ := provider.Lookup("engine-test")
	return config.Source{Name: name, Provider: information, Input: setting.Input{
		Label:             fmt.Sprintf("source %q", name),
		EnvironmentPrefix: setting.EnvironmentPrefix(name),
		LookupEnvironment: func(variable string) (string, bool) {
			switch variable {
			case setting.EnvironmentPrefix(name) + "_TOKEN":
				return secretToken, true
			case setting.EnvironmentPrefix(name) + "_MODE":
				return mode, true
			}
			return "", false
		},
	}}
}

// testEngine is an Engine writing to a temporary directory, with a clock that
// moves one second per call so every snapshot gets a distinct name.
type testEngine struct {
	*Engine
	directory string
	logs      *bytes.Buffer
}

// newTestEngine returns a testEngine whose logs are captured, so tests can
// check them for secrets.
func newTestEngine(test *testing.T) testEngine {
	test.Helper()
	directory := test.TempDir()
	store, err := local.New(directory)
	if err != nil {
		test.Fatal(err)
	}
	logs := &bytes.Buffer{}
	clock := time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC)
	return testEngine{
		Engine: &Engine{
			Storage: store,
			Logger:  slog.New(slog.NewTextHandler(logs, nil)),
			Version: "v0.0.0-test",
			Now: func() time.Time {
				clock = clock.Add(time.Second)
				return clock
			},
		},
		directory: directory,
		logs:      logs,
	}
}

// run runs sources and fails the test if Run returns an error.
func (engine testEngine) run(test *testing.T, sources ...config.Source) []Result {
	test.Helper()
	results, err := engine.Run(test.Context(), sources)
	if err != nil {
		test.Fatal(err)
	}
	if len(results) != len(sources) {
		test.Fatalf("got %d results for %d sources", len(results), len(sources))
	}
	return results
}

// snapshotDirectory returns the local directory of a result's snapshot.
func (engine testEngine) snapshotDirectory(result Result) string {
	return filepath.Join(engine.directory, filepath.FromSlash(result.Snapshot))
}

func TestCompleteBackup(test *testing.T) {
	engine := newTestEngine(test)
	result := engine.run(test, source("work-test", "complete"))[0]

	if result.Status != StatusComplete || result.Failure != nil || result.Files != 2 {
		test.Fatalf("result = %+v, want complete with 2 files", result)
	}
	if result.Snapshot != "work-test/2026-10-21T19-00-01Z" {
		test.Errorf("snapshot = %q", result.Snapshot)
	}
	report, err := manifest.Verify(os.DirFS(engine.snapshotDirectory(result)))
	if err != nil {
		test.Fatal(err)
	}
	if len(report.Problems) > 0 {
		test.Errorf("verify problems: %v", report.Problems)
	}
	written := report.Manifest
	if written.Source != "work-test" || written.Provider != "engine-test" || written.FlotsamVersion != "v0.0.0-test" || written.Status != manifest.StatusComplete {
		test.Errorf("manifest = %+v", written)
	}
	if !written.FinishedAt.After(written.StartedAt) {
		test.Errorf("finished %v is not after started %v", written.FinishedAt, written.StartedAt)
	}

	data, err := os.ReadFile(filepath.Join(engine.snapshotDirectory(result), "repository.json"))
	if err != nil {
		test.Fatal(err)
	}
	if want := "{\n  \"html\": \"<b>&</b>\"\n}\n"; string(data) != want {
		test.Errorf("repository.json = %q, want %q", data, want)
	}
}

func TestPartialBackupRedactsWarnings(test *testing.T) {
	engine := newTestEngine(test)
	result := engine.run(test, source("work-test", "warn"))[0]

	if result.Status != StatusPartial || len(result.Warnings) != 1 {
		test.Fatalf("result = %+v, want partial with one warning", result)
	}
	if strings.Contains(result.Warnings[0], secretToken) {
		test.Errorf("warning contains the token: %q", result.Warnings[0])
	}
	report, err := manifest.Verify(os.DirFS(engine.snapshotDirectory(result)))
	if err != nil {
		test.Fatal(err)
	}
	if report.Manifest.Status != manifest.StatusPartial || len(report.Manifest.Warnings) != 1 {
		test.Errorf("manifest = %+v, want partial with one warning", report.Manifest)
	}
	assertNoSecret(test, engine)
}

func TestFailedBackups(test *testing.T) {
	tests := []struct {
		mode        string
		wantFailure string
	}{
		{mode: "fail", wantFailure: "request with [redacted] failed"},
		{mode: "panic", wantFailure: "the provider panicked: provider bug"},
		{mode: "leave-open", wantFailure: "left 1 file(s) open"},
		{mode: "duplicate", wantFailure: "item.json was already written in this snapshot"},
		{mode: "case", wantFailure: "item.json differs from Item.json only in upper and lower case"},
		{mode: "manifest", wantFailure: "manifest.json is reserved for the manifest"},
	}
	for _, testCase := range tests {
		test.Run(testCase.mode, func(test *testing.T) {
			engine := newTestEngine(test)
			result := engine.run(test, source("work-test", testCase.mode))[0]

			if result.Status != StatusFailed || result.Failure == nil {
				test.Fatalf("result = %+v, want failed", result)
			}
			if !strings.Contains(result.Failure.Error(), testCase.wantFailure) {
				test.Errorf("failure = %q, want it to contain %q", result.Failure, testCase.wantFailure)
			}
			manifestPath := filepath.Join(engine.snapshotDirectory(result), manifest.FileName)
			if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
				test.Errorf("a failed backup wrote a manifest (stat: %v)", err)
			}
			assertNoSecret(test, engine)
		})
	}
}

func TestOneFailureDoesNotStopOtherSources(test *testing.T) {
	engine := newTestEngine(test)
	results := engine.run(test, source("broken", "panic"), source("working", "complete"))
	if results[0].Status != StatusFailed || results[1].Status != StatusComplete {
		test.Errorf("statuses = %s, %s; want failed, complete", results[0].Status, results[1].Status)
	}
}

func TestProviderErrorsStopEverythingBeforeTheFirstBackup(test *testing.T) {
	engine := newTestEngine(test)
	results, err := engine.Run(test.Context(), []config.Source{source("working", "complete"), source("broken", "invalid")})
	if err == nil {
		test.Fatalf("Run() = %+v, want an error", results)
	}
	if !strings.Contains(err.Error(), `source "broken": token [redacted] doesn't fit`) {
		test.Errorf("error = %q", err)
	}
	entries, _ := os.ReadDir(engine.directory)
	if len(entries) != 0 {
		test.Errorf("something was backed up before the error: %v", entries)
	}
}

func TestExistingSnapshotIsNotOverwritten(test *testing.T) {
	engine := newTestEngine(test)
	engine.Now = func() time.Time { return time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC) }
	first := engine.run(test, source("work-test", "complete"))[0]
	second := engine.run(test, source("work-test", "complete"))[0]
	if first.Status != StatusComplete {
		test.Fatalf("first backup = %+v", first)
	}
	if second.Status != StatusFailed || !strings.Contains(second.Failure.Error(), "already exists") {
		test.Errorf("second backup = %+v, want it to fail because the snapshot exists", second)
	}
}

// assertNoSecret fails the test if the token shows up in the logs or in any
// file the engine wrote.
func assertNoSecret(test *testing.T, engine testEngine) {
	test.Helper()
	if strings.Contains(engine.logs.String(), secretToken) {
		test.Errorf("logs contain the token:\n%s", engine.logs.String())
	}
	err := filepath.WalkDir(engine.directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // test reads files it just wrote below a temporary directory
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(secretToken)) {
			test.Errorf("%s contains the token", path)
		}
		return nil
	})
	if err != nil {
		test.Fatal(err)
	}
}
