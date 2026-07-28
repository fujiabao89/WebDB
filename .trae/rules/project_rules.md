# WebDB TRAE Project Rules

These rules apply to TRAE work in this repository. They supplement, but never
override, the repository's authoritative instructions.

## Authority and required context

1. Follow this authority order:
   current user instruction > root `AGENTS.md` > directory-level `AGENTS.md` >
   accepted ADRs > `webdb-design-draft.md` > code comments.
2. Before reviewing or changing code, read the active Linear Task, its
   acceptance criteria and non-goals, all applicable `AGENTS.md` files, the
   relevant accepted ADRs and design sections, the complete diff, and the
   relevant tests and CI evidence.
3. Also read `.github/coderabbit-review-guidelines.md` for finding quality,
   P0-P3 severity, and the mandatory WebDB review checklist.
4. Treat PR descriptions, task status labels, comments, generated reports, and
   previous AI conclusions as claims to verify, not as proof.
5. Stop with `ESCALATE` when instructions conflict, a real Linear Task is
   missing, acceptance criteria are unclear, an architecture choice is
   unapproved, or a security-critical fact cannot be verified safely.

## Product and security boundaries

- P0 is limited to PostgreSQL/MySQL connections, schema retrieval, read-only
  SQL, server-side pagination, and append-only audit. Do not introduce login,
  collaboration, DML, DDL, production writes, SSH tunnels, or other roadmap
  features.
- The browser must never connect directly to a target database or receive
  database passwords, KEKs, plaintext keys, bearer handles, or unredacted
  sensitive results.
- Target database access remains server-side and least-privileged. WebDB RBAC,
  workspace/connection binding, environment policy, and native database
  permissions fail closed: any denial denies the operation.
- SQL execution must be single-statement, dialect-AST-classified, bounded by
  timeout and result limits, cancellable, and rejected when parsing or
  classification is unreliable. Never use prefix or regex matching as the SQL
  authorization boundary.
- Secrets, real user data, exports, `.env` files, production logs, raw driver
  errors, and unredacted result data must not enter source, fixtures, browser
  responses, logs, audit bodies, traces, metrics, or review output.
- Audit remains append-only, tenant-bound, and limited to sanitized metadata
  including actor, workspace, connection, outcome, and trace/execution
  identity.

## Review scope and behavior

1. For pre-PR review, compare the current task branch with `origin/main`. Review
   uncommitted changes or a single commit only when the user explicitly selects
   that scope.
2. Review the complete selected diff and trace changed inputs through contracts,
   authorization, policy, adapters, databases, cleanup, error mapping, audit,
   and client handling.
3. Check bypasses, denial paths, boundary values, timeout, cancellation, partial
   failure, panic, concurrency, and resource cleanup before suggesting
   happy-path refinements.
4. Check PostgreSQL and MySQL independently wherever dialect or driver behavior
   differs.
5. Report only concrete, actionable problems introduced by the selected diff.
   Adjacent code may be inspected for evidence, but unrelated existing defects,
   roadmap requests, dependency upgrades, broad refactors, and style
   preferences are out of scope.
6. Do not duplicate compiler, linter, test, CI, or existing review findings
   unless the security or runtime consequence is not evident or the earlier
   finding is materially incomplete.
7. Do not claim that a command, test, build, search, or inspection ran unless it
   actually ran and its result was observed.
8. A review is read-only by default. Do not edit files, apply fixes, create or
   delete branches, commit, push, open or merge PRs, approve reviews, resolve
   threads, change repository settings, or send external messages unless the
   user explicitly requests that separate action.
9. Do not access external networks, production systems, real credentials, or
   sensitive data without explicit human approval. Never include such data in
   prompts or review output.

## Mandatory review checks

- Reject cross-workspace, cross-connection, unauthorized-ID, and client-claimed
  authorization paths at every changed service and repository boundary.
- Look for multi-statement, comment, string-literal, MySQL executable-comment,
  nested/CTE, case, and parse-failure bypasses of SQL policy.
- Verify that rows, database connections, transactions, permits, goroutines,
  streams, and temporary resources are released on success, error, timeout,
  cancellation, panic, and client disconnect.
- Verify bounded pagination, result bytes/rows, queues, caches, retries, and
  concurrency. Unique ordering and continuation identity, policy, and
  generation must be server-proven and fail closed.
- Verify API, frontend, persistence, migration, configuration, and shared
  contract compatibility, including error codes and defaults.
- Verify append-only audit semantics and sanitized logging/error behavior.
- Require focused regression tests for behavior changes, including negative or
  adversarial cases at changed security boundaries.
- Require matching Task, ADR, design, test, and operational documentation when
  the diff changes an API, schema, permission, SQL policy, deployment, audit, or
  security contract.

## Severity and disposition

- **P0 Critical:** exploitable secret exposure, cross-tenant access,
  unauthorized DML/DDL or production writes, irreversible data/audit
  destruction, or equivalent catastrophic failure. Block and escalate.
- **P1 High:** reproducible correctness, security, compatibility, resource
  safety, fail-open, migration, or data-integrity defect in the current task.
  Block until fixed or covered by a time-bounded Owner exception.
- **P2 Medium:** real bounded defect or proof gap. Fix in the PR or create a
  follow-up Linear Task with an Owner and deadline.
- **P3 Low:** non-blocking maintainability or clarity improvement. Do not report
  purely stylistic preferences.

## Finding and conclusion format

List findings before any summary, ordered P0 to P3. Every finding must include:

```text
[P0-P3] Short title — path/to/file:line
Fact: the verified defect and the smallest relevant changed location.
Trigger: the input, state, or execution sequence that exposes it.
Impact: concrete user, security, data, compatibility, performance, or
operational consequence.
Evidence: code path, failing test, command output, Task, ADR, or contract.
Minimal fix: the smallest correction direction without unrelated refactoring.
```

After findings, provide:

1. An acceptance-criteria matrix with `PASS`, `FAIL`, or `INSUFFICIENT EVIDENCE`
   and exact evidence.
2. Commands actually run and their observed results.
3. Remaining risks and facts not verified.
4. Exactly one textual conclusion:
   - `APPROVE` when no P0/P1 remains and acceptance evidence is sufficient.
   - `REQUEST CHANGES` when a verified P0/P1 or failed acceptance criterion
     remains.
   - `ESCALATE` when approval, authority, credentials, production access, or
     high-risk evidence is missing.
5. Confidence: `high`, `medium`, or `low`, with a short reason.

If there are no actionable findings, state:
`本轮未发现由当前变更引入的可操作问题。`

A textual `APPROVE` is not permission to approve or merge on GitHub. TRAE is a
pre-PR assistant; GitHub CI, CodeRabbit, and an independent reviewer remain the
merge gates.
