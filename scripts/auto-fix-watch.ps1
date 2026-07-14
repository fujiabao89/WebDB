param([int]$Interval=0,[switch]$DryRun)
$cfg=Get-Content "$PSScriptRoot/config.json" -Raw|ConvertFrom-Json;if($Interval -le 0){$Interval=$cfg.poll_seconds}
while($true){& "$PSScriptRoot/auto-fix.ps1" -DryRun:$DryRun;if($LASTEXITCODE){Write-Warning 'poll failed; retrying next cycle'};Start-Sleep $Interval}
