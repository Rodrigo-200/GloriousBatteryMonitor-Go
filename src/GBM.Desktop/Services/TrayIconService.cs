using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Platform;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using System;

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
    private WindowIcon? _currentIcon;
    private int _lastLevel;
    private bool _lastCharging;
    private bool _lastConnected;
    private bool _lastShowPercentage;
    private string _lastTooltipText = string.Empty;
    private string _lastInfoHeaderText = string.Empty;
    private string _lastUpdateHeaderText = string.Empty;

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
            _lastTooltipText = _trayIcon.ToolTipText ?? string.Empty;
            _trayIcon.Clicked += (_, _) => ToggleMainWindow();

            // Try to set icon from embedded resource
            try
            {
                var uri = new Uri("avares://GBM.Desktop/Assets/app-icon.ico");
                _currentIcon = new WindowIcon(AssetLoader.Open(uri));
                _trayIcon.Icon = _currentIcon;
            }
            catch
            {
                // Icon not critical
            }

            _monitorService.BatteryStateChanged += OnBatteryStateChanged;
            _settingsService.SettingsChanged += OnSettingsChanged;
            _lastShowPercentage = _settingsService.Current.ShowPercentageOnTrayIcon;
            _lastInfoHeaderText = _infoItem.Header?.ToString() ?? string.Empty;
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
                var toolTipText = !_lastConnected
                    ? state.Connection == ConnectionState.LastKnown
                        ? $"Last known: {state.Level}%"
                        : "Mouse Not Found"
                    : $"{state.DeviceName} — {state.Level}%{(state.IsCharging ? " (Charging)" : string.Empty)}";
                UpdateToolTipText(toolTipText);

                // Update menu info item
                var infoHeader = _lastConnected
                    ? $"{state.DeviceName} — {state.Level}%"
                    : "Mouse Not Found";
                UpdateInfoHeader(infoHeader);
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

        if (icon != null && !ReferenceEquals(_currentIcon, icon))
        {
            _currentIcon = icon;
            _trayIcon.Icon = icon;
        }
    }

    public void ShowUpdateAvailable(string version)
    {
        Avalonia.Threading.Dispatcher.UIThread.Post(() =>
        {
            if (_updateItem != null)
            {
                var header = $"Update Available ({version})";
                if (!string.Equals(_lastUpdateHeaderText, header, StringComparison.Ordinal))
                {
                    _updateItem.Header = header;
                    _lastUpdateHeaderText = header;
                }
                _updateItem.IsVisible = true;
            }
        });
    }

    private void UpdateToolTipText(string text)
    {
        if (_trayIcon == null || string.Equals(_lastTooltipText, text, StringComparison.Ordinal))
            return;

        _trayIcon.ToolTipText = text;
        _lastTooltipText = text;
    }

    private void UpdateInfoHeader(string text)
    {
        if (_infoItem == null || string.Equals(_lastInfoHeaderText, text, StringComparison.Ordinal))
            return;

        _infoItem.Header = text;
        _lastInfoHeaderText = text;
    }

    private void ShowMainWindow()
    {
        if (Application.Current is App app)
            app.ShowMainWindowFromTray();
    }

    private void ToggleMainWindow()
    {
        if (Application.Current is App app)
            app.ToggleMainWindowFromTray();
    }

    public void Dispose()
    {
        _monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        _settingsService.SettingsChanged -= OnSettingsChanged;
        if (_trayIcon != null)
        {
            _trayIcon.IsVisible = false;
            _trayIcon.Dispose();
            _currentIcon = null;
        }
    }
}
