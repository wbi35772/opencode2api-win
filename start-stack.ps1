$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$usqueDir = Join-Path $root 'usque'
$logs = Join-Path $root 'logs'
$exe = Join-Path $root 'opencode2api.exe'
$cfg = Join-Path $root 'config.json'
$usque = Join-Path $usqueDir 'usque.exe'
$ports = 18101..18106

if (-not (Test-Path -LiteralPath $usque)) {
    Write-Host "usque.exe not found in $usqueDir"
    exit 1
}
if (-not (Test-Path -LiteralPath $exe)) {
    Write-Host "opencode2api.exe not found."
    exit 1
}
New-Item -ItemType Directory -Force -Path $logs | Out-Null

Get-CimInstance Win32_Process -Filter "Name='usque.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.CommandLine -and $_.CommandLine -like "*$usqueDir*" } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 600

$children = @()
try {
    for ($i = 0; $i -lt $ports.Count; $i++) {
        $config = Join-Path $usqueDir ("config-{0}.json" -f ($i + 1))
        if (-not (Test-Path -LiteralPath $config)) {
            throw "Missing $config"
        }
        $log = Join-Path $logs ("usque-{0}.log" -f ($i + 1))
        $out = Join-Path $logs ("usque-{0}.out" -f ($i + 1))
        $child = Start-Process -FilePath $usque -ArgumentList @(
            'http-proxy', '-c', $config, '-P', '8443', '--always-reconnect',
            '-b', '127.0.0.1', '-p', "$($ports[$i])"
        ) -PassThru -RedirectStandardError $log -RedirectStandardOutput $out -WindowStyle Hidden
        $children += $child
        Write-Host ("WARP exit {0}: http://127.0.0.1:{1}" -f ($i + 1), $ports[$i])
    }

    Start-Sleep -Seconds 2
    $dead = @($children | Where-Object { $_.HasExited })
    if ($dead.Count -gt 0) {
        Write-Host ("Warning: {0} WARP exit(s) exited immediately; see logs\usque-*.log" -f $dead.Count)
    }

    Write-Host ""
    Write-Host "Starting gateway..."
    Write-Host ""
    & $exe -config $cfg 2>&1 | Tee-Object -FilePath (Join-Path $logs 'console.log')
}
finally {
    foreach ($child in $children) {
        if ($child -and -not $child.HasExited) {
            Stop-Process -Id $child.Id -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Host "WARP exits stopped."
}
