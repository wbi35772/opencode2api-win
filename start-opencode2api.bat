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
echo   Closing the window also stops the edge proxy: the gateway,
echo   the proxy and this console share one job object, so nothing
echo   is left running and no network listener survives.
echo   Logs are mirrored to logs\console.log
echo ============================================================
echo.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-supervised.ps1"
set "CODE=%ERRORLEVEL%"

echo.
if not "%CODE%"=="0" echo Gateway exited with code %CODE%.
echo Gateway stopped.
pause
