using System.Collections.ObjectModel;
using System.Linq;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using GBM.Core.Models;
using GBM.Core.Services;
using Avalonia.Threading;

namespace GBM.Desktop.ViewModels;

public partial class MainViewModel : ViewModelBase, IDisposable
{
    private const double FullChargeDurationDisplayCapHours = 24.0 * 30.0;
    private static readonly TimeSpan FullChargeDurationRefreshInterval = TimeSpan.FromMinutes(1);

    private readonly IBatteryMonitorService _monitorService;
    private readonly ISettingsService _settingsService;
    private readonly IHidDeviceService _hidService;
    private readonly IAutoStartService _autoStartService;
    private readonly IUpdateService _updateService;
    private readonly IStorageService _storageService;
    private readonly IBatteryEstimationService _estimationService;
    private bool _betaDefaultAppliedThisSession;

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
    [ObservableProperty] private string _fullChargeDurationText = "—";

    // Last sync
    [ObservableProperty] private string _lastSyncText = "—";
    private DateTime? _lastReadTime;
    private DateTime _lastFullChargeDurationRefreshUtc;
    private string _lastFullChargeDurationDeviceName = string.Empty;

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
    [ObservableProperty] private int _notificationCooldownMinutes = 5;
    [ObservableProperty] private bool _debugLogging;
    [ObservableProperty] private bool _enableBetaUpdates;
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
    [ObservableProperty] private bool _isUpdateReadyToInstall = false;
    [ObservableProperty] private string _updateActionButtonText = "Download Update";
    [ObservableProperty] private bool _showUpdateIndicator = false;
    [ObservableProperty] private string _updateIndicatorText = string.Empty;
    [ObservableProperty] private string _updateIndicatorActionText = "Open";
    [ObservableProperty] private bool _showUpdateIndicatorAction = false;

    // Toast
    [ObservableProperty] private bool _isToastVisible;
    [ObservableProperty] private string _toastMessage = string.Empty;

    public MainViewModel(
        IBatteryMonitorService monitorService,
        ISettingsService settingsService,
        IHidDeviceService hidService,
        IAutoStartService autoStartService,
        IUpdateService updateService,
        IStorageService storageService,
        IBatteryEstimationService estimationService)
    {
        _monitorService = monitorService;
        _settingsService = settingsService;
        _hidService = hidService;
        _autoStartService = autoStartService;
        _updateService = updateService;
        _storageService = storageService;
        _estimationService = estimationService;

        _monitorService.BatteryStateChanged += OnBatteryStateChanged;
        _monitorService.EstimateChanged += OnEstimateChanged;

        LoadSettings();
    }

    public async Task InitializeAsync()
    {
        await _monitorService.StartAsync();
        StartSyncTimer();

        if (_updateService.IsUpdatePendingRestart())
        {
            ConfigurePendingUpdateState();
            return;
        }

        if (Program.SkipUpdateCheckOnLaunch)
        {
            UpdateStatusText = $"Updated to v{_updateService.CurrentVersion}";
            UpdateUpdateIndicator();
            return;
        }

        _ = DelayedStartupUpdateCheckAsync();
    }

    private async Task DelayedStartupUpdateCheckAsync()
    {
        try
        {
            await Task.Delay(TimeSpan.FromSeconds(3));
            await CheckForUpdateAsync();
        }
        catch
        {
            // Best-effort background update check.
        }
    }

    private void ConfigurePendingUpdateState()
    {
        IsUpdateAvailable = true;
        IsUpdateReadyToInstall = true;
        IsDownloadingUpdate = false;
        UpdateDownloadProgress = 100;
        UpdateActionButtonText = "Restart to Update";
        UpdateStatusText = "Update ready — restart to apply";
        UpdateUpdateIndicator();
    }

    private void UpdateUpdateIndicator()
    {
        if (IsUpdateReadyToInstall)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = "Update ready";
            UpdateIndicatorActionText = "Restart";
            ShowUpdateIndicatorAction = true;
            return;
        }

        if (IsDownloadingUpdate)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = $"Updating {UpdateDownloadProgress}%";
            ShowUpdateIndicatorAction = false;
            return;
        }

        if (IsUpdateAvailable)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = string.IsNullOrWhiteSpace(UpdateVersion)
                ? "Update available"
                : $"v{UpdateVersion} available";
            UpdateIndicatorActionText = "Download";
            ShowUpdateIndicatorAction = true;
            return;
        }

        ShowUpdateIndicator = false;
        ShowUpdateIndicatorAction = false;
        UpdateIndicatorText = string.Empty;
        UpdateIndicatorActionText = "Open";
    }

    [RelayCommand]
    private async Task UpdateIndicatorActionAsync()
    {
        if (IsCheckingUpdate || IsDownloadingUpdate)
            return;

        if (IsUpdateAvailable || IsUpdateReadyToInstall)
        {
            await DownloadUpdateAsync();
            return;
        }

        OpenSettings();
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
                TimeRemainingText = FormatDuration(estimate.TimeRemaining);
            }
            else
            {
                TimeRemainingText = "—";
            }
            UpdateFullChargeDuration();
        });
    }

    private void UpdateFullChargeDuration()
    {
        // Get display name from current state
        string displayName = _monitorService.CurrentState.DeviceName;
        DateTime now = DateTime.UtcNow;

        if (string.IsNullOrEmpty(displayName) || displayName == "Glorious Mouse")
        {
            FullChargeDurationText = "—";
            _lastFullChargeDurationDeviceName = string.Empty;
            _lastFullChargeDurationRefreshUtc = DateTime.MinValue;
            return;
        }

        bool sameDevice = string.Equals(
            _lastFullChargeDurationDeviceName,
            displayName,
            StringComparison.Ordinal);

        if (sameDevice &&
            _lastFullChargeDurationRefreshUtc != DateTime.MinValue &&
            now - _lastFullChargeDurationRefreshUtc < FullChargeDurationRefreshInterval)
        {
            return;
        }

        _lastFullChargeDurationDeviceName = displayName;
        _lastFullChargeDurationRefreshUtc = now;

        // Load cached charge data and iterate to find the current device's learned rates
        // The estimation service only has in-memory state for the currently active device,
        // so we iterate through all keys and find the one with non-null, positive discharge rate
        var chargeData = _storageService.LoadChargeData();
        foreach (var kvp in chargeData.Devices)
        {
            var rates = _estimationService.GetLearnedRates(kvp.Key);
            if (rates?.DischargeRate is not double rate || rate <= 0)
                continue;

            double totalHours = 100.0 / rate;

            // Keep display bounded but allow realistic multi-day durations.
            if (totalHours > FullChargeDurationDisplayCapHours)
            {
                FullChargeDurationText = "~30d+";
                return;
            }

            var span = TimeSpan.FromHours(totalHours);
            FullChargeDurationText = "~" + FormatDuration(span, includeMinutesForDays: false);
            return;
        }

        // No device with learned rates found
        FullChargeDurationText = "—";
    }

    private static string FormatDuration(TimeSpan span, bool includeMinutesForDays = false)
    {
        if (span.TotalHours >= 24)
        {
            int days = (int)span.TotalDays;
            int hours = span.Hours;
            int mins = span.Minutes;

            if (includeMinutesForDays && mins > 0)
                return $"{days}d {hours}h {mins}m";

            return hours > 0 ? $"{days}d {hours}h" : $"{days}d";
        }

        int shortHours = (int)span.TotalHours;
        int shortMins = span.Minutes;
        return shortHours > 0 ? $"{shortHours}h {shortMins}m" : $"{shortMins}m";
    }

    private void LoadSettings()
    {
        TryEnableBetaByDefaultForPrereleaseBuild();

        var s = _settingsService.Current;
        StartWithOS = s.StartWithOS;
        StartMinimized = s.StartMinimized;
        NotificationsEnabled = s.NotificationsEnabled;
        ShowPercentageOnTray = s.ShowPercentageOnTrayIcon;
        RefreshInterval = s.RefreshIntervalSeconds;
        LowThreshold = s.LowBatteryThreshold;
        CriticalThreshold = s.CriticalBatteryThreshold;
        NotificationCooldownMinutes = s.NotificationCooldownMinutes;
        DebugLogging = s.DebugLogging;
        EnableBetaUpdates = s.EnableBetaUpdates;
        CurrentTheme = s.Theme;
        IsDarkTheme = s.Theme == "dark";
    }

    private void TryEnableBetaByDefaultForPrereleaseBuild()
    {
        if (_betaDefaultAppliedThisSession)
            return;

        _betaDefaultAppliedThisSession = true;

        var current = _settingsService.Current;
        if (current.EnableBetaUpdates)
            return;

        if (current.HasSetBetaChannelPreference)
            return;

        // Prerelease builds (e.g. 3.3.0-beta.13) should default to beta channel.
        if (!IsPrereleaseVersion(_updateService.CurrentVersion))
            return;

        var migrated = current.Clone();
        migrated.EnableBetaUpdates = true;
        _settingsService.Save(migrated);
    }

    private static bool IsPrereleaseVersion(string version)
    {
        return !string.IsNullOrWhiteSpace(version) &&
               version.Contains('-', StringComparison.Ordinal);
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
        bool oldBetaSetting = _settingsService.Current.EnableBetaUpdates;

        var settings = new AppSettings
        {
            StartWithOS = StartWithOS,
            StartMinimized = StartMinimized,
            NotificationsEnabled = NotificationsEnabled,
            ShowPercentageOnTrayIcon = ShowPercentageOnTray,
            RefreshIntervalSeconds = Math.Clamp(RefreshInterval, 1, 60),
            LowBatteryThreshold = Math.Clamp(LowThreshold, 5, 50),
            CriticalBatteryThreshold = Math.Clamp(CriticalThreshold, 5, 30),
            NotificationCooldownMinutes = Math.Clamp(NotificationCooldownMinutes, 1, 60),
            DebugLogging = DebugLogging,
            Theme = CurrentTheme,
            EnableBetaUpdates = EnableBetaUpdates,
            HasSetBetaChannelPreference = true
        };

        bool betaSettingChanged = oldBetaSetting != settings.EnableBetaUpdates;

        _settingsService.Save(settings);
        _autoStartService.SetAutoStart(settings.StartWithOS);
        IsSettingsOpen = false;
        ShowToast("Settings saved successfully");

        if (betaSettingChanged)
        {
            IsUpdateAvailable = false;
            IsUpdateReadyToInstall = false;
            IsDownloadingUpdate = false;
            UpdateDownloadProgress = 0;
            UpdateActionButtonText = "Download Update";
            UpdateStatusText = settings.EnableBetaUpdates
                ? "Beta channel enabled — checking for beta updates..."
                : "Stable channel enabled — checking for stable updates...";
            UpdateUpdateIndicator();

            _ = CheckForUpdateAsync();
        }
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
        UpdateUpdateIndicator();
        try
        {
            if (_updateService.IsUpdatePendingRestart())
            {
                ConfigurePendingUpdateState();
                return;
            }

            IsUpdateAvailable = false;
            IsUpdateReadyToInstall = false;
            UpdateDownloadProgress = 0;
            UpdateActionButtonText = "Download Update";
            UpdateStatusText = EnableBetaUpdates
                ? "Checking beta updates..."
                : "Checking stable updates...";

            var result = await _updateService.CheckForUpdateAsync();
            if (result == null)
            {
                string channelName = EnableBetaUpdates ? "beta" : "stable";
                UpdateStatusText =
                    $"Up to date ({channelName}) — v{_updateService.CurrentVersion}";
            }
            else
            {
                string channelName = EnableBetaUpdates ? "Beta" : "Stable";
                UpdateStatusText = $"{channelName} update available: v{result.NewVersion}";
                UpdateVersion = result.NewVersion;
                IsUpdateAvailable = true;
                UpdateUpdateIndicator();
            }
        }
        catch
        {
            UpdateStatusText = "Update check failed";
            UpdateUpdateIndicator();
        }
        finally
        {
            IsCheckingUpdate = false;
            UpdateUpdateIndicator();
        }
    }

    [RelayCommand]
    private async Task DownloadUpdateAsync()
    {
        if (IsUpdateReadyToInstall || _updateService.IsUpdatePendingRestart())
        {
            UpdateStatusText = "Restarting to apply update...";
            UpdateUpdateIndicator();
            bool started = _updateService.ApplyPendingUpdateAndRestart(new[] { Program.SkipUpdateCheckArg });
            if (started)
            {
                if (Avalonia.Application.Current?.ApplicationLifetime
                    is Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime desktop)
                {
                    desktop.Shutdown();
                }
                return;
            }

            UpdateStatusText = "Could not start updater — try again";
            UpdateUpdateIndicator();
            return;
        }

        IsDownloadingUpdate = true;
        UpdateDownloadProgress = 0;
        UpdateUpdateIndicator();
        var progress = new Progress<int>(p =>
        {
            UpdateDownloadProgress = p;
            UpdateStatusText = $"Downloading update... {p}%";
            UpdateUpdateIndicator();
        });

        var success = await _updateService.DownloadUpdateAsync(progress);
        IsDownloadingUpdate = false;

        if (success)
        {
            ConfigurePendingUpdateState();
            ShowToast("Update downloaded. Restart when ready.");
        }
        else
        {
            UpdateStatusText = "Download failed — try again";
            UpdateUpdateIndicator();
        }

        UpdateUpdateIndicator();
    }

    private bool _disposed;
    private System.Timers.Timer? _toastTimer;
    private System.Timers.Timer? _syncTimer;

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        _monitorService.EstimateChanged -= OnEstimateChanged;
        _syncTimer?.Stop();
        _syncTimer?.Dispose();
        _syncTimer = null;
        _toastTimer?.Stop();
        _toastTimer?.Dispose();
        _toastTimer = null;
    }

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

    public void ShowToast(string message)
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
            _toastTimer = null;
        };
        _toastTimer.AutoReset = false;
        _toastTimer.Start();
    }
}
