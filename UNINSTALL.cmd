@echo off
setlocal
cd /d "%~dp0"
codex-route-guard.exe uninstall
if errorlevel 1 (
  echo.
  echo Uninstall failed. See the error above.
  pause
  exit /b 1
)
echo.
echo Codex Route Guard autostart disabled.
pause
