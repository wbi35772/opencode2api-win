$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot

if (-not (Test-Path -LiteralPath .\opencode2api.exe)) {
    throw "opencode2api.exe not found in $PSScriptRoot"
}
if (-not (Test-Path -LiteralPath .\config.json)) {
    throw "config.json not found in $PSScriptRoot"
}

Write-Host "Starting opencode2api in the foreground. Press Ctrl+C to stop."
& .\opencode2api.exe -config .\config.json
