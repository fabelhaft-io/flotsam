## What and why

<!-- What does this change, and why? Link the issue if there is one. -->

## Checklist

- [ ] `go vet ./...` and `go test ./...` pass
- [ ] No tokens, emails or other personal data in code, logs, test fixtures or screenshots

### New provider (delete this section otherwise)

- [ ] Package in `providers/<name>/` with a tagged `Config` struct and `init()` registration
- [ ] Blank import added to `cmd/flotsam/providers.go`
- [ ] Tests run against recorded fixtures in `testdata/`, not the live API
- [ ] `providers/<name>/README.md`: required credentials and permissions, what is backed up, known gaps
- [ ] Row added to the provider table in the root `README.md`
