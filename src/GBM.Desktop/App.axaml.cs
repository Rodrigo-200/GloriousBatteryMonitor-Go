using Avalonia;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Data.Core.Plugins;
using Avalonia.Markup.Xaml;
using Avalonia.Styling;
using GBM.Core.Models;
using GBM.Core.Services;
using GBM.Desktop.Services;
using GBM.Desktop.ViewModels;
using GBM.Desktop.Views;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Serilog;
using System.Linq;

namespace GBM.Desktop;

public partial class App : Application
{
    private ServiceProvider? _serviceProvider;
    private TrayIconService? _trayService;
    private Lazy<WindowsToastService>? _lazyToastService;

    public static ServiceProvider? Services { get; private set; }

    public override void Initialize()
    {
        AvaloniaXamlLoader.Load(this);
    }

    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            // Remove duplicate data annotation validation
            var toRemove = BindingPlugins.DataValidators
                .OfType<DataAnnotationsValidationPlugin>().ToArray();
            foreach (var p in toRemove)
                BindingPlugins.DataValidators.Remove(p);

            // Build DI container
            var services = new ServiceCollection();
            ConfigureServices(services);
            _serviceProvider = services.BuildServiceProvider();
            Services = _serviceProvider;

            // Apply theme from settings
            var settingsService = _serviceProvider.GetRequiredService<ISettingsService>();
            settingsService.Load();
            ApplyTheme(settingsService.Current.Theme);
            settingsService.SettingsChanged += s => ApplyTheme(s.Theme);

            // Prevent auto-shutdown when we close the splash window
            desktop.ShutdownMode = Avalonia.Controls.ShutdownMode.OnExplicitShutdown;

            // Show splash screen immediately
            var splash = new SplashWindow();
            desktop.MainWindow = splash;
            splash.Show();

            // Launch startup flow (updates are handled in-app after main window starts)
            _ = RunStartupAsync(desktop, splash, settingsService);
        }

        base.OnFrameworkInitializationCompleted();
    }

    private async Task RunStartupAsync(
        IClassicDesktopStyleApplicationLifetime desktop,
        SplashWindow splash,
        ISettingsService settingsService)
    {
        try
        {
            splash.SetStatus("Starting...");

            var vm = _serviceProvider!.GetRequiredService<MainViewModel>();
            var mainWindow = new MainWindow { DataContext = vm };

            _trayService = _serviceProvider!.GetRequiredService<TrayIconService>();
            _trayService.Initialize();

            var notificationService = _serviceProvider!.GetRequiredService<INotificationService>();
            var lazyToast = _serviceProvider!.GetRequiredService<Lazy<WindowsToastService>>();
            _lazyToastService = lazyToast;

            notificationService.NotificationTriggered += (type, title, message) =>
            {
                // Accessing .Value here triggers construction on first notification only.
                // Subsequent calls reuse the already-constructed singleton.
                Task.Run(() => lazyToast.Value.ShowNotification(type, title, message));
            };

            // Start monitor BEFORE device search so events fire while splash is visible
            _ = vm.InitializeAsync();

            // Device connection phase on splash — max 20 seconds
            var monitorService = _serviceProvider!.GetRequiredService<IBatteryMonitorService>();
            await splash.ShowDeviceSearchAsync(monitorService, TimeSpan.FromSeconds(20));

            // Transition to main window
            desktop.MainWindow = mainWindow;
            mainWindow.Show();
            splash.Close();

            desktop.ShutdownMode = Avalonia.Controls.ShutdownMode.OnMainWindowClose;

            if (settingsService.Current.StartMinimized)
                mainWindow.Hide();

            desktop.ShutdownRequested += OnShutdown;
        }
        catch
        {
            // If anything goes wrong during startup, still try to launch main window
            try
            {
                var vm = _serviceProvider!.GetRequiredService<MainViewModel>();
                var mainWindow = new MainWindow { DataContext = vm };
                desktop.MainWindow = mainWindow;
                mainWindow.Show();
                splash.Close();
                desktop.ShutdownMode = Avalonia.Controls.ShutdownMode.OnMainWindowClose;

                _trayService = _serviceProvider!.GetRequiredService<TrayIconService>();
                _trayService.Initialize();

                _ = vm.InitializeAsync();
                desktop.ShutdownRequested += OnShutdown;
            }
            catch
            {
                // Fatal — nothing we can do
            }
        }
    }

    private void ConfigureServices(IServiceCollection services)
    {
        // Logging
        var settingsPath = GetSettingsPath();
        var logPath = System.IO.Path.Combine(settingsPath, "debug.log");
        System.IO.Directory.CreateDirectory(settingsPath);

        // Rename previous session log before overwriting, so crash logs survive
        string prevLogPath = System.IO.Path.Combine(settingsPath, "debug.prev.log");
        try
        {
            if (System.IO.File.Exists(logPath))
            {
                if (System.IO.File.Exists(prevLogPath))
                    System.IO.File.Delete(prevLogPath);
                System.IO.File.Move(logPath, prevLogPath);
            }
        }
        catch { }

        // Check debug mode from both env var and persisted settings before DI is built
        bool debugMode = System.Environment.GetEnvironmentVariable("GBM_DEBUG") == "1";
        if (!debugMode)
        {
            try
            {
                string settingsFile = System.IO.Path.Combine(settingsPath, "settings.json");
                if (System.IO.File.Exists(settingsFile))
                {
                    string json = System.IO.File.ReadAllText(settingsFile);
                    var parsed = System.Text.Json.JsonSerializer.Deserialize(
                        json, GbmJsonContext.Default.AppSettings);
                    debugMode = parsed?.DebugLogging ?? false;
                }
            }
            catch { }
        }

        var minSerilogLevel = debugMode
            ? Serilog.Events.LogEventLevel.Debug
            : Serilog.Events.LogEventLevel.Information;

        var serilogLogger = new LoggerConfiguration()
            .MinimumLevel.Is(minSerilogLevel)
            .WriteTo.File(logPath,
                rollingInterval: RollingInterval.Infinite,
                fileSizeLimitBytes: 5 * 1024 * 1024,
                rollOnFileSizeLimit: true)
            .CreateLogger();

        services.AddLogging(builder =>
        {
            builder.AddSerilog(serilogLogger, dispose: true);
            builder.SetMinimumLevel(debugMode ? LogLevel.Debug : LogLevel.Information);
        });

        // Core services
        services.AddSingleton<ISettingsService, SettingsService>();
        services.AddSingleton<IStorageService, StorageService>();
        services.AddSingleton<IHidDeviceService, HidDeviceService>();
        services.AddSingleton<IBatteryEstimationService, BatteryEstimationService>();
        services.AddSingleton<INotificationService, NotificationService>();
        services.AddSingleton<IBatteryMonitorService, BatteryMonitorService>();
        services.AddSingleton<IAutoStartService, AutoStartService>();
        services.AddSingleton<IUpdateService, UpdateService>();

        // Desktop services
        services.AddSingleton<TrayIconService>();
        services.AddSingleton<WindowsToastService>();
        services.AddSingleton<Lazy<WindowsToastService>>(sp =>
            new Lazy<WindowsToastService>(() => sp.GetRequiredService<WindowsToastService>()));

        // ViewModels
        services.AddSingleton<MainViewModel>();
    }

    private void ApplyTheme(string theme)
    {
        Avalonia.Threading.Dispatcher.UIThread.Post(() =>
        {
            // Dark-mode only — always force dark regardless of saved preference
            RequestedThemeVariant = ThemeVariant.Dark;

            var resources = Resources as Avalonia.Controls.ResourceDictionary;
            if (resources?.MergedDictionaries.Count > 0)
            {
                resources.MergedDictionaries.Clear();
            }

            var themeUri = "avares://GBM.Desktop/Themes/DarkTheme.axaml";

            try
            {
                var rd = new Avalonia.Markup.Xaml.Styling.ResourceInclude(new System.Uri(themeUri))
                {
                    Source = new System.Uri(themeUri)
                };
                resources?.MergedDictionaries.Add(rd);
            }
            catch { }
        });
    }

    private void OnShutdown(object? sender, ShutdownRequestedEventArgs e)
    {
        // Only dispose if the lazy was ever initialized (i.e. at least one notification fired)
        if (_lazyToastService?.IsValueCreated == true)
            _lazyToastService.Value.Dispose();

        _trayService?.Dispose();

        if (_serviceProvider?.GetService<MainViewModel>() is MainViewModel vm)
            vm.Dispose();

        if (_serviceProvider?.GetService<IBatteryMonitorService>() is BatteryMonitorService monitor)
        {
            _ = monitor.StopAsync();
        }
        _serviceProvider?.Dispose();
    }

    private static string GetSettingsPath()
    {
        if (OperatingSystem.IsWindows())
            return System.IO.Path.Combine(
                System.Environment.GetFolderPath(System.Environment.SpecialFolder.ApplicationData),
                "GloriousBatteryMonitor");
        if (OperatingSystem.IsMacOS())
            return System.IO.Path.Combine(
                System.Environment.GetFolderPath(System.Environment.SpecialFolder.UserProfile),
                "Library", "Application Support", "GloriousBatteryMonitor");
        return System.IO.Path.Combine(
            System.Environment.GetEnvironmentVariable("XDG_CONFIG_HOME") ??
            System.IO.Path.Combine(
                System.Environment.GetFolderPath(System.Environment.SpecialFolder.UserProfile),
                ".config"),
            "GloriousBatteryMonitor");
    }
}
