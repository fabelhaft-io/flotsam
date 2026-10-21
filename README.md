# flotsam

**Back up your SaaS data before it drifts away.**

flotsam is an open-source tool, written in Go, for backing up the data you keep in SaaS
products such as GitHub and Jira. It pulls everything out through each service's API and
writes it to storage you control, in plain, documented formats you can read without flotsam.

> **Status: early development.** The commands, config and interfaces below describe the
> target design and may change before the first release.

## Why

SaaS vendors keep your data safe from *their* failures, but not from yours: a deleted
project, a compromised account, a cancelled subscription, or a vendor that shuts down. Most
services offer some kind of export, but each one is different, manual, and incomplete.
flotsam gives you one tool, one config file and one snapshot format for all of them.

## Goals

- **Extensible by the community.** Each SaaS integration is a self-contained *provider*
  package. Adding a new service means adding one directory, with no changes to the core.
- **Open output.** Snapshots are directories of JSON documents and raw files plus a
  manifest. You never need flotsam to read your own backup.
- **Safe to automate.** A single static binary that runs well from cron, CI or Kubernetes.
  It respects rate limits, retries transient errors, and never writes credentials into a
  snapshot.
- **Pluggable storage.** Local disk first; S3-compatible object storage and others behind
  the same interface.

### Non-goals (for now)

- **Restore.** The first priority is getting data out reliably. Restoring into a service is
  much harder and differs a lot between services. It may come later, per provider.
- **Hosted service.** flotsam is a tool you run yourself.

## Providers

| Provider | Status | What gets backed up |
|----------|--------|---------------------|
| [`github`](providers/github/README.md) | available | Repositories and wikis (git bundles), issues, pull requests, comments, releases, labels, milestones |
| `jira` | planned | Projects, issues, comments, attachments |

Missing something? [Write a provider](#writing-a-provider). Contributions are very welcome.

## Usage

```sh
go install github.com/fabelhaft-io/flotsam/cmd/flotsam@latest

# Quick backups, no config file needed
flotsam backup https://github.com/fabelhaft-io/flotsam   # one repository
flotsam backup https://github.com/fabelhaft-io           # every repository of an org or user
flotsam backup github                                    # everything the token can access

# Scheduled backups from a config file
flotsam backup -c flotsam.json                           # every configured source
flotsam backup -c flotsam.json -s work-github            # a single source

flotsam providers                                        # providers built into this binary
flotsam providers github                                 # settings and env vars for github
```

Quick backups write to `./backups` (change it with `-o`) and read the token from
`FLOTSAM_GITHUB_TOKEN`: without a config file, the source is named after the provider.
Public repositories work without a token, within GitHub's limits for anonymous API use.

### Configuration

The config file lists your sources and where to store backups. Copy
[flotsam.example.json](flotsam.example.json) to `flotsam.json` and replace the `<...>`
placeholders. A relative storage `path` is relative to the config file, so a scheduled run
writes to the same place whatever directory it starts in.

```json
{
  "storage": {
    "type": "local",
    "settings": { "path": "./backups" }
  },
  "sources": [
    {
      "name": "work-github",
      "provider": "github",
      "settings": {
        "targets": ["https://github.com/<your-org>", "https://github.com/<other-owner>/<repository>"]
      }
    },
    {
      "name": "work-jira",
      "provider": "jira",
      "settings": {
        "url": "https://<your-site>.atlassian.net",
        "email": "<you@example.com>"
      }
    }
  ]
}
```

Source names are your own labels. They name the snapshot folder and the environment
variables for that source, so pick something that says what is backed up.

Most providers take `targets`, a list of URLs to back up. Leave it out to back up
everything the token can access.

Every setting can also be set with an environment variable named
`FLOTSAM_<SOURCE>_<SETTING>`, and the environment variable wins over the file. Secrets
such as API tokens **must** come from the environment; a token written into the config
file is rejected.

```sh
export FLOTSAM_WORK_GITHUB_TOKEN=github_pat_...
export FLOTSAM_WORK_JIRA_TOKEN=...
```

To build the name, the source name is upper-cased and `-` becomes `_`. Storage settings
use `FLOTSAM_STORAGE_<SETTING>`. Run `flotsam providers <name>` to see every setting a
provider takes.

### Snapshot layout

A quick backup of `https://github.com/fabelhaft-io/flotsam` produces:

```
backups/
└── github/                          # the source name
    └── 2026-10-21T19-00-00Z/
        ├── manifest.json            # provider, version, timings, file list with checksums
        └── fabelhaft-io/flotsam/
            ├── repository.json      # repository metadata from the API
            ├── git.bundle           # every branch and tag
            └── issues/42.json
```

Each run writes a new timestamped snapshot. A snapshot only counts as complete once its
`manifest.json` has been written, so an interrupted run never looks like a good backup.
`flotsam verify <snapshot-directory>` checks every file against the checksums in the
manifest. [docs/snapshot-format.md](docs/snapshot-format.md) describes the format.

`flotsam backup` exits with 0 if every backup is complete, 1 if any failed or was partial
(or the configuration is invalid), and 2 if the command line is wrong.

## Writing a provider

A provider is a Go package under `providers/<name>/` that registers itself at init time,
in the same way `database/sql` drivers do. Its settings are a plain struct: flotsam fills
it from the config file and environment, validates it, keeps secrets out of logs, and
generates the provider's documentation from it.

```go
package example

import (
	"context"
	"net/http"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

func init() {
	provider.Register("example", provider.Spec[Config]{
		Summary: "Everything in an Example workspace",
		New:     New,
	})
}

type Config struct {
	Token     string `setting:"token,required,secret" help:"API token with read access"`
	Workspace string `setting:"workspace,required"    help:"Workspace to back up"`
}

type Example struct {
	configuration Config
	client        *http.Client
}

// New receives validated settings and an Env that provides an HTTP client with
// retries and rate limiting already built in.
func New(configuration Config, environment provider.Env) (provider.Provider, error) {
	return &Example{configuration: configuration, client: environment.HTTPClient()}, nil
}

func (e *Example) Backup(ctx context.Context, snapshot provider.Snapshot) error {
	// Page through the API and write each resource into the snapshot:
	//   snapshot.WriteJSON(ctx, "items/123.json", item)
	//   writer, _ := snapshot.Create(ctx, "files/report.pdf") // stream large data
	// Report problems with single items and carry on:
	//   snapshot.Warnf("item %d: %s", id, response.Status)
	return nil
}
```

Then enable it with a blank import in `cmd/flotsam/providers.go`, add tests that run against
recorded HTTP fixtures (never the live API), and document what it backs up in
`providers/<name>/README.md`. The `pkg/provider/providertest` package gives you an
in-memory snapshot and an HTTP client that answers from fixture files.
[CLAUDE.md](CLAUDE.md) has the full conventions.

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). For a new
provider, open an issue first so people don't duplicate work. Before you push, make sure
these pass:

```sh
go vet ./...
go test ./...
```

## License

[Apache 2.0](LICENSE)
