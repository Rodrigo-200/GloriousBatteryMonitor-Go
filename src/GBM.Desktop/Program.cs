using Avalonia;
using Velopack;

namespace GBM.Desktop;

internal sealed class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
        // Register AppUserModelId so Windows toast notifications work for
        // this non-packaged app. Must be called before any notification API.
        SetCurrentProcessExplicitAppUserModelID("GloriousBatteryMonitor.App");

        // Prevent multiple instances — exit silently if already running
        using var mutex = new Mutex(true, "GloriousBatteryMonitor_SingleInstance", out bool isNew);
        if (!isNew)
            return;

        VelopackApp.Build().Run();
        BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }

    public static AppBuilder BuildAvaloniaApp()
        => AppBuilder.Configure<App>()
            .UsePlatformDetect()
            .WithInterFont()
            .LogToTrace();

    [System.Runtime.InteropServices.DllImport("shell32.dll", SetLastError = true)]
    private static extern void SetCurrentProcessExplicitAppUserModelID(
        [System.Runtime.InteropServices.MarshalAs(System.Runtime.InteropServices.UnmanagedType.LPWStr)]
        string AppID);
}
