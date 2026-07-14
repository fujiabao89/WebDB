<#
.SYNOPSIS
  Local Codex auto-fix polling loop.
.DESCRIPTION
  Calls auto-fix.ps1 on a configurable interval until stopped.
  Errors are caught and logged; the loop always continues.
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
  try {
    & "$PSScriptRoot/auto-fix.ps1" -DryRun:$DryRun
  } catch {
    Write-Warning "watch cycle failed: $($_.Exception.Message); retrying in $Interval seconds"
  }
  Start-Sleep $Interval
}
