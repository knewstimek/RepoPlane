# Public release metadata

Status: released publicly after the MVP completion gates and privacy checks passed.

## GitHub description

Read-only MCP server for bounded repository search, path facts, tool discovery, and lossless JSONL
queries.

## Suggested topics

`mcp`, `model-context-protocol`, `golang`, `developer-tools`, `coding-agents`, `code-search`,
`ripgrep`, `sqlite`, `jsonl`, `repository-tools`

## Release checklist

- Run `go run ./cmd/repoplane-dev preflight`.
- Run `go run ./cmd/repoplane-dev verify` for schema drift, fresh tests, vet, and build.
- From a clean worktree, run `go run ./cmd/repoplane-dev public-release-check` for the tracked-tree
  and complete reachable Git-history privacy scan.
- Confirm local DB, key, logs, binaries, and temporary files are ignored and untracked.
- Create the public repository only after every preceding check passes.
- Set the description and topics above, enable private vulnerability reporting, and push the
  reviewed default branch.
- Clone into a new temporary directory and rerun build and tests from the public default branch.
