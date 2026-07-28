# WebDB TRAE Compatibility Rules

These project rules are a compatibility entry point for TRAE clients. The root
`AGENTS.md` is the authoritative WebDB instruction and code-review policy.

1. Read and follow the root `AGENTS.md` and every applicable nested
   `AGENTS.md` before planning, editing, or reviewing.
2. In TRAE Work Desktop, enable **Settings → Rules → Import settings → Include
   AGENTS.md in context**. Do not rely on this compatibility file alone.
3. For code or pull-request review, use the project skill at
   `.trae/skills/webdb-code-review/SKILL.md`.
4. Keep review execution strictly read-only. Fixes or GitHub writes require a
   separately authorized post-review implementation action and must not be
   performed through the review skill.
5. Never claim that TRAE Work is a GitHub required status check. GitHub CI,
   CodeRabbit, and an independent reviewer remain the merge gates. A textual
   `APPROVE` from TRAE Work is not a GitHub platform approval.
