// Package engine runs backups. It creates every provider before the first
// backup starts, streams what each provider writes into storage while
// hashing it, and writes the manifest last.
//
// The engine is where flotsam's guarantees are kept, so that providers don't
// each have to keep them:
//
//   - A snapshot with a manifest is complete; one without is not. The manifest
//     is written only after every file is stored.
//   - Every file in the manifest has a checksum computed while it was written,
//     not read back afterwards, so it describes exactly what the provider sent.
//   - Secrets are removed from every log line, error and warning.
//   - One broken source, even one whose provider panics, doesn't stop the
//     others.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/fabelhaft-io/flotsam/internal/config"
	"github.com/fabelhaft-io/flotsam/internal/httpclient"
	"github.com/fabelhaft-io/flotsam/internal/manifest"
	"github.com/fabelhaft-io/flotsam/internal/redaction"
	"github.com/fabelhaft-io/flotsam/internal/snapshot"
	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
	"github.com/fabelhaft-io/flotsam/pkg/storage"
)

// timestampLayout names snapshots. It has no colons, which Windows doesn't
// allow in file names. It sorts chronologically as text, so the newest
// snapshot is simply the last one in a listing.
const timestampLayout = "2006-01-02T15-04-05Z"

// Status is the outcome of one source's backup. It extends manifest.Status
// with StatusFailed, which never appears in a manifest because failed backups
// don't get one.
type Status string

const (
	StatusComplete = Status(manifest.StatusComplete)
	StatusPartial  = Status(manifest.StatusPartial)
	// StatusFailed means the backup didn't finish and has no manifest.
	StatusFailed Status = "failed"
)

// Result is the outcome of one source's backup.
type Result struct {
	Source string
	// Snapshot is the storage key of the snapshot directory, such as
	// "work-github/2026-10-21T19-00-00Z".
	Snapshot string
	Status   Status
	Files    int
	Warnings []string
	// Failure says why the backup failed; it is nil unless Status is
	// StatusFailed. Secrets are already removed.
	Failure error
}

// Engine runs backups into one storage. Its fields are set by the CLI; tests
// replace the clock and the network.
type Engine struct {
	Storage storage.Storage
	Logger  *slog.Logger
	// Version is recorded in every manifest and the User-Agent.
	Version string
	// Now replaces the clock in tests.
	Now func() time.Time
}

// preparedSource is a source whose provider has been created, with the
// redactor and logger that belong to it.
type preparedSource struct {
	source   config.Source
	provider provider.Provider
	redactor *redaction.Redactor
	logger   *slog.Logger
}

// Run backs up sources one after another. It returns an error without
// backing anything up if any provider can't be created, so configuration
// mistakes show up before the first API call. Once backups have started, a
// failure only affects its own source and is reported in its Result.
//
// Sources run one after another rather than in parallel for now: it keeps
// logs readable and load on shared rate limits predictable.
func (engine *Engine) Run(ctx context.Context, sources []config.Source) ([]Result, error) {
	var prepared []preparedSource
	var problems []error
	for _, source := range sources {
		created, err := engine.prepare(source)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		prepared = append(prepared, created)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(prepared))
	for _, source := range prepared {
		results = append(results, engine.backup(ctx, source))
	}
	return results, nil
}

// prepare fills the settings of source and creates its provider, with an Env
// whose logger and HTTP client are tied to that source. Errors start with
// the source's label.
func (engine *Engine) prepare(source config.Source) (preparedSource, error) {
	// The redactor is built before the provider exists, so even errors from
	// creating it are redacted. Each source only redacts its own secrets: a
	// provider never sees those of other sources.
	redactor := redaction.New(setting.SecretValues(source.Provider.Settings, source.Input)...)
	logger := slog.New(redactor.Handler(engine.logger().Handler())).With("source", source.Name)
	environment := sourceEnvironment{client: engine.httpClient(logger), logger: logger}

	var created provider.Provider
	var newError error
	panicked := callSafely(logger, func() error {
		created, newError = source.Provider.New(source.Input, environment)
		return nil
	})
	var err error
	switch {
	case panicked != nil:
		err = fmt.Errorf("%s: %w", source.Input.Label, panicked)
	case newError != nil:
		// Already labeled by provider.Info.New.
		err = newError
	case created == nil:
		// A nil provider would only panic later, inside the backup, with a
		// far less useful message.
		err = fmt.Errorf("%s: the provider's New returned nil", source.Input.Label)
	}
	if err != nil {
		return preparedSource{}, redactor.Error(err)
	}
	return preparedSource{source: source, provider: created, redactor: redactor, logger: logger}, nil
}

// backup runs one source into a new snapshot and writes its manifest.
func (engine *Engine) backup(ctx context.Context, prepared preparedSource) Result {
	started := engine.now().UTC()
	snapshotKey := prepared.source.Name + "/" + started.Format(timestampLayout)
	result := Result{Source: prepared.source.Name, Snapshot: snapshotKey}
	logger := prepared.logger.With("snapshot", snapshotKey)
	// fail records a failure. The snapshot keeps the files written so far, for
	// inspection, but gets no manifest, so it can't be mistaken for a good
	// backup.
	fail := func(err error) Result {
		result.Status = StatusFailed
		result.Failure = prepared.redactor.Error(err)
		logger.Error("backup failed", "error", result.Failure)
		return result
	}

	// Snapshot names have one-second resolution, so two runs in the same
	// second would write into the same snapshot. Storage refuses to
	// overwrite files anyway, but checking first gives a clear error instead
	// of a confusing one in the middle of the backup.
	existing, err := engine.Storage.List(ctx, snapshotKey+"/")
	if err != nil {
		return fail(fmt.Errorf("check for an existing snapshot: %w", err))
	}
	if len(existing) > 0 {
		return fail(fmt.Errorf("snapshot %s already exists", snapshotKey))
	}

	logger.Info("backup started")
	written := snapshot.New(engine.Storage, snapshotKey+"/", prepared.redactor, logger)
	err = callSafely(logger, func() error {
		return prepared.provider.Backup(ctx, written)
	})
	result.Warnings = written.Warnings()
	// Files the provider forgot to close are closed now, whether the backup
	// failed or not, so no file handles leak into the next source.
	abandoned := written.CloseAbandoned()
	if err != nil {
		return fail(err)
	}
	// An open writer means the provider may not have finished that file, so
	// the snapshot can't be called complete.
	if abandoned > 0 {
		return fail(fmt.Errorf("the provider left %d file(s) open; every writer from Create must be closed", abandoned))
	}

	// Any warning makes the snapshot partial, so a backup that skipped data
	// never looks the same as one that didn't, and the CLI exits non-zero.
	status := manifest.StatusComplete
	if len(result.Warnings) > 0 {
		status = manifest.StatusPartial
	}
	files := written.Files()
	data, err := manifest.Encode(manifest.Manifest{
		Format:         manifest.Format,
		FlotsamVersion: engine.Version,
		Source:         prepared.source.Name,
		Provider:       prepared.source.Provider.Name,
		StartedAt:      started,
		FinishedAt:     engine.now().UTC(),
		Status:         status,
		Warnings:       result.Warnings,
		Files:          files,
	})
	if err != nil {
		return fail(fmt.Errorf("encode the manifest: %w", err))
	}
	if err := writeAll(ctx, engine.Storage, snapshotKey+"/"+manifest.FileName, data); err != nil {
		return fail(fmt.Errorf("write the manifest: %w", err))
	}

	result.Status = Status(status)
	result.Files = len(files)
	logger.Info("backup finished",
		"status", status,
		"files", len(files),
		"warnings", len(result.Warnings),
		"duration", engine.now().Sub(started).Round(time.Millisecond),
	)
	return result
}

// callSafely turns a panic in provider code into an error, so one broken
// provider doesn't stop the backups of all other sources. The stack trace is
// logged, because it is what a provider author needs to fix the bug.
func callSafely(logger *slog.Logger, function func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("provider panicked", "panic", recovered, "stack", string(debug.Stack()))
			err = fmt.Errorf("the provider panicked: %v", recovered)
		}
	}()
	return function()
}

// writeAll stores data under key in one go. It is used for the manifest,
// which goes to storage directly instead of through the snapshot, so it
// isn't listed in itself.
func writeAll(ctx context.Context, store storage.Storage, key string, data []byte) error {
	writer, err := store.Create(ctx, key)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

// logger and now return the configured value or a default, so a zero Engine
// with only Storage set works.
func (engine *Engine) logger() *slog.Logger {
	if engine.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return engine.Logger
}

// now returns the current time from the configured clock.
func (engine *Engine) now() time.Time {
	if engine.Now == nil {
		return time.Now()
	}
	return engine.Now()
}

// httpClient returns a new retrying client that logs to logger. Each source
// gets its own, so retries are logged with the right source name.
func (engine *Engine) httpClient(logger *slog.Logger) *http.Client {
	// The User-Agent tells API operators who is calling and where to find
	// out more, which some services ask for and which helps when they need to
	// contact someone about traffic.
	return httpclient.New(httpclient.Options{
		UserAgent: "flotsam/" + engine.Version + " (+https://github.com/fabelhaft-io/flotsam)",
		Logger:    logger,
	})
}

// sourceEnvironment is the provider.Env of one source.
type sourceEnvironment struct {
	client *http.Client
	logger *slog.Logger
}

// HTTPClient implements provider.Env.
func (environment sourceEnvironment) HTTPClient() *http.Client { return environment.client }

// Logger implements provider.Env.
func (environment sourceEnvironment) Logger() *slog.Logger { return environment.logger }
