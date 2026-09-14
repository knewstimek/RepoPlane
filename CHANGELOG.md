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
- README, security, storage, and verification documentation describe the Records capabilities and
  their opt-in write boundaries.
