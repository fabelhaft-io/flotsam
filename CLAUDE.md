# CLAUDE.md

Guidance for Claude Code (and human contributors) working in this repository.

## Project

flotsam backs up SaaS data (GitHub, Jira, ...) to storage the user controls. It
is a public, community-driven Go project, so code should be easy for an outsider to read,
and adding a new service should never require touching the core.

The project is in early development: the core exists, the providers don't yet. Check what
actually exists before you rely on it, and update this file when the design changes.

## Simplicity first

Keeping the code as simple as possible matters more than anything else in this repository.
Contributors should be able to read any package in one sitting.

- Build what a provider needs now, not what one might need later. No options, hooks or
  setting types nobody uses yet; add them together with their first user.
- One implementation per rule. If two places need the same behavior (like the snapshot
  rules for the engine and for `providertest`), share the code instead of copying it.
- One path through the code. Quick backups and config files both end up in
  `config.File` and `File.Resolve`; don't add a second way to do the same thing.
- Prefer removing code to adding it. When a check catches almost nothing but costs code
  and edge cases (like the old `<...>` placeholder check), drop it.

## Basics

- Module: `github.com/fabelhaft-io/flotsam`
- License: Apache 2.0
- Go: use the version in `go.mod`

## Commands

```sh
go build ./...
go test ./...
go test -race ./...              # needs cgo; not available on this Windows machine, CI runs it
go vet ./...
go mod tidy -diff                # must print nothing
golangci-lint run                # config in .golangci.yml, includes gofmt/goimports
go run ./cmd/flotsam providers
```

golangci-lint isn't installed locally; run it with
`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run`, the version CI
pins in `.github/workflows/ci.yml`.

## Layout

```
cmd/flotsam/                  CLI: one command_<name>.go per command
  providers.go                blank imports that enable the built-in providers
  storage.go                  blank imports that enable the built-in storage backends
pkg/setting/                  settings structs: tag parsing, filling from file + env, validation
pkg/provider/                 Provider, Snapshot and Env interfaces, Register, Spec
pkg/provider/providertest/    in-memory snapshot and fixture-replaying HTTP client for tests
pkg/storage/                  Storage interface, Register, Spec, optional Locator
pkg/storage/storagetest/      conformance suite every storage backend runs
pkg/storage/local/            local directory storage
providers/<name>/             one package per SaaS integration, plus its README.md
internal/config/              JSON config and quick-backup arguments -> sources to run
internal/engine/              creates providers, runs sources, writes manifests last
internal/snapshot/            provider.Snapshot: path rules, checksums (engine and providertest)
internal/manifest/            manifest.json format and verification
internal/httpclient/          HTTP client behind provider.Env: retries, rate limits
internal/redaction/           removes secret values from logs, errors and warnings
internal/safepath/            snapshot path rules (relative, no "..", Windows-safe)
internal/registry/            generic name -> entry registry used by provider and storage
docs/snapshot-format.md       the snapshot format, for people reading backups without flotsam
```

`pkg/` is the public API that third-party providers build against, so keep it small and
stable. Everything else goes in `internal/`; `pkg/` may import `internal/` packages.

## Architecture rules

- **Providers know nothing about storage.** A provider only sees `provider.Snapshot`
  (`WriteJSON`, `Create`, `Warnf`). The engine maps that onto the configured `Storage`.
- **Registration happens in `init()`.** A provider calls
  `provider.Register("<name>", provider.Spec[Config]{...})`. The core has no switch
  statements or lists naming providers, except the blank imports in
  `cmd/flotsam/providers.go`.
- **Settings are declared, never parsed by hand.** A provider's `Config` struct with
  `setting:"name,required,secret"` and `help:"..."` tags is its whole settings definition.
  The core fills it from the JSON config and from `FLOTSAM_<SOURCE>_<SETTING>` env vars
  (env wins), validates it, and generates docs from it. Providers never read `os.Getenv`.
  Secrets must come from env; a `secret` setting in the config file is an error.
- **Scope is set with `targets`.** A provider that can back up parts of an account takes a
  `targets` setting (`[]string` of URLs); empty means everything the credentials can
  access. It lists its URL hosts in `Spec.Hosts` so `flotsam backup <url>` works without a
  config file. Unsupported URL forms are a config error, never silently ignored.
- **`Spec.New` does no I/O.** The engine creates every provider before the first backup,
  so invalid settings fail before any API call. Put cross-setting checks (for example
  "token is required when targets is empty") in `New`.
- **Providers get shared behavior through `provider.Env`**, most importantly an
  `*http.Client` with retries and rate limiting built in, and a logger that redacts
  secrets. Providers must not import each other or anything under `internal/`.
- **Warn, don't fail, for single items.** If one repository or attachment can't be read,
  call `snapshot.Warnf` and continue; the snapshot becomes `partial`. Return an error from
  `Backup` only when the backup as a whole can't continue.
- **Snapshots are append-only and self-describing.** The manifest is written last, so a
  snapshot without one is incomplete. Never change files in an existing snapshot.
- **No encryption, ever.** flotsam writes plain files and leaves encryption to the storage
  or the user. Don't add encryption features or options: a bug there could make backups
  undecryptable, and the project doesn't take that responsibility.
- **Optional features use optional interfaces.** Like `storage.Locator`, an optional
  capability is a separate interface that the core detects with a type assertion, not an
  extra method every implementation must have.

## Conventions

- Spell names out; abbreviations should be rare. Write `snapshot`, `repository`,
  `configuration`, `request`, `response` and `testCase`, not `snap`, `repo`, `cfg`, `req`,
  `resp` and `tt`. This applies to variables, parameters, fields, functions, types, files and
  setting names. Short forms that every Go developer expects are fine: standard library
  package names (`fmt`, `os`, `io`), `ctx`, `err`, `ok`, well-known acronyms (`URL`, `HTTP`,
  `JSON`, `API`, `ID`) and the usual layout directories (`cmd/`, `pkg/`, `internal/`).
- No single-letter names, not even where Go code often uses them:
  - Test parameters are `test *testing.T` (or `test testing.TB`), not `t`. Naming it
    `testing` would hide the `testing` package that subtests need.
  - Receivers are full words: `func (registry *Registry)`, `func (command *cli)`. If
    the natural name is the type's own name, rename the type (`handler` became
    `redactingHandler`) rather than shortening the receiver.
  - Loop variables are named after what they hold: `index`, `testCase`, `definition`.
- Document the why, not only the what. Contributors arrive without context, so:
  - Every package doc says why the package exists and what problem it solves, not just
    what it contains.
  - Every type and function has a doc comment, including unexported ones and test
    helpers. A one-line "X implements Y." is enough for plain interface methods.
  - Every decision a reader might question gets a comment at that spot: why this
    default, why this check, why it's rejected rather than repaired, why it's done in this
    order. Don't comment what the code already says.
- Prefer the standard library: `net/http`, `encoding/json`, `log/slog`, `context`. Every
  new dependency needs a clear reason; official API SDKs are fine when they save real work.
- Pass `context.Context` as the first argument to anything that does I/O, and respect
  cancellation inside pagination loops.
- Wrap errors with context: `fmt.Errorf("list issues for %s: %w", repository, err)`. Don't
  log and also return the same error. Error messages start lower case.
- Use `log/slog` for logging. **Never log or write credentials**: no tokens in log lines,
  error messages, snapshot files or test fixtures. The engine redacts secret settings as a
  safety net, but don't rely on it.
- When writing to stdout/stderr, discard the error explicitly (`_, _ = fmt.Fprintln(...)`).
  Don't relax `errcheck`; it protects the code that writes snapshot files.
- Every `//nolint` names the linter and explains why (`//nolint:gosec // reason`).
- Stream large data (repositories, attachments) through `snapshot.Create`. Never buffer a
  whole export in memory.
- Use the HTTP client from `environment.HTTPClient()`. It already backs off on 429 and 5xx
  and honors rate-limit headers, so don't write retry loops in providers. If an API needs
  special handling, improve `internal/httpclient` instead.
- Write JSON exactly as the API returns it (`json.RawMessage`) unless there is a good reason
  to reshape it, so no fields are lost.
- Snapshot paths use forward slashes, include no secrets, and are safe on Windows: no `:`,
  `*`, `?` and similar characters. `internal/safepath` enforces this.

## Testing

- Tests must never call live SaaS APIs. Use `providertest.Fixtures` (or your own
  `http.Handler`) with recorded responses under `providers/<name>/testdata/`, and remove
  tokens, emails and other personal data from the fixtures.
- `providertest.Backup` runs a provider against a handler and returns the in-memory
  snapshot to check. Its HTTP client retries like the real one, without waiting.
- Test pagination, rate-limit responses and partial failures, not only the happy path.
- Write table-driven tests where they fit, and keep each provider's tests in its own package.
- Storage backends run `storagetest.Run` in their tests.

## Adding a provider (checklist)

1. Create `providers/<name>/` with `New`, a tagged `Config` struct and `init()`
   registration. A small provider can be a single `<name>.go` file.
2. Add a blank import to `cmd/flotsam/providers.go`.
3. Add fixture-based tests.
4. Add `providers/<name>/README.md`: required credentials and scopes, what is backed up,
   the snapshot layout, and known gaps.
5. Add a row to the provider table in the root `README.md`.
