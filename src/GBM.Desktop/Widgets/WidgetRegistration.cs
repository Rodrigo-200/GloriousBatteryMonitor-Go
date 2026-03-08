using System.Runtime.InteropServices;
using Microsoft.Extensions.Logging;
using Microsoft.Windows.Widgets.Providers;

namespace GBM.Desktop.Widgets;

/// <summary>
/// Handles widget provider COM registration at app startup.
/// Widgets require Windows 11 22H2 (build 22621) or later.
///
/// The activation flow:
///   1. We call CoRegisterClassObject to tell COM "here is my factory for CLSID X"
///   2. The sparse MSIX manifest tells Windows "CLSID X is a widget provider"
///   3. When the user adds the widget, Windows calls CoCreateInstance → our factory → new BatteryWidgetProvider()
///   4. On shutdown we call CoRevokeClassObject to clean up
/// </summary>
public static class WidgetRegistration
{
    private const int MinBuildForWidgets = 22621;
    private static readonly Guid WidgetProviderClsid = Guid.Parse("7719E9DB-2949-4ABF-977A-15E9E22D5F7B");

    private const uint CLSCTX_LOCAL_SERVER = 0x4;
    private const uint REGCLS_MULTIPLEUSE = 0x1;

    private static uint _cookie;
    private static bool _registered;

    [DllImport("ole32.dll")]
    private static extern int CoRegisterClassObject(
        [MarshalAs(UnmanagedType.LPStruct)] Guid rclsid,
        [MarshalAs(UnmanagedType.IUnknown)] object pUnk,
        uint dwClsContext,
        uint flags,
        out uint lpdwRegister);

    [DllImport("ole32.dll")]
    private static extern int CoRevokeClassObject(uint dwRegister);

    public static void TryRegister(ILoggerFactory loggerFactory)
    {
        var logger = loggerFactory.CreateLogger("WidgetRegistration");

        if (!OperatingSystem.IsWindowsVersionAtLeast(10, 0, MinBuildForWidgets))
        {
            logger.LogInformation(
                "[WIDGET] Windows build {Build} is below {Min} — widget registration skipped",
                Environment.OSVersion.Version.Build, MinBuildForWidgets);
            return;
        }

        try
        {
            // Verify the Widget API is available (will throw if sparse manifest not registered)
            _ = WidgetManager.GetDefault();

            // Register our COM class factory so Windows can CoCreateInstance our widget provider
            var factory = new WidgetProviderFactory();
            int hr = CoRegisterClassObject(
                WidgetProviderClsid,
                factory,
                CLSCTX_LOCAL_SERVER,
                REGCLS_MULTIPLEUSE,
                out _cookie);

            if (hr != 0)
            {
                Marshal.ThrowExceptionForHR(hr);
            }

            _registered = true;
            logger.LogInformation(
                "[WIDGET] COM class factory registered (CLSID={Clsid}, cookie={Cookie}) — widget available in Win+W",
                WidgetProviderClsid, _cookie);
        }
        catch (Exception ex)
        {
            // Widget registration is best-effort — don't crash the app
            logger.LogWarning(ex, "[WIDGET] Failed to register widget COM class factory — widgets unavailable");
        }
    }

    public static void TryRevoke()
    {
        if (!_registered) return;

        try
        {
            CoRevokeClassObject(_cookie);
            _registered = false;
        }
        catch
        {
            // Best-effort cleanup
        }
    }
}
