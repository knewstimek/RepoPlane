# MVP verification matrix

This matrix maps the completion conditions in [`MVP-Spec.md`](MVP-Spec.md) to executable
evidence. The authoritative check is `go test -count=1 ./...`; individual tests are named here so
the scope of each claim remains reviewable.

| Completion condition | Evidence |
|---|---|
| Four tools negotiate and run over stdio | `TestStdioNegotiationHasNoOutputPollution`, `TestApplicationExposesCatalogQuery` |
| Empty catalog and search are exact empty results | `TestEmptyCatalogAndSearchReturnExactEmptyResults`, `TestIndexerMissingDefaultRootProducesEmptyCatalog` |
| Broken, duplicate, unsupported, missing, changed, and unregistered catalog sources are distinct | catalog manifest, indexer, and audit tests, including `TestIndexerFlagsExecutableChangeWithoutRevisionChange` |
| Partial scope and lower-bound counts survive pagination | `TestServicePaginationPreservesPartialMetadata`, `TestServiceDeadlinePreservesObservedLowerBound`, CP949/EUC-KR backend test |
| Pagination has no gaps or duplicates | catalog/search/data-query pagination tests and `TestResultSetPaginationAndBoundedExpiry` |
| Source changes between pages are rejected | `TestCursorDetectsSourceChangeBetweenPages`, `TestContentHashRefDetectsSourceChange` |
| UTF-8, BOM, CP949, EUC-KR, and mixed newlines preserve stated semantics | textcodec tests and path-facts encoding/newline tests |
| Large JSON integers retain precision | `TestJSONLPreservesLargeIntegerProjectionAndPagination` |
| Oversized and malformed JSONL records follow explicit policies | `TestJSONLOversizedRecordHasExplicitPolicy`, malformed skip/fail tests |
| Lexical, symlink, and junction workspace escapes are rejected | workspace root tests, including the Windows junction test |
| Link facts remain observations | path-facts symlink, junction, and hardlink tests |
| MCP stdout contains protocol frames only | child-process stdio test |
| Public schemas match the registered tools | schema generation drift and cursor-only contract tests |
| Public errors do not expose internal diagnostics | `TestPublicErrorUsesStableSanitizedCodes` |
| Public tree excludes common local-identity and secret patterns | `TestPublicTreeHasNoLocalIdentityOrSecretMaterial` |
| SQLite is replaceable behind domain interfaces and migrates explicitly | compile-time repository assertion plus SQLite atomicity, migration, ordering, pagination, and expiry tests |

## Release commands

```text
go generate ./internal/mcpserver
go test -count=1 ./...
go vet ./...
go build -trimpath ./cmd/repoplane
```

CI runs these checks on Windows and Linux and runs `go test -race ./...` on Linux. Cross-compiled
Windows and Linux amd64 binaries are also built during the local release audit. A Windows race
run is not a release gate because its availability depends on the installed C toolchain and race
runtime; race correctness is gated by the Linux CI job.

## Deliberate MVP limits

RepoPlane refuses sources larger than its documented bounded-read limits instead of silently
streaming an unbounded fallback. It does not execute tools, write project records, expose a network
transport, authenticate users, or claim semantic/symbol/Git-history coverage.
