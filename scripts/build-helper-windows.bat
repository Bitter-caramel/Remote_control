@echo off
setlocal
REM Build helper (server) Windows executable -> ..\bin\helper.exe
cd /d "%~dp0..\helper"
if not exist "..\bin" mkdir "..\bin"
go build -o "..\bin\helper.exe" ./cmd/helper
echo.
echo Build finished, exit code: %errorlevel%
pause
