# Security policy

## Supported versions

RepoPlane is currently an early MVP. Security fixes are applied to the latest commit on the default
branch until versioned releases are published.

## Reporting a vulnerability

Please use the repository's private GitHub Security Advisory reporting flow. Do not open a public
issue for suspected workspace escapes, command injection, secret exposure, cursor forgery, or other
sensitive findings.

Include the affected revision, platform, minimal reproduction, expected boundary, and observed
behavior. Remove credentials, personal paths, private repository contents, and unrelated logs from
the report. Maintainers will acknowledge a report when it is reviewed and coordinate disclosure
after a fix is available.

## Security boundaries

RepoPlane is a local stdio MCP server. It does not authenticate clients or provide a network
listener. The host is responsible for choosing which local MCP client may start it and which
workspace it may inspect. Checkpoint/memo mutation, local report import, and registered-capability
execution are disabled by default and must be enabled independently. Record IDs and workspace read
access do not grant mutation or execution capability.

Runner accepts only a registered capability ID, its current revision, and manifest-declared typed
arguments. It does not accept request-supplied executables, argv arrays, or shell strings. Enabling
`--enable-runner`, setting `trusted_for_run: true`, and approving the MCP execution tool are all
required. Plans bind the executable identity and declared inputs before one-time execution.
Environment probes store credential presence but not values. Raw command output and explicitly
captured artifacts are not automatically redacted, so sensitive capabilities should use metadata
mode and must not receive credentials as catalog arguments.

The report importer accepts only bounded workspace-relative files resolved through the workspace
boundary. It stores a content hash and normalized check statuses, not raw diagnostics. Durable
records live in `records.db`, separately from the regenerable cache, under the host-selected state
directory outside the workspace.
