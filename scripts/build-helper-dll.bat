@echo off
setlocal
REM Build helper (server) as DLL (requires gcc, e.g. mingw-w64) -> ..\bin\helper.dll
cd /d "%~dp0..\helper"
if not exist "..\bin" mkdir "..\bin"
go build -buildmode=c-shared -o "..\bin\helper.dll" ./cmd/helper
echo.
echo Build finished, exit code: %errorlevel%
pause
