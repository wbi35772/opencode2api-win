@echo off
setlocal
cd /d "%~dp0"

if not exist "opencode2api.exe" (
    echo opencode2api.exe not found.
    pause
    exit /b 1
)

if not exist "logs" mkdir "logs"

echo ============================================================
echo   opencode2api
echo   API : http://127.0.0.1:28080/v1
echo   UI  : http://127.0.0.1:28081
echo.
echo   Close this window or press Ctrl+C to stop the gateway.
echo   Logs are mirrored to logs\console.log
echo ============================================================
echo.

rem The edge proxy pins opencode.ai to Cloudflare edges that answer on
rem this line; it falls back to the system resolver when they do not.
if exist "%~dp0tools\edgeproxy.exe" (
    powershell -NoProfile -ExecutionPolicy Bypass -Command "if (-not (Get-NetTCPConnection -LocalPort 29090 -State Listen -ErrorAction SilentlyContinue)) { Start-Process -FilePath '%~dp0tools\edgeproxy.exe' -WindowStyle Hidden }"
)

powershell -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Continue'; & '.\opencode2api.exe' -config '.\config.json' 2>&1 | Tee-Object -FilePath '.\logs\console.log'"

if exist "tools\edgeproxy.exe" (
    taskkill /IM edgeproxy.exe /F >nul 2>&1
)

echo.
echo Gateway stopped.
pause
