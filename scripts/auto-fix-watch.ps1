<#
.SYNOPSIS
  Local Codex auto-fix polling loop.
.DESCRIPTION
  Calls auto-fix.ps1 on a configurable interval until stopped.
.PARAMETER Interval
  Seconds between polls. Defaults to config.poll_seconds.
.PARAMETER DryRun
  Passed through to auto-fix.ps1 — no Claude invocation, no pushes.
#>
[CmdletBinding()]
param([int]$Interval = 0, [switch]$DryRun)

$cfg = if (Test-Path "$PSScriptRoot/config.json") {
  Get-Content "$PSScriptRoot/config.json" -Raw | ConvertFrom-Json
} else {
  Get-Content "$PSScriptRoot/config.example.json" -Raw | ConvertFrom-Json
}

if ($Interval -le 0) { $Interval = $cfg.poll_seconds }

while ($true) {
  & "$PSScriptRoot/auto-fix.ps1" -DryRun:$DryRun
  if ($LASTEXITCODE) { Write-Warning "poll failed; retrying next cycle" }
  Start-Sleep $Interval
}
