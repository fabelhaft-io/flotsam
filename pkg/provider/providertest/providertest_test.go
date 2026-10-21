package providertest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

// exampleSettings and exampleProvider form the kind of small provider
// authors write, to test the package the way they would use it.
type exampleSettings struct {
	Token string `setting:"token,required,secret" help:"Token"`
}

// exampleProvider fetches one item from an example API.
type exampleProvider struct {
	client *http.Client
}

// Backup fetches one item and copies it into the snapshot.
func (example *exampleProvider) Backup(ctx context.Context, snapshot provider.Snapshot) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/items/1", nil)
	if err != nil {
		return err
	}
	response, err := example.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		snapshot.Warnf("item 1: %s", response.Status)
		return nil
	}
	var item json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&item); err != nil {
		return fmt.Errorf("decode item 1: %w", err)
	}
	return snapshot.WriteJSON(ctx, "items/1.json", item)
}

var exampleSpec = provider.Spec[exampleSettings]{
	Summary: "Example",
	New: func(_ exampleSettings, environment provider.Env) (provider.Provider, error) {
		return &exampleProvider{client: environment.HTTPClient()}, nil
	},
}

func TestBackupWithFixtures(test *testing.T) {
	directory := test.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "item.json"), []byte(`{"id":1}`), 0o600); err != nil {
		test.Fatal(err)
	}
	handler := Fixtures(test, directory, map[string]string{"GET /items/1": "item.json"})

	snapshot, err := Backup(test, exampleSpec, exampleSettings{Token: "test"}, handler)
	if err != nil {
		test.Fatal(err)
	}
	if got := snapshot.Paths(); !slices.Equal(got, []string{"items/1.json"}) {
		test.Errorf("paths = %v", got)
	}
	if data, _ := snapshot.File("items/1.json"); string(data) != "{\n  \"id\": 1\n}\n" {
		test.Errorf("items/1.json = %q", data)
	}
}

func TestBackupRetriesWithoutWaiting(test *testing.T) {
	attempts := 0
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			response.Header().Set("Retry-After", "3600")
			response.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(response, `{"id":1}`)
	})
	snapshot, err := Backup(test, exampleSpec, exampleSettings{Token: "test"}, handler)
	if err != nil {
		test.Fatal(err)
	}
	if attempts != 2 || len(snapshot.Warnings()) != 0 {
		test.Errorf("attempts = %d, warnings = %v; want 2 attempts and no warnings", attempts, snapshot.Warnings())
	}
}

func TestSnapshotRules(test *testing.T) {
	snapshot := NewSnapshot()
	ctx := test.Context()
	if err := snapshot.WriteJSON(ctx, "Item.json", 1); err != nil {
		test.Fatal(err)
	}
	for _, path := range []string{"item.json", "../escape.json", "manifest.json", "colon:name"} {
		if err := snapshot.WriteJSON(ctx, path, 1); err == nil {
			test.Errorf("WriteJSON(%q) succeeded, want an error", path)
		}
	}
	snapshot.Warnf("skipped %d items", 2)
	if got := snapshot.Warnings(); !slices.Equal(got, []string{"skipped 2 items"}) {
		test.Errorf("warnings = %v", got)
	}
}
