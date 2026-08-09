using Avalonia;
using GBM.Desktop.Services;
using System.Linq;
using Velopack;

namespace GBM.Desktop;

internal sealed class Program
{
    internal const string SkipUpdateCheckArg = "--gbm-skip-update-check-once";
    internal static bool SkipUpdateCheckOnLaunch { get; private set; }
    internal static SingleInstanceCoordinator? SingleInstance { get; private set; }

    [STAThread]
    public static void Main(string[] args)
    {
        // Register AppUserModelId so Windows toast notifications work for
        // this non-packaged app. Must be called before any notification API.
        SetCurrentProcessExplicitAppUserModelID("GloriousBatteryMonitor.App");

        using var singleInstance = SingleInstanceCoordinator.CreateForCurrentSession();
        if (!singleInstance.IsPrimaryInstance)
        {
            _ = singleInstance
                .NotifyPrimaryInstanceAsync(TimeSpan.FromSeconds(3))
                .GetAwaiter()
                .GetResult();
            return;
        }

        SingleInstance = singleInstance;

        SkipUpdateCheckOnLaunch = args.Any(a =>
            string.Equals(a, SkipUpdateCheckArg, StringComparison.OrdinalIgnoreCase));
        string[] filteredArgs = args
            .Where(a => !string.Equals(a, SkipUpdateCheckArg, StringComparison.OrdinalIgnoreCase))
            .ToArray();

        try
        {
            VelopackApp.Build().Run();
            BuildAvaloniaApp().StartWithClassicDesktopLifetime(filteredArgs);
        }
        finally
        {
            SingleInstance = null;
        }
    }

    public static AppBuilder BuildAvaloniaApp()
        => AppBuilder.Configure<App>()
            .UsePlatformDetect();

    [System.Runtime.InteropServices.DllImport("shell32.dll", SetLastError = true)]
    private static extern void SetCurrentProcessExplicitAppUserModelID(
        [System.Runtime.InteropServices.MarshalAs(System.Runtime.InteropServices.UnmanagedType.LPWStr)]
        string AppID);
}
