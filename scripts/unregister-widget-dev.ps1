# Unregisters the sparse MSIX manifest for the widget provider.
# Run this before re-registering after manifest changes.

$ErrorActionPreference = "Stop"

$Package = Get-AppxPackage -Name "GloriousBatteryMonitor" -ErrorAction SilentlyContinue

if ($Package) {
    Write-Host "Removing package: $($Package.PackageFullName)"
    Remove-AppxPackage -Package $Package.PackageFullName
    Write-Host "Widget provider unregistered." -ForegroundColor Green
} else {
    Write-Host "Package 'GloriousBatteryMonitor' is not registered." -ForegroundColor Yellow
}
