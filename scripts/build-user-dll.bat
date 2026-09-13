@echo off
setlocal
REM Build user (client) as DLL (requires gcc, e.g. mingw-w64) -> ..\bin\user.dll
cd /d "%~dp0..\user"
if not exist "..\bin" mkdir "..\bin"
go build -buildmode=c-shared -o "..\bin\user.dll" ./cmd/user
echo.
echo Build finished, exit code: %errorlevel%
pause
