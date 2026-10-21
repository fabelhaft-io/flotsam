// Package storagetest checks that a storage.Storage implementation meets the
// contract documented on the interface.
//
// The engine relies on that contract for the safety of backups: it trusts that
// a file nobody has closed is invisible, and that nothing is ever overwritten.
// A backend that gets this wrong doesn't fail loudly; it produces backups that
// look complete and aren't. A shared suite makes sure every backend,
// including community ones, is held to the same rules. Every backend should
// run it:
//
//	func TestConformance(test *testing.T) {
//		storagetest.Run(test, func(test *testing.T) storage.Storage {
//			return newBackendForTest(test)
//		})
//	}
package storagetest

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/storage"
)

// Run checks the storage that newStorage returns. newStorage is called once
// per subtest and must return an empty storage each time, so the subtests
// can't affect each other.
func Run(test *testing.T, newStorage func(test *testing.T) storage.Storage) {
	test.Run("write and read back", func(test *testing.T) {
		store := newStorage(test)
		write(test, store, "source/snapshot/file.json", "content")
		if got := read(test, store, "source/snapshot/file.json"); got != "content" {
			test.Errorf("read back %q, want %q", got, "content")
		}
	})

	// The manifest is written last and lists only closed files. If unclosed
	// data were visible, a crash could leave a half-written file that looks
	// like a real one.
	test.Run("data is hidden until Close", func(test *testing.T) {
		store := newStorage(test)
		writer, err := store.Create(test.Context(), "pending.json")
		if err != nil {
			test.Fatal(err)
		}
		if _, err := writer.Write([]byte("partial")); err != nil {
			test.Fatal(err)
		}
		if _, err := store.Open(test.Context(), "pending.json"); !errors.Is(err, fs.ErrNotExist) {
			test.Errorf("Open() before Close = %v, want fs.ErrNotExist", err)
		}
		if keys := list(test, store, ""); len(keys) != 0 {
			test.Errorf("List() before Close = %v, want nothing", keys)
		}
		if err := writer.Close(); err != nil {
			test.Fatal(err)
		}
		if keys := list(test, store, ""); !slices.Equal(keys, []string{"pending.json"}) {
			test.Errorf("List() after Close = %v, want [pending.json]", keys)
		}
	})

	// Snapshots are append-only: an existing backup must never be
	// overwritten, not even by a bug that reuses a key.
	test.Run("create fails for an existing key", func(test *testing.T) {
		store := newStorage(test)
		write(test, store, "existing.json", "first")
		if _, err := store.Create(test.Context(), "existing.json"); !errors.Is(err, fs.ErrExist) {
			test.Errorf("Create() of an existing key = %v, want fs.ErrExist", err)
		}
		if got := read(test, store, "existing.json"); got != "first" {
			test.Errorf("existing data changed to %q", got)
		}
	})

	// Callers tell "missing" apart from other failures with errors.Is.
	test.Run("open fails for a missing key", func(test *testing.T) {
		store := newStorage(test)
		if _, err := store.Open(test.Context(), "missing.json"); !errors.Is(err, fs.ErrNotExist) {
			test.Errorf("Open() of a missing key = %v, want fs.ErrNotExist", err)
		}
	})

	// Prefixes are plain string prefixes ("a" matches "ab/3"), the way object
	// stores list keys, and results are sorted so callers get a stable order.
	test.Run("list by prefix", func(test *testing.T) {
		store := newStorage(test)
		for _, key := range []string{"b/4", "a/2", "a/1", "ab/3", "a/nested/5"} {
			write(test, store, key, key)
		}
		tests := []struct {
			prefix string
			want   []string
		}{
			{prefix: "", want: []string{"a/1", "a/2", "a/nested/5", "ab/3", "b/4"}},
			{prefix: "a/", want: []string{"a/1", "a/2", "a/nested/5"}},
			{prefix: "a", want: []string{"a/1", "a/2", "a/nested/5", "ab/3"}},
			{prefix: "a/nested/", want: []string{"a/nested/5"}},
			{prefix: "missing/", want: nil},
		}
		for _, testCase := range tests {
			if got := list(test, store, testCase.prefix); !slices.Equal(got, testCase.want) {
				test.Errorf("List(%q) = %v, want %v", testCase.prefix, got, testCase.want)
			}
		}
	})

	// Keys come from providers, and providers are community code. A key like
	// "../escape" must not reach outside the backup location.
	test.Run("rejects unsafe keys", func(test *testing.T) {
		store := newStorage(test)
		for _, key := range []string{"", "../escape", "/absolute", "a//b", `a\b`, "a/../../b"} {
			if writer, err := store.Create(test.Context(), key); err == nil {
				_ = writer.Close()
				test.Errorf("Create(%q) succeeded, want an error", key)
			}
			if reader, err := store.Open(test.Context(), key); err == nil {
				_ = reader.Close()
				test.Errorf("Open(%q) succeeded, want an error", key)
			}
		}
	})

	// Ctrl-C or a timeout cancels the context; storage must stop instead of
	// starting new writes.
	test.Run("respects a canceled context", func(test *testing.T) {
		store := newStorage(test)
		ctx, cancel := context.WithCancel(test.Context())
		cancel()
		if writer, err := store.Create(ctx, "canceled.json"); err == nil {
			_ = writer.Close()
			test.Error("Create() with a canceled context succeeded, want an error")
		}
	})
}

// write stores content under key and fails the test on any error.
func write(test *testing.T, store storage.Storage, key, content string) {
	test.Helper()
	writer, err := store.Create(test.Context(), key)
	if err != nil {
		test.Fatalf("Create(%q): %v", key, err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		test.Fatalf("Write(%q): %v", key, err)
	}
	if err := writer.Close(); err != nil {
		test.Fatalf("Close(%q): %v", key, err)
	}
}

// read returns what is stored under key and fails the test on any error.
func read(test *testing.T, store storage.Storage, key string) string {
	test.Helper()
	reader, err := store.Open(test.Context(), key)
	if err != nil {
		test.Fatalf("Open(%q): %v", key, err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		test.Fatalf("read %q: %v", key, err)
	}
	return string(data)
}

// list returns the keys under prefix and fails the test on any error.
func list(test *testing.T, store storage.Storage, prefix string) []string {
	test.Helper()
	keys, err := store.List(test.Context(), prefix)
	if err != nil {
		test.Fatalf("List(%q): %v", prefix, err)
	}
	return keys
}
