# Widget Development Setup

Windows 11 widgets require the app to have a package identity.
In dev we use a sparse MSIX manifest with a self-signed certificate.

## Prerequisites

- Windows 11 22H2 (build 22621) or later
- .NET 8 SDK
- PowerShell (run as Administrator for registration)

## One-time setup

1. Build the app:
   ```
   dotnet build --configuration Release
   ```

2. Run the registration script (as Administrator):
   ```
   powershell -ExecutionPolicy Bypass -File scripts/register-widget-dev.ps1
   ```

3. Launch the app from `src/GBM.Desktop/bin/Release/net8.0-windows10.0.22621.0/GBM.Desktop.exe`

4. Open the Widgets board: `Win+W`

5. Click `+ Add widgets` and find "Mouse Battery"

## After any manifest change

Re-run the registration script. If it fails, unregister first:
```
powershell -ExecutionPolicy Bypass -File scripts/unregister-widget-dev.ps1
```
Then re-run `register-widget-dev.ps1`.

## Architecture

```
sparse-manifest/
  AppxManifest.xml        <- Sparse MSIX manifest (package identity + widget registration)
  Assets/                 <- Placeholder icons for the manifest
  Public/                 <- Required public folder for widget extensions

src/GBM.Desktop/
  Widgets/
    BatteryWidgetProvider.cs  <- IWidgetProvider implementation (COM-activated)
    WidgetRegistration.cs     <- Startup code (version check + initialization)

scripts/
  register-widget-dev.ps1     <- Creates dev cert + registers sparse manifest
  unregister-widget-dev.ps1   <- Removes the registered package
```

### How it works

1. The sparse manifest gives the unpackaged app a package identity (required by Windows for widget providers)
2. The manifest registers a COM class (CLSID `7719E9DB-2949-4ABF-977A-15E9E22D5F7B`) that points to `GBM.Desktop.exe`
3. At startup, `WidgetRegistration.TryRegister()` creates a `BatteryWidgetProvider` singleton that subscribes to `BatteryStateChanged` events
4. When Windows activates the widget, `IWidgetProvider.CreateWidget()` is called, and the provider pushes Adaptive Card updates to the Widgets board
5. On older Windows versions (< 22621), widget registration is silently skipped

## Notes

- The self-signed cert (`CN=GloriousBatteryMonitor-Dev`) is created automatically by the script
- This setup is for development only -- production will require a proper code-signing certificate
- Widget support requires Windows 11 22H2 (build 22621) or later
- The app gracefully skips widget registration on older Windows versions
- The widget uses Adaptive Cards to render battery level, charging status, device name, and last update time
