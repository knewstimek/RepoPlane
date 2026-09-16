# Contributing to RepoPlane

Thanks for helping make repository tooling easier for agents to inspect safely.

## Before opening a change

1. Check existing issues, the [README](README.md), and public tool schemas in [`schemas/`](schemas/).
2. Keep query tools read-only. Record mutation tools require explicit host pre-authorization or a
   user-approved ephemeral stdio runtime grant; do not add
   Runner or cache reuse through the record surface.
3. For a public contract change, update the README, generated schemas, and regression tests in the
   same change.

## Development

Requirements are Go 1.26+ and `rg` on `PATH`.

```sh
go run ./cmd/repoplane-dev preflight
go run ./cmd/repoplane-dev verify
```

Both commands write bounded `check-report.v1` JSON under the ignored `.tmp/reports` directory.
Preflight reports only whether explicitly requested environment variables exist, never their
values. Before a public push or release, run `go run ./cmd/repoplane-dev public-release-check` from
a clean worktree.

Tests should use temporary directories and generated non-secret values. Do not commit local paths,
usernames, environment dumps, databases, keys, logs, or unrelated project identifiers.

## Pull requests

- Keep changes focused and explain observable contract changes.
- Add regression tests for boundary, pagination, encoding, and partial-result behavior.
- Preserve exact vs. lower-bound vs. unknown count semantics.
- Confirm stdout remains reserved for MCP protocol frames.
- Run `git diff --check` and the commands above before submitting.

By contributing, you agree that your contribution is licensed under the MIT License.
