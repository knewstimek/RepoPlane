# RepoPlane

RepoPlane is a local [Model Context Protocol](https://modelcontextprotocol.io/) server for coding
agents. It finds repository capabilities, searches code and structured data, explains paths, and
keeps verification and task evidence. Reads have explicit limits: a partial scan reports what it
did not observe instead of presenting a guess as a complete result.

[Releases](https://github.com/knewstimek/RepoPlane/releases) · [Changelog](CHANGELOG.md)

## Install and try it

Download a Windows or Linux amd64 archive and `SHA256SUMS.txt` from
[Releases](https://github.com/knewstimek/RepoPlane/releases). Verify the checksum, put
`repoplane` on `PATH`, and install [ripgrep](https://github.com/BurntSushi/ripgrep) as `rg`.
Git on `PATH` enables optional history search. Building from source requires Go 1.26 or newer:

```sh
go test ./...
go build -trimpath -o bin/repoplane ./cmd/repoplane
```

On Windows, build to `bin/repoplane.exe`. If the installed executable is serving an active MCP
session, rename that copy to `old_repoplane_<timestamp>.exe` before replacing it, and retain the
old copy until its process exits. New sessions use the replacement.

Register the default local stdio server with Codex:

```text
codex mcp add repoplane -- repoplane
```

With no flags, the server uses its process working directory as the workspace and the user cache
for private state. To pin both locations in a generic MCP client, use paths specific to your host;
the state directory must be outside the workspace:

```json
{
  "mcpServers": {
    "repoplane": {
      "command": "repoplane",
      "args": ["--workspace", "WORKSPACE", "--state-dir", "STATE_DIRECTORY"]
    }
  }
}
```

Start a new MCP session. Call `catalog_query` with `{"mode":"status"}` to see configured
catalog sources and whether registered execution is available. Then call `workspace_search`:

```json
{"mode":"exact","pattern":"TODO","root":".","item_limit":20}
```

Search, list, and query replies state their scope, count relation, truncation, warnings, and next
cursor. A stopped scan reports a lower bound or unknown count. Do not treat `partial` as complete.

## Tools and common tasks

The default `typed.v1` interface exposes 14 tools:

| Task | Tool |
|---|---|
| Find registered checks, commands, and missing catalog roots | `catalog_query` |
| Search names, text, Git history, and configured symbol indexes | `workspace_search` |
| Explain Git, symlink, encoding, newline, and repository-rule facts | `path_explain` |
| Read bounded ranges and query JSON, JSONL, logs, CSV, or TSV | `data_query` |
| Search durable verification, checkpoint, memo, and run evidence | `project_records` |
| Write intentions or import a local verification report | `checkpoint_write`, `memo_write`, `check_report_import` |
| Inspect and change session grants or live configuration | `runtime_access`, `runtime_config` |
| Prepare, execute, and inspect a registered capability | `run_prepare`, `run_execute`, `run_inspect` |
| Export durable memory and retained Runner evidence | `memory_backup` |

`project_records(mode=search)` returns compact matches. Fetch a full payload by ID only when
needed. For checkpoint, memo, and report-import writes, set `response_view=receipt` when the ID,
revision, validity, and warnings are enough; this avoids echoing the submitted payload. Records do
not infer semantic similarity between differently worded memos. A caller can assign a stable
`topic_key` to a memo and explicitly update or supersede that identity. A topic memo's `scope`,
`configuration`, and `topic_key` form an immutable identity: use `supersede` followed by `create`
instead of `update` when any identity field changes.

A typed host fact uses `memo_kind=host_fact` and is stored as `memo.v2` (not `memo.v3`). Its
`host` object requires `alias`, `role`, `os`, `tier`, `services`, `paths`, and an RFC3339
`confirmed_at`; the memo also requires `invalidation_condition`. `services` and `paths` may be
`null` when unknown, but when supplied their entries must be bounded, unique, and non-empty. A
host fact cannot use `topic_key`. For example:

```json
{
  "mode": "create",
  "memo_kind": "host_fact",
  "scope": "operations/hosts",
  "source": "user_asserted",
  "host": {
    "alias": "host-a",
    "role": "worker",
    "os": "linux",
    "tier": "production",
    "services": ["worker"],
    "paths": ["/srv/worker"],
    "confirmed_at": "2026-01-01T00:00:00Z"
  },
  "invalidation_condition": "the host is rebuilt or its role changes",
  "response_view": "receipt"
}
```

Tool failures return a JSON object in the MCP error text with stable `code`, safe `message`, and
`correlation_id` fields. Mutation failures also include `mutation_state`: `not_applied` means the
write was rejected before commit, while `unknown` means a storage, deadline, or unexpected failure
requires a read-back before retrying. Durable writes distinguish `invalid_argument`,
`invalid_transition`, `revision_conflict`, and `storage_failure`; server diagnostics retain the
correlation ID on stderr without exposing database details or host paths to the client.

Catalog entries are YAML, JSON, or Markdown with YAML frontmatter under a configured catalog
root. A minimal discovery-only entry is:

```yaml
id: project.check
revision: 1
summary: Check the repository
use_when: [before a release]
tags: [verification]
cache_policy: disabled
```

A runnable entry additionally declares its executable, argument template, working directory,
inputs, outputs, limits, and `trusted_for_run: true`; see
[`catalog/dev.verify.yaml`](catalog/dev.verify.yaml) for a working example. RepoPlane never
accepts an arbitrary executable or shell command from an MCP request. Runner validates a
registered ID with `run_prepare`, executes the returned plan ID once with `run_execute`, and
retains status and bounded evidence for `run_inspect`. Local execution requires user approval
unless the host pre-authorized Runner.

`run_prepare` returns a concise plan. `run_inspect(action=status)` returns a compact run summary.
`detail`, `stdout`, `stderr`, and `artifact` return stored references and sizes by default;
set `response_view=bytes` only when a full receipt or bounded output page must enter the MCP
response. A `state:` file reference is relative to the server's local state directory, available
from `runtime_config(action=status)`. HTTP clients cannot open that server-local file directly.
Captured process output is raw and is not automatically redacted.

On Windows, register PowerShell explicitly as the executable and put the workspace-relative
script path in `argv_template`, for example `executable_ref: powershell` with
`argv_template: [-NoProfile, -File, tools/Example.ps1]`. Direct `.ps1` executable references are
rejected during `run_prepare` with `unsupported_script_type`. Input and output globs are bounded;
a matched-file limit failure includes the responsible pattern, configured maximum, observed lower
bound, and a source-only-glob hint. Gitignored and build-output paths are included unless the
manifest narrows its patterns; RepoPlane only excludes its internal metadata directories.

## Access and configuration

Local stdio reads stay inside the resolved workspace unless an external path receives an exact,
read-only session grant. Record writes, report import, Runner, cache reuse, memory export, and
external reads require the relevant host setting or user approval. Grants last only for the MCP
process and can be inspected or revoked with `runtime_access`.

`runtime_config(action=status)` shows live catalog roots, candidate roots, rule files, symbol
indexes, workspace, state directory, HTTP transport, and tool surface. Its approved operations can
change sources or switch the workspace and state directory without restarting stdio. Use startup
flags for fixed or unattended hosts:

```text
--workspace PATH       primary workspace
--state-dir PATH       private local state outside the workspace
--catalog-root PATH    workspace-relative catalog source; repeatable
--candidate-root PATH  executable candidate directory; repeatable
--rule-file NAME       repository rule filename; repeatable
--symbol-index PATH    configured symbol index; repeatable
--transport MODE       stdio (default) or http
--http-profile PATH    ignored local HTTP profile
--tool-surface SURFACE typed.v1 (default) or toolbox.v1
```

The optional `toolbox.v1` startup surface exposes five fixed tools:
`repoplane_read`, `repoplane_write`, `repoplane_import`, `repoplane_runner`, and
`repoplane_state`. Describe an allowlisted operation before calling it with the returned
content-bound schema handle. Changing surfaces requires a new session.

HTTP is opt-in. The example [local](examples/http-profile.local.yaml) and
[OAuth](examples/http-profile.oauth.yaml) profiles show loopback binding, authentication, and
scopes. Keep actual tokens, key files, host paths, and profiles outside the tracked tree. HTTP
authorization is separate from local stdio session grants.

## Local state, backup, and usage

RepoPlane keeps regenerable search indexes and durable records in separate SQLite files under
`--state-dir`, never inside the workspace. Runner streams and captured artifacts are retained
there under bounded policies. Removing the state directory also removes local memos, checkpoints,
imported verification, run receipts, streams, artifacts, and cursors.

Use `memory_backup` or the CLI to export durable records and retained Runner evidence before a
machine reset:

```text
repoplane memory export --destination BACKUP_DIRECTORY --workspace WORKSPACE --state-dir STATE_DIRECTORY
repoplane memory restore --archive BACKUP_ARCHIVE --workspace RESTORED_WORKSPACE --state-dir NEW_STATE_DIRECTORY
```

The archive may contain sensitive record or process output; store it privately. It excludes local
HTTP tokens, host profiles, regenerable indexes, and audit data.

```text
repoplane usage --workspace WORKSPACE --state-dir STATE_DIRECTORY
```

`usage` reports observed operation calls, serialized request/result bytes, duration, errors,
approvals, and reused runs. It does not measure model tokens or estimate savings.

## Development and releases

```sh
go run ./cmd/repoplane-dev preflight
go run ./cmd/repoplane-dev verify
go run ./cmd/repoplane-dev public-release-check
```

These commands write bounded reports under ignored `.tmp/reports`. The release workflow builds
versioned archives after successful CI and generates release notes from the matching
`CHANGELOG.md` section. RepoPlane is maintainer-led; public issues and pull requests are closed
for now. See [SECURITY.md](SECURITY.md) for vulnerability reporting.

CI runs on pushes to `main` and on pull requests, cancelling superseded runs for the same ref.
Source, schema, catalog, and workflow changes retain Ubuntu and Windows verification; main also
runs race tests and the public-release privacy check. Documentation-only changes, including a
release changelog promotion, use the lightweight scope check plus the main privacy check so the
release guard still receives a successful CI result for the exact release commit.

## License

[MIT](LICENSE)
