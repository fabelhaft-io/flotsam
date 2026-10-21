# Contributing to flotsam

Thanks for helping. New providers, bug fixes, docs and bug reports are all welcome.

## Development setup

You need:

- Go, the version in `go.mod` or newer
- `git`
- [golangci-lint](https://golangci-lint.run) v2 (optional locally; CI runs it on every PR)

```sh
git clone https://github.com/fabelhaft-io/flotsam.git
cd flotsam
go build ./...
go test ./...
go run ./cmd/flotsam version
```

Before you open a pull request, run:

```sh
go vet ./...
go test -race ./...    # -race needs cgo; on Windows without a C compiler, use go test ./...
golangci-lint run      # or: go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run
```

## Conventions

[CLAUDE.md](CLAUDE.md) describes the architecture and code conventions. It is written for
Claude Code, but it applies to everyone, whether or not you use an AI assistant. If you do,
you are still responsible for understanding and testing what you submit.

## Adding a provider

1. Open a [provider request](https://github.com/fabelhaft-io/flotsam/issues/new?template=provider_request.yml)
   first, or comment on an existing one, so nobody does the same work twice.
2. Follow the checklist at the end of [CLAUDE.md](CLAUDE.md). The pull request template
   repeats it.
3. Keep one provider per pull request.

Tests must never call a live API. Record real responses once, then **remove tokens, emails,
names and any other personal data** before committing them to `testdata/`.

After a provider is merged, its author can become its maintainer: add yourself for its
directory in [.github/CODEOWNERS](.github/CODEOWNERS) and you'll be asked to review changes
to it.

## Pull requests

- Keep each pull request focused on one change, and explain *why* in the description.
- Add or update tests for behavior changes.
- Update the docs (`README.md`, provider READMEs) when user-facing behavior changes.

## Security

Please don't report security issues in public issues. See [SECURITY.md](SECURITY.md).

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By taking part, you
agree to uphold it.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), the same license as the project.
