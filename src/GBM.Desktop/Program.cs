using Avalonia;
using GBM.Core.Services;
using GBM.Desktop.Widgets;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Serilog;
using Velopack;

namespace GBM.Desktop;

internal sealed class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
        // When the widget host activates our COM server via the sparse manifest's
        // com:ExeServer, it launches the EXE with this argument.
        // In that case we run headless: no UI, no single-instance mutex.
        if (args.Any(a => a.Contains("RegisterProcessAsComServer", StringComparison.OrdinalIgnoreCase)))
        {
            RunHeadlessComServer();
            return;
        }

        // Normal user-launched startup
        // Prevent multiple instances — exit silently if already running
        using var mutex = new Mutex(true, "GloriousBatteryMonitor_SingleInstance", out bool isNew);
        if (!isNew)
            return;

        VelopackApp.Build().Run();
        BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }

    /// <summary>
    /// Headless mode for COM-activated launches by the Windows widget host.
    /// Builds a minimal DI container (logging + battery monitoring), registers the
    /// COM class factory, and blocks until the process is terminated.
    /// No UI is created — the widget host only needs the IWidgetProvider COM object.
    /// </summary>
    private static void RunHeadlessComServer()
    {
        var services = new ServiceCollection();

        // Logging
        var settingsPath = GetSettingsPath();
        var logPath = System.IO.Path.Combine(settingsPath, "widget-host.log");
        System.IO.Directory.CreateDirectory(settingsPath);

        var serilogLogger = new LoggerConfiguration()
            .MinimumLevel.Debug()
            .WriteTo.File(logPath,
                rollingInterval: RollingInterval.Day,
                fileSizeLimitBytes: 5 * 1024 * 1024,
                rollOnFileSizeLimit: true)
            .CreateLogger();

        services.AddLogging(builder =>
        {
            builder.AddSerilog(serilogLogger, dispose: true);
            builder.SetMinimumLevel(LogLevel.Information);
        });

        // Core services needed for widget data
        services.AddSingleton<ISettingsService, GBM.Core.Services.SettingsService>();
        services.AddSingleton<IStorageService, GBM.Core.Services.StorageService>();
        services.AddSingleton<IHidDeviceService, GBM.Core.Services.HidDeviceService>();
        services.AddSingleton<IBatteryEstimationService, GBM.Core.Services.BatteryEstimationService>();
        services.AddSingleton<INotificationService, GBM.Core.Services.NotificationService>();
        services.AddSingleton<IBatteryMonitorService, GBM.Core.Services.BatteryMonitorService>();

        var serviceProvider = services.BuildServiceProvider();
        App.Services = serviceProvider;

        var loggerFactory = serviceProvider.GetRequiredService<ILoggerFactory>();
        var logger = loggerFactory.CreateLogger("WidgetComServer");
        logger.LogInformation("[WIDGET-COM] Headless COM server starting");

        // Load settings and start battery monitoring so the widget has data
        var settingsService = serviceProvider.GetRequiredService<ISettingsService>();
        settingsService.Load();

        var monitor = serviceProvider.GetRequiredService<IBatteryMonitorService>();
        _ = monitor.StartAsync();
        logger.LogInformation("[WIDGET-COM] Battery monitoring started");

        // Register COM class factory — this is what the widget host will call
        WidgetRegistration.TryRegister(loggerFactory);

        // Block forever — the widget host will terminate us when no longer needed.
        // Use an event that never signals so we don't spin.
        logger.LogInformation("[WIDGET-COM] Headless COM server ready, waiting for widget host calls");
        var shutdownEvent = new ManualResetEventSlim(false);
        Console.CancelKeyPress += (_, e) =>
        {
            e.Cancel = true;
            shutdownEvent.Set();
        };
        shutdownEvent.Wait();

        // Cleanup
        WidgetRegistration.TryRevoke();
        _ = monitor.StopAsync();
        serviceProvider.Dispose();
    }

    public static AppBuilder BuildAvaloniaApp()
        => AppBuilder.Configure<App>()
            .UsePlatformDetect()
            .WithInterFont()
            .LogToTrace();

    private static string GetSettingsPath()
    {
        return System.IO.Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
            "GloriousBatteryMonitor");
    }
}
