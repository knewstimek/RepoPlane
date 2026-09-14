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
| `project_records` | Which verification, checkpoint, memo, environment, run, and artifact records exist? |

RepoPlane does not execute catalog entries by default. Hosts may separately opt into
`checkpoint_write`/`memo_write`, `check_report_import`, and the registered-capability Runner. The
Runner adds exactly `run_prepare`, `run_execute`, and `run_inspect`; arbitrary commands remain out
of scope. A separate host opt-in enables qualified cache observation and reuse inside those same
three tools without adding a gateway or another MCP tool.

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
- Capability-scoped environment preflight with secret values withheld
- Durable run receipts and bounded stdout, stderr, and captured-artifact inspection
- Prepare/execute revalidation that ignores unrelated worktree changes
- HMAC-keyed, qualification-gated cache reuse with observe, bypass, conflict, corruption, and
  false-hit quarantine states

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

The configuration above keeps RepoPlane read-only. To expose checkpoint/memo writes, local
verification-report import, and registered capability execution in Codex, add the independent
opt-in flags to the user-level MCP entry and restart Codex:

```toml
[mcp_servers.repoplane]
command = "repoplane"
args = [
  "--workspace", "WORKSPACE",
  "--state-dir", "STATE_DIRECTORY",
  "--enable-intention-writes",
  "--enable-report-import",
  "--enable-runner",
  "--enable-cache",
]
```

Enabling these flags exposes the tools; it does not invoke them automatically. Runner execution
still requires the MCP client's tool approval and a catalog entry with `trusted_for_run: true`.
Use them only for a trusted workspace and keep the state directory outside that workspace.

Available flags:

```text
--workspace PATH        trusted workspace root; defaults to the current directory
--state-dir PATH        local database and cursor-key directory; must be outside the workspace
--catalog-root PATH     workspace-relative catalog file or directory; repeatable; default: catalog
--candidate-root PATH   executable-candidate directory; repeatable; defaults: tools, scripts
--rule-file NAME        rule filename searched from root to target; repeatable; default: AGENTS.md
--enable-intention-writes  expose checkpoint_write and memo_write; default: false
--enable-report-import     expose check_report_import; default: false
--enable-runner          expose run_prepare, run_execute, and run_inspect; default: false
--enable-cache           allow qualified Runner cache observation and reuse; requires Runner
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

An optional `execution` block may describe a CLI. It remains documentation-only unless the host
enables Runner and the entry explicitly sets `trusted_for_run: true`:

```yaml
execution:
  kind: cli
  executable_ref: go
  cwd: .
  argv_template: [test, ./...]
  trusted_for_run: true
  timeout_sec: 300
  artifact_mode: metadata
  preflight:
    - {id: go.version, kind: executable, ref: go, requirement: required, argv: [version]}
inputs: [go.mod, go.sum, "**/*.go"]
outputs: [.tmp/reports/verify.json]
```

Call `catalog_query(mode=get)` to obtain the current capability revision, then
`run_prepare` with that ID/revision. Execute the returned plan ID once with `run_execute`; use
`run_inspect` for status, cancellation, bounded stdout/stderr ranges, or a retained artifact ref.
Preflight runs during prepare—there is intentionally no fourth environment execution tool.

Pure, side-effect-free transforms may opt into observation first. The full inputs, outputs,
runtime identity and purity assumptions are explicit; ordinary build/test capabilities should use
their native build cache and remain disabled unless they pass a differential qualification check:

```yaml
cache_policy: observe
cache:
  contract_revision: 1
  output_contract: schema-output.v1
  key_checks: [runtime.version]
  qualification_checks: [cache.schema-output.differential]
  restore_policy: missing_or_matching
  assumptions:
    inputs_complete: true
    outputs_complete: true
    external_state: none
    nondeterminism: none
    side_effects: declared_outputs_only
```

Use `cache_mode: bypass` in `run_prepare` to force the normal process path for cache on/off
comparisons. `verified` policy additionally requires a current passed verification record for each
qualification check. A miss or rejected cache never blocks the otherwise valid Runner execution.

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

RepoPlane stores a regenerable SQLite index and cache-entry index, a separate durable `records.db`,
random cursor/cache authentication keys, and opt-in Runner streams/artifact blobs in `--state-dir`. It does not
write databases into the workspace and refuses a state directory that resolves inside it. Runner
streams retain the wider of 14 days or the most recent 200 runs; current intention/imported record
references protect older run streams. Raw stream/artifact bytes are never placed in record payloads.

To uninstall, remove the client configuration entry and binary. After no RepoPlane process is
using it, delete the configured state directory to remove the local index and invalidate cursors.
That deletion also permanently removes checkpoints, memos, imported verification records, run
receipts, streams, and captured artifacts;
back up `records.db` first when those records must be retained. No workspace source files need
cleanup.

## Architecture

Feature services depend on domain repositories in `internal/store`, not SQL. SQLite is the first
adapter, implemented in `internal/store/sqlite` with versioned migrations. Regenerable query state
and durable records use separate database files and domain interfaces.

Public JSON Schemas are committed under [`schemas/`](schemas/). Run
`go generate ./internal/mcpserver` after changing a tool contract; tests reject schema drift.
The compact MCP schema set has a regression budget (30 KiB overall and 5,500 bytes for the three
Runner tools). Its wording is deduplicated without dropping response fields, limits, defaults, or
state semantics; cross-tool references are avoided because each MCP tool schema must stand alone.

## Security model and limitations

- The host chooses the trusted workspace root and local state directory.
- Requests cannot escape the resolved workspace through `..`, symlinks, or junctions.
- RepoPlane controls ripgrep arguments and never builds a shell command from a query.
- Reads, process output, result counts, response bytes, record sizes, and deadlines are bounded.
- Record mutation, report import, and Runner tools are hidden unless explicitly enabled by the host.
- Runner accepts registered capability IDs and typed arguments, never request-supplied executables,
  argv arrays, or shell strings. It records environment-variable presence without values.
- Captured output is raw trusted-tool output and is not content-redacted automatically; use
  `metadata` mode for sensitive outputs and never pass credentials as catalog arguments.
- Cache is disabled by default and requires host opt-in, a cache manifest, captured outputs, stable
  runtime identities, purity assumptions, and—before reuse—current qualification evidence.
- Cache materialization never overwrites a differing file. Whole-root replacement is limited to an
  untracked, input-disjoint directory explicitly owned by the capability.
- Dirty-worktree reports without a content fingerprint are never classified as current.
- The server does not provide authentication because the MVP transport is local stdio.
- JSONL support is deliberately limited to top-level equality filters and field projection.
- Symbol, semantic, Git-history, XML, CSV/TSV, and arbitrary JSONPath queries are out of scope.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and
[`docs/MVP-Spec.md`](docs/MVP-Spec.md) for the precise contract.

## Roadmap

The read-only MVP, durable Records, Execution Foundation, and Conservative Cache slices are
complete. Adopted P1/P2 work next proceeds to Search Adapters. See the
[`full implementation roadmap`](docs/Full-Implementation-Roadmap.md), the
[`Records specification`](docs/Records-Spec.md), and the full
[`design document`](docs/Project-Control-Plane-MCP-Design.md).

Contributions that improve portability, database adapters, fixtures, or contract clarity are
welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
