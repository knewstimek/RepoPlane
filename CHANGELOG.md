# Changelog

Notable changes to RepoPlane are documented here.

## Unreleased

### Added

- `project_records(mode=get_topic)` opens a unique current memo by exact `topic_key`, with optional
  exact `scope` and `configuration`; ambiguous keys return bounded candidates. Search also accepts
  exact topic, scope, configuration, validity, and update-time filters, plus `match_mode=all`.
- Memo title, summary, historical-observation/current-guidance marker, assertion time, and atomic
  successor creation. Superseded memo reads retain their content and point to the successor.
  Detailed memo reads warn when a cited Git basis differs, is dirty, or cannot be observed,
  without treating that as proof that the memo is wrong.
- `project_records(mode=resume)` returns a checkpoint's next action, recorded change summary,
  remaining checks, and evidence in one call; checkpoint writes accept `change_summary` without
  inferring Git changes.
- Typed MCP call metadata reports serialized structured-result bytes and server duration.
  It also reports text-content bytes, exposing full-read compatibility duplication. Missing
  registered capabilities give bounded catalog and runtime-configuration next steps.

### Changed

- Memo and catalog warnings now use shorter messages while retaining their warning codes and
  actionable references.
- Record search defaults to eight brief cards within 8 KiB, ranks current records and multi-term
  coverage first, and retains cursor paging. Explicit `response_view=discovery` restores the prior
  preview shape; `full` and `payload_fields` expose content. Record list and ID-based get defaults
  remain available.
- Checkpoint, memo, and report-import writes default to ID/revision receipts. Typed MCP text
  fallback for brief search, resume, and receipts no longer repeats the full structured payload.
- Pure memo and checkpoint queries avoid an unnecessary Git status probe, so these reads also work
  in workspaces without Git.
- Tool descriptions now prompt agents to record a stable topic key, concise discovery metadata,
  explicit time meaning, and invalidation conditions for reusable decisions, and a change summary
  with background refs for checkpoints. These fields remain optional for older clients.
- New non-host memos without an explicit content time return `memo_time_unknown` in structured
  warnings and the short text receipt. The write succeeds and does not invent an `as_of` value.
- `resume` now requires every goal-query term, preventing a distinctive multi-term goal from
  mixing with older checkpoints that share only one common word. If nothing matches all terms,
  it returns short partial-match candidates with a warning; `match_mode=any` requests broad matching.

### Fixed

- `project_records` infers search from a nonempty query. Empty calls and common missing read inputs now return short, actionable errors instead of opaque mode or validation failures.

## [1.1.7] - 2026-09-21

### Added

- A bundled `repo-memory` Codex skill that guides agents to consult RepoPlane before repository
  work and retain only verified, reusable background knowledge afterward.

### Changed

- The Windows release archive now includes a pinned, checksum-verified ripgrep executable and its
  license files. RepoPlane prefers the adjacent `rg.exe` before looking on `PATH`.
- When ripgrep is absent, RepoPlane starts and returns an explicit `unsupported` search result with
  an actionable `ripgrep_unavailable` warning; path facts mark the basename scan partial. Git
  history and configured symbol search remain available.
- Common MCP search, query, write, and inspection choices now appear as input-schema enums within
  the compact tool-contract budget. Cursor-only calls remain valid, and `memo_write.source` is
  correctly marked required.
- The README introduction now explains bounded search, durable evidence, and gated registered
  execution, with three task comparisons and legacy Korean encoding support.

## [1.1.6] - 2026-09-20

### Changed

- Runner limit errors for broad input/output globs now include bounded structured details naming
  the resource, limit kind, responsible pattern, configured maximum, observed lower bound, ignored
  path policy, and a narrower-glob hint.
- CI now cancels superseded runs, avoids duplicate branch-push and pull-request execution, limits
  race tests to main source changes, and uses a lightweight release-compatible path for
  documentation-only commits while retaining Ubuntu and Windows verification for product changes.

### Fixed

- Windows `run_prepare` now rejects direct `.ps1` executable references with
  `unsupported_script_type` and guidance to declare PowerShell plus the script argument, instead of
  reporting a ready plan that later fails to start with an opaque internal error.

## [1.1.5] - 2026-09-20

### Changed

- MCP tool errors now carry a safe message and correlation ID in structured JSON error text.
  Durable-write failures distinguish validation, invalid transitions, revision conflicts, and
  storage failures, and report whether the mutation was not applied or requires read-back.
- Restored a self-contained README with installation, tool usage, permissions, local state, and
  operational limits. The private `docs/` directory is no longer tracked.
- The release workflow now generates public notes from the matching `CHANGELOG.md` section instead
  of requiring a tracked document for every version.
- Paused public contribution intake and automatic dependency-version pull requests while the
  project is maintained directly.

### Fixed

- Typed host-fact validation failures now return `invalid_argument` with
  `mutation_state=not_applied` instead of an internal error with unknown mutation state. Regression
  coverage includes normal `memo.v2` creation, receipt read-back, and rejected incomplete host
  payloads. Safe field-specific messages identify invalid host fields, and nullable `services` and
  `paths` now agree between the MCP tool contract, writer, and stored-record schema.

## [1.1.4] - 2026-09-16

### Added

- Local `repoplane usage` report for observed MCP calls, serialized request/result bytes, duration,
  errors, approvals, and reused runs. It stores daily aggregates without payloads or savings estimates.

### Changed

- `run_prepare` returns an input count instead of repeated input hashes; `run_inspect` returns a
  concise run summary by default. Detail, stdout, stderr, and artifact inspection now return
  stored record/file references by default; `response_view=bytes` explicitly requests content.
- Clarified runtime configuration, record search/write, and data-query limit tool descriptions.
- The `run_inspect` tool schema now explains how local agents resolve `state:` file refs and why
  HTTP clients must request bounded bytes, without depending on README discovery.

## [1.1.3] - 2026-09-16

### Changed

- Coding-agent guidance now tells Codex hosts to discover deferred `mcp__repoplane__` tools through
  `ALL_TOOLS` before treating RepoPlane as unavailable, and shows an explicit multi-root catalog
  override without adding a bootstrap tool.

### Fixed

- Catalog status and missing-ID diagnostics now report bounded workspace-relative catalog
  candidates that exist outside configured roots, so agents can repair nested-repository source
  selection without reading repository documentation or silently merging executable declarations.

## [1.1.2] - 2026-09-16

### Fixed

- Catalog refresh now reactivates a previously stored deterministic generation when workspace
  sources return to an earlier state, instead of terminating MCP startup on a SQLite uniqueness
  conflict.
- The Windows Runner test fixture now emits probe output with `cmd` built-ins, avoiding unrelated
  PowerShell cold-start latency in the release verification gate.

## [1.1.1] - 2026-09-16

### Added

- An opt-in `toolbox.v1` MCP surface with five fixed authorization-aligned tools, lazy compact or
  complete operation contracts, deterministic content-bound schema handles, strict dispatch through
  the existing concrete request types and handlers, and fixed discovery. The 14-tool `typed.v1`
  surface remains the default.
- A reference harness contract cache that inserts handles automatically while tracking cached
  contracts separately from active model-context residency, forcing rehydration after compaction.

### Changed

- Typed and toolbox surfaces now derive from one operation registry, and generated schema footprint
  data reports both surfaces. Startup `--tool-surface` selection is read-only at runtime.
- Toolbox authorization, approval, and HTTP audit retain the resolved concrete operation identity.

### Fixed

- Verification diagnostics now label paths beneath `GOTMPDIR` as `GO_TMP` instead of the generic
  `TEMP`, making Go build-workspace failures distinguishable without exposing a local absolute path.
- Windows CI installs the pinned upstream ripgrep release with its published SHA-256 checksum,
  avoiding Chocolatey feed failures and misleading zero exit statuses.
- Runner executable-version probes allow bounded Windows cold-start overhead without making the
  first probe fail while an identical warm probe succeeds.

## [1.1.0] - 2026-09-16

### Added

- A guarded `Release` workflow and typed `release.dispatch` capability that publish the canonical
  tracked release note, declared platform archives, checksums, annotated tag, and compact receipt.
- Optional typed host facts through the existing `memo_write`/`project_records` surface, plus a
  catalog `host_ref` argument that adds non-blocking host context and OS/role conflict warnings to
  `run_prepare` without adding another MCP tool.
- Optional stable memo `topic_key` identities through the existing tools, with bounded related-topic
  hints and atomic prevention of duplicate current topics.

### Changed

- Main-branch CI now includes the complete public-release privacy scan, while tag pushes no longer
  repeat the same Windows, Ubuntu, and Linux race jobs.
- Strict YAML manifest errors now explain that commas delimit fields in flow-style mappings and
  recommend quoting scalar values when that pattern produces an unknown field.
- Record and Runner documentation now distinguishes importer idempotency from semantic memo
  comparison, server observations from intention checkpoints, and execution-source fingerprints
  from complete plan identity. It also includes an explicit nested-repository source configuration.
- Repeated write-contract descriptions are compacted so the 14-tool complete contract remains under
  the existing 34 KiB budget after adding typed host facts.
- Topic-addressed memo updates preserve their `(scope, configuration, topic_key)` identity; rename
  requires superseding the old record and creating a new one, while unkeyed free-form memos retain
  their existing behavior.

### Fixed

- Runner Git preflight now starts from the capability's resolved execution working directory, so a
  Git repository nested below the RepoPlane workspace root is detected and rechecked correctly.
- Default catalog discovery now falls back to a nested Git repository's `catalog/` when
  `WORKSPACE/catalog` is absent and either the process startup directory identifies that repository
  or it is the only direct child candidate; explicit roots take precedence and ambiguity stays empty.

## [1.0.3] - 2026-09-15

### Added

- Real bounded lexical search for durable records, including relevance ordering over record
  identity, metadata, and textual payload values.

### Changed

- `project_records(mode=search)` now returns a compact kind-specific discovery payload by default;
  memo discovery derives a 320-character preview, while explicit `payload_fields` can still request
  exact stored fields and `get` remains the full-record path.
- The search contract adds only one compact `query` input field, keeping all 14 tools inside the
  existing 34 KiB complete-contract regression budget.

## [1.0.2] - 2026-09-15

### Fixed

- Emit explicit `mode=form` and object `requestedSchema` fields for confirmation-only MCP
  elicitation so strict clients can complete runtime workspace changes and other approval-gated
  operations.

## [1.0.1] - 2026-09-15

### Added

- Runtime MCP elicitation for local stdio external read paths, intention writes, report import,
  registered Runner execution, and qualified cache reuse without configuration edits or restart.
- A stable `runtime_access` tool for inspecting, explicitly granting, and revoking ephemeral access.
- A compact `memory_backup` runtime tool plus `repoplane memory export/restore` commands for portable
  durable records, complete revision history, and retained Runner evidence across machine/path resets.
- A `runtime_config` control tool for live add/remove/replace/refresh of catalog, candidate, rule,
  and symbol sources; atomic workspace/state switches; and child HTTP start/stop.

### Changed

- All 14 typed tools are stable in local discovery; CLI/TOML values now provide compatible startup
  pre-registration while local stdio configuration changes happen at runtime.
- Workspace search, path explanation, and data query can resume after an approved read-only parent
  path grant while catalog, importer, Runner, and cache writes remain inside the primary workspace.
- Portable memory archives exclude profiles, tokens, keys, audit state, and regenerable cache; restore
  refuses non-empty targets and rebinds records to the current workspace identity.
- Windows workspace/state containment checks canonicalize path casing before comparison, preventing
  the state directory itself from being accepted as a backup destination.

## [1.0.0] - 2026-09-15

### Added

- A durable project-records store, separate from the rebuildable catalog cache.
- Read-only `project_records` queries with bounded, snapshot-based pagination and explicit validity.
- Opt-in checkpoint and memo write tools with optimistic concurrency.
- An opt-in verification-report importer with tracked checklist validation and idempotent imports.
- Versioned schemas for checkpoints, memos, verification checks, and verification results.
- Repository development commands for preflight, verification, and public-release checks.
- Full-implementation roadmap, Records specification, and record write-boundary ADR.
- Opt-in registered-capability execution through `run_prepare`, `run_execute`, and `run_inspect`.
- Capability-scoped preflight observations and durable environment, run, and artifact records.
- Bounded stdout/stderr and content-addressed artifact capture with explicit retention, missing,
  corruption, and partial states.
- Accepted Preflight, Artifact/Run Receipt, and Runner specifications plus the Runner authority ADR.
- Conservative Cache specification, implementation plan, and qualification ADR.
- Opt-in Runner cache observation and verified reuse with HMAC keys, current verification
  qualification, false-hit quarantine, safe materialization, and cache-aware run receipts.
- A regenerable SQLite cache-entry index with bounded expiry and active artifact-blob pins.
- Evidence-separated Git-history and configured symbol-index search adapters.
- Strict Markdown frontmatter catalogs and bounded JSON Pointer, log, CSV, and TSV queries.
- Opt-in stateless Streamable HTTP with local bearer or external OAuth introspection.
- Per-tool HTTP scopes, audience/expiry, Origin/Host/TLS, rate/concurrency, and cancellation gates.
- A separate bounded, redacted HTTP admission/completion audit store.
- A deterministic MCP contract-footprint report that separates full, input, output, and
  name/description/input serialized bytes without presenting them as model-token measurements.
- Optional exact `project_records.payload_fields` projection and compact write/import receipts.

### Changed

- Catalog audit now recognizes and fingerprints fixed workspace script targets passed through an
  interpreter's `argv_template`, without treating dynamic argument placeholders as registrations.
- CI and contributor guidance now use the repository verification workflow.
- Documentation workflow now requires roadmap-status consistency checks and copyable host examples
  for opt-in tools.
- Codex setup guidance now shows how to enable checkpoint, memo, and report-import tools.
- Windows update guidance now rotates an in-use MCP executable before installing its replacement.
- Windows batch wrappers and POSIX shebang capabilities execute through platform adapters while
  preserving argument boundaries; arbitrary request-supplied shell commands remain prohibited.
- Catalog execution policy is additive, and existing manifests and records databases remain valid.
- Compact MCP tool schemas now have regression budgets to prevent accidental model-context growth.
- MCP schema wording is deduplicated without removing contract fields, and tighter 30 KiB/5,500-byte
  discovery budgets include the new cache contract.
- New runs use `run-receipt.v2`; existing v1 receipts remain readable, and reused artifacts retain
  source provenance without claiming a subprocess ran.
- The registered verification capability scopes Go inputs to source directories instead of
  hashing ignored temporary clones into execution plans.
- The compact MCP schema budget is 32 KiB after adding Stage 7 fields; no gateway or new tool was
  added, and stdio remains the default transport.
- The adopted numbered roadmap is complete; ongoing work is compatibility, dogfooding measurement,
  optional adapters, and release maintenance.
- README, security, storage, and verification documentation describe the Records capabilities and
  their opt-in write boundaries.
- The 11 standard typed tools remain stable while tool names and HTTP scope requirements now share
  one fail-closed source; generic toolbox routing and conversation-dependent tool lists remain out.
- Record responses remain full by default; callers can explicitly remove repeated payload bytes
  without losing revision, validity, evidence, pagination, duplicate, or warning semantics.
- Agent guidance now makes RepoPlane the first choice for matching discovery, registered
  verification/release, and recovery workflows while documenting explicit fallbacks to direct
  tools for absent, unsupported, or partial capabilities.
- Developer verification now emits bounded, path-redacted details for failed checks instead of
  leaving CI with only a generic failure status.
- The combined symbol/Git adapter fixture now uses the same canonical workspace root as production,
  avoiding false workspace-escape failures under Windows runner junction paths.
- Agent guidance now names RepoPlane MCP explicitly, and README usage clarifies Runner's bounded
  opt-in role plus consistent encrypted backup and restore of local state.
