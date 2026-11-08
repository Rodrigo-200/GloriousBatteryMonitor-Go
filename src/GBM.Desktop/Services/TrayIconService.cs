using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.ApplicationLifetimes;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Microsoft.Win32;
using System;
using System.IO;
using System.Threading;

namespace GBM.Desktop.Services;

public class TrayIconService : IDisposable
{
    private readonly IBatteryMonitorService _monitorService;
    private readonly ILogger<TrayIconService> _logger;
    private TrayIcon? _trayIcon;
    private NativeMenu? _menu;
    private NativeMenuItem? _infoItem;
    private NativeMenuItem? _updateItem;
    private NativeMenuItem? _rescanItem;
    private NativeMenuItem? _showItem;
    private NativeMenuItem? _widgetItem;
    private NativeMenuItem? _settingsItem;
    private NativeMenuItem? _quitItem;
    private WindowIcon? _currentIcon;
    private Timer? _promotionTimer;
    private int _promotionAttempts;
    private int _lastLevel;
    private bool _lastCharging;
    private bool _lastConnected;
    private string _lastTooltipText = string.Empty;
    private string _lastInfoHeaderText = string.Empty;
    private string _lastUpdateHeaderText = string.Empty;

    public TrayIconService(
        IBatteryMonitorService monitorService,
        ILogger<TrayIconService> logger)
    {
        _monitorService = monitorService;
        _logger = logger;
    }

    public void Initialize()
    {
        try
        {
            _menu = new NativeMenu();

            _infoItem = new NativeMenuItem(Localization.Text("MouseNotFound")) { IsEnabled = false };
            _menu.Items.Add(_infoItem);
            _menu.Items.Add(new NativeMenuItemSeparator());

            _updateItem = new NativeMenuItem("Update Available") { IsVisible = false };
            _updateItem.Click += (_, _) => { /* Could open browser */ };
            _menu.Items.Add(_updateItem);

            _rescanItem = new NativeMenuItem(Localization.Text("Rescan"));
            _rescanItem.Click += (_, _) => _monitorService.TriggerRescan();
            _menu.Items.Add(_rescanItem);

            _showItem = new NativeMenuItem(Localization.Text("MainWindow"));
            _showItem.Click += (_, _) => ShowMainWindow();
            _menu.Items.Add(_showItem);

            _widgetItem = new NativeMenuItem(Localization.Text("Widget"));
            _widgetItem.Click += (_, _) => ShowBatteryWidget();
            _menu.Items.Add(_widgetItem);

            _settingsItem = new NativeMenuItem(Localization.Text("Settings"));
            _settingsItem.Click += (_, _) => ShowSettings();
            _menu.Items.Add(_settingsItem);

            _menu.Items.Add(new NativeMenuItemSeparator());

            _quitItem = new NativeMenuItem(Localization.Text("Quit"));
            _quitItem.Click += (_, _) =>
            {
                if (Application.Current?.ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
                    desktop.Shutdown();
            };
            _menu.Items.Add(_quitItem);

            _trayIcon = new TrayIcon
            {
                ToolTipText = "Glorious Battery Monitor",
                Menu = _menu,
                IsVisible = true
            };
            _lastTooltipText = _trayIcon.ToolTipText ?? string.Empty;
            _trayIcon.Clicked += (_, _) => ToggleMainWindow();
            _currentIcon = TrayIconRenderer.RenderIcon(0, false, false);
            _trayIcon.Icon = _currentIcon;
            StartPromotingOwnIcon();

            _monitorService.BatteryStateChanged += OnBatteryStateChanged;
            _lastInfoHeaderText = _infoItem.Header?.ToString() ?? string.Empty;
            Localization.LanguageChanged += UpdateMenuLanguage;
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
                        ? $"{Localization.Text("LastKnown")}: {state.Level}%"
                        : Localization.Text("MouseNotFound")
                    : $"{state.DeviceName} — {state.Level}%{(state.IsCharging ? $" ({Localization.Text("Charging")})" : string.Empty)}";
                UpdateToolTipText(toolTipText);

                // Update menu info item
                var infoHeader = _lastConnected
                    ? $"{state.DeviceName} — {state.Level}%"
                    : Localization.Text("MouseNotFound");
                UpdateInfoHeader(infoHeader);
            });
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TRAY] Error updating tray");
        }
    }

    private void UpdateTrayIcon()
    {
        if (_trayIcon == null) return;

        var icon = TrayIconRenderer.RenderIcon(_lastLevel, _lastCharging, _lastConnected);

        if (icon != null && !ReferenceEquals(_currentIcon, icon))
        {
            _currentIcon = icon;
            _trayIcon.Icon = icon;
        }
        _trayIcon.IsVisible = true;
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

    private void ShowSettings()
    {
        if (Application.Current is App app) app.ShowSettingsFromTray();
    }

    private void StartPromotingOwnIcon()
    {
        if (!OperatingSystem.IsWindows())
            return;

        // Explorer creates its per-icon record shortly after Shell_NotifyIcon registration.
        _promotionTimer = new Timer(_ =>
        {
            if (TryPromoteOwnIcon() || Interlocked.Increment(ref _promotionAttempts) >= 20)
            {
                _promotionTimer?.Dispose();
                _promotionTimer = null;
            }
        }, null, TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(3));
    }

    private static bool TryPromoteOwnIcon()
    {
        var processPath = Environment.ProcessPath;
        if (string.IsNullOrWhiteSpace(processPath))
            return false;

        using var settings = Registry.CurrentUser.OpenSubKey(
            @"Control Panel\NotifyIconSettings", writable: false);
        if (settings == null)
            return false;

        var promoted = false;
        foreach (var name in settings.GetSubKeyNames())
        {
            using var iconSettings = settings.OpenSubKey(name, writable: true);
            var executable = iconSettings?.GetValue("ExecutablePath") as string;
            if (iconSettings == null || !PathsEqual(executable, processPath))
                continue;

            iconSettings.SetValue("IsPromoted", 1, RegistryValueKind.DWord);
            promoted = true;
        }

        return promoted;
    }

    private static bool PathsEqual(string? left, string right)
    {
        if (string.IsNullOrWhiteSpace(left))
            return false;
        try
        {
            return string.Equals(Path.GetFullPath(left), Path.GetFullPath(right),
                StringComparison.OrdinalIgnoreCase);
        }
        catch
        {
            return string.Equals(left, right, StringComparison.OrdinalIgnoreCase);
        }
    }

    private void UpdateMenuLanguage()
    {
        if (_menu == null) return;
        Avalonia.Threading.Dispatcher.UIThread.Post(() =>
        {
            // Native menu captions are refreshed in-place while preserving menu actions.
            if (_infoItem != null && !_lastConnected) _infoItem.Header = Localization.Text("MouseNotFound");
            if (_rescanItem != null) _rescanItem.Header = Localization.Text("Rescan");
            if (_showItem != null) _showItem.Header = Localization.Text("MainWindow");
            if (_widgetItem != null) _widgetItem.Header = Localization.Text("Widget");
            if (_settingsItem != null) _settingsItem.Header = Localization.Text("Settings");
            if (_quitItem != null) _quitItem.Header = Localization.Text("Quit");
        });
    }

    private void ShowBatteryWidget()
    {
        if (Application.Current is App app)
            app.ShowBatteryWidgetFromTray();
    }

    public void Dispose()
    {
        _monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        Localization.LanguageChanged -= UpdateMenuLanguage;
        _promotionTimer?.Dispose();
        _promotionTimer = null;
        if (_trayIcon != null)
        {
            _trayIcon.IsVisible = false;
            _trayIcon.Dispose();
            _currentIcon = null;
        }
    }
}
