@echo off
echo Building Glorious Battery Monitor...
echo.

REM Clean old builds
if exist GloriousBatteryMonitor.exe del GloriousBatteryMonitor.exe

REM Build with no console window
go build -ldflags="-H windowsgui" -o GloriousBatteryMonitor.exe

if %ERRORLEVEL% EQU 0 (
    echo.
    echo ✓ Build successful: GloriousBatteryMonitor.exe
    echo.
) else (
    echo.
    echo ✗ Build failed
    echo.
    exit /b 1
)
