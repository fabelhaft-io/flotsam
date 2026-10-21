package local

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/setting"
	"github.com/fabelhaft-io/flotsam/pkg/storage"
	"github.com/fabelhaft-io/flotsam/pkg/storage/storagetest"
)

// newForTest returns storage in a fresh temporary directory, which the test
// framework removes afterwards.
func newForTest(test *testing.T) *Storage {
	test.Helper()
	store, err := New(test.TempDir())
	if err != nil {
		test.Fatal(err)
	}
	return store
}

func TestConformance(test *testing.T) {
	storagetest.Run(test, func(test *testing.T) storage.Storage {
		return newForTest(test)
	})
}

func TestFilesLandBelowTheDirectory(test *testing.T) {
	store := newForTest(test)
	writer, err := store.Create(test.Context(), "source/snapshot/manifest.json")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := io.WriteString(writer, "{}"); err != nil {
		test.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		test.Fatal(err)
	}

	location := store.Location("source/snapshot/manifest.json")
	if want := filepath.Join(store.directory, "source", "snapshot", "manifest.json"); location != want {
		test.Errorf("Location() = %q, want %q", location, want)
	}
	data, err := os.ReadFile(location) //nolint:gosec // the test reads the file it just wrote below a temporary directory
	if err != nil || string(data) != "{}" {
		test.Errorf("file content = %q, %v; want {}", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(location))
	if err != nil {
		test.Fatal(err)
	}
	if len(entries) != 1 {
		test.Errorf("directory holds %d entries, want only manifest.json (no temporary files)", len(entries))
	}
}

func TestListSkipsTemporaryFiles(test *testing.T) {
	store := newForTest(test)
	writer, err := store.Create(test.Context(), "open.json")
	if err != nil {
		test.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := store.Create(test.Context(), ".flotsam-ABC.tmp"); err == nil {
		test.Error("Create() of a temporary file name succeeded, want an error")
	}
	keys, err := store.List(test.Context(), "")
	if err != nil {
		test.Fatal(err)
	}
	if len(keys) != 0 {
		test.Errorf("List() = %v, want no keys while the only file is still open", keys)
	}
}

func TestRegistered(test *testing.T) {
	information, found := storage.Lookup("local")
	if !found {
		test.Fatal(`storage "local" isn't registered`)
	}
	directory := test.TempDir()
	store, err := information.New(setting.Input{
		EnvironmentPrefix: setting.StorageEnvironmentPrefix,
		LookupEnvironment: func(name string) (string, bool) {
			return directory, name == "FLOTSAM_STORAGE_PATH"
		},
	})
	if err != nil {
		test.Fatal(err)
	}
	if got := store.(*Storage).directory; got != directory {
		test.Errorf("directory = %q, want %q", got, directory)
	}
}
