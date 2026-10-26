@echo off
rem C2 server build script: produces both Windows and Linux binaries.
setlocal
cd /d "%~dp0"

echo [*] Building Windows server...
go build -trimpath -ldflags "-s -w" -o c2server.exe ./server
if %errorlevel% neq 0 ( echo [-] Windows build FAILED & goto :end )

echo [*] Building Linux server (for VPS deployment)...
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o c2server ./server
if %errorlevel% neq 0 ( echo [-] Linux build FAILED & goto :end )

echo [*] Done:
for %%A in (c2server.exe c2server) do echo     %%A  %%~zA bytes  %%~tA

:end
echo.
pause
endlocal
