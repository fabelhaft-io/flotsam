// Package snapshot implements provider.Snapshot: it checks every path a
// provider writes, streams the data to storage, and records each file's size
// and checksum for the manifest.
//
// It is used both by the engine for real backups and by providertest for
// provider tests. Having one implementation means a provider that passes its
// tests can't fail a real backup because of a path rule the tests didn't know.
package snapshot

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/fabelhaft-io/flotsam/internal/manifest"
	"github.com/fabelhaft-io/flotsam/internal/redaction"
	"github.com/fabelhaft-io/flotsam/internal/safepath"
	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

// Creator is the part of storage.Storage a snapshot needs. Asking for only
// this keeps the in-memory storage of providertest to a single method.
type Creator interface {
	Create(ctx context.Context, key string) (io.WriteCloser, error)
}

// Snapshot implements provider.Snapshot on top of storage. It enforces the
// path rules, so no provider can write outside its snapshot or overwrite a
// file.
type Snapshot struct {
	storage  Creator
	prefix   string
	redactor *redaction.Redactor
	logger   *slog.Logger

	// mutex guards everything below, because providers may write files from
	// several goroutines.
	mutex sync.Mutex
	// reserved maps the lower-case form of every created path to the path,
	// so files that differ only in case can't overwrite each other on
	// Windows and macOS.
	reserved map[string]string
	// files holds the manifest entry of every file that was closed
	// successfully. Only these go into the manifest.
	files map[string]manifest.File
	// writers holds every writer that is still open, so they can be closed
	// if the provider doesn't.
	writers  map[*fileWriter]bool
	warnings []string
}

var _ provider.Snapshot = (*Snapshot)(nil)

// New returns an empty snapshot whose files go to storage below prefix.
// Warnings are redacted with redactor and logged to logger.
func New(storage Creator, prefix string, redactor *redaction.Redactor, logger *slog.Logger) *Snapshot {
	return &Snapshot{
		storage:  storage,
		prefix:   prefix,
		redactor: redactor,
		logger:   logger,
		reserved: make(map[string]string),
		files:    make(map[string]manifest.File),
		writers:  make(map[*fileWriter]bool),
	}
}

// WriteJSON implements provider.Snapshot. It goes through Create, so JSON
// files get the same checks and checksums as every other file.
func (snapshot *Snapshot) WriteJSON(ctx context.Context, path string, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// Go escapes <, > and & by default for safe embedding in HTML. Backups
	// aren't HTML, and the escapes would make issue texts with code in them
	// hard to read.
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	writer, err := snapshot.Create(ctx, path)
	if err != nil {
		return err
	}
	if _, err := writer.Write(buffer.Bytes()); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

// Create implements provider.Snapshot.
func (snapshot *Snapshot) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	if err := safepath.Validate(path); err != nil {
		return nil, err
	}
	// A provider file named manifest.json would make an unfinished snapshot
	// look complete.
	if path == manifest.FileName {
		return nil, fmt.Errorf("%s is reserved for the manifest", path)
	}
	// The path is reserved before storage is asked, so two goroutines
	// creating the same path get a clear error, and storage never sees one
	// key opened twice.
	if err := snapshot.reserve(path); err != nil {
		return nil, err
	}
	destination, err := snapshot.storage.Create(ctx, snapshot.prefix+path)
	if err != nil {
		// Nothing was written, so the provider may try the path again.
		snapshot.release(path)
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	created := &fileWriter{snapshot: snapshot, path: path, destination: destination, hash: sha256.New()}
	snapshot.mutex.Lock()
	snapshot.writers[created] = true
	snapshot.mutex.Unlock()
	return created, nil
}

// Warnf implements provider.Snapshot. The message is redacted before it is
// stored, because warnings end up in the manifest, which is kept for years.
func (snapshot *Snapshot) Warnf(format string, arguments ...any) {
	message := snapshot.redactor.String(fmt.Sprintf(format, arguments...))
	snapshot.mutex.Lock()
	snapshot.warnings = append(snapshot.warnings, message)
	snapshot.mutex.Unlock()
	snapshot.logger.Warn(message)
}

// Files returns the manifest entries of all closed files, sorted by path, so
// the same backup always produces the same manifest.
func (snapshot *Snapshot) Files() []manifest.File {
	snapshot.mutex.Lock()
	defer snapshot.mutex.Unlock()
	files := slices.Collect(maps.Values(snapshot.files))
	slices.SortFunc(files, func(first, second manifest.File) int {
		return cmp.Compare(first.Path, second.Path)
	})
	return files
}

// Warnings returns a copy of the warnings recorded so far.
func (snapshot *Snapshot) Warnings() []string {
	snapshot.mutex.Lock()
	defer snapshot.mutex.Unlock()
	return slices.Clone(snapshot.warnings)
}

// CloseAbandoned closes the writers a provider left open, so no file handles
// leak, and returns how many there were. Their files stay out of the manifest,
// because an open writer means the provider may not have finished them.
func (snapshot *Snapshot) CloseAbandoned() int {
	// The writers are copied out first, because closing one calls finish,
	// which needs the lock.
	snapshot.mutex.Lock()
	abandoned := slices.Collect(maps.Keys(snapshot.writers))
	snapshot.mutex.Unlock()
	for _, writer := range abandoned {
		_ = writer.close(true)
	}
	return len(abandoned)
}

// reserve claims path for one file. Paths are compared without case,
// because Windows and macOS treat "Item.json" and "item.json" as the same
// file: the second would overwrite the first, and the manifest would list a
// checksum for a file that no longer exists.
func (snapshot *Snapshot) reserve(path string) error {
	snapshot.mutex.Lock()
	defer snapshot.mutex.Unlock()
	key := strings.ToLower(path)
	if existing, taken := snapshot.reserved[key]; taken {
		if existing == path {
			return fmt.Errorf("%s was already written in this snapshot", path)
		}
		return fmt.Errorf("%s differs from %s only in upper and lower case, which some file systems treat as the same file", path, existing)
	}
	snapshot.reserved[key] = path
	return nil
}

// release gives up a reservation for a file that was never created.
func (snapshot *Snapshot) release(path string) {
	snapshot.mutex.Lock()
	defer snapshot.mutex.Unlock()
	delete(snapshot.reserved, strings.ToLower(path))
}

// finish records a closed writer. Only committed files are added to the
// manifest; a failed or abandoned file stays out of it even if storage kept
// its data.
func (snapshot *Snapshot) finish(writer *fileWriter, file manifest.File, committed bool) {
	snapshot.mutex.Lock()
	defer snapshot.mutex.Unlock()
	delete(snapshot.writers, writer)
	if committed {
		snapshot.files[file.Path] = file
	}
}

// fileWriter hashes and counts everything on its way to storage. Hashing
// while writing means each file is read only once, which matters for
// multi-gigabyte repositories, and the checksum covers exactly the bytes the
// provider sent.
type fileWriter struct {
	snapshot    *Snapshot
	path        string
	destination io.WriteCloser
	hash        hash.Hash

	// mutex makes Write and Close safe to call at the same time. That only
	// happens when CloseAbandoned closes a writer that a provider goroutine,
	// left running by mistake, is still writing to.
	mutex sync.Mutex
	size  int64
	// failed records a failed Write. The file is then left out of the
	// manifest, even if the provider ignores the error.
	failed bool
	closed bool
}

// Write passes data to storage and adds what was written to the checksum.
func (writer *fileWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.closed {
		return 0, fmt.Errorf("%s: %w", writer.path, fs.ErrClosed)
	}
	written, err := writer.destination.Write(data)
	// Only the bytes that reached storage count, so a short write can't make
	// the checksum describe data that isn't there.
	writer.hash.Write(data[:written])
	writer.size += int64(written)
	if err != nil {
		writer.failed = true
	}
	return written, err
}

// Close commits the file in storage and records it for the manifest.
// Closing twice is harmless, so providers can both defer Close and check its
// error explicitly.
func (writer *fileWriter) Close() error {
	return writer.close(false)
}

// close closes the writer. An abandoned file is closed to free its handle,
// but not recorded in the manifest.
func (writer *fileWriter) close(abandoned bool) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	err := writer.destination.Close()
	if err == nil && writer.failed {
		err = errors.New("an earlier write failed")
	}
	writer.snapshot.finish(writer, manifest.File{
		Path:   writer.path,
		Size:   writer.size,
		SHA256: hex.EncodeToString(writer.hash.Sum(nil)),
	}, err == nil && !abandoned)
	if err != nil {
		return fmt.Errorf("write %s: %w", writer.path, err)
	}
	return nil
}
