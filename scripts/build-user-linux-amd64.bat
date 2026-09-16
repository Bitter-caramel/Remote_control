@echo off
setlocal EnableExtensions
for /F %%a in ('echo prompt $E ^| cmd') do set "ESC=%%a"
title RemoteAssist Build ^| user ^| linux-amd64

cd /d "%~dp0..\user" || goto :path_fail
if not exist "..\bin" mkdir "..\bin"

set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64

echo.
echo %ESC%[96m============================================================%ESC%[0m
echo %ESC%[96m   RemoteAssist :: Build%ESC%[0m
echo %ESC%[96m============================================================%ESC%[0m
echo    Component : %ESC%[93muser (client)%ESC%[0m
echo    Target    : linux / amd64 ^(static, no gcc needed^)
echo    Output    : bin\user_linux_amd64
echo    Env       : CGO_ENABLED=0 GOOS=linux GOARCH=amd64
echo    Time      : %DATE% %TIME%
echo ------------------------------------------------------------
echo    %ESC%[90m[1/2] cross-compiling...%ESC%[0m

go build -o "..\bin\user_linux_amd64" ./cmd/user
set "RC=%errorlevel%"
if not "%RC%"=="0" goto :build_fail

echo    %ESC%[92m[2/2] build succeeded%ESC%[0m
for %%F in ("..\bin\user_linux_amd64") do echo    %ESC%[90martifact  : bin\user_linux_amd64 ^| %%~zF bytes%ESC%[0m
echo ------------------------------------------------------------
echo.
pause
exit /b 0

:path_fail
echo %ESC%[91m[ERROR] source directory not found: %~dp0..\user%ESC%[0m
pause
exit /b 1

:build_fail
echo.
echo    %ESC%[91m[ERROR] go build failed, exit code %RC%%ESC%[0m
echo ------------------------------------------------------------
pause
exit /b %RC%
