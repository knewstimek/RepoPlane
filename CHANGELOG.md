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

### Changed

- CI and contributor guidance now use the repository verification workflow.
- README, security, storage, and verification documentation describe the Records capabilities and
  their opt-in write boundaries.
