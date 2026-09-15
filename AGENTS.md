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

- When work is complete, update README/usage and `Unreleased` notes as needed, verify, commit, and
  push. If the MCP executable changed, rebuild it and replace the installed executable resolved
  from the ignored local profile or `PATH`; never record the host-specific destination in tracked
  files. On Windows, rename an in-use installed executable to
  `old_repoplane_<timestamp>.exe` before copying the replacement, and leave that backup in place
  until no process is using it. Then report the completion time and elapsed time; for goal-tracked
  work, also report the goal's aggregate token usage.
- When a feature or roadmap slice changes status, perform a bounded documentation-consistency pass
  across README, `Unreleased`, the active roadmap, the documentation index, and any frozen plan
  that still names the feature as next, deferred, or incomplete. Preserve historical plans by
  labeling their old status and linking to the active roadmap instead of silently rewriting
  history. Configuration flags and opt-in tools must include a copyable host configuration example.
- Go services depend on domain interfaces in `internal/store`, not directly on SQL or a concrete
  database adapter.
- Keep MCP stdout free of diagnostics; write diagnostics only to stderr.
- Preserve bounded reads, explicit partial states, and exact/lower-bound/unknown distinctions.
- Update public schemas, tests, and the relevant specification in the same change when an external
  contract changes.
- Keep Runner and cache reuse out of scope until their roadmap prerequisites and specifications
  are complete.
- At task start or when prior decisions and failures may matter, search current durable records
  with a few task-derived terms and a small `item_limit`/`byte_limit`; fetch full payloads only for
  relevant IDs. Use exact `payload_fields` when the default compact search view is insufficient and
  `response_view=receipt` for writes; retain validity and evidence needed for decisions.
- When RepoPlane MCP is available, use it first for repository discovery, registered
  verification/release execution, and durable task recovery. Use direct shell tools only when no
  matching capability exists or RepoPlane MCP reports `unsupported`/`partial`, and state the
  fallback reason.

## Verification discipline

- After editing a package, compile and test that affected package before starting the repository
  suite. Run the full verification workflow only after the focused checks are stable and rerun it
  once after the final code or schema change.
- Never start a second copy of a hanging or failed test command. First confirm the original tool
  session and its child process tree have exited; if they have not, diagnose or stop that verified
  tree before retrying.
- Do not rerun an unchanged failing command. Classify the failure as product code, test fixture,
  host/toolchain, or safety-policy related, make a relevant change or choose the documented
  supported environment, then run one focused check.
- Before optional race, sanitizer, cross-runtime, or platform-specific modes, reuse the current
  support result for the same OS, architecture, and toolchain fingerprint. Probe only when no
  current evidence exists or that fingerprint changed; do not repeat the probe merely because a
  session restarted. A mode assigned to CI must not be improvised on an unsupported local host as
  a release gate; record the limitation and rely on the declared CI job.
- Keep a bounded count of failed verification invocations during a goal and report the count and
  classifications when any occurred. Do not present one command's repeated package errors as
  independent failures.
