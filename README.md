# flotsam

**Back up your SaaS data before it drifts away.**

flotsam is an open-source tool, written in Go, for backing up the data you keep in SaaS
products such as GitHub and Jira. It pulls everything out through each service's API and
writes it to storage you control, in plain, documented formats you can read without flotsam.

> **Status: early development.** The commands, config and interfaces below describe the
> target design and may change before the first release.

## Why

Your company's knowledge, code, customers and records increasingly live in other people's
products. Having your own copy of that data matters for several reasons:

- **Recovery.** SaaS vendors keep your data safe from *their* failures, but not from yours:
  a deleted project, a compromised account or a cancelled subscription.
- **Freedom to switch.** With your data in plain, documented formats, moving to another
  vendor is a migration you can plan, not a negotiation with the vendor you are leaving. A
  price increase or a worse product becomes a choice instead of a trap.
- **Use your data your way.** A local copy in plain JSON and git works with any tool you
  like: AI assistants, search, analytics or your own scripts. You choose which AI sees the
  data and where it runs, even fully local models, instead of being limited to the
  vendor's own AI features, API limits or add-on prices.
- **Vendors change.** Services get acquired, change their terms or shut down, sometimes on
  short notice. Your copy doesn't depend on any of that.
- **Even the vendor's safeguards can fail.** In September 2025, a fire in South Korea's
  government data center destroyed G-Drive, the cloud storage of about 125,000 government
  employees. It had no backup anywhere else, and much of its roughly 858 TB of files was
  [lost for good](https://www.datacenterdynamics.com/en/news/858tb-of-government-data-may-be-lost-for-good-after-south-korea-data-center-fire).
  Copies that live inside the same service share its fate; a second, independent copy
  doesn't.
- **Records you must keep.** Invoices, contracts and support history often have to be kept
  for years, longer than you use the tool that holds them. Laws can also require keeping
  data longer than the service itself keeps it, for example when it deletes old records
  after a fixed period or removes everything soon after you cancel.

Most services offer some kind of export, but each one is different, manual and incomplete.
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
- **Encryption.** flotsam never encrypts backups, because a mistake there could leave
  backups nobody can decrypt. Use encrypted storage instead; see
  [Keeping backups safe is up to you](#keeping-backups-safe-is-up-to-you).

## Providers

| Provider | Service | Status | What gets backed up |
|----------|---------|--------|---------------------|
| [`github`](providers/github/README.md) | Code hosting and collaboration | available | Repositories and wikis (git bundles), issues, pull requests, comments, releases, labels, milestones |
| `gitlab` | Code hosting and DevOps platform | planned | Repositories and wikis (git bundles), issues, merge requests, comments, releases |
| `bitbucket` | Atlassian's code hosting | planned | Repositories (git bundles), pull requests, comments |
| `jira` | Atlassian's project and issue tracking | planned | Spaces, work items, comments, attachments |
| `confluence` | Atlassian's team wiki | planned | Spaces, pages, blog posts, comments, attachments |
| `linear` | Issue tracking for software teams | planned | Teams, projects, issues, comments, attachments |
| `notion` | Notes, wikis and databases in one workspace | planned | Pages, databases and their rows, comments, files |
| `trello` | Kanban boards | planned | Boards, lists, cards, comments, checklists, attachments |
| `asana` | Work and project management | planned | Projects, tasks, subtasks, comments, attachments |
| `airtable` | Spreadsheet-like databases | planned | Bases, tables, records, attachments |
| `zendesk` | Customer support and help center | planned | Tickets, comments, users, organizations, help center articles, attachments |
| `git` | Any git server, hosted or self-hosted | maybe | Any git repository by URL, such as Gitea, Forgejo or Codeberg (git bundles) |
| `azure-devops` | Microsoft's boards, repositories and pipelines | maybe | Work items, comments, attachments, repositories and wikis (git bundles) |
| `youtrack` | JetBrains' issue tracker and knowledge base | maybe | Issues, comments, attachments, knowledge base articles |
| `clickup` | Project management and docs | maybe | Tasks, comments, attachments, docs |
| `monday` | Work management boards | maybe | Boards, items, updates, files |
| `shortcut` | Project management for software teams | maybe | Stories, epics, comments, files |
| `wrike` | Project management for larger teams | maybe | Projects, tasks, comments, attachments |
| `coda` | Docs that work like apps | maybe | Docs, pages, tables and rows |
| `gitbook` | Documentation sites and knowledge bases | maybe | Spaces, pages |
| `outline` | Team wiki, also self-hosted | maybe | Collections, documents (Markdown) |
| `guru` | Company knowledge base | maybe | Collections, cards |
| `figma` | Interface design and whiteboards (FigJam) | maybe | Files as JSON and rendered images, comments, versions (not re-importable into Figma) |
| `miro` | Online whiteboards | maybe | Boards and their items |
| `penpot` | Open-source design tool | maybe | Projects, files |
| `hubspot` | CRM, marketing and sales | maybe | Contacts, companies, deals, tickets, notes |
| `pipedrive` | Sales CRM | maybe | Deals, people, organizations, activities, notes |
| `contentful` | Headless CMS | maybe | Spaces, content types, entries, assets |
| `sanity` | Headless CMS | maybe | Documents, assets |
| `typeform` | Online forms and surveys | maybe | Forms, responses |
| `flexport` | Freight forwarding and logistics | maybe | Shipments, bookings, purchase orders, invoices, documents |
| `shippo` | Shipping labels and tracking | maybe | Shipments, labels, tracking |
| `easypost` | Shipping labels and tracking API | maybe | Shipments, labels, tracking |
| `lexoffice` | Accounting and invoicing (Germany) | maybe | Invoices, quotations, vouchers, contacts, files |
| `sevdesk` | Accounting and invoicing (Germany) | maybe | Invoices, vouchers, contacts, documents |
| `stripe` | Online payments and billing | maybe | Customers, invoices, payments, subscriptions |
| `shopify` | Online stores | maybe | Orders, products, customers |
| `freshdesk` | Customer support help desk | maybe | Tickets, conversations, contacts, knowledge base articles |
| `intercom` | Customer messaging and support | maybe | Conversations, contacts, help center articles |
| `toggl` | Time tracking | maybe | Time entries, projects, clients |
| `harvest` | Time tracking and invoicing | maybe | Time entries, projects, clients, invoices |
| `mailchimp` | Email marketing | maybe | Audiences, campaigns, templates |
| `calendly` | Meeting scheduling | maybe | Event types, scheduled events |
| `personio` | HR and payroll for small and medium companies | maybe | Employees, absences, attendances, documents |
| `bamboohr` | HR software | maybe | Employees, time off, files |

*Planned* providers are on the roadmap; *maybe* means a good candidate whose API still needs a
closer look.

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

## Keeping backups safe is up to you

flotsam creates backups. It doesn't protect them once they are written:

- **No encryption.** Snapshots are plain files. If they need to be encrypted, use encrypted
  storage (an encrypted disk or bucket) or encrypt the snapshots yourself.
- **No access control.** Whoever can read the storage can read every backup. Local storage
  creates files readable only by their owner, but anything beyond that is up to you.
- **Personal data.** Backups hold whatever the services hold, often personal data of
  customers or employees, especially from CRM, support and HR tools. Complying with data
  protection law such as the GDPR (retention periods, deletion requests, who may access
  the data) is the responsibility of whoever runs flotsam.
- **Credentials.** Managing tokens is up to you; use read-only tokens with the narrowest
  permissions that cover what you back up.

flotsam is provided as is, without warranty, under the [Apache License 2.0](LICENSE).

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
