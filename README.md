# RepoPlane

RepoPlane is a read-first [Model Context Protocol](https://modelcontextprotocol.io/) server for
coding agents. It helps an agent find repository tools, search code and data, and recover
verification evidence without loading the whole workspace into a conversation. Responses have
explicit limits and report when a result is partial.

[Releases](https://github.com/knewstimek/RepoPlane/releases) · [Documentation](docs/README.md) · [Changelog](CHANGELOG.md)

## Get started

Install a [release](https://github.com/knewstimek/RepoPlane/releases) for Windows or Linux amd64,
verify its checksum, and put `repoplane` on `PATH`. Install
[ripgrep](https://github.com/BurntSushi/ripgrep) as `rg`; Git is needed for optional history search.

Register the local stdio server with Codex:

~~~text
codex mcp add repoplane -- repoplane
~~~

By default, the MCP process working directory is the workspace. To pin a workspace and keep
private state outside it, a generic MCP client can use:

~~~json
{
  "mcpServers": {
    "repoplane": {
      "command": "repoplane",
      "args": ["--workspace", "WORKSPACE", "--state-dir", "STATE_DIRECTORY"]
    }
  }
}
~~~

Start a new MCP session, then try `catalog_query` with `{"mode":"status"}` and
`workspace_search` with:

~~~json
{"mode":"exact","pattern":"TODO","root":".","item_limit":20}
~~~

For source builds, Windows executable replacement, nested repositories, startup flags, and the
optional toolbox surface, see [installation and configuration](docs/Install-and-Configure.md).

## What it helps with

| Task | Tools |
|---|---|
| Find registered checks and commands | `catalog_query` |
| Search filenames, content, Git history, or configured symbols | `workspace_search` |
| Inspect a path's Git, link, encoding, and rule facts | `path_explain` |
| Read bounded ranges or query JSON, JSONL, logs, CSV, and TSV | `data_query` |
| Find durable checks, decisions, run receipts, and evidence | `project_records` |
| Review runtime grants and live configuration | `runtime_access`, `runtime_config` |
| Export durable records and retained run evidence | `memory_backup` |
| Run a registered capability after approval | `run_prepare`, `run_execute`, `run_inspect` |

The [tool usage guide](docs/Tool-Usage.md) shows request examples, catalog entries, records,
Runner, and optional cache reuse. The default interface has 14 typed tools; an optional five-tool
toolbox interface is explained in the [installation guide](docs/Install-and-Configure.md).

## Boundaries and local state

RepoPlane does not accept arbitrary shell commands from MCP requests. Writes, registered Runner
execution, cache reuse, external reads, and backup exports require the relevant host setting or
user approval. HTTP is opt-in and has separate authentication and scopes.

The server keeps its databases and captured Runner output in a state directory outside the
workspace. `run_inspect` returns status and stored references by default. Set
`response_view=bytes` only when a full run receipt or a bounded output page needs to enter the
MCP response; captured output is not automatically redacted.

`repoplane usage --workspace WORKSPACE --state-dir STATE_DIRECTORY` prints observed call and byte
counts. It does not measure model tokens or estimate savings. See [operations and local
state](docs/Operations.md) for backup, removal, HTTP, output references, and limitations.

## For contributors

See [CONTRIBUTING.md](CONTRIBUTING.md) for development checks, the [documentation
index](docs/README.md) for specifications, the [active
roadmap](docs/Full-Implementation-Roadmap.md) for project status, and [SECURITY.md](SECURITY.md)
for vulnerability reporting.

## License

[MIT](LICENSE)
