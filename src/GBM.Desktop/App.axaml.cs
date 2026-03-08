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
using GBM.Desktop.Widgets;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Serilog;
using System.Linq;

namespace GBM.Desktop;

public partial class App : Application
{
    private ServiceProvider? _serviceProvider;
    private TrayIconService? _trayService;

    public static ServiceProvider? Services { get; internal set; }

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

            // Register Windows 11 widget COM factory as early as possible.
            // The widget host may attempt CoCreateInstance immediately when the app starts,
            // so this must happen before the splash screen and update check.
            var loggerFactory = _serviceProvider.GetRequiredService<ILoggerFactory>();
            WidgetRegistration.TryRegister(loggerFactory);

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

            // Run startup update check, then launch main window
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
            // Check for updates
            splash.SetStatus("Checking for updates...");
            var updateService = _serviceProvider!.GetRequiredService<IUpdateService>();

            try
            {
                var updateResult = await updateService.CheckForUpdateAsync();
                if (updateResult != null)
                {
                    // Ask user whether to update or skip
                    bool wantsUpdate = await splash.ShowUpdatePromptAsync(updateResult.NewVersion);
                    if (wantsUpdate)
                    {
                        splash.ShowDownloadProgress();
                        var progress = new Progress<int>(percent => splash.UpdateProgress(percent));
                        bool applied = await updateService.DownloadAndApplyUpdateAsync(progress);
                        if (applied)
                        {
                            // Velopack updater is waiting for us to exit,
                            // then it will apply the update silently and restart.
                            splash.SetStatus("Applying update...");
                            await Task.Delay(500);
                            desktop.Shutdown();
                            return;
                        }
                        // Download/apply failed — continue to normal launch
                        splash.SetStatus("Update failed. Starting...");
                        await Task.Delay(1500);
                    }
                }
            }
            catch
            {
                // Update check failed — continue to normal launch
            }

            splash.SetStatus("Starting...");

            // Create MainViewModel
            var vm = _serviceProvider!.GetRequiredService<MainViewModel>();

            // Create MainWindow
            var mainWindow = new MainWindow { DataContext = vm };

            // Initialize tray
            _trayService = _serviceProvider!.GetRequiredService<TrayIconService>();
            _trayService.Initialize();

            // Wire notification events to UI display
            var notificationService = _serviceProvider!.GetRequiredService<INotificationService>();
            notificationService.NotificationTriggered += (type, title, message) =>
            {
                Avalonia.Threading.Dispatcher.UIThread.Post(() =>
                {
                    vm.ShowToast($"{title}: {message}");

                    // Bring window to front so the user sees the toast
                    if (desktop.MainWindow is { } win)
                    {
                        win.Show();
                        win.Activate();
                    }
                });
            };

            // Switch from splash to main window
            desktop.MainWindow = mainWindow;
            mainWindow.Show();
            splash.Close();

            // Now that the main window is the active window, switch to normal
            // shutdown mode so closing it will exit the app
            desktop.ShutdownMode = Avalonia.Controls.ShutdownMode.OnMainWindowClose;

            // Hide after the transition if start-minimized is enabled
            if (settingsService.Current.StartMinimized)
            {
                mainWindow.Hide();
            }

            // Start monitoring
            _ = vm.InitializeAsync();

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

        // Clear the log file on every startup so it only contains the current session
        try { if (System.IO.File.Exists(logPath)) System.IO.File.Delete(logPath); } catch { }

        var serilogLogger = new LoggerConfiguration()
            .MinimumLevel.Debug()
            .WriteTo.File(logPath,
                rollingInterval: RollingInterval.Infinite,
                fileSizeLimitBytes: 5 * 1024 * 1024,
                rollOnFileSizeLimit: true)
            .CreateLogger();

        services.AddLogging(builder =>
        {
            builder.AddSerilog(serilogLogger, dispose: true);
            if (System.Environment.GetEnvironmentVariable("GBM_DEBUG") == "1")
                builder.SetMinimumLevel(LogLevel.Trace);
            else
                builder.SetMinimumLevel(LogLevel.Information);
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
        WidgetRegistration.TryRevoke();
        _trayService?.Dispose();
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
