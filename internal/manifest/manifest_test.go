package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// checksum returns the SHA-256 checksum of content as Verify expects it.
func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// snapshot returns an in-memory snapshot holding files and a manifest that
// lists listed. The two are passed separately so tests can make them
// disagree.
func snapshot(test *testing.T, files map[string]string, listed []File) fstest.MapFS {
	test.Helper()
	data, err := Encode(Manifest{
		Format:     Format,
		Source:     "work-github",
		Provider:   "github",
		StartedAt:  time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 10, 21, 19, 1, 0, 0, time.UTC),
		Status:     StatusComplete,
		Files:      listed,
	})
	if err != nil {
		test.Fatal(err)
	}
	snapshot := fstest.MapFS{FileName: {Data: data}}
	for path, content := range files {
		snapshot[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return snapshot
}

func TestEncodeWritesEmptyLists(test *testing.T) {
	data, err := Encode(Manifest{Format: Format})
	if err != nil {
		test.Fatal(err)
	}
	if !strings.Contains(string(data), `"warnings": []`) || !strings.Contains(string(data), `"files": []`) {
		test.Errorf("Encode() = %s, want empty lists instead of null", data)
	}
}

func TestDecodeRejectsUnknownFields(test *testing.T) {
	if _, err := Decode([]byte(`{"format": 1, "extra": true}`)); err == nil {
		test.Error("Decode() accepted an unknown field")
	}
}

func TestVerify(test *testing.T) {
	listed := []File{
		{Path: "a/1.json", Size: 2, SHA256: checksum("{}")},
		{Path: "b.bin", Size: 5, SHA256: checksum("bytes")},
	}
	tests := []struct {
		name         string
		files        map[string]string
		wantProblems []string
	}{
		{
			name:  "intact",
			files: map[string]string{"a/1.json": "{}", "b.bin": "bytes"},
		},
		{
			name:         "missing file",
			files:        map[string]string{"a/1.json": "{}"},
			wantProblems: []string{"missing: b.bin"},
		},
		{
			name:         "changed size",
			files:        map[string]string{"a/1.json": "{}", "b.bin": "more bytes"},
			wantProblems: []string{"b.bin: size is 10 bytes, the manifest says 5"},
		},
		{
			name:         "changed content",
			files:        map[string]string{"a/1.json": "{}", "b.bin": "BYTES"},
			wantProblems: []string{"b.bin: checksum doesn't match the manifest"},
		},
		{
			name:         "unexpected file",
			files:        map[string]string{"a/1.json": "{}", "b.bin": "bytes", "a/extra.json": "{}"},
			wantProblems: []string{"not in the manifest: a/extra.json"},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			report, err := Verify(snapshot(test, testCase.files, listed))
			if err != nil {
				test.Fatal(err)
			}
			if !slices.Equal(report.Problems, testCase.wantProblems) {
				test.Errorf("problems = %q, want %q", report.Problems, testCase.wantProblems)
			}
			if report.Manifest.Source != "work-github" {
				test.Errorf("manifest source = %q", report.Manifest.Source)
			}
		})
	}
}

func TestVerifyWithoutManifest(test *testing.T) {
	_, err := Verify(fstest.MapFS{"file.json": {Data: []byte("{}")}})
	if err == nil || !strings.Contains(err.Error(), "didn't finish") {
		test.Errorf("Verify() = %v, want an error explaining the missing manifest", err)
	}
}

func TestVerifyRejectsUnknownFormat(test *testing.T) {
	_, err := Verify(fstest.MapFS{FileName: {Data: []byte(`{"format": 99}`)}})
	if err == nil || !strings.Contains(err.Error(), "format 99") {
		test.Errorf("Verify() = %v, want an unsupported format error", err)
	}
}
