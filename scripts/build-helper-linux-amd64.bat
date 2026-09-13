@echo off
setlocal
REM Cross-compile helper (server) for Linux amd64 (static, no gcc) -> ..\bin\helper_linux_amd64
cd /d "%~dp0..\helper"
if not exist "..\bin" mkdir "..\bin"
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -o "..\bin\helper_linux_amd64" ./cmd/helper
echo.
echo Build finished, exit code: %errorlevel%
pause
