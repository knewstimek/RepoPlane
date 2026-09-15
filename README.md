# RepoPlane

[Changelog](CHANGELOG.md)

RepoPlane is a read-first [Model Context Protocol](https://modelcontextprotocol.io/) server that
helps coding agents discover repository tools, inspect path facts, search source, query large
files, and recover durable verification and task records without pulling an entire workspace into
context. Local stdio sessions can approve access and reconfigure sources, workspace, state, and an
optional HTTP endpoint at runtime without editing the MCP host configuration or restarting stdio.

It is built around a simple rule: **return bounded evidence and say exactly what was not
observed**. Every search reports its scope, count semantics, truncation state, warnings, and a
fixed snapshot cursor.

## Why RepoPlane?

Repositories already contain useful scripts, manifests, rules, and data, but agents often have to
guess where they are or read far too much to find them. RepoPlane provides focused typed tools for
repository evidence, runtime access, and portable memory:

| Tool | What it answers |
|---|---|
| `catalog_query` | What reusable tools are declared here? Are declarations missing or stale? |
| `workspace_search` | Which filenames, lines, Git changes, or configured symbols match this scope? |
| `path_explain` | What is this path really, and what Git, link, encoding, newline, and rule facts apply? |
| `data_query` | Can I range-read or safely query JSON/JSONL/log/CSV/TSV data? |
| `project_records` | Which verification, checkpoint, memo, environment, run, and artifact records exist? |
| `runtime_access` | Which ephemeral grants are active, and should one be approved or revoked? |
| `runtime_config` | Which live sources and service paths are active, and should they change now? |
| `memory_backup` | Can durable records and retained Runner evidence be exported portably now? |

RepoPlane does not execute catalog entries without authorization. Local stdio calls request user
approval when they first need `checkpoint_write`/`memo_write`, `check_report_import`, the
registered-capability Runner, cache reuse, an external read path, or a memory-backup destination.
Grants last only for the MCP process and can be revoked through `runtime_access`; arbitrary commands
remain out of scope. HTTP
deployments retain host opt-in plus bearer/OAuth scopes and do not use local runtime grants.

## Highlights

- Official MCP Go SDK with default stdio and opt-in authenticated Streamable HTTP
- Workspace boundary checks against lexical and symlink/junction escapes
- In-call MCP approval and retry for ephemeral external reads, writes, Runner, and cache access
- Fixed, HMAC-authenticated pagination cursors with a 30-minute TTL
- Deterministic YAML/JSON/Markdown-frontmatter catalogs and bounded executable-candidate audits
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
- Portable runtime memory export and identity-rebinding restore across machine/path resets
- Prepare/execute revalidation that ignores unrelated worktree changes
- HMAC-keyed, qualification-gated cache reuse with observe, bypass, conflict, corruption, and
  false-hit quarantine states
- Git-history and configured symbol-index search with independent evidence and freshness classes
- Bounded JSON Pointer, log, CSV, and TSV queries plus strict Markdown frontmatter catalogs
- Opt-in authenticated Streamable HTTP with scopes, Origin/Host checks, audit, and request limits

## Requirements

- Go 1.26 or newer (to build from source)
- [ripgrep](https://github.com/BurntSushi/ripgrep) available as `rg` on `PATH`
- Git on `PATH` for the optional history adapter

RepoPlane currently targets Windows and Linux.

## Build

Download versioned Windows and Linux archives plus `SHA256SUMS.txt` from the
[Releases page](https://github.com/knewstimek/RepoPlane/releases). Verify the archive checksum
before installation. Release archives contain the `repoplane` MCP server; `rg` and optional Git
remain host dependencies.

Maintainers publish a prepared release with the guarded `Release` workflow after the exact `main`
commit passes CI. Supply `version`; `notes_file` may be omitted to use the canonical tracked
`docs/releases/vVERSION.md`. RepoPlane Runner exposes the same dispatch as `release.dispatch` with
both typed arguments explicit and `publish=true`, so release logs stay in Actions and the MCP
response remains compact. Manual workflow runs default to a build-only dry run.

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

Build the binary and place it on `PATH`. Codex users can register RepoPlane once without manually
editing TOML:

```text
codex mcp add repoplane -- repoplane
```

With no arguments, RepoPlane uses the MCP process working directory as the primary workspace and
the user cache directory for private state. A generic MCP client entry can also specify fixed paths;
the state directory must be outside the primary workspace.

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

No later TOML edit or RepoPlane restart is required. Access-controlled operations request approval
inside the attempted call. External paths are read-only and only the requested existing file or
directory is added. Inspect or revoke grants explicitly when needed:

```json
{"action":"status"}
{"action":"grant","kind":"read_path","path":"../shared-context.txt"}
{"action":"grant","kind":"cache_reuse"}
{"action":"grant","kind":"memory_export","path":"BACKUP_DIRECTORY"}
{"action":"revoke","grant_id":"GRANT_ID"}
```

For example, when the selected workspace is `gameserver`, a call for `../_ETC2` is retried after
an exact `read_path` approval; no `--read-root`, context-file copy, TOML edit, or restart is needed.

Use `runtime_config` for live service configuration. `status` needs no approval; every change is a
one-shot proposal whose exact action, target, and values are bound to the user's approval. Source
paths are relative to the selected workspace and must exist when added or replaced. Workspace,
state, and HTTP-profile paths are absolute. A successful source change rebuilds and refreshes the
catalog before atomically switching the service bundle. A workspace switch drops external path
grants; active Runner work blocks workspace/state/source switching.
Confirmation-only proposals use an explicit MCP form schema so strict clients can complete the
`elicitation/create` round trip instead of rejecting an ambiguous request shape.

```json
{"action":"status"}
{"action":"add","target":"catalog_root","values":["_ETC2"]}
{"action":"remove","target":"catalog_root","values":["catalog"]}
{"action":"replace","target":"rule_file","values":["AGENTS.md","PROJECT_RULES.md"]}
{"action":"refresh","target":"catalog_root"}
{"action":"select","target":"workspace","values":["WORKSPACE"]}
{"action":"select","target":"state_dir","values":["STATE_DIRECTORY"]}
{"action":"start","target":"http_transport","values":["HTTP_PROFILE"]}
{"action":"stop","target":"http_transport"}
```

The four source targets are `catalog_root`, `candidate_root`, `rule_file`, and `symbol_index`.
The status response returns a compact `configuration` map keyed by those targets plus `workspace`,
`state_dir`, and `http_transport`; a running HTTP endpoint has its profile path as the sole
`http_transport` value. HTTP is a child transport of the stdio control session, so starting or
stopping it does not disconnect stdio. HTTP clients cannot call `runtime_config`.

All CLI arguments below are optional startup pre-registration for compatibility or unattended
hosts; they are not the normal way to change a running session. The legacy enable flags
pre-authorize named capabilities and suppress runtime prompts. They do not invoke a tool
automatically. Runner still requires a catalog entry with `trusted_for_run: true`.

Available flags:

```text
--workspace PATH        trusted workspace root; defaults to the current directory
--state-dir PATH        local database and cursor-key directory; must be outside the workspace
--catalog-root PATH     workspace-relative catalog file or directory; repeatable; default: catalog
--candidate-root PATH   executable-candidate directory; repeatable; defaults: tools, scripts
--rule-file NAME        rule filename searched from root to target; repeatable; default: AGENTS.md
--symbol-index PATH     workspace-relative symbol-index.v1 or ctags JSONL; repeatable
--transport MODE        stdio (default) or http
--http-profile PATH     ignored local HTTP YAML profile; required for HTTP
--enable-intention-writes  pre-authorize checkpoint_write and memo_write prompts
--enable-report-import     pre-authorize check_report_import prompts
--enable-runner          pre-authorize run_prepare, run_execute, and run_inspect prompts
--enable-cache           pre-authorize qualified cache reuse; requires Runner
```

MCP frames are the only data written to stdout. Startup failures and diagnostics go to stderr.
Repository verification writes a bounded report under `.tmp/reports`. On failure, the developer
CLI emits the failing check ID, exit status, and bounded diagnostics to stderr after replacing
workspace, temporary, and user-home paths with neutral markers for public CI logs.

Typical tool inputs are intentionally small:

```json
{"mode":"search","query":"schema validation","item_limit":10}
{"mode":"regex","pattern":"TODO|FIXME","root":"internal","item_limit":50}
{"mode":"git_history","pattern":"breaking change","match_kind":"commit","revision":"HEAD"}
{"mode":"symbol","pattern":"Resolve","symbol_kind":"function","language":"Go"}
{"path":"config/settings.yaml","encoding":"utf-8"}
{"mode":"jsonl","ref":"source:mutable:reports/events.jsonl","fields":["id","status"]}
{"mode":"log","dialect":"regex","pattern":"error|panic","ref":"source:mutable:logs/app.log"}
{"mode":"json","dialect":"json-pointer","pointer":"/items","ref":"source:mutable:reports/data.json","fields":["id","status"]}
{"mode":"search","kind":"memo","validity":"current","query":"windows executable replacement","item_limit":5,"byte_limit":6000}
{"mode":"list","kind":"verification","validity":"current","payload_fields":["check_id","status","configuration"]}
```

These correspond to `catalog_query`, `workspace_search`, `path_explain`, `data_query`, and
`project_records` in that order. Pass only `cursor` plus optional limits for a next-page request.

For coding agents, RepoPlane MCP is the first choice where it has a matching repository-control
capability: structured discovery, registered verification/release execution, and durable task
recovery. It is not a universal replacement for `rg`, `git`, or focused package tests. Use those
direct tools when RepoPlane MCP reports `unsupported`/`partial` or has no matching capability, and
make the fallback reason explicit so later sessions do not repeat the same probe.

With report import enabled, a local verification report can be linked to a versioned checklist:

```json
{"path":".tmp/reports/verify.json","checklist_path":"checks/repository.verify.yaml","configuration":"default"}
```

The importer stores a hash and bounded normalized summary, not raw report diagnostics.

`project_records(mode=search)` requires bounded lexical `query` text and searches record identity,
metadata, and textual payload values. Results are relevance ordered and compact by default; memo
results include scope, kind, configuration, and a deterministic content `preview` of at most 320
characters. Other discovery strings are also capped at 320 characters and arrays at eight items.
Use the returned ID with `mode=get` only for records whose full payload is needed.
`mode=list` and `mode=get` remain full by default. An explicit `payload_fields` selection overrides
the compact search projection and returns those exact stored top-level fields;
`payload_complete` tells whether the full payload was returned.

Checkpoint, memo, and report-import writes preserve their full response by default. Pass
`"response_view":"receipt"` when the caller only needs the record ID, revision, validity, evidence,
duplicate state, and warnings and does not need its submitted payload echoed back:

Record only consequential failures whose cause and remedy can prevent repeated work; include the
invalidation condition, and leave one-off typos or noise out of durable memos.

```json
{"mode":"create","goal":"verify the release","status":"incomplete","next_action":"run the release gate","response_view":"receipt"}
```

## Add a catalog entry

Place YAML/JSON manifests or Markdown with leading YAML frontmatter under a configured catalog root. A minimal documentation-only entry
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

For Markdown, wrap the same fields in `---` delimiters before the document body. Plain Markdown
without frontmatter is ignored rather than reported as a broken manifest.

### What Runner is

Runner is RepoPlane MCP's approval-gated executor for pre-registered capabilities. It is not a general
shell and does not accept an executable, argv array, or shell command from an MCP request. A catalog
entry must define the executable, argument template, working directory, inputs, outputs, limits,
and `trusted_for_run: true`; local stdio requests user approval on first use unless the host
pre-authorized it with `--enable-runner`. Runner then uses `prepare → execute → inspect` to validate the
environment, execute at most one prepared plan, and retain bounded status, stream, and artifact
evidence. Leave Runner disabled when repository discovery and records are all that is needed.

An optional `execution` block describes such a registered CLI capability. It remains
documentation-only unless the user or host authorizes Runner and the entry explicitly sets
`trusted_for_run: true`:

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

## Optional HTTP deployment

HTTP is separate from the default stdio process. Copy `examples/http-profile.local.yaml` into an
ignored host state directory, create the referenced `repoplane.token` with at least 32 random
URL-safe characters, and start:

```sh
repoplane --workspace WORKSPACE --state-dir STATE_DIRECTORY \
  --transport http --http-profile STATE_DIRECTORY/http.yaml
```

The local profile binds loopback and grants read scope only. Mutation or Runner access additionally
requires both its existing startup opt-in and the matching token scope. For a remote deployment,
use `examples/http-profile.oauth.yaml` behind a TLS reverse proxy that connects to the loopback
listener and preserves `Host`; set the two named credential environment variables locally.
RepoPlane validates opaque tokens through the configured HTTPS RFC 7662 endpoint and checks expiry,
resource/audience, and per-tool scopes. It never issues or forwards tokens.

Direct non-loopback listeners require `tls.cert_file` and `tls.key_file`. Present browser Origins
must pass the configured policy. HTTP admissions are recorded without tokens, arguments, results,
addresses, or local paths in a separate bounded `audit.db`.

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
bounded HTTP `audit.db`, random cursor/cache/audit authentication keys, and opt-in Runner streams/artifact blobs in `--state-dir`. It does not
write databases into the workspace and refuses a state directory that resolves inside it. Runner
streams retain the wider of 14 days or the most recent 200 runs; current intention/imported record
references protect older run streams. Raw stream/artifact bytes are never placed in record payloads.

To uninstall, remove the client configuration entry and binary. After no RepoPlane process is
using it, delete the configured state directory to remove the local index and invalidate cursors.
That deletion also permanently removes checkpoints, memos, imported verification records, run
receipts, streams, captured artifacts, and HTTP audit events;
export portable memory first when those durable records and Runner evidence must be retained. HTTP
audit events are deliberately outside the portable archive. No workspace source files need cleanup.

### Back up and restore local state

`durable` means records survive client restarts and deletion of the regenerable index; it does not
make a machine reset recoverable by itself. Git-tracked catalogs, rules, and documentation return
with the repository, but checkpoints, memos, imported verification results, run receipts, retained
streams/artifacts, audit data, and local keys are lost if `--state-dir` is not backed up.

Use the typed `memory_backup` tool to export durable records, their complete revision/import history,
and retained Runner streams/artifacts while the local stdio server stays running. The first export to
an absolute destination directory asks for explicit runtime approval. It returns only a compact
archive receipt (name, byte count, SHA-256, and item counts). Active Runner processes block the
snapshot, and the destination must be outside both the workspace and state directory.

The same operation is available directly, and restore is a one-shot command before starting MCP:

```powershell
repoplane memory export --destination BACKUP_DIRECTORY --workspace WORKSPACE --state-dir STATE_DIRECTORY
repoplane memory restore --archive BACKUP_ARCHIVE --workspace RESTORED_WORKSPACE --state-dir NEW_STATE_DIRECTORY
```

Restore requires an empty durable-record/file target and rebases ownership to the current workspace
identity, so the repository may live at a different absolute path after a machine reset. The portable
archive intentionally excludes regenerable indexes/cache, cursor/cache/audit keys, `audit.db`, Codex
TOML, HTTP profiles, and tokens. Re-register MCP with `codex mcp add repoplane -- repoplane`; keep
ignored host profiles and their secrets separately in a password manager or other encrypted store.
The archive itself can contain sensitive record or Runner output, so store it encrypted and never
commit it. See the [portable memory backup specification](docs/Memory-Backup-Spec.md).

## Architecture

Feature services depend on domain repositories in `internal/store`, not SQL. SQLite is the first
adapter, implemented in `internal/store/sqlite` with versioned migrations. Regenerable query state
and durable records use separate database files and domain interfaces.

Public JSON Schemas are committed under [`schemas/`](schemas/). Run
`go generate ./internal/mcpserver` after changing a tool contract; tests reject schema drift.
The compact MCP schema set has a regression budget (34 KiB overall and 5,500 bytes for the three
Runner tools). Its wording is deduplicated without dropping response fields, limits, defaults, or
state semantics; cross-tool references are avoided because each MCP tool schema must stand alone.
The generated [`tool-footprint.v1.json`](schemas/tool-footprint.v1.json) separates complete-contract
bytes from a name/description/input-only comparison. These are deterministic serialized byte
counts—not observed model tokens or proof of what a particular MCP client exposes. RepoPlane keeps
all 14 typed tools and stable discovery; clients may defer model exposure natively without changing
the server contract. See the
[`context-efficiency specification`](docs/Context-Efficiency-Spec.md).

## Security model and limitations

- The host supplies startup defaults; an explicitly approved local stdio `runtime_config` call can
  replace the live workspace or state directory without restarting stdio.
- Requests cannot escape the resolved workspace through `..`, symlinks, or junctions.
- RepoPlane controls ripgrep arguments and never builds a shell command from a query.
- Reads, process output, result counts, response bytes, record sizes, and deadlines are bounded.
- Local record mutation, report import, Runner, cache, and external reads require a host pre-grant
  or an explicit ephemeral user approval; HTTP retains host opt-in and token scopes.
- Runner accepts registered capability IDs and typed arguments, never request-supplied executables,
  argv arrays, or shell strings. It records environment-variable presence without values.
- Captured output is raw trusted-tool output and is not content-redacted automatically; use
  `metadata` mode for sensitive outputs and never pass credentials as catalog arguments.
- Cache is disabled by default and requires runtime or host authorization, a cache manifest, captured outputs, stable
  runtime identities, purity assumptions, and—before reuse—current qualification evidence.
- Cache materialization never overwrites a differing file. Whole-root replacement is limited to an
  untracked, input-disjoint directory explicitly owned by the capability.
- Dirty-worktree reports without a content fingerprint are never classified as current.
- stdio uses the local host process boundary. Opt-in HTTP requires bearer authentication, resource
  and scope authorization, Origin/Host checks, bounded admission, and a separate redacted audit.
- JSONL support is deliberately limited to top-level equality filters and field projection.
- Symbol search reads only configured existing indexes; semantic search, XML, and arbitrary
  JSONPath/JMESPath remain out of scope.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and
[`docs/MVP-Spec.md`](docs/MVP-Spec.md), the
[`Search adapter specification`](docs/Search-Adapters-Spec.md), and the
[`HTTP security specification`](docs/HTTP-Security-Spec.md) for precise contracts.

## Roadmap

The read-only MVP, all adopted roadmap slices through Search Adapters and HTTP/Auth, runtime access,
and portable memory backup are complete. Ongoing work is compatibility, measured dogfooding, and
release maintenance. See the
[`full implementation roadmap`](docs/Full-Implementation-Roadmap.md), the
[`Records specification`](docs/Records-Spec.md), and the full
[`design document`](docs/Project-Control-Plane-MCP-Design.md).

Contributions that improve portability, database adapters, fixtures, or contract clarity are
welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
