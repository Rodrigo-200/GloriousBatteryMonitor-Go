using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Microsoft.Windows.Widgets.Providers;

namespace GBM.Desktop.Widgets;

/// <summary>
/// Handles conditional widget provider registration at app startup.
/// Widgets require Windows 11 22H2 (build 22621) or later.
///
/// The actual widget activation is handled by the OS via COM:
///   1. The sparse MSIX manifest registers our COM CLSID
///   2. When the user adds the widget, Windows activates our COM class
///   3. We hold a singleton BatteryWidgetProvider in memory so the
///      BatteryStateChanged subscription keeps it alive and able to push updates
/// </summary>
public static class WidgetRegistration
{
    private const int MinBuildForWidgets = 22621;

    /// <summary>
    /// The singleton widget provider instance, kept alive for the app lifetime
    /// so it can receive BatteryStateChanged events and push widget updates.
    /// </summary>
    public static BatteryWidgetProvider? Instance { get; private set; }

    public static void TryRegister(IBatteryMonitorService monitorService, ILoggerFactory loggerFactory)
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
            // Verify the Widget API is available by trying to access WidgetManager.
            // This will throw if the sparse manifest isn't registered or the OS doesn't support widgets.
            _ = WidgetManager.GetDefault();

            var widgetLogger = loggerFactory.CreateLogger<BatteryWidgetProvider>();
            Instance = new BatteryWidgetProvider(monitorService, widgetLogger);

            logger.LogInformation("[WIDGET] Widget provider initialized — waiting for COM activation from Windows");
        }
        catch (Exception ex)
        {
            // Widget registration is best-effort — don't crash the app
            logger.LogWarning(ex, "[WIDGET] Failed to initialize widget provider — widgets unavailable");
        }
    }
}
