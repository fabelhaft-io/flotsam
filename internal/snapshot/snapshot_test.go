package snapshot

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/fabelhaft-io/flotsam/internal/redaction"
)

// discardStorage accepts every file and keeps nothing.
type discardStorage struct{}

// Create implements Creator.
func (discardStorage) Create(context.Context, string) (io.WriteCloser, error) {
	return discardWriter{}, nil
}

// discardWriter throws away everything written to it.
type discardWriter struct{}

// Write implements io.Writer.
func (discardWriter) Write(data []byte) (int, error) { return len(data), nil }

// Close implements io.Closer.
func (discardWriter) Close() error { return nil }

// A provider that leaks a goroutine can still be writing when the engine
// closes its abandoned files. Run with -race, this test fails if Write and
// CloseAbandoned aren't synchronized.
func TestCloseAbandonedWhileAProviderStillWrites(test *testing.T) {
	written := New(discardStorage{}, "", redaction.New(), slog.New(slog.DiscardHandler))
	writer, err := written.Create(test.Context(), "file.bin")
	if err != nil {
		test.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			if _, err := writer.Write([]byte("data")); err != nil {
				return
			}
		}
	}()
	if abandoned := written.CloseAbandoned(); abandoned != 1 {
		test.Errorf("CloseAbandoned() = %d, want 1", abandoned)
	}
	<-done

	if files := written.Files(); len(files) != 0 {
		test.Errorf("Files() = %v, want the abandoned file left out of the manifest", files)
	}
}
