using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Media.Imaging;
using Avalonia.Platform;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using System;
using System.IO;

namespace GBM.Desktop.Services;

public class TrayIconService : IDisposable
{
    private readonly IBatteryMonitorService _monitorService;
    private readonly ISettingsService _settingsService;
    private readonly ILogger<TrayIconService> _logger;
    private TrayIcon? _trayIcon;
    private NativeMenu? _menu;
    private NativeMenuItem? _infoItem;
    private NativeMenuItem? _updateItem;
    private int _lastLevel;
    private bool _lastCharging;
    private bool _lastConnected;
    private bool _lastShowPercentage;

    public TrayIconService(
        IBatteryMonitorService monitorService,
        ISettingsService settingsService,
        ILogger<TrayIconService> logger)
    {
        _monitorService = monitorService;
        _settingsService = settingsService;
        _logger = logger;
    }

    public void Initialize()
    {
        try
        {
            _menu = new NativeMenu();

            _infoItem = new NativeMenuItem("Mouse Not Found") { IsEnabled = false };
            _menu.Items.Add(_infoItem);
            _menu.Items.Add(new NativeMenuItemSeparator());

            _updateItem = new NativeMenuItem("Update Available") { IsVisible = false };
            _updateItem.Click += (_, _) => { /* Could open browser */ };
            _menu.Items.Add(_updateItem);

            var rescanItem = new NativeMenuItem("Rescan");
            rescanItem.Click += (_, _) => _monitorService.TriggerRescan();
            _menu.Items.Add(rescanItem);

            var showItem = new NativeMenuItem("Show Window");
            showItem.Click += (_, _) => ShowMainWindow();
            _menu.Items.Add(showItem);

            _menu.Items.Add(new NativeMenuItemSeparator());

            var quitItem = new NativeMenuItem("Quit");
            quitItem.Click += (_, _) =>
            {
                if (Application.Current?.ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
                    desktop.Shutdown();
            };
            _menu.Items.Add(quitItem);

            _trayIcon = new TrayIcon
            {
                ToolTipText = "Glorious Battery Monitor",
                Menu = _menu,
                IsVisible = true
            };
            _trayIcon.Clicked += (_, _) => ToggleMainWindow();

            // Try to set icon from embedded resource
            try
            {
                var uri = new Uri("avares://GBM.Desktop/Assets/app-icon.ico");
                _trayIcon.Icon = new WindowIcon(AssetLoader.Open(uri));
            }
            catch
            {
                // Icon not critical
            }

            _monitorService.BatteryStateChanged += OnBatteryStateChanged;
            _settingsService.SettingsChanged += OnSettingsChanged;
            _lastShowPercentage = _settingsService.Current.ShowPercentageOnTrayIcon;
            _logger.LogInformation("[TRAY] Tray icon initialized");
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TRAY] Failed to initialize tray icon");
        }
    }

    private void OnBatteryStateChanged(BatteryState state)
    {
        try
        {
            Avalonia.Threading.Dispatcher.UIThread.Post(() =>
            {
                var newLevel = state.Level;
                var newCharging = state.IsCharging;
                var newConnected = state.Connection == ConnectionState.Connected;

                bool iconNeedsUpdate = newLevel != _lastLevel
                                    || newCharging != _lastCharging
                                    || newConnected != _lastConnected;

                _lastLevel = newLevel;
                _lastCharging = newCharging;
                _lastConnected = newConnected;

                if (iconNeedsUpdate)
                    UpdateTrayIcon();

                // Update tooltip
                if (_trayIcon != null)
                {
                    if (!_lastConnected)
                    {
                        _trayIcon.ToolTipText = state.Connection == ConnectionState.LastKnown
                            ? $"Last known: {state.Level}%"
                            : "Mouse Not Found";
                    }
                    else
                    {
                        var chargingText = state.IsCharging ? " (Charging)" : "";
                        _trayIcon.ToolTipText = $"{state.DeviceName} — {state.Level}%{chargingText}";
                    }
                }

                // Update menu info item
                if (_infoItem != null)
                {
                    _infoItem.Header = _lastConnected
                        ? $"{state.DeviceName} — {state.Level}%"
                        : "Mouse Not Found";
                }
            });
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TRAY] Error updating tray");
        }
    }

    private void OnSettingsChanged(AppSettings settings)
    {
        Avalonia.Threading.Dispatcher.UIThread.Post(() =>
        {
            var showPercentage = settings.ShowPercentageOnTrayIcon;
            if (showPercentage != _lastShowPercentage)
            {
                _lastShowPercentage = showPercentage;
                UpdateTrayIcon();
            }
        });
    }

    private void UpdateTrayIcon()
    {
        if (_trayIcon == null) return;

        var icon = TrayIconRenderer.RenderIcon(
            _lastLevel, _lastCharging, _lastConnected, _lastShowPercentage);

        if (icon != null)
            _trayIcon.Icon = icon;
    }

    public void ShowUpdateAvailable(string version)
    {
        Avalonia.Threading.Dispatcher.UIThread.Post(() =>
        {
            if (_updateItem != null)
            {
                _updateItem.Header = $"Update Available ({version})";
                _updateItem.IsVisible = true;
            }
        });
    }

    private void ShowMainWindow()
    {
        if (Application.Current?.ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            desktop.MainWindow?.Show();
            desktop.MainWindow?.Activate();
        }
    }

    private void ToggleMainWindow()
    {
        if (Application.Current?.ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            var window = desktop.MainWindow;
            if (window == null) return;

            if (window.IsVisible)
                window.Hide();
            else
            {
                window.Show();
                window.Activate();
            }
        }
    }

    public void Dispose()
    {
        _monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        _settingsService.SettingsChanged -= OnSettingsChanged;
        if (_trayIcon != null)
        {
            _trayIcon.IsVisible = false;
            _trayIcon.Dispose();
        }
    }
}
