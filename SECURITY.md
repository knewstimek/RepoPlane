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

RepoPlane is a local stdio MCP server. It does not authenticate clients, execute catalog entries,
or provide a network listener. The host is responsible for choosing which local MCP client may
start it and which workspace it may inspect.
