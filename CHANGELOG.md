# Changelog

Notable changes to RepoPlane are documented here.

## Unreleased

### Added

- A guarded `Release` workflow and typed `release.dispatch` capability that publish the canonical
  tracked release note, declared platform archives, checksums, annotated tag, and compact receipt.
- Optional typed host facts through the existing `memo_write`/`project_records` surface, plus a
  catalog `host_ref` argument that adds non-blocking host context and OS/role conflict warnings to
  `run_prepare` without adding another MCP tool.

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

### Fixed

- Runner Git preflight now starts from the capability's resolved execution working directory, so a
  Git repository nested below the RepoPlane workspace root is detected and rechecked correctly.

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
