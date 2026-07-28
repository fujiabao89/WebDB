---
name: webdb-code-review
description: Review WebDB local changes, task branches, or GitHub pull requests against the active Linear Task, repository instructions, accepted architecture, tests, CI evidence, and security boundaries. Use for pre-PR review, remote PR review, review reruns after fixes, or requests to assess whether a WebDB change is ready for independent review.
---

# WebDB Code Review

Perform an evidence-backed review without modifying code or GitHub state.
Apply the complete review policy from the root `AGENTS.md`; do not duplicate or
weaken it here.

## 1. Establish the review target

1. Identify the real Linear Task, acceptance criteria, non-goals, base ref, and
   head ref.
2. For a GitHub PR, use its actual base and head refs. For a local task branch,
   compare it with the current `origin/main` using three-dot diff semantics.
3. Treat the selected WebDB repository or PR and a user-supplied Linear Task ID
   as authorization to read only those exact resources through configured
   connectors. Ask before accessing any other network resource.
4. Stop with `ESCALATE` if the Task is missing, the refs cannot be verified, the
   acceptance criteria are unclear, or the selected environment lacks access.
   Never substitute the PR description for direct Task evidence.

## 2. Load authoritative context

Read, in order:

1. The current user instruction.
2. Root and applicable nested `AGENTS.md` files.
3. The Linear Task and its acceptance criteria.
4. Relevant accepted ADRs and `webdb-design-draft.md` sections.
5. `.github/coderabbit-review-guidelines.md`.
6. The complete selected diff and its direct callers, callees, contracts, tests,
   migrations, and operational documentation.
7. Current CI results and unresolved review threads, when available.

Treat PR descriptions, task status, comments, generated reports, and earlier AI
conclusions as claims to verify, not evidence by themselves.

## 3. Inspect and verify

1. Review only problems introduced or exposed by the selected diff and
   reasonably fixable in the current Task.
2. Trace changed inputs through validation, authorization, policy, persistence,
   adapters, cleanup, error mapping, audit, and client handling.
3. Prioritize denial paths, tenant isolation, secrets, SQL-policy bypasses,
   timeout, cancellation, partial failure, concurrency, resource cleanup,
   compatibility, and bounded resource use.
4. Inspect PostgreSQL and MySQL behavior independently where dialect or driver
   behavior differs.
5. Run the smallest safe command that can verify a suspected defect. Record the
   exact command and observed result. Never invent execution evidence.
6. Do not access production systems, real credentials, real user data,
   unredacted logs, or database exports.
7. Do not report style preferences, unrelated historical defects, speculative
   risks without a trigger path, or duplicate unresolved findings.

## 4. Return the required review

Follow the root `AGENTS.md` output contract exactly:

1. List findings first, ordered P0 through P3. Include the smallest relevant
   path and line range, fact, trigger, impact, evidence, and minimal fix.
2. Provide an acceptance-criteria matrix with `通过`, `失败`, or `证据不足`.
3. List commands actually run and their observed results.
4. State remaining risks and unverified facts.
5. End with exactly one textual conclusion: `APPROVE`, `REQUEST CHANGES`, or
   `ESCALATE`, plus confidence.
6. If no actionable finding exists, state:
   `本轮未发现由当前变更引入的可操作问题。`

## 5. Preserve review independence

Do not edit files, apply fixes, commit, push, post GitHub reviews, resolve
threads, approve, or merge during this review. A textual `APPROVE` is not a
GitHub approval. If the user asks for fixes, finish the review first and treat
the repair as a separate implementation action. The implementation agent cannot
serve as the final independent reviewer of its own PR.
