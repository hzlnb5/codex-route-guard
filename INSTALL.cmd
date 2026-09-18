@echo off
setlocal
cd /d "%~dp0"
codex-route-guard.exe install
if errorlevel 1 (
  echo.
  echo Installation failed. See the error above.
  pause
  exit /b 1
)
echo.
echo Codex Route Guard installed successfully.
pause
