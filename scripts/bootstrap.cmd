@echo off
powershell.exe -NoProfile -File "%~dp0bootstrap.ps1" %*
exit /b %errorlevel%
