---
name: repo-memory
description: Use RepoPlane before repository or operational work and save verified reusable knowledge before completing feature, interface, configuration, or operational changes. Applies when RepoPlane is available; prior memory need not already exist.
---

# RepoPlane Memory

Use RepoPlane as the workspace's durable background memory.

## Before work

- Search current RepoPlane project records for terms relevant to the request. Query narrowly and keep returned payloads bounded.
- Check the RepoPlane catalog before direct repository discovery or verification when a registered capability may fit.
- Treat a memo as context, not proof of current state. Honor its validity and invalidation condition, and verify facts that are volatile, security-sensitive, or essential to the result.
- If RepoPlane is unavailable or has no relevant record or capability, continue with bounded local discovery and briefly state the fallback reason when it matters.

## Registered execution

- Runner launches `execution.executable_ref` and passes `argv_template` as its arguments from `execution.cwd`; it does not infer a Python interpreter from a `.py` file association.
- For Python or PowerShell scripts, register an available interpreter as the executable and put the script path in its arguments. For example, with `cwd: .`, use `executable_ref: python` and `argv_template: [tools/example.py, "{message}"]`. Use the interpreter name/path actually verified on the host; a workspace executable path must be relative.
- Inspect the registered manifest and the prepared executable, arguments, and working directory before execution. Keep the existing trust, approval, and cache qualification gates; registration alone does not authorize a run.

## Retain reusable knowledge

Retention is part of completing repository or operational work. Before the final completion response, review the actual result for a new or changed feature, supported interface/configuration, durable decision, operational invariant, or consequential reusable failure. When any applies, search for an existing memo, create/update/supersede the appropriate memo, and confirm the write receipt. Do this in the same task without waiting for a user reminder; a commit, push, changelog, or documentation update does not satisfy memory retention.

For an implemented feature, retain a compact discovery summary: behavior and usage, important limitations, source/commit or other evidence pointers, and actual verification/deployment status. Distinguish implemented and statically reviewed from built, executed, synchronized, pushed, and deployed; record only statuses supported by evidence.

Good memo subjects include:

- canonical source, artifact, symbol, backup, build, test, or deployment paths;
- verified environment or host facts;
- durable project decisions and non-obvious operational invariants;
- reproducible failures with their cause, remedy, and invalidation condition;
- genuine tool or environment limitations that will affect later work.

Keep each memo concise and scoped. Include the evidence references available from RepoPlane, a useful stable topic key, and a concrete invalidation condition. Mark user-provided facts as `user_asserted`; use `llm_proposed` for facts established from inspected evidence.

Do not store secrets, credentials, personal data, speculative conclusions, routine task progress, transient command output, one-off mistakes, or copies of facts already obvious from tracked project documentation. A documented new feature still needs a concise memory discovery entry with pointers; avoid duplicating the full documentation. Do not duplicate an existing current memo; update or supersede it when the durable fact changed.

## Write recovery

- `invalid_argument`: check current tool schema; fix the cited field. `limit_exceeded`: shrink the cited input.
- `memo_time_unknown`: saved; no retry or invented timestamp.
- `memo_topic_exists`: new content not saved. `revision_conflict`: stale revision. Read the full record and compare the intended content. If already retained, skip the write; otherwise merge and update using its ID/latest `expected_revision`, preserving other fields/evidence.
- If the memo kind/schema or topic identity (`scope`, `configuration`, `topic_key`) must change, use supersession instead of `update`: create the successor with `supersedes` and the current `expected_revision`.
- Timeout/storage error or unknown outcome: read back by ID/`topic_key` before retrying.
- `permission_denied`: report; never bypass. Never repeat an unchanged failed write.

## Handoff

Report retained knowledge only after confirmed storage. If unresolved/unavailable, report incomplete retention and continue other authorized work. Skip writes with no reusable result.
