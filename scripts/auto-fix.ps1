<#
.SYNOPSIS
  Local Codex auto-fix poller - single pass.
.DESCRIPTION
  Reads Codex reviews on allowlisted open PRs, classifies findings, and for
  P1/P2 issues invokes Claude Code to fix, test, commit, and push. Never
  merges, never touches production data, never modifies rulesets or CI/CD.
.PARAMETER PullRequest
  Check a single PR number instead of all allowed open PRs.
.PARAMETER DryRun
  Evaluate reviews and log the action plan without invoking Claude or pushing.
.PARAMETER Once
  Alias - the default mode is a single pass (no built-in loop).
#>
[CmdletBinding()]
param(
  [int]$PullRequest = 0,
  [switch]$DryRun,
  [switch]$Once
)

$ErrorActionPreference = 'Stop'

# ------------------------------------------------------------------
# 1. Bootstrap - paths, config, directories
# ------------------------------------------------------------------
$Root      = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ConfPath  = Join-Path $PSScriptRoot 'config.json'
$ConfEx    = Join-Path $PSScriptRoot 'config.example.json'

if (Test-Path $ConfPath) {
  $Cfg = Get-Content $ConfPath -Raw | ConvertFrom-Json
} else {
  $Cfg = Get-Content $ConfEx -Raw | ConvertFrom-Json
}

$Repo      = $Cfg.repo
$Owner     = $Cfg.owner
$CodexUser = $Cfg.codex_login
$MaxRounds = $Cfg.max_rounds
$Allowed   = @($Cfg.allowed_prs | ForEach-Object { [int]$_ })

$StateDir  = Join-Path $Root $Cfg.state_dir
$LogDir    = Join-Path $Root $Cfg.log_dir
$StateFile = Join-Path $StateDir 'state.json'
$LockFile  = Join-Path $StateDir 'run.lock'
$LogFile   = Join-Path $LogDir 'auto-fix.jsonl'

New-Item -Force -ItemType Directory $StateDir, $LogDir | Out-Null

# Guard: ensure we are in a git repository
if (-not (Test-Path (Join-Path $Root '.git'))) {
  throw "project root is not a git repository: $Root"
}

# Pin working directory to project root for all subsequent git operations
Set-Location -LiteralPath $Root

# ------------------------------------------------------------------
# 2. Helpers
# ------------------------------------------------------------------

function Write-Log($Event, $Data = @{}) {
  $entry = [ordered]@{
    ts    = (Get-Date).ToUniversalTime().ToString('o')
    event = $Event
    data  = $Data
  } | ConvertTo-Json -Compress -Depth 10
  $entry | Tee-Object $LogFile -Append | Write-Host
}

function Invoke-Gh([string[]]$Args) {
  $out = & gh @Args 2>$null
  if ($LASTEXITCODE) {
    $err = & gh @Args 2>&1 | Out-String
    throw ($err.Trim())
  }
  return ($out -join "`n")
}

function Set-Label($Pr, $Label) {
  $labels = (Invoke-Gh @('pr', 'view', "$Pr", '--repo', $Repo, '--json', 'labels', '--jq', '.labels[].name')) -split "`n"
  if ($labels -notcontains $Label) {
    if (-not $DryRun) {
      Invoke-Gh @('pr', 'edit', "$Pr", '--repo', $Repo, '--add-label', $Label) | Out-Null
      Write-Log 'label-added' @{ pr = $Pr; label = $Label }
    } else {
      Write-Log 'label-dryrun' @{ pr = $Pr; label = $Label }
    }
  }
}

function Test-HighRisk($Body) {
  $keywords = @('P0', 'critical', 'permission', 'secret', 'credential',
    'production', 'architecture', 'ruleset', 'merge', 'security')
  foreach ($k in $keywords) {
    if ($Body -match "(?i)\b$k\b") { return $true }
  }
  return $false
}

# ------------------------------------------------------------------
# 3. Load or initialize state
# ------------------------------------------------------------------
$State = if (Test-Path $StateFile) {
  Get-Content $StateFile -Raw | ConvertFrom-Json
} else {
  [pscustomobject]@{ prs = @{} }
}

if (-not $State.prs) {
  $State | Add-Member -NotePropertyName 'prs' -NotePropertyValue ([pscustomobject]@{}) -Force
}

# ------------------------------------------------------------------
# 4. Single-instance lock
# ------------------------------------------------------------------
if (Test-Path $LockFile) {
  Write-Log 'locked' @{ message = 'another poller is already running' }
  throw 'another poller is already running'
}
if (-not $DryRun) {
  New-Item $LockFile -ItemType File | Out-Null
}

# ------------------------------------------------------------------
# 5. Guard: worktree must be clean
# ------------------------------------------------------------------
$dirty = git status --porcelain 2>&1
if ($dirty) {
  Write-Log 'dirty-worktree' @{ message = 'worktree is not clean, aborting' }
  if (-not $DryRun) { Remove-Item $LockFile -Force -ErrorAction SilentlyContinue }
  throw 'worktree is not clean; commit or stash changes before running auto-fix'
}

# ------------------------------------------------------------------
# 6. Resolve PR list
# ------------------------------------------------------------------
$PrIds = if ($PullRequest) {
  [int[]]@($PullRequest)
} else {
  [int[]]((Invoke-Gh @('pr', 'list', '--repo', $Repo, '--state', 'open', '--json', 'number', '--jq', '.[].number')) -split "`n" | Where-Object { $_ })
}

Write-Log 'start' @{ prs = $PrIds; dryrun = $DryRun.IsPresent }

# ------------------------------------------------------------------
# 7. Main loop - process each PR
# ------------------------------------------------------------------
foreach ($pr in $PrIds) {
  try {
    # --- 7a. Fetch PR metadata ---
    $Meta = Invoke-Gh @('pr', 'view', "$pr", '--repo', $Repo, '--json', 'state,headRefName,headRefOid,headRepositoryOwner') | ConvertFrom-Json

    if ($Meta.state -ne 'OPEN') {
      Write-Log 'skip-closed' @{ pr = $pr }
      continue
    }
    if ($Meta.headRepositoryOwner.login -ne $Owner) {
      Write-Log 'skip-fork' @{ pr = $pr; owner = $Meta.headRepositoryOwner.login }
      continue
    }

    if ($Allowed.Count -and $Allowed -notcontains [int]$pr) {
      Write-Log 'skip-not-allowed' @{ pr = $pr; allowed = $Allowed }
      continue
    }

    $Branch = $Meta.headRefName
    $Sha    = $Meta.headRefOid

    # --- 7b. Initialize per-PR state ---
    if (-not $State.prs.$pr) {
      $entry = [pscustomobject]@{ rounds = @{}; processed = @{}; fails = 0 }
      $State.prs | Add-Member -NotePropertyName "$pr" -NotePropertyValue $entry -Force
    }
    $PrState = $State.prs.$pr

    # Guard: round cap
    $Round = if ($PrState.rounds.$Sha) { [int]$PrState.rounds.$Sha } else { 0 }
    if ($Round -ge $MaxRounds) {
      Set-Label $pr 'human-review'
      Write-Log 'round-cap' @{ pr = $pr; sha = $Sha; round = $Round }
      continue
    }

    # Guard: too many consecutive failures
    if ($PrState.fails -ge 2) {
      Set-Label $pr 'human-review'
      Write-Log 'fail-cap' @{ pr = $pr; fails = $PrState.fails }
      continue
    }

    # --- 7c. Fetch matching Codex review ---
    $Reviews = Invoke-Gh @('api', "repos/$Repo/pulls/$pr/reviews") | ConvertFrom-Json
    $Review  = $Reviews | Where-Object {
      $_.user.login -eq $CodexUser -and
      $_.state -eq 'COMMENTED' -and
      $_.commit_id -eq $Sha -and
      -not $PrState.processed.([string]$_.id)
    } | Sort-Object submitted_at | Select-Object -Last 1

    if (-not $Review) {
      Write-Log 'no-new-review' @{ pr = $pr; sha = $Sha }
      continue
    }

    $ReviewId = $Review.id

    # --- 7d. Fetch review comments ---
    $Comments = Invoke-Gh @('api', "repos/$Repo/pulls/$pr/reviews/$ReviewId/comments") | ConvertFrom-Json
    if (-not $Comments) {
      Write-Log 'no-comments' @{ pr = $pr; review_id = $ReviewId }
      $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force
      continue
    }

    $Body = ($Comments | ForEach-Object { "$($_.path): $($_.body)" }) -join "`n"

    # --- 7e. Severity classification ---
    if (Test-HighRisk $Body) {
      Set-Label $pr 'needs-human-review'
      $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force
      $State | ConvertTo-Json -Depth 10 | Set-Content $StateFile
      Write-Log 'escalated-human' @{ pr = $pr; review_id = $ReviewId; reason = 'high-risk keywords detected' }
      continue
    }

    # --- 7f. Dry-run: log intent and stop ---
    if ($DryRun) {
      Write-Log 'dry-run' @{
        pr        = $pr
        sha       = $Sha
        branch    = $Branch
        review_id = $ReviewId
        action    = 'would invoke claude, test, commit, push'
        comments  = $Comments.Count
      }
      $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force
      continue
    }

    # --- 7g. Execute Claude Code fix ---
    Write-Log 'fix-start' @{ pr = $pr; sha = $Sha; branch = $Branch; review_id = $ReviewId; round = $Round + 1 }

    git switch --force-create "$Branch" "$Sha" | Out-Null
    Write-Log 'checkout' @{ branch = $Branch; sha = $Sha }

    $Prompt = "You are an auto-fix agent. Fix only P1/P2 issues from the review below. Do NOT merge, change CI/rulesets, access production, or commit. Review:`n$Body"
    $Prompt | & claude -p --dangerously-skip-permissions --max-turns 15 --output-format text
    if ($LASTEXITCODE) {
      throw "claude exited with code $LASTEXITCODE"
    }

    # Guard: confirm we are still on the correct branch
    $CurrentBranch = git branch --show-current
    if ($CurrentBranch -ne $Branch) {
      throw "branch changed unexpectedly: expected '$Branch', got '$CurrentBranch'"
    }

    # Guard: confirm there are changes
    $Changes = git status --porcelain
    if (-not $Changes) {
      Write-Log 'no-changes' @{ pr = $pr; review_id = $ReviewId }
      $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force
      continue
    }

    # --- 7h. Run test commands ---
    foreach ($TestCmd in $Cfg.test_commands) {
      Write-Log 'test-run' @{ pr = $pr; command = $TestCmd }
      & powershell -NoProfile -Command $TestCmd
      if ($LASTEXITCODE) {
        throw "tests failed: $TestCmd (exit $LASTEXITCODE)"
      }
    }
    Write-Log 'test-pass' @{ pr = $pr }

    # --- 7i. Commit and push ---
    git add -A
    git commit -m "fix: auto-fix for PR #$pr (review $ReviewId round $($Round+1))" | Out-Null

    git push origin "HEAD:$Branch" | Out-Null

    # --- 7j. Update state ---
    $PrState.rounds.$Sha = $Round + 1
    $PrState.processed | Add-Member -NotePropertyName ([string]$ReviewId) -NotePropertyValue $true -Force
    $PrState.fails = 0
    $State | ConvertTo-Json -Depth 10 | Set-Content $StateFile

    Set-Label $pr "auto-fix-round-$($Round + 1)"

    if ($Round + 1 -ge $MaxRounds) {
      Set-Label $pr 'human-review'
      Write-Log 'round-cap-reached' @{ pr = $pr; round = $Round + 1 }
    }

    Write-Log 'fix-done' @{ pr = $pr; sha = $Sha; round = $Round + 1; review_id = $ReviewId }

  } catch {
    if ($State.prs.$pr) {
      $State.prs.$pr.fails = [int]$State.prs.$pr.fails + 1
      $State | ConvertTo-Json -Depth 10 | Set-Content $StateFile
    }
    Write-Log 'error' @{ pr = $pr; message = $_.Exception.Message }
    Write-Host "ERROR [PR #$pr]: $($_.Exception.Message)" -ForegroundColor Red
  }
}

# ------------------------------------------------------------------
# 8. Cleanup
# ------------------------------------------------------------------
if (-not $DryRun) {
  Remove-Item $LockFile -Force -ErrorAction SilentlyContinue
}

Write-Log 'done' @{ prs_checked = $PrIds.Count }
