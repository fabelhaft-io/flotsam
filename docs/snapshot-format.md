# Snapshot format

A snapshot is a directory of plain files. You don't need flotsam to read it: every file
is either JSON or the raw data the service returned, such as a git bundle. This document
describes format version 1.

## Layout

```
<storage root>/
└── <source>/                        # the source name from the config, or the provider for quick backups
    └── <timestamp>/                 # UTC start time, e.g. 2026-10-21T19-00-00Z
        ├── manifest.json            # written last
        └── ...                      # the provider's files, see the provider's README
```

The timestamp uses `-` instead of `:` so the name is valid on Windows. Each run creates a
new snapshot; flotsam never changes or deletes files in an existing one.

## Completeness

**A snapshot is complete only if it has a `manifest.json`.** flotsam writes the manifest
after every other file is safely stored. A directory without one is a backup that failed
or was interrupted, and shouldn't be trusted.

A complete snapshot has one of two statuses:

- `complete`: the provider backed up everything in scope.
- `partial`: the backup finished, but some items were skipped, for example a repository
  the token can't read. `warnings` says which.

## manifest.json

```json
{
  "format": 1,
  "flotsam_version": "v0.1.0",
  "source": "work-github",
  "provider": "github",
  "started_at": "2026-10-21T19:00:00Z",
  "finished_at": "2026-10-21T19:04:12Z",
  "status": "partial",
  "warnings": [
    "fabelhaft-io/private-tool: issues: 403 Forbidden; the token needs the Issues permission"
  ],
  "files": [
    {
      "path": "fabelhaft-io/flotsam/git.bundle",
      "size": 482113,
      "sha256": "5f1c...e9a0"
    }
  ]
}
```

| Field             | Meaning |
|-------------------|---------|
| `format`          | Version of this format. Readers should reject versions they don't know. |
| `flotsam_version` | Version of flotsam that wrote the snapshot. |
| `source`          | Source name; also the parent directory's name. |
| `provider`        | Provider that wrote the files. |
| `started_at`, `finished_at` | UTC times in RFC 3339 format. |
| `status`          | `complete` or `partial`, see above. |
| `warnings`        | Problems that didn't stop the backup. Empty for `complete`. |
| `files`           | Every file in the snapshot except the manifest, sorted by path, with its size in bytes and its SHA-256 checksum in lowercase hex. |

Paths are relative to the snapshot directory and use `/` on every operating system.

## Checking a snapshot

`flotsam verify <snapshot-directory>` re-computes every checksum and reports files that are
missing, changed, or not listed in the manifest. Without flotsam, on Linux:

```sh
cd backups/work-github/2026-10-21T19-00-00Z
jq -r '.files[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum --check
```

## Secrets

Snapshots never contain the credentials flotsam used: secret settings are removed from
warnings, and the manifest doesn't record settings at all. Snapshots do contain everything
the credentials could read, so store them as carefully as the data in the service itself.
