# Changelog

Notable changes to RepoPlane are documented here.

## Unreleased

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
