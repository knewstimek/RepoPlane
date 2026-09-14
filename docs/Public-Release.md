# Public release metadata

Status: public repository released; versioned release process adopted for v1.0.0.

## GitHub description

Repository control-plane MCP server for bounded search, path facts, durable task evidence, and
opt-in registered execution.

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
- Prepare notes using [Release-Notes-Spec.md](Release-Notes-Spec.md) and freeze `Unreleased` under
  the version and release date.
- Build only declared platform archives with `-trimpath` and an injected tag version; generate
  `SHA256SUMS.txt` and verify every archive before upload.
- Create an annotated SemVer tag only from a clean commit whose required GitHub Actions jobs passed.
- Publish a non-draft, non-prerelease GitHub Release from the same tag and tracked notes, then verify
  its tag, assets, checksums, and displayed metadata through the GitHub API.
