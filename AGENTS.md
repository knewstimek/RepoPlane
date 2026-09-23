# Repository working agreements

## Public-repository privacy

- Treat tracked files, commits, fixtures, examples, generated artifacts, and release assets as public.
  Never commit personal names, usernames, emails, hostnames, home or absolute local paths, private
  URLs, secrets, keys, tokens, or identifiers of unrelated workspaces.
- Use neutral placeholders (`USER`, `WORKSPACE`, `example.invalid`) and test-created temporary
  directories. Never use a real secret in a test, even briefly.
- Minimize and sanitize any environment dump, history, database content, local profile, log, or tool
  output before tracking it. Keep local state, SQLite, cursors, coverage, build output, and logs
  ignored; never weaken `.gitignore` to publish them.
- Before a public push or release, scan the full tracked tree and Git history for secrets, personal
  paths, usernames, unrelated project names, and local artifacts; stop on unreviewed findings.

## Implementation

- Finish work by updating README/usage and `Unreleased` as needed, verifying, committing, and
  pushing. Report completion and elapsed time; for goal-tracked work, report aggregate token usage.
- If the MCP executable changed, rebuild and replace the installed copy found via the ignored
  local profile or `PATH`; never track its host-specific destination. On Windows, rename an in-use
  copy to `old_repoplane_<timestamp>.exe` before replacement and retain it while in use.
- After a feature change, update README and `Unreleased`; keep private planning notes outside Git.
  Include a copyable host configuration example for flags and opt-in tools.
- Go services use domain interfaces in `internal/store`, not SQL or concrete database adapters.
  Keep MCP diagnostics on stderr, never stdout. Preserve bounded reads, explicit partial states,
  and exact/lower-bound/unknown distinctions.
- Change public schemas, tests, and README tool contracts together for external contract changes.
  Keep Runner and cache reuse behind their existing approval and qualification gates.
- At task start or when past decisions matter, search durable records with task terms and small
  limits; fetch only relevant IDs. Request exact `payload_fields` when needed, use
  `response_view=receipt` for writes, and retain validity and decision evidence.
- For new non-host memos, provide `temporal_kind` and `as_of` only when evidence supports the
  content time. A `memo_time_unknown` write warning is honest when it does not; do not substitute
  `created_at` or `updated_at`. To correct a memo, read it first and update with the expected
  revision while preserving the other payload fields and evidence refs.
- Use RepoPlane MCP first for discovery, registered verification/release, and task recovery. Use
  shell only if no matching tool exists or MCP reports `unsupported`/`partial`, and state why.
  Discover deferred tools by matching `mcp__repoplane__` in `ALL_TOOLS`.

- For MCP agent UX, fix what agents see through tools; never treat README or usage edits as the
  solution on the assumption that agents read them.

## Verification discipline

- Compile and test an edited package before the repository suite. Run full verification after
  focused checks stabilize, then once more after the final code or schema change.
- Before retrying a hung or failed test, confirm its session and child tree have exited; diagnose
  or stop the verified tree if still running. Never rerun an unchanged failure: classify it as
  product code, fixture, host/toolchain, or safety policy; change something relevant or use a
  supported environment, then run one focused check.
- Reuse optional race, sanitizer, cross-runtime, or platform support evidence for the same OS,
  architecture, and toolchain. Probe only when absent or changed; leave CI-assigned modes to CI
  on unsupported local hosts and record that limitation.
- Count failed verification invocations during a goal and report their classifications. Repeated
  package errors within one command count as one failed invocation.
