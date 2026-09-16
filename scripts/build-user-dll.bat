@echo off
setlocal EnableExtensions
for /F %%a in ('echo prompt $E ^| cmd') do set "ESC=%%a"
title RemoteAssist Build ^| user ^| dll

cd /d "%~dp0..\user" || goto :path_fail
if not exist "..\bin" mkdir "..\bin"

echo.
echo %ESC%[96m============================================================%ESC%[0m
echo %ESC%[96m   RemoteAssist :: Build%ESC%[0m
echo %ESC%[96m============================================================%ESC%[0m
echo    Component : %ESC%[93muser (client)%ESC%[0m
echo    Target    : windows / amd64 ^(c-shared DLL, needs gcc^)
echo    Output    : bin\user.dll ^(+ bin\user.h^)
echo    Time      : %DATE% %TIME%
echo ------------------------------------------------------------
echo    %ESC%[90m[1/2] compiling...%ESC%[0m

go build -buildmode=c-shared -o "..\bin\user.dll" ./cmd/user
set "RC=%errorlevel%"
if not "%RC%"=="0" goto :build_fail

echo    %ESC%[92m[2/2] build succeeded%ESC%[0m
for %%F in ("..\bin\user.dll") do echo    %ESC%[90martifact  : bin\user.dll ^| %%~zF bytes%ESC%[0m
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
echo    %ESC%[91m[ERROR] go build failed, exit code %RC% (is gcc installed? e.g. mingw-w64^)%ESC%[0m
echo ------------------------------------------------------------
pause
exit /b %RC%
