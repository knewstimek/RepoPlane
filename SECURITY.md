# Security policy

## Supported versions

| Version | Supported |
|---|---|
| 1.0.x | Yes |
| < 1.0 | No |

Security fixes are applied to the latest `1.0.x` release and the default branch. A newer minor or
major line replaces the preceding line unless this table explicitly says otherwise.

## Reporting a vulnerability

Please use the repository's private GitHub Security Advisory reporting flow. Do not open a public
issue for suspected workspace escapes, command injection, secret exposure, cursor forgery, or other
sensitive findings.

Include the affected revision, platform, minimal reproduction, expected boundary, and observed
behavior. Remove credentials, personal paths, private repository contents, and unrelated logs from
the report. Maintainers will acknowledge a report when it is reviewed and coordinate disclosure
after a fix is available.

## Security boundaries

RepoPlane defaults to a local stdio MCP server, where the host chooses which client starts it and
which workspace it may inspect. Opt-in Streamable HTTP serves exactly one configured workspace and
requires bearer authentication. Local-token mode is loopback-only; remote deployments use direct
TLS or a loopback reverse proxy and an external OAuth Authorization Server. OAuth tokens must be
active, unexpired, audience-bound to the configured resource, and carry the operation scope.
RepoPlane does not issue, exchange, forward, or log tokens.

HTTP validates Host and browser Origin, limits body/header/rate/concurrency, propagates disconnect
cancellation, and records admitted requests in a separate bounded `audit.db`. Audit identities are
host-keyed pseudonyms and events exclude arguments, results, tokens, network addresses, and local
paths. Admission fails closed if its audit event cannot be stored. Forwarded headers are not an
authorization input; a reverse proxy must preserve the configured Host.

Checkpoint/memo mutation, local report import, and registered-capability execution and cache reuse
remain disabled by default and must be enabled explicitly. HTTP token scopes are an additional
gate; record IDs and workspace read access do not grant mutation or execution capability.

Runner accepts only a registered capability ID, its current revision, and manifest-declared typed
arguments. It does not accept request-supplied executables, argv arrays, or shell strings. Enabling
`--enable-runner`, setting `trusted_for_run: true`, and approving the MCP execution tool are all
required. Plans bind the executable identity and declared inputs before one-time execution.
Environment probes store credential presence but not values. Raw command output and explicitly
captured artifacts are not automatically redacted, so sensitive capabilities should use metadata
mode and must not receive credentials as catalog arguments.

`--enable-cache` requires Runner and does not add an arbitrary write surface. Reuse additionally
requires a strict cache manifest, host-local HMAC key, current passed qualification evidence, and
verified artifact blobs. It does not overwrite a differing output; whole-root replacement is
restricted to an explicitly owned untracked directory that does not overlap declared inputs.

The report importer accepts only bounded workspace-relative files resolved through the workspace
boundary. It stores a content hash and normalized check statuses, not raw diagnostics. Durable
records live in `records.db`, separately from the regenerable cache, under the host-selected state
directory outside the workspace.
