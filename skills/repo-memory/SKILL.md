---
name: repo-memory
description: Use RepoPlane durable memory before repository or operational work, and retain verified reusable workspace knowledge afterward. Applies when RepoPlane is available and prior paths, environment facts, decisions, or known failures could affect the task.
---

# RepoPlane Memory

Use RepoPlane as the workspace's durable background memory.

## Before work

- Search current RepoPlane project records for terms relevant to the request. Query narrowly and keep returned payloads bounded.
- Check the RepoPlane catalog before direct repository discovery or verification when a registered capability may fit.
- Treat a memo as context, not proof of current state. Honor its validity and invalidation condition, and verify facts that are volatile, security-sensitive, or essential to the result.
- If RepoPlane is unavailable or has no relevant record or capability, continue with bounded local discovery and briefly state the fallback reason when it matters.

## Retain reusable knowledge

When the work reveals consequential knowledge likely to help a later task, search for an existing memo first, then create, update, or supersede the appropriate RepoPlane memo.

Good memo subjects include:

- canonical source, artifact, symbol, backup, build, test, or deployment paths;
- verified environment or host facts;
- durable project decisions and non-obvious operational invariants;
- reproducible failures with their cause, remedy, and invalidation condition;
- genuine tool or environment limitations that will affect later work.

Keep each memo concise and scoped. Include evidence references, a stable topic key, and an invalidation condition. Classify user-provided facts as `user_asserted` and inspected facts as `llm_proposed`.

Do not store secrets, credentials, personal data, speculative conclusions, routine progress, transient output, one-off mistakes, or obvious tracked documentation. Update or supersede an existing memo instead of duplicating it.

## Handoff

Mention newly retained or materially updated background knowledge in the final response. Do not claim it was saved unless the write was confirmed.
