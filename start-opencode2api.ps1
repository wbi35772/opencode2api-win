$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

if (-not (Test-Path -LiteralPath .\opencode2api.exe)) {
    throw "opencode2api.exe not found in $PSScriptRoot"
}
if (-not (Test-Path -LiteralPath .\config.json)) {
    throw "config.json not found in $PSScriptRoot"
}

# Same supervisor as the .bat entry point: closing the console removes the
# gateway and the edge proxy together instead of leaving an orphan.
& (Join-Path $PSScriptRoot 'start-supervised.ps1')
