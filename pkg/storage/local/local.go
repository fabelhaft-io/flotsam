// Package local stores backups in a directory on the local file system, which
// may also be a mounted NAS or network share.
//
// It is the default storage and the one quick backups use, so it has to be
// safe without any configuration:
//
//   - Files are written under a temporary name and renamed when complete, so an
//     interrupted backup never leaves a truncated file under its real name.
//   - All access goes through os.Root, so no key can reach outside the backup
//     directory, not even through a symlink planted inside it.
//   - Directories and files are created readable only by their owner, because
//     a backup holds everything the token could read, private data included.
package local

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fabelhaft-io/flotsam/internal/safepath"
	"github.com/fabelhaft-io/flotsam/pkg/storage"
)

// init registers local storage under the name used in the config file's
// "type".
func init() {
	storage.Register("local", storage.Spec[Settings]{
		Summary: "A directory on the local file system",
		New: func(configuration Settings) (storage.Storage, error) {
			return New(configuration.Path)
		},
	})
}

// Settings are the settings of local storage.
type Settings struct {
	Path string `setting:"path,required" help:"Directory to write backups to; created if it doesn't exist"`
}

const (
	// Backups can hold private data, so only the owner may read them.
	directoryPermissions = 0o700
	filePermissions      = 0o600

	// Temporary files start with a dot, so they are hidden in most file
	// managers, and have a distinct prefix and suffix, so List can skip them
	// and leftovers from a crash are easy to recognize and delete.
	temporaryPrefix = ".flotsam-"
	temporarySuffix = ".tmp"
)

// Storage keeps every key as a file below a directory. Files are written
// under a temporary name and renamed when they are complete.
type Storage struct {
	// directory is absolute, so a later change of the working directory
	// can't move the backups.
	directory string
}

var (
	_ storage.Storage = (*Storage)(nil)
	_ storage.Locator = (*Storage)(nil)
)

// New returns storage below directory, creating it if needed. Creating it
// here, rather than on the first write, makes a wrong or unwritable path fail
// before any backup starts.
func New(directory string) (*Storage, error) {
	if directory == "" {
		return nil, errors.New("local storage: path is empty")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("local storage: %w", err)
	}
	if err := os.MkdirAll(absolute, directoryPermissions); err != nil {
		return nil, fmt.Errorf("local storage: %w", err)
	}
	return &Storage{directory: absolute}, nil
}

// Location returns the file path of key, so the CLI can tell people where a
// snapshot is.
func (store *Storage) Location(key string) string {
	return filepath.Join(store.directory, filepath.FromSlash(key))
}

// Create implements storage.Storage.
func (store *Storage) Create(ctx context.Context, key string) (io.WriteCloser, error) {
	if err := validateKey(ctx, key); err != nil {
		return nil, err
	}
	// os.Root makes sure no path can leave the directory, even through
	// symlinks. It is opened per call and closed by the writer, because an
	// open directory handle would keep Windows from deleting or moving the
	// directory.
	root, err := os.OpenRoot(store.directory)
	if err != nil {
		return nil, err
	}
	name := filepath.FromSlash(key)
	if _, err := root.Stat(name); err == nil {
		_ = root.Close()
		return nil, fmt.Errorf("%s: %w", key, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = root.Close()
		return nil, err
	}

	directory := filepath.Dir(name)
	if err := root.MkdirAll(directory, directoryPermissions); err != nil {
		_ = root.Close()
		return nil, err
	}
	// The temporary file sits next to the final one, because a rename is
	// only atomic within one file system. The random part keeps concurrent
	// writers apart, and O_EXCL makes sure an existing file is never reused.
	temporaryName := filepath.Join(directory, temporaryPrefix+rand.Text()+temporarySuffix)
	file, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePermissions)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &fileWriter{root: root, file: file, key: key, temporaryName: temporaryName, finalName: name}, nil
}

// Open implements storage.Storage.
func (store *Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(ctx, key); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(store.directory)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(filepath.FromSlash(key))
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	// Keys name files only. A directory would open fine but fail on the
	// first read, so it is reported as missing right away.
	if information, err := file.Stat(); err != nil || information.IsDir() {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("%s: %w", key, fs.ErrNotExist)
	}
	return &fileReader{File: file, root: root}, nil
}

// List implements storage.Storage.
func (store *Storage) List(ctx context.Context, prefix string) ([]string, error) {
	// Only walk the deepest directory the prefix names, instead of the whole
	// backup directory, which can hold millions of files after a while.
	start := "."
	if index := strings.LastIndex(prefix, "/"); index > 0 {
		start = prefix[:index]
		if err := safepath.Validate(start); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(store.directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var keys []string
	err = fs.WalkDir(root.FS(), start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A prefix nothing was written under yet isn't an error.
			if path == start && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Temporary files are files still being written, or leftovers of a
		// crash; neither is a key.
		if !entry.IsDir() && !isTemporary(entry.Name()) && strings.HasPrefix(path, prefix) {
			keys = append(keys, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)
	return keys, nil
}

// validateKey rejects keys that are unsafe as paths or that would collide
// with flotsam's own temporary files.
func validateKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := safepath.Validate(key); err != nil {
		return err
	}
	if isTemporary(key[strings.LastIndex(key, "/")+1:]) {
		return fmt.Errorf("key %q looks like one of flotsam's temporary files", key)
	}
	return nil
}

// isTemporary reports whether a file name has the form of a temporary file.
func isTemporary(name string) bool {
	return strings.HasPrefix(name, temporaryPrefix) && strings.HasSuffix(name, temporarySuffix)
}

// fileWriter writes one key to its temporary file and moves it into place on
// Close.
type fileWriter struct {
	// root stays open until Close, since the rename goes through it.
	root          *os.Root
	file          *os.File
	key           string
	temporaryName string
	finalName     string
	// failed records a failed Write, so Close discards the file instead of
	// committing something incomplete.
	failed bool
	closed bool
}

// Write writes to the temporary file.
func (writer *fileWriter) Write(data []byte) (int, error) {
	if writer.closed {
		return 0, fmt.Errorf("%s: %w", writer.key, fs.ErrClosed)
	}
	written, err := writer.file.Write(data)
	if err != nil {
		writer.failed = true
	}
	return written, err
}

// Close flushes the file to disk and moves it to its final name.
func (writer *fileWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	defer func() { _ = writer.root.Close() }()

	// Sync before the rename: otherwise a power loss right after it can leave
	// a file under its final name whose content never reached the disk, and
	// the manifest would vouch for it.
	err := errors.Join(writer.file.Sync(), writer.file.Close())
	if err == nil && writer.failed {
		err = errors.New("an earlier write failed")
	}
	if err != nil {
		_ = writer.root.Remove(writer.temporaryName)
		return fmt.Errorf("write %s: %w", writer.key, err)
	}
	if err := writer.root.Rename(writer.temporaryName, writer.finalName); err != nil {
		_ = writer.root.Remove(writer.temporaryName)
		return fmt.Errorf("write %s: %w", writer.key, err)
	}
	return nil
}

// fileReader closes the os.Root together with the file, so Open doesn't leak
// directory handles.
type fileReader struct {
	*os.File
	root *os.Root
}

// Close closes the file and its root.
func (reader *fileReader) Close() error {
	return errors.Join(reader.File.Close(), reader.root.Close())
}
