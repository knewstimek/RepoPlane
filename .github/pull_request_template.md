## Summary

Describe the observable change and why it belongs in RepoPlane.

## Verification

- [ ] `go generate ./internal/mcpserver`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] Public schemas and specifications are updated when contracts changed
- [ ] Fixtures and output contain no secrets, personal paths, or unrelated project identifiers
- [ ] MCP stdout remains protocol-only
