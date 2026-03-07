using Avalonia;
using Velopack;

namespace GBM.Desktop;

internal sealed class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
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
}
