# CodeRabbit review guidelines for WebDB

This file supplements `AGENTS.md`. It changes review focus, not product scope or
architecture. When sources conflict, follow the authority order in `AGENTS.md`.

## Review method

1. Identify the active Task/Issue, its status, acceptance criteria, non-goals
   and approved dependencies. A blocked task is not implementation authority.
2. Read the accepted ADRs and the relevant sections of
   `webdb-design-draft.md`. Compare documentation with the current code and
   migrations; do not assume a task-card claim is already implemented.
3. Review the complete diff and trace each changed input across API contracts,
   authorization/policy, adapters, databases, cleanup, error mapping, audit and
   client handling.
4. Look for bypasses and failure paths before happy-path refinements. Check both
   PostgreSQL and MySQL where behavior is dialect- or driver-dependent.
5. Confirm that tests would fail for the regression being reported and cover
   the relevant success, denial, timeout, cancellation, panic/concurrency and
   cleanup paths.
6. Report only issues introduced by the PR. Attach each finding to the smallest
   useful changed-line range and state the trigger, impact and minimal correction
   direction.

## Finding quality

- A finding must be specific, reproducible or logically demonstrable, and
  actionable. Cite the conflicting Task, ADR, schema, test or code path.
- Do not duplicate a linter/compiler/CI finding unless the security or runtime
  consequence is not obvious from that tool's output.
- Do not request unrelated refactors, dependency upgrades, new roadmap features
  or style preferences.
- Do not treat intentionally trusted metadata migrations or synthetic local
  fixtures as user-submitted target-database SQL.
- If evidence is insufficient, ask a bounded question instead of asserting a
  defect. Never invent an API, field, threat, requirement or test result.
- If no merge-blocking issue exists, explicitly say that this review found none.

## Severity

- **P0 — Critical:** a directly exploitable path to secret/KEK/plaintext
  credential exposure, cross-workspace or cross-connection data access,
  unintended DML/DDL/production write, audit destruction, or similarly
  catastrophic impact. Block merge.
- **P1 — High:** a realistic correctness/security/resource-safety defect,
  fail-open decision, connection/permit leak, unbounded memory/queue behavior,
  broken migration, incompatible contract, or missing proof on a changed
  security boundary. Block merge.
- **P2 — Medium:** a real but non-immediate defect with a bounded workaround or
  lower likelihood. It must be fixed in the PR or captured as a follow-up Task
  with an Owner, per `AGENTS.md`.
- **P3 — Low:** minor maintainability or clarity issue. Do not block merge and
  avoid reporting it when it is merely stylistic.

## Mandatory WebDB checks

- Deny cross-workspace, cross-connection and unauthorized-ID access at every
  affected repository/API boundary.
- Reject SQL that is multi-statement, mutating, dialect-ambiguous, unparsable or
  otherwise not reliably classifiable. Comments, strings, case, MySQL executable
  comments and nested/CTE constructs must not bypass the AST policy.
- Ensure timeout, cancellation, errors, panic and client disconnect always
  return rows, database connections and admission permits.
- Keep pagination and result handling bounded. Unique ordering and continuation
  identity/policy/generation must be server-proven and fail closed.
- Keep passwords, KEK/plaintext keys, bearer handles, raw arguments, unredacted
  result data and raw driver errors out of browser responses, logs, audit bodies,
  traces and metric labels.
- Keep audit append-only and tenant-bound; record the actor, workspace,
  connection, outcome summary and trace/execution identity without sensitive
  body content.
- Require same-PR Task/ADR/design/test updates when a changed API, schema,
  permission, SQL policy, deployment, audit or security contract requires them.
