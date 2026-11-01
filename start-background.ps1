$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot

$exe = Join-Path $PSScriptRoot "opencode2api.exe"
$config = Join-Path $PSScriptRoot "config.json"
$logs = Join-Path $PSScriptRoot "logs"
$pidFile = Join-Path $logs "pid.txt"
$stdout = Join-Path $logs "stdout.log"
$stderr = Join-Path $logs "stderr.log"

if (-not (Test-Path -LiteralPath $exe)) { throw "opencode2api.exe not found" }
if (-not (Test-Path -LiteralPath $config)) { throw "config.json not found" }
New-Item -ItemType Directory -Path $logs -Force | Out-Null

if (Test-Path -LiteralPath $pidFile) {
    $serverPid = [int](Get-Content -LiteralPath $pidFile)
    if (Get-Process -Id $serverPid -ErrorAction SilentlyContinue) {
        Write-Host "Already running (PID $serverPid)."
        exit 0
    }
    Remove-Item -LiteralPath $pidFile -Force
}

$cfg = Get-Content -LiteralPath $config -Raw | ConvertFrom-Json
$listen = $cfg.listen
if ($listen -match ':(?<port>\d+)$') {
    $port = [int]$Matches.port
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
        throw "Port $port is already in use. Stop the other process or change listen in config.json."
    }
} else {
    throw "Cannot parse the API port from listen=$listen"
}

$webPort = 28081
if ($cfg.webui.listen -match ':(?<port>\d+)$') {
    $webPort = [int]$Matches.port
}

$p = Start-Process -FilePath $exe `
    -ArgumentList "-config", "config.json" `
    -WorkingDirectory $PSScriptRoot `
    -WindowStyle Hidden `
    -RedirectStandardOutput $stdout `
    -RedirectStandardError $stderr `
    -PassThru
$p.Id | Set-Content -LiteralPath $pidFile

$api = "http://127.0.0.1:$port"
$started = $false
$ready = $false
for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Seconds 1
    if (-not (Get-Process -Id $p.Id -ErrorAction SilentlyContinue)) { break }
    $body = $null
    try {
        $body = & curl.exe -sS --noproxy "*" --max-time 2 "$api/healthz" 2>$null
    } catch {
        $body = $null
    }
    if ($body) {
        try {
            $health = $body | ConvertFrom-Json
            $started = $true
            if ($health.ready -eq $true) {
                $ready = $true
                break
            }
        } catch { }
    }
}

if (-not $started) {
    Write-Host "Process started (PID $($p.Id)) but the API did not respond in time."
    Write-Host "Check logs: $stderr"
    if (Test-Path -LiteralPath $stderr) { Get-Content -LiteralPath $stderr -Tail 20 }
    exit 1
}

$apiKey = $cfg.server_keys[0]
Write-Host "opencode2api is running (PID $($p.Id))."
Write-Host "API:   $api/v1"
Write-Host "WebUI: http://127.0.0.1:$webPort"
Write-Host "Key:   $apiKey"
Write-Host "Logs:  $stderr"

if ($ready) {
    Write-Host "Health: ready=true"
} else {
    Write-Host "Health: not ready yet. The server is up, but the free lane or a proxy may be unavailable."
    Write-Host "Open /healthz or the WebUI for details."
}
