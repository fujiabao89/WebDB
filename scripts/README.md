# Local Codex auto-fix loop

Create `config.json` from `config.example.json`, then run:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\auto-fix.ps1 --DryRun
powershell -ExecutionPolicy Bypass -File .\scripts\auto-fix.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\auto-fix-watch.ps1
```

`--DryRun` never invokes Claude or pushes. State and JSONL logs are local and must not be committed. The loop only handles the configured repository, owner, allowlisted PRs, and Codex bot reviews whose commit SHA matches the current PR head. It stops on high-risk findings, repeated failures, or three rounds and waits for human review; it never merges.

Claude usage consumes local Claude Code quota/API credits. Review `test_commands` before enabling the non-dry-run mode.
