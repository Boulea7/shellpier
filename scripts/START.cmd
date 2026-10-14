@echo off
setlocal
cd /d "%~dp0"
powershell.exe -NoProfile -Command "if ([Environment]::OSVersion.Version.Major -lt 10) { exit 7 } else { exit 0 }"
if errorlevel 7 (
  echo This modern executable requires Windows 10 or newer.
  echo Follow docs\legacy-windows.md for the independent legacy setup.
  call "%~dp0legacy\shellpier.cmd" doctor
  pause
  exit /b 7
)
echo Temporary foreground access. Close with Ctrl+C. No autostart is installed.
echo Direct mode requires peer reachability. Set SHELLPIER_RELAY_URL and SHELLPIER_RELAY_KEY for your approved relay.
"%~dp0shellpier.exe" share --keep-awake --output "%~dp0access.json"
pause
