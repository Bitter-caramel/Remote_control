@echo off
setlocal
REM Build user (client) Windows executable -> ..\bin\user.exe
cd /d "%~dp0..\user"
if not exist "..\bin" mkdir "..\bin"
go build -o "..\bin\user.exe" ./cmd/user
echo.
echo Build finished, exit code: %errorlevel%
pause
