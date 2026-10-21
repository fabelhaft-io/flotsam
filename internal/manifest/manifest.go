// Package manifest reads, writes and verifies manifest.json, the file that
// marks a snapshot as complete. docs/snapshot-format.md describes the format.
//
// A backup you can't trust is worse than none, because you only find out
// when you need it. The manifest answers the questions that matter before a
// restore: did this backup finish, what is missing, and has anything changed
// since it was written? It is written after every other file, so its
// presence means "complete". Checksums make silent damage, such as a bad disk
// or a truncated copy, detectable. It is plain JSON, so it can be checked
// without flotsam, with jq and sha256sum.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"
)

const (
	// FileName is the manifest's path inside a snapshot.
	FileName = "manifest.json"
	// Format is the version of the manifest format this package writes.
	// Snapshots are kept for years, so readers must be able to recognize
	// manifests written by a different flotsam version instead of
	// misreading them.
	Format = 1
)

// Status says whether a snapshot holds everything the provider meant to back up.
type Status string

const (
	// StatusComplete means the backup finished without warnings.
	StatusComplete Status = "complete"
	// StatusPartial means the backup finished, but some items were skipped;
	// the warnings say which.
	StatusPartial Status = "partial"
)

// Manifest describes one snapshot. It holds no settings, so it can never
// contain a secret, and no data from the service beyond file names.
type Manifest struct {
	Format         int       `json:"format"`
	FlotsamVersion string    `json:"flotsam_version"`
	Source         string    `json:"source"`
	Provider       string    `json:"provider"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	Status         Status    `json:"status"`
	Warnings       []string  `json:"warnings"`
	Files          []File    `json:"files"`
}

// File describes one file of a snapshot. Size is redundant with the checksum,
// but it makes the most common damage, a truncated file, obvious at a glance.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Encode returns the manifest as indented JSON. Empty lists are written as []
// instead of null, so scripts can iterate over them without a null check.
func Encode(manifest Manifest) ([]byte, error) {
	if manifest.Warnings == nil {
		manifest.Warnings = []string{}
	}
	if manifest.Files == nil {
		manifest.Files = []File{}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Decode parses a manifest and rejects unknown fields, which would mean the
// manifest comes from a format this version doesn't fully understand.
func Decode(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return manifest, nil
}

// Report is the result of Verify.
type Report struct {
	Manifest Manifest
	// Problems lists every file that is missing, changed or not in the
	// manifest. An empty list means the snapshot is intact.
	Problems []string
}

// Verify checks every file of the snapshot in snapshot against its manifest,
// and reports files that the manifest doesn't list. Extra files are reported
// too, because a complete snapshot never has any: they mean the snapshot was
// changed after it was written.
//
// It takes an fs.FS so it works on a local directory today and on other
// storage later, and so tests can use an in-memory file system.
func Verify(snapshot fs.FS) (Report, error) {
	data, err := fs.ReadFile(snapshot, FileName)
	if errors.Is(err, fs.ErrNotExist) {
		return Report{}, fmt.Errorf("no %s: this isn't a snapshot, or the backup didn't finish", FileName)
	}
	if err != nil {
		return Report{}, err
	}
	manifest, err := Decode(data)
	if err != nil {
		return Report{}, err
	}
	if manifest.Format != Format {
		return Report{}, fmt.Errorf("manifest format %d isn't supported; this flotsam reads format %d", manifest.Format, Format)
	}

	report := Report{Manifest: manifest}
	listed := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		listed[file.Path] = true
		size, checksum, err := hash(snapshot, file.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			report.Problems = append(report.Problems, "missing: "+file.Path)
		case err != nil:
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", file.Path, err))
		case size != file.Size:
			report.Problems = append(report.Problems, fmt.Sprintf("%s: size is %d bytes, the manifest says %d", file.Path, size, file.Size))
		case checksum != file.SHA256:
			report.Problems = append(report.Problems, file.Path+": checksum doesn't match the manifest")
		}
	}
	err = fs.WalkDir(snapshot, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path != FileName && !listed[path] {
			report.Problems = append(report.Problems, "not in the manifest: "+path)
		}
		return nil
	})
	return report, err
}

// hash returns the size and SHA-256 checksum of one file, streaming it so
// large files don't have to fit in memory.
func hash(snapshot fs.FS, path string) (int64, string, error) {
	file, err := snapshot.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}
