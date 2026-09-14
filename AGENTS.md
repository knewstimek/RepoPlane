# Repository working agreements

## Public-repository privacy

- Treat every tracked file, commit message, fixture, example, generated artifact, and release
  asset as public information.
- Never commit personal names, usernames, email addresses, machine names, home directories,
  absolute local paths, private repository URLs, credentials, tokens, keys, or identifiers that
  reveal unrelated projects or workspaces.
- Use neutral placeholders such as `USER`, `WORKSPACE`, `example.invalid`, and temporary
  directories created by tests.
- Do not copy environment dumps, command histories, database contents, local profiles, logs, or
  tool output into tracked files unless they have been deliberately minimized and sanitized.
- Keep local state, SQLite databases, cursor keys, coverage data, build output, and diagnostic logs
  in ignored locations. Never weaken `.gitignore` to publish them.
- Before any public push or release, scan the complete tracked tree and Git history for secrets,
  personal paths, usernames, unrelated project names, and local artifacts. Stop the release if any
  finding has not been reviewed.
- Do not place a real secret in a test, even temporarily. Generate ephemeral test values or use an
  unmistakably non-secret placeholder.

## Implementation

- Go services depend on domain interfaces in `internal/store`, not directly on SQL or a concrete
  database adapter.
- Keep MCP stdout free of diagnostics; write diagnostics only to stderr.
- Preserve bounded reads, explicit partial states, and exact/lower-bound/unknown distinctions.
- Update public schemas, tests, and the relevant specification in the same change when an external
  contract changes.
- Do not implement Runner, cache reuse, `project_records`, or record writes as part of the current
  read-only MVP.
