# Contributing to RepoPlane

Thanks for helping make repository tooling easier for agents to inspect safely.

## Before opening a change

1. Check existing issues and the MVP scope in [`docs/MVP-Spec.md`](docs/MVP-Spec.md).
2. Keep the current server read-only. Runner, cache reuse, and record writes need a separate design
   decision before implementation.
3. For a public contract change, update the relevant specification and generated schemas in the
   same change.

## Development

Requirements are Go 1.26+ and `rg` on `PATH`.

```sh
go generate ./internal/mcpserver
go test ./...
go vet ./...
go build -trimpath ./cmd/repoplane
```

Tests should use temporary directories and generated non-secret values. Do not commit local paths,
usernames, environment dumps, databases, keys, logs, or unrelated project identifiers.

## Pull requests

- Keep changes focused and explain observable contract changes.
- Add regression tests for boundary, pagination, encoding, and partial-result behavior.
- Preserve exact vs. lower-bound vs. unknown count semantics.
- Confirm stdout remains reserved for MCP protocol frames.
- Run `git diff --check` and the commands above before submitting.

By contributing, you agree that your contribution is licensed under the MIT License.
