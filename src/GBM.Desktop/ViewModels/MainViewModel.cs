using System.Collections.ObjectModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using GBM.Core.Models;
using GBM.Core.Services;
using Avalonia.Threading;

namespace GBM.Desktop.ViewModels;

public partial class MainViewModel : ViewModelBase
{
    private readonly IBatteryMonitorService _monitorService;
    private readonly ISettingsService _settingsService;
    private readonly IHidDeviceService _hidService;
    private readonly IAutoStartService _autoStartService;
    private readonly IUpdateService _updateService;
    private readonly IStorageService _storageService;

    // Battery state properties
    [ObservableProperty] private int _batteryLevel;
    [ObservableProperty] private bool _isCharging;
    [ObservableProperty] private bool _isConnected;
    [ObservableProperty] private string _deviceName = "Glorious Mouse";
    [ObservableProperty] private ConnectionState _connectionState = ConnectionState.NotConnected;
    [ObservableProperty] private string _statusText = "Not Connected";

    // Estimation
    [ObservableProperty] private bool _hasEstimate;
    [ObservableProperty] private TimeSpan _timeRemaining;
    [ObservableProperty] private string _timeRemainingText = "—";
    [ObservableProperty] private string _estimateLabel = "Time Remaining";

    // Charge history
    [ObservableProperty] private string _lastChargedText = "—";
    [ObservableProperty] private string _chargedToText = "—";

    // Last sync
    [ObservableProperty] private string _lastSyncText = "—";
    private DateTime? _lastReadTime;

    // Settings overlay
    [ObservableProperty] private bool _isSettingsOpen;

    // Settings fields (bound to SettingsView controls)
    [ObservableProperty] private bool _startWithOS;
    [ObservableProperty] private bool _startMinimized;
    [ObservableProperty] private bool _notificationsEnabled;
    [ObservableProperty] private bool _showPercentageOnTray;
    [ObservableProperty] private int _refreshInterval = 5;
    [ObservableProperty] private int _lowThreshold = 20;
    [ObservableProperty] private int _criticalThreshold = 10;
    [ObservableProperty] private bool _nonIntrusiveMode;
    [ObservableProperty] private bool _safeHidMode = true;
    [ObservableProperty] private bool _isAdvancedExpanded;
    [ObservableProperty] private bool _isDevToolsExpanded;

    // Developer tools output
    [ObservableProperty] private string _devOutput = string.Empty;

    // Theme
    [ObservableProperty] private string _currentTheme = "system";
    [ObservableProperty] private bool _isDarkTheme;

    // Update
    [ObservableProperty] private bool _updateAvailable;
    [ObservableProperty] private string _updateVersion = string.Empty;
    [ObservableProperty] private string _updateStatusText = string.Empty;
    [ObservableProperty] private bool _isCheckingUpdate = false;
    [ObservableProperty] private bool _isUpdateAvailable = false;
    [ObservableProperty] private int _updateDownloadProgress = 0;
    [ObservableProperty] private bool _isDownloadingUpdate = false;

    // Toast
    [ObservableProperty] private bool _isToastVisible;
    [ObservableProperty] private string _toastMessage = string.Empty;

    public MainViewModel(
        IBatteryMonitorService monitorService,
        ISettingsService settingsService,
        IHidDeviceService hidService,
        IAutoStartService autoStartService,
        IUpdateService updateService,
        IStorageService storageService)
    {
        _monitorService = monitorService;
        _settingsService = settingsService;
        _hidService = hidService;
        _autoStartService = autoStartService;
        _updateService = updateService;
        _storageService = storageService;

        _monitorService.BatteryStateChanged += OnBatteryStateChanged;
        _monitorService.EstimateChanged += OnEstimateChanged;

        LoadSettings();
    }

    public async Task InitializeAsync()
    {
        await _monitorService.StartAsync();
        StartSyncTimer();
        _ = CheckForUpdateAsync();
    }

    private void OnBatteryStateChanged(BatteryState state)
    {
        Dispatcher.UIThread.Post(() =>
        {
            BatteryLevel = state.Level;
            IsCharging = state.IsCharging;
            IsConnected = state.Connection == ConnectionState.Connected ||
                         state.Connection == ConnectionState.LastKnown ||
                         state.Connection == ConnectionState.Sleeping;
            DeviceName = state.DeviceName;
            ConnectionState = state.Connection;
            EstimateLabel = state.IsCharging ? "Time to Full" : "Time Remaining";

            if (state.LastReadTime > DateTime.MinValue)
                _lastReadTime = state.LastReadTime;

            if (state.LastChargeTime.HasValue)
                LastChargedText = state.LastChargeTime.Value.ToString("MMM d, h:mm tt");
            else
                LastChargedText = "—";

            if (state.LastChargeLevel.HasValue)
                ChargedToText = $"{state.LastChargeLevel.Value}%";
            else
                ChargedToText = "—";

            UpdateLastSyncText();
        });
    }

    private void OnEstimateChanged(BatteryEstimate estimate)
    {
        Dispatcher.UIThread.Post(() =>
        {
            HasEstimate = estimate.IsValid;
            if (estimate.IsValid)
            {
                TimeRemaining = estimate.TimeRemaining;
                var hours = (int)estimate.TimeRemaining.TotalHours;
                var mins = estimate.TimeRemaining.Minutes;
                TimeRemainingText = hours > 0 ? $"{hours}h {mins}m" : $"{mins}m";
            }
            else
            {
                TimeRemainingText = "—";
            }
        });
    }

    private void LoadSettings()
    {
        var s = _settingsService.Current;
        StartWithOS = s.StartWithOS;
        StartMinimized = s.StartMinimized;
        NotificationsEnabled = s.NotificationsEnabled;
        ShowPercentageOnTray = s.ShowPercentageOnTrayIcon;
        RefreshInterval = s.RefreshIntervalSeconds;
        LowThreshold = s.LowBatteryThreshold;
        CriticalThreshold = s.CriticalBatteryThreshold;
        NonIntrusiveMode = s.NonIntrusiveMode;
        SafeHidMode = s.SafeHidMode;
        CurrentTheme = s.Theme;
        IsDarkTheme = s.Theme == "dark";
    }

    [RelayCommand]
    private void OpenSettings()
    {
        LoadSettings();
        IsSettingsOpen = true;
    }

    [RelayCommand]
    private void CloseSettings()
    {
        IsSettingsOpen = false;
    }

    [RelayCommand]
    private void SaveSettings()
    {
        var settings = new AppSettings
        {
            StartWithOS = StartWithOS,
            StartMinimized = StartMinimized,
            NotificationsEnabled = NotificationsEnabled,
            ShowPercentageOnTrayIcon = ShowPercentageOnTray,
            RefreshIntervalSeconds = Math.Clamp(RefreshInterval, 1, 60),
            LowBatteryThreshold = Math.Clamp(LowThreshold, 5, 50),
            CriticalBatteryThreshold = Math.Clamp(CriticalThreshold, 5, 30),
            NonIntrusiveMode = NonIntrusiveMode,
            SafeHidMode = SafeHidMode,
            Theme = CurrentTheme
        };
        _settingsService.Save(settings);
        _autoStartService.SetAutoStart(settings.StartWithOS);
        IsSettingsOpen = false;
        ShowToast("Settings saved successfully");
    }

    [RelayCommand]
    private void ToggleTheme()
    {
        IsDarkTheme = !IsDarkTheme;
        CurrentTheme = IsDarkTheme ? "dark" : "light";
    }

    [RelayCommand]
    private void Rescan()
    {
        _monitorService.TriggerRescan();
    }

    [RelayCommand]
    private void ScanHidDevices()
    {
        DevOutput = _hidService.GetHidDiagnostics();
        ShowToast("Scan complete");
    }

    [RelayCommand]
    private void CaptureReport()
    {
        // Try to capture from current device
        var profiles = _storageService.LoadProfiles();
        if (profiles.Count > 0)
        {
            var data = _hidService.CaptureRawReport(profiles[0]);
            if (data != null)
            {
                DevOutput = $"Raw report ({data.Length} bytes):\n{BitConverter.ToString(data)}";
                return;
            }
        }
        DevOutput = "No device connected to capture report from.";
    }

    [RelayCommand]
    private async Task CopyOutputAsync()
    {
        if (Avalonia.Application.Current?.ApplicationLifetime is
            Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime desktop)
        {
            var clipboard = desktop.MainWindow?.Clipboard;
            if (clipboard != null)
            {
                await clipboard.SetTextAsync(DevOutput);
                ShowToast("Copied to clipboard");
            }
        }
    }

    [RelayCommand]
    private void CaptureDiagnostics()
    {
        var diag = $"=== Glorious Battery Monitor Diagnostics ===\n" +
                   $"Time: {DateTime.Now:O}\n" +
                   $"Version: {_updateService.CurrentVersion}\n\n" +
                   $"=== Battery State ===\n" +
                   $"Level: {BatteryLevel}%\n" +
                   $"Charging: {IsCharging}\n" +
                   $"Connection: {ConnectionState}\n" +
                   $"Device: {DeviceName}\n\n" +
                   $"=== HID Devices ===\n" +
                   _hidService.GetHidDiagnostics();
        DevOutput = diag;
        ShowToast("Diagnostics captured");
    }

    [RelayCommand]
    private async Task CheckForUpdateAsync()
    {
        IsCheckingUpdate = true;
        IsUpdateAvailable = false;
        UpdateStatusText = "Checking...";
        try
        {
            var result = await _updateService.CheckForUpdateAsync();
            if (result == null)
            {
                UpdateStatusText =
                    $"Up to date — v{_updateService.CurrentVersion}";
            }
            else
            {
                UpdateStatusText = $"Update available: v{result.NewVersion}";
                IsUpdateAvailable = true;
            }
        }
        catch
        {
            UpdateStatusText = "Update check failed";
        }
        finally
        {
            IsCheckingUpdate = false;
        }
    }

    [RelayCommand]
    private async Task DownloadUpdateAsync()
    {
        IsDownloadingUpdate = true;
        var progress = new Progress<int>(p =>
        {
            UpdateDownloadProgress = p;
            UpdateStatusText = $"Downloading... {p}%";
        });
        var success = await _updateService.DownloadAndApplyUpdateAsync(progress);
        if (!success)
        {
            UpdateStatusText = "Download failed — try again";
            IsDownloadingUpdate = false;
        }
        // if success, app restarts — this line never reached
    }

    private System.Timers.Timer? _toastTimer;
    private System.Timers.Timer? _syncTimer;

    private void StartSyncTimer()
    {
        _syncTimer = new System.Timers.Timer(1000);
        _syncTimer.Elapsed += (_, _) =>
        {
            Dispatcher.UIThread.Post(UpdateLastSyncText);
        };
        _syncTimer.AutoReset = true;
        _syncTimer.Start();
    }

    private void UpdateLastSyncText()
    {
        if (_lastReadTime == null)
        {
            LastSyncText = "—";
            return;
        }

        var elapsed = DateTime.UtcNow - _lastReadTime.Value;

        LastSyncText = elapsed.TotalSeconds < 5 ? "Just now"
            : elapsed.TotalSeconds < 60 ? $"{(int)elapsed.TotalSeconds}s ago"
            : elapsed.TotalMinutes < 60 ? $"{(int)elapsed.TotalMinutes}m ago"
            : elapsed.TotalHours < 24 ? $"{(int)elapsed.TotalHours}h ago"
            : $"{(int)elapsed.TotalDays}d ago";
    }

    private void ShowToast(string message)
    {
        ToastMessage = message;
        IsToastVisible = true;
        _toastTimer?.Stop();
        _toastTimer?.Dispose();
        _toastTimer = new System.Timers.Timer(3000);
        _toastTimer.Elapsed += (_, _) =>
        {
            Dispatcher.UIThread.Post(() => IsToastVisible = false);
            _toastTimer?.Dispose();
        };
        _toastTimer.AutoReset = false;
        _toastTimer.Start();
    }
}
