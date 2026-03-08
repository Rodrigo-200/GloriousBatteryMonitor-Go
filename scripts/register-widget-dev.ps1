# Registers the sparse MSIX manifest for development widget testing.
# Run this once after build, and re-run after any manifest changes.
# Requires elevation (Run as Administrator).

param(
    [string]$BuildOutput = "$PSScriptRoot\..\src\GBM.Desktop\bin\Release\net8.0-windows10.0.22621.0"
)

$ErrorActionPreference = "Stop"

$ManifestPath = Resolve-Path "$PSScriptRoot\..\sparse-manifest\AppxManifest.xml"
$ExePath = Join-Path $BuildOutput "GBM.Desktop.exe"

if (-not (Test-Path $ExePath)) {
    Write-Error "Build output not found at $ExePath. Run 'dotnet build --configuration Release' first."
    exit 1
}

$BuildOutputFull = Resolve-Path $BuildOutput

# Generate a self-signed cert for dev if it doesn't exist
$CertSubject = "CN=GloriousBatteryMonitor-Dev"
$Cert = Get-ChildItem Cert:\CurrentUser\My | Where-Object { $_.Subject -eq $CertSubject } | Select-Object -First 1

if (-not $Cert) {
    Write-Host "Creating self-signed developer certificate..."
    $Cert = New-SelfSignedCertificate `
        -Subject $CertSubject `
        -CertStoreLocation "Cert:\CurrentUser\My" `
        -KeyUsage DigitalSignature `
        -Type CodeSigningCert

    # Trust it by adding to the Trusted Root store
    $Store = New-Object System.Security.Cryptography.X509Certificates.X509Store(
        [System.Security.Cryptography.X509Certificates.StoreName]::Root,
        [System.Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser
    )
    $Store.Open("ReadWrite")
    $Store.Add($Cert)
    $Store.Close()
    Write-Host "Certificate created and trusted: $($Cert.Thumbprint)"
} else {
    Write-Host "Using existing certificate: $($Cert.Thumbprint)"
}

# Register the sparse manifest
Write-Host ""
Write-Host "Registering sparse manifest..."
Write-Host "  Manifest: $ManifestPath"
Write-Host "  External: $BuildOutputFull"
Write-Host ""

try {
    Add-AppxPackage -Register $ManifestPath -ExternalLocation $BuildOutputFull
    Write-Host ""
    Write-Host "SUCCESS: Widget provider registered." -ForegroundColor Green
    Write-Host "  1. Launch GBM.Desktop.exe from: $BuildOutputFull"
    Write-Host "  2. Open the Widgets board: Win+W"
    Write-Host "  3. Click '+ Add widgets' and find 'Mouse Battery'"
    Write-Host ""
} catch {
    Write-Error "Registration failed: $_"
    Write-Host ""
    Write-Host "If the package is already registered, try unregistering first:" -ForegroundColor Yellow
    Write-Host "  powershell -File scripts\unregister-widget-dev.ps1"
    exit 1
}
