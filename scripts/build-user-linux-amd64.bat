@echo off
setlocal
REM Cross-compile user (client) for Linux amd64 (static, no gcc) -> ..\bin\user_linux_amd64
cd /d "%~dp0..\user"
if not exist "..\bin" mkdir "..\bin"
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -o "..\bin\user_linux_amd64" ./cmd/user
echo.
echo Build finished, exit code: %errorlevel%
pause
