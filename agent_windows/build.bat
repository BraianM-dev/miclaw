@echo off
setlocal EnableDelayedExpansion

:: ============================================================
:: build.bat — Compila Frank v2.1-beta para Windows
::
:: USO:
::   build.bat          → compila 64-bit y 32-bit
::   build.bat 64       → solo 64-bit
::   build.bat 32       → solo 32-bit
::   build.bat clean    → elimina binarios anteriores
::
:: REQUISITOS:
::   Go 1.21+  en PATH
::   MinGW-w64 64-bit en PATH (para Frank64)
::     Instalar en MSYS2: pacman -S mingw-w64-x86_64-gcc
::     PATH: C:\msys64\mingw64\bin
::   MinGW-w64 32-bit (para Frank32, solo si lo necesitas):
::     Instalar en MSYS2: pacman -S mingw-w64-i686-gcc
::     PATH NO es necesario — se detecta automáticamente en C:\msys64\mingw32\bin
:: ============================================================

set GOOS=windows
set CGO_ENABLED=1
set LDFLAGS=-H=windowsgui -s -w

:: Rutas al compilador 32-bit (se detectan automáticamente)
set GCC32_PATH=C:\msys64\mingw32\bin\gcc.exe

if "%1"=="clean" goto :clean
if "%1"=="32"    goto :build32
if "%1"=="64"    goto :build64

:: Sin argumento → ambos
goto :build64_then_32

:: ─────────────────────────────── 64-bit ──────────────────────────────────
:build64
echo.
echo [BUILD 64-bit] GOARCH=amd64 -- renderer OpenGL/GLFW
set GOARCH=amd64
set CC=
if exist Frank64.exe del Frank64.exe
go build -ldflags="%LDFLAGS%" -o Frank64.exe .
if %ERRORLEVEL% neq 0 (
    echo [ERROR] Compilacion 64-bit fallida.
    exit /b 1
)
echo [OK] Frank64.exe listo.
call :sign Frank64.exe
goto :eof

:: ─────────────────────────────── 32-bit ──────────────────────────────────
:build32
echo.
echo [BUILD 32-bit] GOARCH=386 -- renderer SOFTWARE (sin OpenGL)

:: Verificar que el compilador 32-bit existe
if not exist "%GCC32_PATH%" (
    echo [ERROR] No se encontro el compilador 32-bit en: %GCC32_PATH%
    echo.
    echo Para instalarlo, abre MSYS2 y ejecuta:
    echo   pacman -S mingw-w64-i686-gcc
    echo.
    echo Luego vuelve a ejecutar: build.bat 32
    exit /b 1
)

echo       Usando: %GCC32_PATH%
set GOARCH=386
set CC=%GCC32_PATH%
if exist Frank32.exe del Frank32.exe
go build -tags softwarerender -ldflags="%LDFLAGS%" -o Frank32.exe .
if %ERRORLEVEL% neq 0 (
    echo [ERROR] Compilacion 32-bit fallida.
    exit /b 1
)
echo [OK] Frank32.exe listo.
call :sign Frank32.exe
goto :eof

:: ─────────────────────────── firma Authenticode ───────────────────────────
:sign
if exist sign-frank.ps1 (
    echo [SIGN] Firmando %~1 con certificado AFE...
    powershell -NoProfile -ExecutionPolicy Bypass -File sign-frank.ps1 2>nul
    if %ERRORLEVEL% equ 0 (
        echo [OK] %~1 firmado.
    ) else (
        echo [WARN] Firma omitida ^(ejecuta sign-frank.ps1 como Administrador una vez^).
    )
) else (
    echo [WARN] sign-frank.ps1 no encontrado — ejecutable SIN firmar.
)
goto :eof

:: ─────────────────────────── ambos en secuencia ──────────────────────────
:build64_then_32
call :build64
if %ERRORLEVEL% neq 0 exit /b 1
call :build32
if %ERRORLEVEL% neq 0 (
    echo [WARN] Frank32 no compilado. Frank64 esta disponible.
)
goto :eof

:: ─────────────────────────────── clean ───────────────────────────────────
:clean
echo [CLEAN] Eliminando binarios...
if exist Frank64.exe del Frank64.exe && echo   Eliminado Frank64.exe
if exist Frank32.exe del Frank32.exe && echo   Eliminado Frank32.exe
echo [CLEAN] Listo.
goto :eof
