// Package providertest helps provider authors test against recorded API
// responses, without network access.
//
// Provider tests must never call the real service: they would need real
// credentials in CI, fail when the service is down or rate-limits, and change
// data in someone's account. This package makes the offline way the easy way,
// so contributors don't have to build their own test setup:
//
//	func TestBackup(test *testing.T) {
//		handler := providertest.Fixtures(test, "testdata", map[string]string{
//			"GET /repos/fabelhaft-io/flotsam": "repository.json",
//		})
//		snapshot, err := providertest.Backup(test, spec, Config{Token: "test"}, handler)
//		if err != nil {
//			test.Fatal(err)
//		}
//		// check snapshot.Paths(), snapshot.File(...) and snapshot.Warnings()
//	}
package providertest

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fabelhaft-io/flotsam/internal/httpclient"
	"github.com/fabelhaft-io/flotsam/internal/redaction"
	"github.com/fabelhaft-io/flotsam/internal/snapshot"
	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/setting"
)

// Snapshot is an in-memory provider.Snapshot. It is the engine's own
// snapshot, writing to memory instead of storage, so the same path rules
// apply (safe paths, each path once, no paths that differ only in case,
// manifest.json reserved) and a provider that passes its tests here doesn't
// fail on the first real backup.
type Snapshot struct {
	written *snapshot.Snapshot
	memory  *memory
}

var _ provider.Snapshot = (*Snapshot)(nil)

// NewSnapshot returns an empty Snapshot.
func NewSnapshot() *Snapshot {
	files := &memory{files: make(map[string][]byte)}
	return &Snapshot{
		written: snapshot.New(files, "", redaction.New(), slog.New(slog.DiscardHandler)),
		memory:  files,
	}
}

// WriteJSON implements provider.Snapshot.
func (snapshot *Snapshot) WriteJSON(ctx context.Context, path string, value any) error {
	return snapshot.written.WriteJSON(ctx, path, value)
}

// Create implements provider.Snapshot.
func (snapshot *Snapshot) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	return snapshot.written.Create(ctx, path)
}

// Warnf implements provider.Snapshot.
func (snapshot *Snapshot) Warnf(format string, arguments ...any) {
	snapshot.written.Warnf(format, arguments...)
}

// Paths returns the paths of all closed files, sorted.
func (snapshot *Snapshot) Paths() []string {
	snapshot.memory.mutex.Lock()
	defer snapshot.memory.mutex.Unlock()
	return slices.Sorted(maps.Keys(snapshot.memory.files))
}

// File returns the content of a closed file.
func (snapshot *Snapshot) File(path string) ([]byte, bool) {
	snapshot.memory.mutex.Lock()
	defer snapshot.memory.mutex.Unlock()
	data, found := snapshot.memory.files[path]
	return data, found
}

// Warnings returns every warning the provider recorded.
func (snapshot *Snapshot) Warnings() []string {
	return snapshot.written.Warnings()
}

// memory stands in for storage: it keeps every closed file by key. The
// snapshot does all the checking, so this only has to store bytes.
type memory struct {
	mutex sync.Mutex
	files map[string][]byte
}

// Create implements snapshot.Creator.
func (memory *memory) Create(ctx context.Context, key string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &memoryWriter{memory: memory, key: key}, nil
}

// memoryWriter collects one file and stores it on Close, so files still being
// written aren't visible, just as with real storage.
type memoryWriter struct {
	memory *memory
	key    string
	buffer bytes.Buffer
}

// Write adds data to the file.
func (writer *memoryWriter) Write(data []byte) (int, error) {
	return writer.buffer.Write(data)
}

// Close stores the file.
func (writer *memoryWriter) Close() error {
	writer.memory.mutex.Lock()
	defer writer.memory.mutex.Unlock()
	writer.memory.files[writer.key] = writer.buffer.Bytes()
	return nil
}

// NewEnv returns a provider.Env whose HTTP client sends every request to
// handler, whatever host the request names, so nothing leaves the process.
// Because the host doesn't matter, providers keep their real API URLs in
// tests and need no setting just to point them at a test server.
//
// The client is the real retrying client with the waits removed, so a test
// can answer 429 or 503 to check that a provider copes, without the test
// taking minutes. Logs go to test.Log, so they only show up when a test fails
// or runs with -v.
func NewEnv(test testing.TB, handler http.Handler) provider.Env {
	test.Helper()
	logger := slog.New(slog.NewTextHandler(testWriter{test}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := httpclient.New(httpclient.Options{
		UserAgent: "flotsam/test",
		Logger:    logger,
		Transport: handlerTransport{handler: handler},
		Sleep:     func(context.Context, time.Duration) error { return nil },
	})
	return testEnvironment{client: client, logger: logger}
}

// Fixtures returns a handler that answers requests with files from directory.
// routes maps patterns of http.ServeMux, such as "GET /repos/fabelhaft-io/flotsam",
// to file names in directory. Requests that match no route fail the test, so
// a provider can't silently depend on a request the fixtures don't cover.
// For responses that depend on the query string, such as pages, write your
// own http.Handler.
//
// Files are read once, up front, so a missing fixture fails the test right
// away with its name instead of as a 404 somewhere inside the provider.
func Fixtures(test testing.TB, directory string, routes map[string]string) http.Handler {
	test.Helper()
	files := os.DirFS(directory)
	mux := http.NewServeMux()
	for pattern, name := range routes {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			test.Fatalf("fixture for %q: %v", pattern, err)
		}
		mux.HandleFunc(pattern, func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write(data)
		})
	}
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		test.Errorf("unexpected request: %s %s", request.Method, request.URL)
		http.NotFound(response, request)
	})
	return mux
}

// CheckSettings fails the test if Settings isn't a valid settings struct, so
// mistakes show up in the provider's own tests instead of at startup.
func CheckSettings[Settings any](test testing.TB) {
	test.Helper()
	if _, err := setting.Describe(reflect.TypeFor[Settings]()); err != nil {
		test.Fatal(err)
	}
}

// Backup creates the provider from spec with configuration, runs a backup
// with requests going to handler, and returns the snapshot. configuration is
// passed as a typed struct rather than through environment variables, so a
// test states exactly what it sets up. The snapshot is returned even when
// Backup fails, so tests can check what was written before the failure.
func Backup[Settings any](test testing.TB, spec provider.Spec[Settings], configuration Settings, handler http.Handler) (*Snapshot, error) {
	test.Helper()
	CheckSettings[Settings](test)
	created, err := spec.New(configuration, NewEnv(test, handler))
	if err != nil {
		return nil, err
	}
	snapshot := NewSnapshot()
	return snapshot, created.Backup(test.Context(), snapshot)
}

// testEnvironment is the provider.Env that NewEnv returns.
type testEnvironment struct {
	client *http.Client
	logger *slog.Logger
}

// HTTPClient implements provider.Env.
func (environment testEnvironment) HTTPClient() *http.Client { return environment.client }

// Logger implements provider.Env.
func (environment testEnvironment) Logger() *slog.Logger { return environment.logger }

// handlerTransport serves requests with an http.Handler instead of the
// network. It is faster than httptest.Server and needs no port, so tests can
// run in parallel and in sandboxes without networking.
type handlerTransport struct {
	handler http.Handler
}

// RoundTrip passes request to the handler and returns what it wrote.
func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// A RoundTripper must close the request body, and a handler may read the
	// body after the caller's request is gone, so the body is read into
	// memory and handed over as a copy.
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
		request = request.Clone(request.Context())
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	response.Request = request
	return response, nil
}

// testWriter sends log output to test.Log, which keeps it with the test it
// belongs to.
type testWriter struct {
	test testing.TB
}

// Write logs one record. slog writes each record with a single Write call.
func (writer testWriter) Write(data []byte) (int, error) {
	writer.test.Log(strings.TrimRight(string(data), "\n"))
	return len(data), nil
}
