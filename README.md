# RepoPlane

[Changelog](CHANGELOG.md)

RepoPlane is a read-first [Model Context Protocol](https://modelcontextprotocol.io/) server that
helps coding agents discover repository tools, inspect path facts, search source, query large
files, and recover durable verification and task records without pulling an entire workspace into
context. Mutation capabilities are disabled unless the host explicitly enables them.

It is built around a simple rule: **return bounded evidence and say exactly what was not
observed**. Every search reports its scope, count semantics, truncation state, warnings, and a
fixed snapshot cursor.

## Why RepoPlane?

Repositories already contain useful scripts, manifests, rules, and data, but agents often have to
guess where they are or read far too much to find them. RepoPlane provides five small read tools:

| Tool | What it answers |
|---|---|
| `catalog_query` | What reusable tools are declared here? Are declarations missing or stale? |
| `workspace_search` | Which filenames or lines match within this explicit scope? |
| `path_explain` | What is this path really, and what Git, link, encoding, newline, and rule facts apply? |
| `data_query` | Can I read this exact text range or filter/project these JSONL records safely? |
| `project_records` | Which verification, checkpoint, and memo records exist and are they current? |

RepoPlane never executes catalog entries. Hosts may opt into the separate `checkpoint_write`,
`memo_write`, and `check_report_import` tools. Runner, artifact cache, and network transport remain
out of scope.

## Highlights

- Official MCP Go SDK with stdio transport
- Workspace boundary checks against lexical and symlink/junction escapes
- Fixed, HMAC-authenticated pagination cursors with a 30-minute TTL
- Deterministic YAML/JSON catalog indexing and bounded executable-candidate audits
- Ripgrep-backed filename, exact-text, and regex search with explicit scope policies
- Explicit UTF-8, CP949, and strict EUC-KR decoding
- Lossless JSONL integer handling, including values larger than JavaScript's safe integer range
- Database-independent domain interfaces with a pure-Go SQLite adapter
- Stable public error codes without leaking local paths or database diagnostics
- A durable `records.db` separated from the regenerable search cache
- Idempotent `check-report.v1` import with checklist revision and conservative freshness
- Optimistic concurrency for checkpoint and memo updates

## Requirements

- Go 1.26 or newer (to build from source)
- [ripgrep](https://github.com/BurntSushi/ripgrep) available as `rg` on `PATH`

RepoPlane currently targets Windows and Linux.

## Build

From a repository checkout:

```sh
go test ./...
go build -trimpath -o bin/repoplane ./cmd/repoplane
```

On Windows, use `bin/repoplane.exe` as the output path if desired.

To update an installed Windows binary that may still be serving an MCP session, build the
replacement first, resolve the current installation from `PATH`, then rotate and replace it:

```powershell
go build -trimpath -o bin/repoplane.exe ./cmd/repoplane
$installed = (Get-Command repoplane -CommandType Application).Source
$backup = Join-Path (Split-Path $installed) ("old_repoplane_{0}.exe" -f (Get-Date -Format yyyyMMddHHmmss))
Move-Item -LiteralPath $installed -Destination $backup
Copy-Item -LiteralPath bin/repoplane.exe -Destination $installed
```

New MCP sessions use the replacement. Remove the rotated binary only after the older process has
exited.

## Configure an MCP client

Build the binary, place it on `PATH`, then add a stdio server entry to your MCP client. Replace the
placeholder values with local paths; the state directory must be outside the workspace.

```json
{
  "mcpServers": {
    "repoplane": {
      "command": "repoplane",
      "args": [
        "--workspace", "WORKSPACE",
        "--state-dir", "STATE_DIRECTORY"
      ]
    }
  }
}
```

Available flags:

```text
--workspace PATH        trusted workspace root; defaults to the current directory
--state-dir PATH        local database and cursor-key directory; must be outside the workspace
--catalog-root PATH     workspace-relative catalog file or directory; repeatable; default: catalog
--candidate-root PATH   executable-candidate directory; repeatable; defaults: tools, scripts
--rule-file NAME        rule filename searched from root to target; repeatable; default: AGENTS.md
--enable-intention-writes  expose checkpoint_write and memo_write; default: false
--enable-report-import     expose check_report_import; default: false
```

MCP frames are the only data written to stdout. Startup failures and diagnostics go to stderr.

Typical tool inputs are intentionally small:

```json
{"mode":"search","query":"schema validation","item_limit":10}
{"mode":"regex","pattern":"TODO|FIXME","root":"internal","item_limit":50}
{"path":"config/settings.yaml","encoding":"utf-8"}
{"mode":"jsonl","ref":"source:mutable:reports/events.jsonl","fields":["id","status"]}
{"mode":"list","kind":"verification","validity":"current"}
```

These correspond to `catalog_query`, `workspace_search`, `path_explain`, `data_query`, and
`project_records` in that order. Pass only `cursor` plus optional limits for a next-page request.

With report import enabled, a local verification report can be linked to a versioned checklist:

```json
{"path":".tmp/reports/verify.json","checklist_path":"checks/repository.verify.yaml","configuration":"default"}
```

The importer stores a hash and bounded normalized summary, not raw report diagnostics.

## Add a catalog entry

Place YAML or JSON manifests under a configured catalog root. A minimal documentation-only entry
needs an ID, revision, and summary:

```yaml
id: schema.validate
revision: 1
summary: Validate a configuration file against its schema
use_when:
  - changing configuration fields
tags: [configuration, schema, validation]
cache_policy: disabled
```

An optional `execution` block may describe a CLI, but RepoPlane only indexes and audits it. The
`trusted_for_run` field is informational and never grants execution permission.

## Response semantics

List and search responses use one common envelope:

```json
{
  "status": "ok",
  "items": [],
  "counts": {"matched": 0, "relation": "exact", "returned": 0},
  "scan": {"state": "complete", "scope_ref": "scope:..."},
  "truncated": false,
  "next_cursor": null,
  "snapshot_ref": "snapshot:...",
  "warnings": []
}
```

`partial` is not treated as success with missing details hidden. A stopped scan uses
`lower_bound` or `unknown` rather than inventing an exact total. Cursor pages come from a fixed
ordered result set; `data_query` additionally rechecks the source hash between pages and returns
`source_changed` if the file changed or disappeared.

## Local state and removal

RepoPlane stores a regenerable SQLite index, a separate durable `records.db`, and a random
cursor-authentication key in `--state-dir`. It does not write databases into the workspace and
refuses a state directory that resolves inside it.

To uninstall, remove the client configuration entry and binary. After no RepoPlane process is
using it, delete the configured state directory to remove the local index and invalidate cursors.
That deletion also permanently removes checkpoints, memos, and imported verification records;
back up `records.db` first when those records must be retained. No workspace source files need
cleanup.

## Architecture

Feature services depend on domain repositories in `internal/store`, not SQL. SQLite is the first
adapter, implemented in `internal/store/sqlite` with versioned migrations. Regenerable query state
and durable records use separate database files and domain interfaces.

Public JSON Schemas are committed under [`schemas/`](schemas/). Run
`go generate ./internal/mcpserver` after changing a tool contract; tests reject schema drift.

## Security model and limitations

- The host chooses the trusted workspace root and local state directory.
- Requests cannot escape the resolved workspace through `..`, symlinks, or junctions.
- RepoPlane controls ripgrep arguments and never builds a shell command from a query.
- Reads, process output, result counts, response bytes, record sizes, and deadlines are bounded.
- Record mutation and report import tools are hidden unless explicitly enabled by the host.
- Dirty-worktree reports without a content fingerprint are never classified as current.
- The server does not provide authentication because the MVP transport is local stdio.
- JSONL support is deliberately limited to top-level equality filters and field projection.
- Symbol, semantic, Git-history, XML, CSV/TSV, and arbitrary JSONPath queries are out of scope.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and
[`docs/MVP-Spec.md`](docs/MVP-Spec.md) for the precise contract.

## Roadmap

The read-only MVP is complete. Adopted P1/P2 work now proceeds through bounded vertical slices,
starting with durable records and verification; a central orchestrator and general integration
graph are not required. See the [`full implementation roadmap`](docs/Full-Implementation-Roadmap.md),
the [`Records specification`](docs/Records-Spec.md), and the full
[`design document`](docs/Project-Control-Plane-MCP-Design.md).

Contributions that improve portability, database adapters, fixtures, or contract clarity are
welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
