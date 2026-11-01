$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot

$pidFile = Join-Path $PSScriptRoot "logs\pid.txt"
if (-not (Test-Path -LiteralPath $pidFile)) {
    Write-Host "No pid file found; nothing to stop."
    exit 0
}

$serverPid = [int](Get-Content -LiteralPath $pidFile)
$proc = Get-Process -Id $serverPid -ErrorAction SilentlyContinue
if ($proc) {
    Stop-Process -Id $serverPid -Force
    Write-Host "Stopped opencode2api (PID $serverPid)."
} else {
    Write-Host "PID $serverPid is not running."
}
Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
