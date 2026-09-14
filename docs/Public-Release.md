# Public release metadata

Use this metadata only after the MVP completion gates and privacy checks pass.

## GitHub description

Read-only MCP server for bounded repository search, path facts, tool discovery, and lossless JSONL
queries.

## Suggested topics

`mcp`, `model-context-protocol`, `golang`, `developer-tools`, `coding-agents`, `code-search`,
`ripgrep`, `sqlite`, `jsonl`, `repository-tools`

## Release checklist

- Run `go generate ./internal/mcpserver` and verify no schema drift.
- Run fresh tests, vet, and Windows/Linux builds.
- Run the tracked-tree and Git-history privacy/secret scan.
- Confirm local DB, key, logs, binaries, and temporary files are ignored and untracked.
- Create the public repository only after every preceding check passes.
- Set the description and topics above, enable private vulnerability reporting, and push the
  reviewed default branch.
- Clone into a new temporary directory and rerun build and tests from the public default branch.
