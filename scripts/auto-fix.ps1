<#
.SYNOPSIS
  Local Codex auto-fix poller - single pass.
.DESCRIPTION
  Reads Codex reviews on allowlisted open PRs, classifies P1/P2 findings,
  invokes Claude Code (up to 3 comments per round, max 3 rounds per PR).
  Never merges, never touches production, never modifies rulesets or CI/CD.
#>
[CmdletBinding()]
param([int]$PullRequest = 0, [switch]$DryRun, [switch]$Once)

$ErrorActionPreference = 'Stop'

# ------------------------------------------------------------------
# 1. Bootstrap
# ------------------------------------------------------------------
$Root      = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ConfPath  = Join-Path $PSScriptRoot 'config.json'
$ConfEx    = Join-Path $PSScriptRoot 'config.example.json'

if (Test-Path $ConfPath) { $Cfg = Get-Content $ConfPath -Raw | ConvertFrom-Json }
else { $Cfg = Get-Content $ConfEx -Raw | ConvertFrom-Json }

$Repo      = $Cfg.repo
$Owner     = $Cfg.owner
$CodexUser = $Cfg.codex_login
$MaxRounds = $Cfg.max_rounds
$MaxPerCall= 3
$Allowed   = @($Cfg.allowed_prs | ForEach-Object { [int]$_ })

$StateDir  = Join-Path $Root $Cfg.state_dir
$LogDir    = Join-Path $Root $Cfg.log_dir
$StateFile = Join-Path $StateDir 'state.json'
$LockFile  = Join-Path $StateDir 'run.lock'
$LogFile   = Join-Path $LogDir 'auto-fix.jsonl'

New-Item -Force -ItemType Directory $StateDir, $LogDir | Out-Null

if (-not (Test-Path (Join-Path $Root '.git'))) { throw "not a git repository: $Root" }
Set-Location -LiteralPath $Root

# ------------------------------------------------------------------
# 2. Helpers
# ------------------------------------------------------------------

function Write-Log($Event, $Data = @{}) {
  $entry = [ordered]@{ ts = (Get-Date).ToUniversalTime().ToString('o'); event = $Event; data = $Data } | ConvertTo-Json -Compress -Depth 10
  if (-not $entry) { return }
  $entry | Tee-Object $LogFile -Append | Write-Host
}

function Invoke-Gh([string[]]$GhArgs) {
  $out = & gh @GhArgs 2>$null
  if ($LASTEXITCODE) { $err = & gh @GhArgs 2>&1 | Out-String; throw ($err.Trim()) }
  return ($out -join "`n")
}

function Set-Label($Pr, $Label) {
  $labels = (Invoke-Gh @('pr', 'view', "$Pr", '--repo', $Repo, '--json', 'labels', '--jq', '.labels[].name')) -split "`n"
  if ($labels -notcontains $Label) {
    if (-not $DryRun) { Invoke-Gh @('pr', 'edit', "$Pr", '--repo', $Repo, '--add-label', $Label) | Out-Null }
    Write-Log 'label' @{ pr = $Pr; label = $Label; dryrun = $DryRun.IsPresent }
  }
}

function Get-CommentTitle($Body) {
  $rawLines = $Body -split "`n"
  $cleaned = foreach ($line in $rawLines) {
    $c = $line -replace '!\[.*?\]\(.*?\)', ''
    $c = $c -replace '<[^>]*>', ''
    $c = $c -replace '\*\*', ''
    $c = $c -replace 'https?://\S+', ''
    $c = $c.Trim()
    if ($c) { $c }
  }
  if ($cleaned.Count -gt 0) { return ($cleaned[0] -replace '\s+', ' ') }
  return ''
}

function Test-HighRisk($Comments) {
  foreach ($c in $Comments) {
    $title = Get-CommentTitle $c.body
    # Only match explicit compound patterns — never standalone keywords
    if ($title -match '(?i)\bP0\b') { return $true }
    if ($title -match '(?i)\bCRITICAL\b') { return $true }
    if ($title -match '(?i)(secret|token|password|credential|key)\s+(exposed|leaked|committed|hardcoded|visible|logged|stored)') { return $true }
    if ($title -match '(?i)(privilege|permission)\s+escalation') { return $true }
    if ($title -match '(?i)unauthorized\s+(access|modification|change)') { return $true }
    if ($title -match '(?i)(production|prod)\s+(data|database|deploy|environment)\s+(read|write|access|modify|expose)') { return $true }
    if ($title -match '(?i)ruleset\s+(changed|modified|altered|broken|bypass)') { return $true }
    if ($title -match '(?i)\bmerge\s+(performed|executed|done|completed|auto)') { return $true }
    if ($title -match '(?i)(architecture|schema)\s+(decision|change|redesign|overhaul|break)') { return $true }
    if ($title -match '(?i)data\s+(loss|corruption|breach)') { return $true }
    if ($title -match '(?i)(security|auth)\s+(vulnerability|bypass|flaw|hole)') { return $true }
    if ($title -match '(?i)arbitrary\s+(code|command)\s+execution') { return $true }
    if ($title -match '(?i)(bypass|disable)\s+(security|auth|permission|rate.limit)') { return $true }
  }
  return $false
}

function Get-Severity($Body) {
  if ($Body -match 'P1[-\s]Badge|P1-orange|severity[:\s]*high\b') { return 'P1' }
  if ($Body -match 'P2[-\s]Badge|P2-yellow|severity[:\s]*medium\b') { return 'P2' }
  if ($Body -match '(?i)\bseverity\s*[:=]\s*high\b') { return 'P1' }
  if ($Body -match '(?i)\bseverity\s*[:=]\s*medium\b') { return 'P2' }
  if ($Body -match '(?i)\bP1\b') { return 'P1' }
  if ($Body -match '(?i)\bP2\b') { return 'P2' }
  return 'skip'
}

function Select-Comments($Comments, [int]$MaxCount) {
  $p1 = @(); $p2 = @()
  foreach ($c in $Comments) {
    $sev = Get-Severity $c.body
    if ($sev -eq 'P1') { $p1 += $c } elseif ($sev -eq 'P2') { $p2 += $c }
  }
  $selected = @()
  foreach ($c in $p1) { if ($selected.Count -ge $MaxCount) { break }; $selected += $c }
  foreach ($c in $p2) { if ($selected.Count -ge $MaxCount) { break }; $selected += $c }
  $remainingP2 = [Math]::Max(0, $p2.Count - ($MaxCount - [Math]::Min($MaxCount, $p1.Count)))
  [pscustomobject]@{ selected = $selected; p1_total = $p1.Count; p2_total = $p2.Count; remaining_p2 = $remainingP2 }
}

function Build-Prompt($Selected, $Pr) {
  $lines = @(
    "You are an auto-fix agent for the $Repo project.",
    '',
    'Fix ONLY the issues listed below. Do NOT fix anything not explicitly listed.',
    'Do NOT commit or push. Do NOT run tests.',
    'Do NOT modify CI/CD, rulesets, permissions, secrets, production configs, or architecture.',
    'After each fix, verify the change is correct and minimal.',
    '',
    "ISSUES TO FIX ($($Selected.Count) items):",
    ''
  )
  $i = 1
  foreach ($c in $Selected) {
    $sev = Get-Severity $c.body
    $normalized = $c.body -replace '\n', ' ' -replace '\s+', ' '
    $limit = [Math]::Min(500, $normalized.Length)
    $desc = $normalized.Substring(0, $limit)
    $lines += "$i. [FILE: $($c.path)] [SEVERITY: $sev] [LINE: $($c.line)]"
    $lines += "   $desc"
    $lines += ''
    $i++
  }
  return ($lines -join "`n")
}

function Save-State {
  if (-not $DryRun -and $State) { $State | ConvertTo-Json -Depth 10 | Set-Content $StateFile }
}

# Guard: state serialization needs State in scope; define a script-level variable
$State = $null

# ------------------------------------------------------------------
# 3. Guards
# ------------------------------------------------------------------
if (-not $DryRun -and $Allowed.Count -eq 0) {
  Write-Log 'no-allowed-prs' @{ message = 'allowed_prs is empty; refusing to process all PRs' }
  throw 'allowed_prs is empty. Add PR numbers to config.json or use -DryRun.'
}

if (Test-Path $LockFile) {
  Write-Log 'locked' @{ message = 'another poller is already running' }
  throw 'another poller is already running'
}

$dirty = git status --porcelain 2>&1
if ($dirty) {
  Write-Log 'dirty-worktree' @{ message = 'worktree is not clean, aborting' }
  throw 'worktree is not clean'
}

# ------------------------------------------------------------------
# 4. State
# ------------------------------------------------------------------
$State = if (Test-Path $StateFile) {
  try { Get-Content $StateFile -Raw | ConvertFrom-Json } catch { [pscustomobject]@{ prs = [pscustomobject]@{} } }
} else {
  [pscustomobject]@{ prs = [pscustomobject]@{} }
}
if (-not ($State.prs -is [PSCustomObject])) { $State.prs = [pscustomobject]@{} }

# ------------------------------------------------------------------
# 5. Main - wrapped in try/finally for lock cleanup
# ------------------------------------------------------------------
$CreatedLock = $false
try {
  if (-not $DryRun) { New-Item $LockFile -ItemType File | Out-Null; $CreatedLock = $true }

  $PrIds = if ($PullRequest) {
    [int[]]@($PullRequest)
  } else {
    [int[]]((Invoke-Gh @('pr', 'list', '--repo', $Repo, '--state', 'open', '--limit', '100', '--json', 'number', '--jq', '.[].number')) -split "`n" | Where-Object { $_ })
  }
  Write-Log 'start' @{ prs = $PrIds; dryrun = $DryRun.IsPresent }

  foreach ($pr in $PrIds) {
    try {
      # --- Fetch PR metadata ---
      $Meta = Invoke-Gh @('pr', 'view', "$pr", '--repo', $Repo, '--json', 'state,headRefName,headRefOid,headRepositoryOwner') | ConvertFrom-Json
      if ($Meta.state -ne 'OPEN') { Write-Log 'skip-closed' @{ pr = $pr }; continue }
      if ($Meta.headRepositoryOwner.login -ne $Owner) { Write-Log 'skip-fork' @{ pr = $pr }; continue }
      if ($Allowed -notcontains [int]$pr) { Write-Log 'skip-not-allowed' @{ pr = $pr }; continue }

      $Branch = $Meta.headRefName
      $Sha    = $Meta.headRefOid

      # --- Per-PR state ---
      if (-not $State.prs.$pr) {
        $entry = [pscustomobject]@{ round = 0; processed = [pscustomobject]@{}; fails = 0 }
        $State.prs | Add-Member -NotePropertyName "$pr" -NotePropertyValue $entry -Force -ErrorAction SilentlyContinue
      }
      $PrState = $State.prs.$pr
      if (-not $PrState) { throw "failed to init state for PR #$pr" }
      if (-not $PrState.processed) { $PrState | Add-Member -NotePropertyName 'processed' -NotePropertyValue ([pscustomobject]@{}) -Force -ErrorAction SilentlyContinue }

      $Round = [int]$PrState.round
      if ($Round -ge $MaxRounds) {
        Set-Label $pr 'human-review'
        Write-Log 'round-cap' @{ pr = $pr; round = $Round }
        continue
      }
      if ($PrState.fails -ge 2) {
        Set-Label $pr 'human-review'
        Write-Log 'fail-cap' @{ pr = $pr; fails = $PrState.fails }
        continue
      }

      # --- Fetch Codex reviews (paginated) ---
      $AllReviews = Invoke-Gh @('api', "--paginate", "--slurp", "repos/$Repo/pulls/$pr/reviews?per_page=100") | ConvertFrom-Json | ForEach-Object { $_ }
      $Review = $AllReviews | Where-Object {
        $_.user.login -eq $CodexUser -and
        $_.state -eq 'COMMENTED' -and
        $_.commit_id -eq $Sha -and
        -not $PrState.processed.([string]$_.id)
      } | Sort-Object submitted_at | Select-Object -Last 1

      if (-not $Review) { Write-Log 'no-new-review' @{ pr = $pr; sha = $Sha }; continue }

      $ReviewId = $Review.id

      # --- Fetch review comments (paginated) ---
      $Comments = Invoke-Gh @('api', "--paginate", "--slurp", "repos/$Repo/pulls/$pr/reviews/$ReviewId/comments?per_page=100") | ConvertFrom-Json | ForEach-Object { $_ }
      if (-not $Comments) {
        Write-Log 'no-comments' @{ pr = $pr; review_id = $ReviewId }
        if ($PrState.processed) { $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force -ErrorAction SilentlyContinue }
        Save-State
        continue
      }

      # --- High-risk check (per-comment title, never standalone keywords) ---
      if (Test-HighRisk $Comments) {
        Set-Label $pr 'needs-human-review'
        if ($PrState.processed) { $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force -ErrorAction SilentlyContinue }
        Save-State
        Write-Log 'escalated-human' @{ pr = $pr; review_id = $ReviewId }
        continue
      }

      # --- Select comments (up to MaxPerCall, P1 first) ---
      $Selection = Select-Comments $Comments $MaxPerCall
      $FixList = $Selection.selected

      if ($FixList.Count -eq 0) {
        Write-Log 'no-fixable' @{ pr = $pr; review_id = $ReviewId; p1 = $Selection.p1_total; p2 = $Selection.p2_total }
        if ($PrState.processed) { $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force -ErrorAction SilentlyContinue }
        Save-State
        continue
      }

      # --- P2 overflow check ---
      if ($Selection.remaining_p2 -gt 0 -and ($Round + 1) -ge $MaxRounds) {
        Set-Label $pr 'human-review'
        Write-Log 'p2-overflow' @{ pr = $pr; review_id = $ReviewId; remaining_p2 = $Selection.remaining_p2; round = $Round }
        continue
      }

      # --- Dry-run ---
      if ($DryRun) {
        Write-Log 'dry-run' @{ pr = $pr; sha = $Sha; branch = $Branch; review_id = $ReviewId; fix_count = $FixList.Count; p1 = $Selection.p1_total; p2 = $Selection.p2_total; remaining_p2 = $Selection.remaining_p2 }
        continue
      }

      # --- Execute Claude ---
      Write-Log 'fix-start' @{ pr = $pr; sha = $Sha; branch = $Branch; review_id = $ReviewId; round = $Round + 1; fix_count = $FixList.Count }

      git fetch origin "$Branch" 2>$null
      git switch --force-create "$Branch" "$Sha" | Out-Null
      Write-Log 'checkout' @{ branch = $Branch; sha = $Sha }

      $Prompt = Build-Prompt $FixList $pr
      Write-Log 'prompt' @{ pr = $pr; chars = $Prompt.Length; items = $FixList.Count }

      $Prompt | & claude -p --dangerously-skip-permissions --max-turns 15 --output-format text
      if ($LASTEXITCODE) { throw "claude exited with code $LASTEXITCODE" }

      # --- Post-Claude guards ---
      $CurBranch = git branch --show-current
      if ($CurBranch -ne $Branch) { throw "branch changed: $CurBranch != $Branch" }

      $Changes = git status --porcelain
      if (-not $Changes) {
        Write-Log 'no-changes' @{ pr = $pr; review_id = $ReviewId }
        if ($PrState.processed) { $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force -ErrorAction SilentlyContinue }
        $PrState.round = $Round + 1
        Save-State
        continue
      }

      # --- Run tests ---
      foreach ($TestCmd in $Cfg.test_commands) {
        Write-Log 'test-run' @{ pr = $pr; command = $TestCmd }
        & powershell -NoProfile -Command $TestCmd
        if ($LASTEXITCODE) { throw "tests failed: $TestCmd (exit $LASTEXITCODE)" }
      }
      Write-Log 'test-pass' @{ pr = $pr }

      # --- Commit & push ---
      git add -A
      git commit -m "fix: auto-fix for PR #$pr (review $ReviewId round $($Round+1))" | Out-Null
      git push origin "HEAD:$Branch" | Out-Null

      # --- Update state ---
      if ($PrState.processed) { $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force -ErrorAction SilentlyContinue }
      $PrState.round = $Round + 1
      $PrState.fails = 0
      Save-State

      Set-Label $pr "auto-fix-round-$($Round + 1)"
      if ($Round + 1 -ge $MaxRounds) { Set-Label $pr 'human-review' }

      Write-Log 'fix-done' @{ pr = $pr; sha = $Sha; round = $Round + 1; review_id = $ReviewId }

    } catch {
      if (-not $DryRun -and $State.prs.$pr) {
        $State.prs.$pr.fails = [int]$State.prs.$pr.fails + 1
        Save-State
      }
      Write-Log 'error' @{ pr = $pr; message = $_.Exception.Message }
      Write-Host "ERROR [PR #$pr]: $($_.Exception.Message)" -ForegroundColor Red
    }
  }

} finally {
  if ($CreatedLock) { Remove-Item $LockFile -Force -ErrorAction SilentlyContinue }
}

Write-Log 'done' @{ prs_checked = $PrIds.Count }
