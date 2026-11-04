$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$pidFile = Join-Path $PSScriptRoot 'logs\pid.txt'
$exe = Join-Path $PSScriptRoot 'opencode2api.exe'
$edge = Join-Path $PSScriptRoot 'tools\edgeproxy.exe'
$stopped = @()

if (Test-Path -LiteralPath $pidFile) {
    $serverPid = 0
    if ([int]::TryParse((Get-Content -LiteralPath $pidFile -Raw).Trim(), [ref]$serverPid)) {
        $proc = Get-CimInstance Win32_Process -Filter "ProcessId=$serverPid" -ErrorAction SilentlyContinue
        if ($proc -and $proc.ExecutablePath -eq $exe) {
            Stop-Process -Id $serverPid -Force -ErrorAction SilentlyContinue
            $stopped += $serverPid
        }
    }
    Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
}

# The .bat entry point does not write a pid file; it owns the gateway and the
# edge proxy through a job object. Find both by path so a manual stop from
# another window still removes everything this checkout started.
foreach ($path in @($exe, $edge)) {
    if (-not (Test-Path -LiteralPath $path)) { continue }
    $name = [IO.Path]::GetFileName($path)
    Get-CimInstance Win32_Process -Filter "Name='$name'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -eq $path } |
        ForEach-Object {
            Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
            $stopped += $_.ProcessId
        }
}

$stopped = @($stopped | Sort-Object -Unique)
if ($stopped.Count -gt 0) {
    Write-Host ("Stopped: {0}" -f ($stopped -join ', '))
} else {
    Write-Host "Nothing to stop."
}
