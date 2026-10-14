@echo off
setlocal
if not exist "%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" (
  echo [UNSUPPORTED] Windows PowerShell is required. No changes made.
  exit /b 2
)
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "%~dp0shellpier.ps1" %*
exit /b %errorlevel%
