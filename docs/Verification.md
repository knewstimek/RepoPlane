# RepoPlane verification matrix

This matrix maps the completion conditions in [`MVP-Spec.md`](MVP-Spec.md) to executable
evidence. The developer entry point is `go run ./cmd/repoplane-dev verify`; its `test.all` check
runs `go test -count=1 ./...`. Individual tests are named here so the scope of each claim remains
reviewable.

| Completion condition | Evidence |
|---|---|
| Stable typed tools negotiate and run over stdio | `TestStdioNegotiationHasNoOutputPollution`, `TestApplicationExposesCatalogQuery` |
| Empty catalog and search are exact empty results | `TestEmptyCatalogAndSearchReturnExactEmptyResults`, `TestIndexerMissingDefaultRootProducesEmptyCatalog` |
| Broken, duplicate, unsupported, missing, changed, and unregistered catalog sources are distinct | catalog manifest, indexer, and audit tests, including `TestIndexerFlagsExecutableChangeWithoutRevisionChange` |
| Default catalog discovery follows only a safe startup-repository fallback | `TestParseDefaultsCatalogRootToNestedStartupRepository`, `TestParsePrefersWorkspaceCatalogAndExplicitRoots` |
| Partial scope and lower-bound counts survive pagination | `TestServicePaginationPreservesPartialMetadata`, `TestServiceDeadlinePreservesObservedLowerBound`, CP949/EUC-KR backend test |
| Pagination has no gaps or duplicates | catalog/search/data-query pagination tests and `TestResultSetPaginationAndBoundedExpiry` |
| Source changes between pages are rejected | `TestCursorDetectsSourceChangeBetweenPages`, `TestContentHashRefDetectsSourceChange` |
| UTF-8, BOM, CP949, EUC-KR, and mixed newlines preserve stated semantics | textcodec tests and path-facts encoding/newline tests |
| Large JSON integers retain precision | `TestJSONLPreservesLargeIntegerProjectionAndPagination` |
| Oversized and malformed JSONL records follow explicit policies | `TestJSONLOversizedRecordHasExplicitPolicy`, malformed skip/fail tests |
| Lexical, symlink, and junction escapes fail closed unless an exact runtime read path is approved | workspace root/grant tests, including the Windows junction test; application tests also cover an external state directory on another Windows volume |
| Link facts remain observations | path-facts symlink, junction, and hardlink tests |
| MCP stdout contains protocol frames only | child-process stdio test |
| Public schemas match the registered tools | schema generation drift and cursor-only contract tests |
| Public errors do not expose internal diagnostics | `TestPublicErrorUsesStableSanitizedCodes` |
| Public tree excludes common local-identity and secret patterns | `TestPublicTreeHasNoLocalIdentityOrSecretMaterial` |
| SQLite is replaceable behind domain interfaces and migrates explicitly | compile-time repository assertion plus SQLite atomicity, migration, ordering, pagination, and expiry tests |
| Durable records survive cache deletion | `TestRecordDatabaseIsIndependentFromCacheDatabase` |
| Concurrent record updates use optimistic concurrency | checkpoint CAS/history and concurrent-CAS tests |
| Report import is bounded, idempotent, and omits raw diagnostics | records importer idempotency, oversized, and sensitive-diagnostic tests |
| Verification validity becomes stale after workspace change | `TestImportReportIsIdempotentAndBecomesStale` |
| Mutation and external read tools require a host pre-grant or one-time user-approved runtime lease | `TestApplicationGrantsExternalReadAndWriteAtRuntime`, runtimeaccess decline/expiry/revoke tests |
| Live source/workspace/state/HTTP configuration is one-shot approved and switches complete bundles | `TestRuntimeConfigurationUsesOneShotMCPApproval`, `TestApplicationReconfiguresSourcesWorkspaceStateAndHTTPAtRuntime` |
| Portable memory survives a changed absolute workspace path without exporting secrets | `TestPortableMemoryExportRestoreRebindsAndExcludesSecrets`, `TestRecordRepositoryTransferPreservesHistoryAndRebindsWorkspace`, `TestApplicationExportsPortableMemoryAtRuntime` |
| Cache authorization is runtime-gated and keeps a private host key | config, `TestApplicationPreservesHostGrantedRunnerAndCache`, and `TestRuntimeCacheAuthorizationFailsClosed` |
| Cache keys preserve argv/config/input/runtime distinctions | `TestCacheKeyPreservesArgvOrderAndConfiguration` |
| Cache qualification follows dynamic verification validity | `TestImportReportIsIdempotentAndBecomesStale` qualification assertions |
| Verified miss, reuse, conflict, bypass, corruption and output bytes are explicit | Runner cache integration tests |
| Same cache output converges and different output quarantines under concurrency | SQLite cache observation tests |
| Whole-root restore is staged and removes undeclared prior output | `TestIsolatedRootMaterializationReplacesWholeTree` on Windows/Linux |
| Cache pins and expiry are bounded | `TestCachePinsAndExpiryAreBounded` and Runner retention tests |
| MCP discovery remains bounded without output-schema removal | `TestCompactToolSchemaFootprintStaysBounded` |
| Tool footprint bytes remain reproducible and distinct from model-token claims | `TestGeneratedFootprintIsCurrentAndSeparatesContractParts` |
| Record compact options preserve defaults, exact fields, pagination, and receipt semantics | `TestRecordPayloadProjectionAndMutationReceipt`, `TestRecordProjectionSurvivesCursorPagination`, `TestCompactRecordOptionsRejectInvalidValues` |
| Optional host facts remain typed, searchable, non-blocking, and conflict-aware | `TestHostFactIsTypedSearchableAndWarnsOnConflict`, `TestHostFactValidationIsSeparateFromOrdinaryMemo`, `TestHostRefAddsOptionalFactCheckAndConflictWarning` |
| Git history and symbol adapters preserve channel, absence, scope, and validity | search adapter backend/service tests |
| Frontmatter and JSON/log/CSV/TSV parsing remains strict, bounded, and precise | catalog and data-query Stage 7 tests |
| HTTP auth, audience, scope, Origin/Host, and official-client negotiation work | `internal/httptransport` profile/auth/E2E tests |
| HTTP audit admission is fail-closed, redacted, and bounded | audit adapter and HTTP middleware tests |

## Release commands

```text
go run ./cmd/repoplane-dev preflight
go run ./cmd/repoplane-dev verify
go run ./cmd/repoplane-dev public-release-check
```

The first two commands write bounded `check-report.v1` files under `.tmp/reports`. The public
release check additionally requires a clean worktree and scans both the tracked tree and reachable
Git history. CI runs preflight and verify on Windows and Linux and runs `go test -race ./...` on
Linux. A Windows race run is not a release gate because its availability depends on the installed C
toolchain and race runtime; race correctness is gated by the Linux CI job.

For `main` pushes, CI also runs `public-release-check` from a full-history checkout. The guarded
`Release` workflow requires that exact commit's CI run to succeed, so it does not rerun the same
Windows, Ubuntu, and Linux race jobs. Tag pushes are excluded from CI; release publication instead
builds the declared archives, verifies their embedded version and checksums, and retains a compact
post-publication receipt.

Failed developer checks emit only their check ID, status, exit code, duration, summary, and bounded
diagnostics. Workspace, temporary, and user-home paths are redacted before stderr output so CI
failures remain actionable without publishing host-specific paths.

## Deliberate MVP limits

The frozen MVP refuses sources larger than its documented bounded-read limits instead of silently
streaming an unbounded fallback. Subsequent slices add opt-in records, registered execution and
 qualified cache reuse. Later slices add evidence-separated search adapters and opt-in authenticated
HTTP while keeping stdio unchanged. RepoPlane still does not expose arbitrary commands, claim
semantic coverage, or parse source languages as a universal symbol graph.
